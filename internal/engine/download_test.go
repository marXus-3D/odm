package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// deterministicBody builds a payload whose every byte depends on its offset,
// so any mis-ordered or duplicated range write shows up as a hash mismatch.
func deterministicBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

// rangeServer serves the payload with full Range/ETag support via
// http.ServeContent, and counts how many requests it saw.
func rangeServer(t *testing.T, body []byte, delay time.Duration) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	mod := time.Unix(1700000000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Disposition", `attachment; filename="payload.bin"`)
		http.ServeContent(w, r, "payload.bin", mod, bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func testOpts(conns int, minSplit int64) Options {
	return Options{
		MaxConns:     conns,
		MinSplitSize: minSplit,
		ChunkSize:    8 << 10,
		MaxRetries:   3,
		RetryBackoff: 10 * time.Millisecond,
	}
}

func TestParallelDownloadMatchesSource(t *testing.T) {
	body := deterministicBody(4 << 20) // 4 MiB
	srv, hits := rangeServer(t, body, 0)

	dir := t.TempDir()
	d := New("t1", Request{URL: srv.URL, Dir: dir}, testOpts(8, 64<<10))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(body))
	}
	if filepath.Base(d.Path) != "payload.bin" {
		t.Errorf("filename from Content-Disposition = %q, want payload.bin", filepath.Base(d.Path))
	}
	if d.State() != StateDone {
		t.Errorf("state = %q, want done", d.State())
	}
	// One probe plus at least one request per connection: proof the segments
	// really did go out in parallel rather than collapsing to a single stream.
	if *hits < 3 {
		t.Errorf("server saw %d requests, expected several parallel ranges", *hits)
	}
	if _, err := os.Stat(d.Path + metaSuffix); !os.IsNotExist(err) {
		t.Errorf("sidecar %s should be removed after completion", d.Path+metaSuffix)
	}
}

func TestDynamicSplitProducesManySegments(t *testing.T) {
	body := deterministicBody(8 << 20)
	srv, _ := rangeServer(t, body, 0)

	dir := t.TempDir()
	d := New("t2", Request{URL: srv.URL, Dir: dir}, testOpts(8, 128<<10))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	st := d.Stats()
	if len(st.Segments) < 8 {
		t.Fatalf("got %d segments, want at least the 8 connections", len(st.Segments))
	}
	// Segments must tile [0,size) exactly: no gaps, no overlaps.
	assertTiling(t, st.Segments, int64(len(body)))
	if st.Downloaded != int64(len(body)) {
		t.Errorf("downloaded = %d, want %d", st.Downloaded, len(body))
	}
}

func assertTiling(t *testing.T, segs []SegmentSnapshot, size int64) {
	t.Helper()
	covered := make([]bool, size)
	for _, s := range segs {
		if s.Start < 0 || s.End > size || s.Start > s.End {
			t.Fatalf("segment %d out of bounds: [%d,%d) size %d", s.ID, s.Start, s.End, size)
		}
		for i := s.Start; i < s.End; i++ {
			if covered[i] {
				t.Fatalf("segment %d overlaps at byte %d", s.ID, i)
			}
			covered[i] = true
		}
	}
	for i, c := range covered {
		if !c {
			t.Fatalf("byte %d not covered by any segment", i)
		}
	}
}

func TestResumeAfterInterruption(t *testing.T) {
	body := deterministicBody(6 << 20)
	srv, _ := rangeServer(t, body, 0)

	dir := t.TempDir()
	opts := testOpts(4, 128<<10)
	d := New("t3", Request{URL: srv.URL, Dir: dir}, opts)

	// Cut the transfer off partway through.
	ctx, cancel := context.WithCancel(context.Background())
	d.OnUpdate = func(s Stats) {
		if s.Downloaded > int64(len(body))/4 {
			cancel()
		}
	}
	err := d.Run(ctx)
	if err == nil {
		t.Skip("download finished before the interrupt landed; timing-dependent")
	}
	partial := d.Downloaded()
	if partial == 0 {
		t.Fatal("nothing was downloaded before the interrupt")
	}
	if _, err := os.Stat(d.Path + metaSuffix); err != nil {
		t.Fatalf("sidecar missing after interrupt: %v", err)
	}

	// A fresh Download object, pointed at the same path, must pick up the
	// sidecar and only fetch what is missing.
	d2 := New("t3", Request{URL: srv.URL, Dir: dir}, opts)
	d2.Path = d.Path
	d2.OnUpdate = nil
	if err := d2.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, err := os.ReadFile(d2.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("resumed content mismatch (%d bytes)", len(got))
	}
}

