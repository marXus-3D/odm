package hls

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
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

// Progress is a snapshot of an in-flight playlist download.
type Progress struct {
	SegmentsDone  int
	SegmentsTotal int
	BytesWritten  int64
	Conns         int
}

// Fetcher downloads the segments of a media playlist into one file.
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

	keysMu sync.Mutex
	keys   map[string][]byte // decryption keys, cached by URI
}

const (
	defaultConcurrency = 6
	defaultRetries     = 4
	defaultBackoff     = 400 * time.Millisecond
	maxSegmentSize     = 256 << 20 // a single segment this large is pathological
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

// Fetch downloads every segment and writes them to w in playlist order.
//
// Segments are fetched concurrently but written sequentially: the container
// format depends on order, so out-of-order output is a corrupt file. skip is
// how many leading segments are already on disk, which is how resume works.
func (f *Fetcher) Fetch(ctx context.Context, m *Media, w io.Writer, skip int) error {
	if skip < 0 {
		skip = 0
	}
	if skip > len(m.Segments) {
		return fmt.Errorf("cannot skip %d of %d segments", skip, len(m.Segments))
	}

	// The fMP4 initialization segment must lead the file, so it is only
	// written on a fresh start.
	if m.InitURI != "" && skip == 0 {
		init := Segment{
			URI:         m.InitURI,
			Key:         m.InitKey,
			HasRange:    m.InitHasRange,
			RangeLength: m.InitRangeLength,
			RangeStart:  m.InitRangeStart,
		}
		data, err := f.fetchSegment(ctx, init)
		if err != nil {
			return fmt.Errorf("initialization segment: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}

	segs := m.Segments[skip:]
	n := len(segs)
	if n == 0 {
		return nil
	}

	conns := f.concurrency()
	if conns > n {
		conns = n
	}
	// Bound how far downloaders may run ahead of the writer, or a long
	// playlist would buffer the whole video in memory.
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
			ferr = fmt.Errorf("segment %d of %d: %w", skip+i+1, len(m.Segments), s.err)
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
				SegmentsTotal: len(m.Segments),
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

// fetchSegment downloads and decrypts one segment, retrying transient errors.
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
			return f.decrypt(ctx, s, data)
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
		req.Header.Set("Range",
			fmt.Sprintf("bytes=%d-%d", s.RangeStart, s.RangeStart+s.RangeLength-1))
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

// decrypt undoes AES-128-CBC when the segment is encrypted.
func (f *Fetcher) decrypt(ctx context.Context, s Segment, data []byte) ([]byte, error) {
	if s.Key == nil || s.Key.Method == "" || s.Key.Method == "NONE" {
		return data, nil
	}
	if s.Key.Method != "AES-128" {
		// SAMPLE-AES needs container-level parsing and usually DRM; refuse
		// rather than write a file that looks fine and will not play.
		return nil, fmt.Errorf("unsupported encryption method %s", s.Key.Method)
	}

	key, err := f.fetchKey(ctx, s.Key.URI)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := s.Key.IV
	if len(iv) == 0 {
		// With no explicit IV the spec says use the segment's media sequence
		// number as a 128-bit big-endian value.
		iv = make([]byte, 16)
		binary.BigEndian.PutUint64(iv[8:], s.Sequence)
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("IV is %d bytes, want %d", len(iv), block.BlockSize())
	}
	if len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("encrypted segment is %d bytes, not a multiple of %d",
			len(data), block.BlockSize())
	}

	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return stripPKCS7(out, block.BlockSize())
}

// stripPKCS7 removes the trailing padding each segment is encrypted with.
func stripPKCS7(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty plaintext")
	}
	pad := int(b[len(b)-1])
	if pad == 0 || pad > blockSize || pad > len(b) {
		return nil, fmt.Errorf("bad padding byte %d", pad)
	}
	for _, c := range b[len(b)-pad:] {
		if int(c) != pad {
			return nil, errors.New("inconsistent padding")
		}
	}
	return b[:len(b)-pad], nil
}

// fetchKey retrieves and caches a decryption key. Playlists usually reuse one
// key for every segment, so this is one request rather than hundreds.
func (f *Fetcher) fetchKey(ctx context.Context, uri string) ([]byte, error) {
	if uri == "" {
		return nil, errors.New("encrypted segment has no key URI")
	}
	f.keysMu.Lock()
	if k, ok := f.keys[uri]; ok {
		f.keysMu.Unlock()
		return k, nil
	}
	f.keysMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	f.applyHeaders(req)
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch key %s: %s", uri, resp.Status)
	}
	key, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if err != nil {
		return nil, err
	}
	if len(key) != 16 {
		return nil, fmt.Errorf("key is %d bytes, want 16", len(key))
	}

	f.keysMu.Lock()
	if f.keys == nil {
		f.keys = map[string][]byte{}
	}
	f.keys[uri] = key
	f.keysMu.Unlock()
	return key, nil
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

// LoadPlaylist fetches a URL and parses it as a master or media playlist.
// When it is a master, pick selects the variant; nil means the highest
// bandwidth one.
func (f *Fetcher) LoadPlaylist(ctx context.Context, rawURL string,
	pick func(*Master) Variant) (*Media, *Master, error) {

	body, final, err := f.get(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}
	if !LooksLikePlaylist(body) {
		return nil, nil, fmt.Errorf("%s is not an M3U8 playlist", rawURL)
	}

	if IsMaster(body) {
		master, err := ParseMaster(bytes.NewReader(body), final)
		if err != nil {
			return nil, nil, err
		}
		v := master.Best()
		if pick != nil {
			v = pick(master)
		}
		media, _, err := f.loadMedia(ctx, v.URI)
		return media, master, err
	}

	media, err := ParseMedia(bytes.NewReader(body), final)
	return media, nil, err
}

func (f *Fetcher) loadMedia(ctx context.Context, rawURL string) (*Media, *Master, error) {
	body, final, err := f.get(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}
	if IsMaster(body) {
		return nil, nil, fmt.Errorf("%s points at another master playlist", rawURL)
	}
	m, err := ParseMedia(bytes.NewReader(body), final)
	return m, nil, err
}

// get fetches a playlist, returning the body and the URL it finally came
// from, which is what relative segment URIs resolve against.
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, nil, err
	}
	return body, resp.Request.URL, nil
}
