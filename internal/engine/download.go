package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// unknownEnd stands in for "stream until EOF" when the server will not tell
// us the content length.
const unknownEnd = math.MaxInt64

const updateInterval = 500 * time.Millisecond

// ErrPaused is returned by Run when the caller pauses the download. It is a
// clean stop, not a failure: the sidecar is intact and Run can be called again.
var ErrPaused = errors.New("download paused")

// Download is a single file transfer. It owns its output file, its segment
// list and the worker goroutines that fill them.
type Download struct {
	ID    string
	Req   Request
	Opts  Options
	Probe *Probe
	Path  string

	// OnUpdate, if set, is called roughly every updateInterval with a fresh
	// snapshot. It must not block.
	OnUpdate func(Stats)

	mu     sync.Mutex
	segs   []*Segment
	nextID int
	file   *os.File
	meta   *Meta

	state    atomic.Value // State
	errMsg   atomic.Value // string
	conns    atomic.Int32 // segments currently in flight
	live     atomic.Int32 // workers still running
	rawBytes atomic.Int64 // includes split overlap; feeds the speed meter only

	// throttled records that the server pushed back with 429/503 at least
	// once, so the UI can explain why we are using fewer connections.
	throttled atomic.Bool

	m       meter
	paused  atomic.Bool
	cancel  context.CancelFunc
	fatalMu sync.Mutex
	fatal   error
}

// New builds a Download. Call Run to actually transfer.
func New(id string, req Request, opts Options) *Download {
	opts.applyDefaults()
	if req.MaxConns > 0 {
		opts.MaxConns = req.MaxConns
	}
	d := &Download{ID: id, Req: req, Opts: opts}
	d.state.Store(StateQueued)
	d.errMsg.Store("")
	return d
}

func (d *Download) State() State {
	s, _ := d.state.Load().(State)
	return s
}

func (d *Download) setState(s State) { d.state.Store(s) }

// Pause stops the workers and leaves the sidecar on disk so a later Run
// resumes from the same offsets.
func (d *Download) Pause() {
	d.paused.Store(true)
	d.mu.Lock()
	c := d.cancel
	d.mu.Unlock()
	if c != nil {
		c()
	}
}

// Run performs the transfer and blocks until it finishes, fails or is paused.
func (d *Download) Run(ctx context.Context) error {
	d.paused.Store(false)
	d.setFatalReset()

	ctx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()
	defer cancel()

	d.setState(StateProbing)
	if err := d.prepare(ctx); err != nil {
		return d.failf("%w", err)
	}
	defer d.closeFile()

	d.setState(StateDownloading)
	d.m.reset()

	conns := d.Opts.MaxConns
	if !d.resumable() {
		// Without range support there is exactly one stream to be had.
		conns = 1
	}

	stop := make(chan struct{})
	var reporterWG sync.WaitGroup
	reporterWG.Add(1)
	go func() {
		defer reporterWG.Done()
		d.reporter(stop)
	}()

	var workers sync.WaitGroup
	for i := 0; i < conns; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			d.worker(ctx)
		}()
	}
	workers.Wait()
	close(stop)
	reporterWG.Wait()

	if err := d.firstFatal(); err != nil {
		d.setState(StateError)
		d.errMsg.Store(err.Error())
		d.saveMeta()
		return err
	}
	if d.paused.Load() {
		d.setState(StatePaused)
		d.saveMeta()
		return ErrPaused
	}
	if err := ctx.Err(); err != nil {
		d.setState(StatePaused)
		d.saveMeta()
		return err
	}
	if err := d.verifyComplete(); err != nil {
		d.setState(StateError)
		d.errMsg.Store(err.Error())
		d.saveMeta()
		return err
	}

	d.closeFile()
	removeMeta(d.Path)
	d.setState(StateDone)
	d.emit()
	return nil
}