// noRangeServer refuses Range requests, forcing the single-stream fallback.
func TestFallbackWhenServerRefusesRanges(t *testing.T) {
	body := deterministicBody(512 << 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "none")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := New("t4", Request{URL: srv.URL, Dir: dir}, testOpts(8, 64<<10))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, _ := os.ReadFile(d.Path)
	if sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("content mismatch: %d bytes vs %d", len(got), len(body))
	}
	if d.Probe.Resumable {
		t.Error("probe should not report resumable when the server refuses ranges")
	}
}

func TestUnknownLengthStream(t *testing.T) {
	body := deterministicBody(300 << 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Content-Length: Go switches to chunked transfer encoding.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := New("t5", Request{URL: srv.URL, Dir: dir}, testOpts(8, 64<<10))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, _ := os.ReadFile(d.Path)
	if sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("content mismatch: %d bytes vs %d", len(got), len(body))
	}
}

func TestFatalStatusIsNotRetried(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := New("t6", Request{URL: srv.URL, Dir: dir}, testOpts(4, 64<<10))
	err := d.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error for a 403")
	}
	if hits > 2 {
		t.Errorf("403 was retried %d times; it should fail fast", hits)
	}
	if d.State() != StateError {
		t.Errorf("state = %q, want error", d.State())
	}
}

func TestPause(t *testing.T) {
	body := deterministicBody(8 << 20)
	srv, _ := rangeServer(t, body, 5*time.Millisecond)

	dir := t.TempDir()
	d := New("t7", Request{URL: srv.URL, Dir: dir}, testOpts(4, 128<<10))
	go func() {
		time.Sleep(60 * time.Millisecond)
		d.Pause()
	}()
	err := d.Run(context.Background())
	if err != nil && !errors.Is(err, ErrPaused) && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
	if err != nil && d.State() != StatePaused {
		t.Errorf("state = %q, want paused", d.State())
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		`normal.zip`:            "normal.zip",
		`../../etc/passwd`:      "passwd",
		`a\b\c.txt`:             "c.txt",
		`bad:name?.mp4`:         "bad_name_.mp4",
		`CON.txt`:               "_CON.txt",
		`trailing.  `:           "trailing",
		``:                      "download",
		`"quoted".bin`:          "_quoted_.bin",
		strings.Repeat("x", 40): strings.Repeat("x", 40),
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStalledConnectionIsDetected(t *testing.T) {
	body := deterministicBody(2 << 20)
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"v1"`)
		// The probe asks for a short range; answer it properly, and stall
		// only the real segment requests that follow.
		if r.Header.Get("Range") == fmt.Sprintf("bytes=0-%d", HeadSize-1) {
			w.Header().Set("Content-Range",
				fmt.Sprintf("bytes 0-%d/%d", HeadSize-1, len(body)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[:HeadSize])
			return
		}
		// Send a token amount, then go silent forever.
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[:16])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Go silent until the client gives up or the test tears down.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	// Defers run last-in-first-out: release the wedged handlers *before*
	// Close, which otherwise blocks waiting for them to return.
	defer srv.Close()
	defer close(release)

	opts := testOpts(2, 128<<10)
	opts.Timeout = 300 * time.Millisecond
	opts.MaxRetries = 1

	dir := t.TempDir()
	d := New("t8", Request{URL: srv.URL, Dir: dir}, opts)

	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a stall error")
		}
		if !strings.Contains(err.Error(), "stalled") {
			t.Fatalf("error should name the stall, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stall watchdog never fired; the download hung")
	}
}

// TestRateLimitedServerStillCompletes models a host like Hetzner that answers
// 429 as soon as more than one range request is in flight. The download must
// shed connections and finish rather than give up.
func TestRateLimitedServerStillCompletes(t *testing.T) {
	body := deterministicBody(2 << 20)
	mod := time.Unix(1700000000, 0)

	var mu sync.Mutex
	inFlight := 0
	var saw429 bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		over := inFlight > 1
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()

		if over {
			mu.Lock()
			saw429 = true
			mu.Unlock()
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		time.Sleep(20 * time.Millisecond) // widen the concurrency window
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "payload.bin", mod, bytes.NewReader(body))
	}))
	defer srv.Close()

	opts := testOpts(8, 128<<10)
	opts.RetryBackoff = 20 * time.Millisecond

	dir := t.TempDir()
	d := New("t9", Request{URL: srv.URL, Dir: dir}, opts)
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	mu.Lock()
	hit := saw429
	mu.Unlock()
	if !hit {
		t.Skip("server never hit its concurrency limit; nothing was exercised")
	}
	if !d.Stats().Throttled {
		t.Error("stats should report the download as throttled")
	}
	got, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("content mismatch after throttling: %d bytes vs %d", len(got), len(body))
	}
}
