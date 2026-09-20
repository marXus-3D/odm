package dash

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Limiter is the throughput ceiling interface, satisfied by engine.Limiter.
// Declared here rather than imported so this package does not depend on the
// engine.
type Limiter interface {
	Wait(ctx context.Context, n int) error
}

// Progress is a snapshot of an in-flight track download.
type Progress struct {
	SegmentsDone  int
	SegmentsTotal int
	BytesWritten  int64
	Conns         int
}

// Fetcher downloads the segments of one track into one file.
type Fetcher struct {
	Client      *http.Client
	Headers     map[string]string
	UserAgent   string
	Limiter     Limiter
	Concurrency int
	MaxRetries  int
	Backoff     time.Duration

	// OnProgress is called as segments complete. It must not block.
	OnProgress func(Progress)
}

const (
	defaultConcurrency = 6
	defaultRetries     = 4
	defaultBackoff     = 400 * time.Millisecond
	maxSegmentSize     = 512 << 20 // a single segment this large is pathological
	maxManifestSize    = 32 << 20
)

func (f *Fetcher) concurrency() int {
	if f.Concurrency > 0 {
		return f.Concurrency
	}
	return defaultConcurrency
}

func (f *Fetcher) retries() int {
	if f.MaxRetries > 0 {
		return f.MaxRetries
	}
	return defaultRetries
}

func (f *Fetcher) backoff() time.Duration {
	if f.Backoff > 0 {
		return f.Backoff
	}
	return defaultBackoff
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return http.DefaultClient
}

// slot carries one downloaded segment to the writer.
type slot struct {
	data []byte
	err  error
}

// Fetch downloads every segment of a track and writes them to w in order.
//
// Segments are fetched concurrently but written sequentially. A fragmented
// MP4 is a stack of boxes that the decoder reads front to back, so writing
// them out of order produces a file that looks the right size and will not
// open. skip is how many leading segments are already on disk, which is how
// resume works.
func (f *Fetcher) Fetch(ctx context.Context, t *Track, w io.Writer, skip int) error {
	if skip < 0 {
		skip = 0
	}
	if skip > len(t.Segments) {
		return fmt.Errorf("cannot skip %d of %d segments", skip, len(t.Segments))
	}

	// The initialization segment carries the codec configuration and must
	// lead the file, so it is only written on a fresh start.
	if t.InitURI != "" && skip == 0 {
		init := Segment{
			URI:         t.InitURI,
			HasRange:    t.InitHasRange,
			RangeStart:  t.InitStart,
			RangeLength: t.InitLength,
		}
		data, err := f.fetchSegment(ctx, init)
		if err != nil {
			return fmt.Errorf("initialization segment: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}

	segs := t.Segments[skip:]
	n := len(segs)
	if n == 0 {
		return nil
	}

	conns := f.concurrency()
	if conns > n {
		conns = n
	}
	// Bound how far downloaders may run ahead of the writer, or a feature
	// film would buffer itself into memory.
	window := conns * 2
	if window < conns+2 {
		window = conns + 2
	}

	results := make([]chan slot, n)
	for i := range results {
		results[i] = make(chan slot, 1)
	}
	sem := make(chan struct{}, window)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var next atomic.Int64
	var inFlight atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := int(next.Add(1) - 1)
				if idx >= n {
					return
				}
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					results[idx] <- slot{err: ctx.Err()}
					return
				}
				inFlight.Add(1)
				data, err := f.fetchSegment(ctx, segs[idx])
				inFlight.Add(-1)
				results[idx] <- slot{data: data, err: err}
				if err != nil {
					return // the writer will surface this and cancel
				}
			}
		}()
	}

	var written int64
	var ferr error
	for i := 0; i < n; i++ {
		var s slot
		select {
		case s = <-results[i]:
		case <-ctx.Done():
			ferr = ctx.Err()
		}
		if ferr != nil {
			break
		}
		if s.err != nil {
			ferr = fmt.Errorf("%s segment %d of %d: %w",
				t.Kind, skip+i+1, len(t.Segments), s.err)
			break
		}
		if _, err := w.Write(s.data); err != nil {
			ferr = err
			break
		}
		written += int64(len(s.data))
		<-sem // let a downloader run one further ahead

		if f.OnProgress != nil {
			f.OnProgress(Progress{
				SegmentsDone:  skip + i + 1,
				SegmentsTotal: len(t.Segments),
				BytesWritten:  written,
				Conns:         int(inFlight.Load()),
			})
		}
	}

	cancel()
	// Drain so no worker is left blocked writing into an unread channel.
	go func() {
		for i := 0; i < n; i++ {
			select {
			case <-results[i]:
			default:
			}
		}
	}()
	wg.Wait()
	return ferr
}

// fetchSegment downloads one segment, retrying transient errors.
func (f *Fetcher) fetchSegment(ctx context.Context, s Segment) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= f.retries(); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(f.backoff() << (attempt - 1)):
			}
		}
		data, err := f.getOnce(ctx, s)
		if err == nil {
			return data, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
	}
	return nil, lastErr
}

func (f *Fetcher) getOnce(ctx context.Context, s Segment) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URI, nil)
	if err != nil {
		return nil, err
	}
	f.applyHeaders(req)
	if s.HasRange {
		// A SegmentBase track asks for "everything after the header", which
		// has no known end, so an open-ended range is the honest request.
		if s.RangeLength >= 1<<31-1 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", s.RangeStart))
		} else {
			req.Header.Set("Range",
				fmt.Sprintf("bytes=%d-%d", s.RangeStart, s.RangeStart+s.RangeLength-1))
		}
	}

	resp, err := f.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s: %s", s.URI, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSegmentSize))
	if err != nil {
		return nil, err
	}
	if f.Limiter != nil {
		if err := f.Limiter.Wait(ctx, len(data)); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func (f *Fetcher) applyHeaders(req *http.Request) {
	for k, v := range f.Headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	if req.Header.Get("User-Agent") == "" && f.UserAgent != "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}
}

// LoadManifest fetches a URL and parses it as an MPD, returning the best
// video and audio tracks it describes.
func (f *Fetcher) LoadManifest(ctx context.Context, rawURL string) (
	video, audio *Track, err error) {

	body, final, err := f.get(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}
	if !LooksLikeManifest(body) {
		return nil, nil, fmt.Errorf("%s is not a DASH manifest", rawURL)
	}
	m, err := Parse(body, final)
	if err != nil {
		return nil, nil, err
	}
	return m.Tracks(final)
}

// get fetches a manifest, returning the body and the URL it finally came
// from, which is what relative segment URLs resolve against.
func (f *Fetcher) get(ctx context.Context, rawURL string) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	f.applyHeaders(req)
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("%s: %s", rawURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
	if err != nil {
		return nil, nil, err
	}
	return body, resp.Request.URL, nil
}