// prepare probes the URL, resolves the output path, restores any resumable
// state and opens the file.
func (d *Download) prepare(ctx context.Context) error {
	p, err := d.probeWithRetry(ctx)
	if err != nil {
		return err
	}
	d.Probe = p

	resuming := false
	if d.Path == "" {
		dir := d.Req.Dir
		if dir == "" {
			dir = DefaultDownloadDir()
		}
		path, err := UniquePath(dir, p.Filename)
		if err != nil {
			return err
		}
		d.Path = path
	} else if old, err := loadMeta(d.Path); err == nil && old.matches(p) {
		d.restore(old)
		resuming = true
	}

	if err := os.MkdirAll(filepath.Dir(d.Path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(d.Path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.file = f
	if !resuming {
		d.segs = nil
		d.nextID = 0
	}
	d.meta = &Meta{
		Version:      metaVersion,
		URL:          d.Req.URL,
		FinalURL:     p.FinalURL,
		Path:         d.Path,
		Size:         p.Size,
		Resumable:    p.Resumable,
		ETag:         p.ETag,
		LastModified: p.LastModified,
		Headers:      d.Req.Headers,
	}
	d.mu.Unlock()

	if p.Resumable && p.Size > 0 {
		if err := prealloc(f, p.Size); err != nil {
			return err
		}
	}
	d.saveMeta()
	return nil
}

// probeWithRetry runs the probe, waiting out rate limiting rather than
// failing the whole download before it starts.
func (d *Download) probeWithRetry(ctx context.Context) (*Probe, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		p, err := DoProbe(ctx, d.Req, d.Opts)
		if err == nil {
			return p, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !strings.Contains(err.Error(), "429") &&
			!strings.Contains(err.Error(), "503") {
			return nil, err
		}
		d.throttled.Store(true)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return nil, lastErr
}

// restore rebuilds the segment list from a sidecar written by an earlier run.
func (d *Download) restore(m *Meta) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.segs = nil
	maxID := -1
	for _, s := range m.Segments {
		cur := s.Cur
		if cur > s.End {
			cur = s.End
		}
		d.segs = append(d.segs, newSegment(s.ID, s.Start, cur, s.End))
		if s.ID > maxID {
			maxID = s.ID
		}
	}
	d.nextID = maxID + 1
}

func (d *Download) resumable() bool {
	return d.Probe != nil && d.Probe.Resumable && d.Probe.Size > 0
}

// worker pulls segments until there is no more work to steal.
func (d *Download) worker(ctx context.Context) {
	d.live.Add(1)
	defer d.live.Add(-1)

	for {
		if ctx.Err() != nil || d.firstFatal() != nil {
			return
		}
		seg := d.acquire()
		if seg == nil {
			return
		}
		d.conns.Add(1)
		err := d.runSegment(ctx, seg)
		d.conns.Add(-1)
		seg.claimed.Store(false)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			// Backing off under rate limiting: leave the range for whoever
			// is still running and stop competing for bandwidth.
			if errors.Is(err, errReleaseSegment) {
				return
			}
			d.setFatal(err)
			d.mu.Lock()
			c := d.cancel
			d.mu.Unlock()
			if c != nil {
				c()
			}
			return
		}
	}
}

// acquire hands an idle worker something to do.
//
// This is the dynamic-segmentation core. Rather than carving the file into N
// fixed pieces up front -- which leaves every fast connection idle while one
// straggler grinds through its share -- we start with a single whole-file
// range and let each idle worker split the largest *remaining* range in half.
// Workers therefore stay busy right to the end of the transfer, and a slow
// peer automatically ends up owning less of the file.
func (d *Download) acquire() *Segment {
	d.mu.Lock()
	defer d.mu.Unlock()

	// First worker takes the whole file (or the whole unknown-length stream).
	if len(d.segs) == 0 {
		end := int64(unknownEnd)
		if d.Probe != nil && d.Probe.Size >= 0 {
			end = d.Probe.Size
		}
		s := newSegment(d.nextID, 0, 0, end)
		s.claimed.Store(true)
		d.nextID++
		d.segs = append(d.segs, s)
		return s
	}
	if !d.resumable() {
		return nil
	}

	// A segment restored from a sidecar but not yet owned by any worker can be
	// claimed outright -- no split needed.
	for _, s := range d.segs {
		if s.Remaining() > 0 && s.claimed.CompareAndSwap(false, true) {
			return s
		}
	}

	// Otherwise steal the tail of whichever active range has the most left.
	var best *Segment
	for _, s := range d.segs {
		if best == nil || s.Remaining() > best.Remaining() {
			best = s
		}
	}
	if best == nil || best.Remaining() < 2*d.Opts.MinSplitSize {
		return nil
	}

	cur, end := best.Cur(), best.End()
	mid := cur + (end-cur)/2
	best.setEnd(mid)

	// The donor may have advanced past mid in the instant between our read and
	// our store. Take wherever it actually reached as the true boundary, so the
	// two ranges never overlap by more than one in-flight read.
	if actual := best.Cur(); actual > mid {
		if actual >= end {
			best.setEnd(end) // donor consumed it all; nothing left to hand over
			return nil
		}
		mid = actual
		best.setEnd(mid)
	}

	s := newSegment(d.nextID, mid, mid, end)
	s.claimed.Store(true)
	d.nextID++
	d.segs = append(d.segs, s)
	return s
}

// runSegment transfers one range, retrying transient failures from wherever
// the previous attempt left off.
func (d *Download) runSegment(ctx context.Context, seg *Segment) error {
	var lastErr error
	waits := 0
	for attempt := 0; attempt <= d.Opts.MaxRetries; attempt++ {
		if seg.Remaining() <= 0 {
			return nil
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.Opts.RetryBackoff << (attempt - 1)):
			}
		}
		err := d.fetchRange(ctx, seg)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var fatal fatalError
		if errors.As(err, &fatal) {
			return err
		}

		// A rate limiter is telling us we are being greedy. Retrying harder
		// makes it worse, so the right move is to use fewer connections: every
		// worker but the last hands its range back and exits, and the survivor
		// simply waits out the interval the server asked for.
		var pb pushbackError
		if errors.As(err, &pb) {
			d.throttled.Store(true)
			if d.live.Load() > 1 {
				return errReleaseSegment
			}
			if waits >= maxPushbackWaits {
				return fmt.Errorf("segment %d: server kept rate limiting us: %w",
					seg.ID, pb.error)
			}
			waits++
			attempt-- // waiting out a rate limit is not a failed attempt
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pb.after):
			}
			continue
		}
		lastErr = err
	}
	return fmt.Errorf("segment %d: gave up after %d attempts: %w",
		seg.ID, d.Opts.MaxRetries, lastErr)
}

