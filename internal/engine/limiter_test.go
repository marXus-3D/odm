package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestLimiterUnlimitedByDefault(t *testing.T) {
	var l *Limiter // nil must behave as unlimited, not panic
	if err := l.Wait(context.Background(), 1<<20); err != nil {
		t.Fatalf("nil limiter should not block: %v", err)
	}
	l2 := NewLimiter(0)
	start := time.Now()
	for i := 0; i < 100; i++ {
		if err := l2.Wait(context.Background(), 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("a zero rate should not throttle, took %s", d)
	}
}

func TestLimiterEnforcesRate(t *testing.T) {
	const rate = 256 << 10 // 256 KiB/s
	const chunk = 16 << 10
	const total = 256 << 10 // one second worth

	l := NewLimiter(rate)
	start := time.Now()
	for sent := 0; sent < total; sent += chunk {
		if err := l.Wait(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)

	// The bucket starts empty and holds a quarter second of burst, so a
	// second's worth of data should take roughly 0.75s. Allow generous slack
	// for timer granularity but insist real throttling happened.
	if elapsed < 400*time.Millisecond {
		t.Errorf("sent %d bytes at %d B/s in only %s -- not throttled",
			total, rate, elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("throttling overshot badly: %s for one second of data", elapsed)
	}
}

func TestLimiterRespectsContext(t *testing.T) {
	l := NewLimiter(1024) // 1 KiB/s: the request below cannot be met quickly
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = l.Wait(ctx, 1<<20) // drain the initial bucket
	err := l.Wait(ctx, 1<<20)
	if err == nil {
		t.Fatal("expected the wait to be cut short by the context")
	}
}

func TestLimiterHandlesOversizedRequest(t *testing.T) {
	// A single request larger than the whole bucket must still complete
	// rather than wait forever for tokens that can never accumulate.
	l := NewLimiter(64 << 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.Wait(ctx, 8<<20); err != nil {
		t.Fatalf("oversized request deadlocked: %v", err)
	}
}

func TestDownloadRespectsLimiter(t *testing.T) {
	body := deterministicBody(512 << 10) // 512 KiB
	mod := time.Unix(1700000000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "payload.bin", mod, bytes.NewReader(body))
	}))
	defer srv.Close()

	opts := testOpts(8, 64<<10)
	opts.Limiter = NewLimiter(512 << 10) // ~1s worth of data
	opts.Timeout = 5 * time.Second

	dir := t.TempDir()
	d := New("lim", Request{URL: srv.URL, Dir: dir}, opts)

	start := time.Now()
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)

	got, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatal("throttled download produced different bytes")
	}
	// Without a limiter this finishes in milliseconds against localhost.
	if elapsed < 300*time.Millisecond {
		t.Errorf("download finished in %s; the limiter was not applied", elapsed)
	}
}