// fatalError marks a failure that retrying cannot fix.
type fatalError struct{ error }

func (f fatalError) Unwrap() error { return f.error }

// pushbackError is a 429/503: the server is not broken, it just wants fewer
// or slower connections from us.
type pushbackError struct {
	error
	after time.Duration
}

func (p pushbackError) Unwrap() error { return p.error }

// errReleaseSegment tells the worker loop to hand its segment back and exit,
// shrinking the connection pool. It is not a failure.
var errReleaseSegment = errors.New("segment released to reduce concurrency")

// maxPushbackWaits caps how long the last worker standing will keep waiting
// out a rate limiter before declaring the download failed.
const maxPushbackWaits = 10

// parseRetryAfter reads a Retry-After header in either of its two legal
// forms: delay-seconds, or an HTTP-date.
func parseRetryAfter(v string) time.Duration {
	const fallback = 2 * time.Second
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(v); err == nil {
		d := time.Duration(secs) * time.Second
		return clampWait(d)
	}
	if t, err := http.ParseTime(v); err == nil {
		return clampWait(time.Until(t))
	}
	return fallback
}

func clampWait(d time.Duration) time.Duration {
	switch {
	case d < time.Second:
		return time.Second
	case d > 60*time.Second:
		return 60 * time.Second
	}
	return d
}

// errStalled marks a connection that stopped delivering bytes. It is
// retryable: the usual cause is a wedged TCP flow, and a fresh connection to
// the same range normally works.
var errStalled = errors.New("connection stalled")

func (d *Download) fetchRange(ctx context.Context, seg *Segment) error {
	start, end := seg.Cur(), seg.End()
	if start >= end {
		return nil
	}

	// A connection that goes quiet must not hold a worker hostage: without
	// this, a black-holed route pins the segment until the process dies.
	ctx, cancelStall := context.WithCancel(ctx)
	defer cancelStall()
	var lastByte atomic.Int64
	var stalled atomic.Bool
	lastByte.Store(time.Now().UnixNano())
	go d.watchStall(ctx, &lastByte, &stalled, cancelStall)

	method := d.Req.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, d.Probe.FinalURL,
		bytes.NewReader(d.Req.Body))
	if err != nil {
		return fatalError{err}
	}
	applyHeaders(req, d.Req.Headers, d.Opts.UserAgent)

	ranged := d.resumable()
	if ranged {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
		// If-Range makes the server answer with a full 200 when the resource
		// changed, which we detect below instead of silently splicing bytes
		// from two different versions of the file together.
		if v := d.meta.validator(); v != "" && start > 0 {
			req.Header.Set("If-Range", v)
		}
	}

	resp, err := d.Opts.Client.Do(req)
	if err != nil {
		if stalled.Load() {
			return fmt.Errorf("segment %d: %w before response", seg.ID, errStalled)
		}
		return err
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// We already hold every byte the server has.
		seg.setEnd(seg.Cur())
		return nil
	case resp.StatusCode == http.StatusOK && ranged && start > 0:
		return fatalError{fmt.Errorf(
			"segment %d: resource changed on the server (200 for a range request)", seg.ID)}
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusServiceUnavailable:
		return pushbackError{
			error: fmt.Errorf("segment %d: %s", seg.ID, resp.Status),
			after: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	case resp.StatusCode >= 400:
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusGone:
			return fatalError{fmt.Errorf("segment %d: %s", seg.ID, resp.Status)}
		}
		return fmt.Errorf("segment %d: %s", seg.ID, resp.Status)
	}

	buf := make([]byte, d.Opts.ChunkSize)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			lastByte.Store(time.Now().UnixNano())
			// End may have shrunk while we were reading: a splitter handed our
			// tail to another worker. Write only what is still ours.
			room := seg.End() - seg.Cur()
			if room <= 0 {
				return nil
			}
			if int64(n) > room {
				n = int(room)
			}
			if _, werr := d.file.WriteAt(buf[:n], seg.Cur()); werr != nil {
				return fatalError{fmt.Errorf("segment %d: write: %w", seg.ID, werr)}
			}
			seg.advance(int64(n))
			d.rawBytes.Add(int64(n))
			if seg.Remaining() <= 0 {
				return nil
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				if seg.End() == unknownEnd {
					// Streaming case: EOF is how we learn the real length.
					seg.setEnd(seg.Cur())
					return nil
				}
				if seg.Remaining() > 0 {
					return fmt.Errorf("segment %d: short read, %d bytes left",
						seg.ID, seg.Remaining())
				}
				return nil
			}
			if stalled.Load() {
				return fmt.Errorf("segment %d: %w after %s idle",
					seg.ID, errStalled, d.Opts.Timeout)
			}
			return rerr
		}
	}
}

// watchStall cancels the per-attempt context when no bytes have arrived for
// Opts.Timeout, flagging the cause so the caller can report it as retryable
// rather than as a user cancellation.
func (d *Download) watchStall(ctx context.Context, lastByte *atomic.Int64,
	stalled *atomic.Bool, cancel context.CancelFunc) {

	tick := d.Opts.Timeout / 4
	if tick < 250*time.Millisecond {
		tick = 250 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			idle := now.Sub(time.Unix(0, lastByte.Load()))
			if idle >= d.Opts.Timeout {
				stalled.Store(true)
				cancel()
				return
			}
		}
	}
}

// verifyComplete checks that every byte we promised is actually on disk.
func (d *Download) verifyComplete() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.segs {
		if s.End() != unknownEnd && s.Cur() < s.End() {
			return fmt.Errorf("incomplete: segment %d stopped at %d of %d",
				s.ID, s.Cur(), s.End())
		}
	}
	if d.Probe != nil && d.Probe.Size > 0 && d.file != nil {
		if st, err := d.file.Stat(); err == nil && st.Size() != d.Probe.Size {
			return fmt.Errorf("size mismatch: wrote %d, expected %d",
				st.Size(), d.Probe.Size)
		}
	}
	return nil
}

// Downloaded is the exact byte count on disk, summed from the segments so a
// momentary overshoot at a split boundary cannot inflate it.
func (d *Download) Downloaded() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int64
	for _, s := range d.segs {
		n += s.Progress()
	}
	return n
}

// Stats snapshots progress for the UI or API.
func (d *Download) Stats() Stats {
	d.mu.Lock()
	snaps := make([]SegmentSnapshot, 0, len(d.segs))
	var done int64
	for _, s := range d.segs {
		snaps = append(snaps, s.snapshot())
		done += s.Progress()
	}
	d.mu.Unlock()

	total := int64(-1)
	if d.Probe != nil {
		total = d.Probe.Size
	}
	rate := d.m.rate()
	var eta time.Duration
	if rate > 1 && total > 0 && done < total {
		eta = time.Duration(float64(total-done)/rate) * time.Second
	}
	errStr, _ := d.errMsg.Load().(string)
	return Stats{
		State:      d.State(),
		Downloaded: done,
		Total:      total,
		SpeedBPS:   rate,
		ETA:        eta,
		Conns:      int(d.conns.Load()),
		Throttled:  d.throttled.Load(),
		Segments:   snaps,
		Err:        errStr,
	}
}

// reporter samples speed, persists resume state and notifies the caller.
func (d *Download) reporter(stop <-chan struct{}) {
	t := time.NewTicker(updateInterval)
	defer t.Stop()
	var sinceSave time.Duration
	for {
		select {
		case <-stop:
			d.m.sample(d.rawBytes.Load())
			d.saveMeta()
			d.emit()
			return
		case <-t.C:
			d.m.sample(d.rawBytes.Load())
			sinceSave += updateInterval
			if sinceSave >= time.Second {
				sinceSave = 0
				d.saveMeta()
			}
			d.emit()
		}
	}
}

func (d *Download) emit() {
	if d.OnUpdate != nil {
		d.OnUpdate(d.Stats())
	}
}

func (d *Download) saveMeta() {
	d.mu.Lock()
	m := d.meta
	if m == nil {
		d.mu.Unlock()
		return
	}
	snaps := make([]SegmentSnapshot, 0, len(d.segs))
	for _, s := range d.segs {
		snaps = append(snaps, s.snapshot())
	}
	m.Segments = snaps
	d.mu.Unlock()
	_ = m.save()
}

func (d *Download) closeFile() {
	d.mu.Lock()
	f := d.file
	d.file = nil
	d.mu.Unlock()
	if f != nil {
		f.Sync()
		f.Close()
	}
}

func (d *Download) setFatal(err error) {
	d.fatalMu.Lock()
	defer d.fatalMu.Unlock()
	if d.fatal == nil {
		d.fatal = err
	}
}

func (d *Download) setFatalReset() {
	d.fatalMu.Lock()
	d.fatal = nil
	d.fatalMu.Unlock()
}

func (d *Download) firstFatal() error {
	d.fatalMu.Lock()
	defer d.fatalMu.Unlock()
	return d.fatal
}

func (d *Download) failf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	d.setState(StateError)
	d.errMsg.Store(err.Error())
	d.emit()
	return err
}
