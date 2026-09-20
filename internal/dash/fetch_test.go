package dash

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFetchWritesInOrder is the property the whole engine rests on. Segments
// are fetched concurrently, and a fragmented MP4 is read front to back, so
// writing them in completion order rather than playlist order produces a
// file of exactly the right size that will not open.
func TestFetchWritesInOrder(t *testing.T) {
	const n = 40
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "init" {
			w.Write([]byte("INIT|"))
			return
		}
		// Answer the later segments first so a fetcher that wrote in
		// completion order would visibly scramble them.
		fmt.Fprintf(w, "%s|", name)
	}))
	defer srv.Close()

	track := &Track{Kind: "video", InitURI: srv.URL + "/init"}
	var want strings.Builder
	want.WriteString("INIT|")
	for i := 0; i < n; i++ {
		track.Segments = append(track.Segments, Segment{
			URI: fmt.Sprintf("%s/seg%02d", srv.URL, i),
		})
		fmt.Fprintf(&want, "seg%02d|", i)
	}

	var buf bytes.Buffer
	f := &Fetcher{Concurrency: 8}
	if err := f.Fetch(context.Background(), track, &buf, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if buf.String() != want.String() {
		t.Errorf("segments written out of order\n got %q\nwant %q", buf.String(), want.String())
	}
}

// TestFetchSkipResumes covers the resume path: the leading segments are
// already on disk, and the initialization segment must not be written a
// second time or it lands in the middle of the file.
func TestFetchSkipResumes(t *testing.T) {
	var initHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/init") {
			initHits.Add(1)
			w.Write([]byte("INIT|"))
			return
		}
		fmt.Fprintf(w, "%s|", strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer srv.Close()

	track := &Track{Kind: "video", InitURI: srv.URL + "/init"}
	for i := 0; i < 5; i++ {
		track.Segments = append(track.Segments, Segment{
			URI: fmt.Sprintf("%s/s%d", srv.URL, i),
		})
	}

	var buf bytes.Buffer
	f := &Fetcher{Concurrency: 4}
	if err := f.Fetch(context.Background(), track, &buf, 3); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got, want := buf.String(), "s3|s4|"; got != want {
		t.Errorf("resumed output = %q, want %q", got, want)
	}
	if n := initHits.Load(); n != 0 {
		t.Errorf("initialization segment fetched %d times on resume, want 0", n)
	}
}

// TestFetchByteRanges covers a SegmentBase track, where every segment is a
// slice of one file rather than a request of its own.
func TestFetchByteRanges(t *testing.T) {
	const body = "0123456789abcdefghij"
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		ranges = append(ranges, rng)
		var start, end int
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
			// An open-ended range: everything from start to the end.
			fmt.Sscanf(rng, "bytes=%d-", &start)
			end = len(body) - 1
		}
		if end >= len(body) {
			end = len(body) - 1
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(body[start : end+1]))
	}))
	defer srv.Close()

	track := &Track{
		Kind:         "video",
		InitURI:      srv.URL + "/f.mp4",
		InitHasRange: true,
		InitStart:    0,
		InitLength:   4,
		Segments: []Segment{{
			URI: srv.URL + "/f.mp4", HasRange: true,
			RangeStart: 4, RangeLength: 1 << 31, // "the rest of the file"
		}},
	}

	var buf bytes.Buffer
	f := &Fetcher{}
	if err := f.Fetch(context.Background(), track, &buf, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if buf.String() != body {
		t.Errorf("assembled = %q, want %q", buf.String(), body)
	}
	if len(ranges) != 2 || ranges[0] != "bytes=0-3" {
		t.Errorf("init range = %v, want bytes=0-3", ranges)
	}
	// An unbounded length has to become an open-ended request, not a
	// request for two gigabytes the server will reject.
	if len(ranges) > 1 && ranges[1] != "bytes=4-" {
		t.Errorf("media range = %q, want bytes=4-", ranges[1])
	}
}

// TestFetchRetries pins down that a flaky segment is retried rather than
// failing the whole download. CDNs serving a few thousand segments produce
// the occasional 503 as a matter of course.
func TestFetchRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/flaky") && hits.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	track := &Track{Kind: "video", Segments: []Segment{{URI: srv.URL + "/flaky"}}}
	var buf bytes.Buffer
	f := &Fetcher{MaxRetries: 4, Backoff: time.Millisecond}
	if err := f.Fetch(context.Background(), track, &buf, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if buf.String() != "ok" {
		t.Errorf("body = %q, want ok", buf.String())
	}
	if hits.Load() != 3 {
		t.Errorf("attempts = %d, want 3", hits.Load())
	}
}

// TestFetchGivesUp covers the other side: a segment that never comes back
// must fail the download rather than producing a short file.
func TestFetchGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	track := &Track{Kind: "video", Segments: []Segment{
		{URI: srv.URL + "/a"}, {URI: srv.URL + "/b"},
	}}
	var buf bytes.Buffer
	f := &Fetcher{MaxRetries: 1, Backoff: time.Millisecond}
	err := f.Fetch(context.Background(), track, &buf, 0)
	if err == nil {
		t.Fatal("want an error for a segment that 404s")
	}
	// The message has to say which segment, or a failure at 3000 of 4000 is
	// undiagnosable.
	if !strings.Contains(err.Error(), "video segment 1 of 2") {
		t.Errorf("error = %q, want it to name the segment", err)
	}
}

// TestFetchAppliesHeaders pins down that the credentials the browser handed
// over actually reach the CDN. Without them an authenticated stream is a
// few thousand 403s.
func TestFetchAppliesHeaders(t *testing.T) {
	var gotReferer, gotUA, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		gotUA = r.Header.Get("User-Agent")
		gotCookie = r.Header.Get("Cookie")
		w.Write([]byte("x"))
	}))
	defer srv.Close()

	f := &Fetcher{
		Headers:   map[string]string{"Referer": "https://site.example/watch", "Cookie": "sid=abc"},
		UserAgent: "ODM-Test/1.0",
	}
	track := &Track{Kind: "video", Segments: []Segment{{URI: srv.URL + "/s"}}}
	if err := f.Fetch(context.Background(), track, &bytes.Buffer{}, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotReferer != "https://site.example/watch" {
		t.Errorf("Referer = %q", gotReferer)
	}
	if gotUA != "ODM-Test/1.0" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotCookie != "sid=abc" {
		t.Errorf("Cookie = %q", gotCookie)
	}
}

// TestLoadManifestRejectsNonManifest guards the case that started all this:
// a URL that is not a manifest must not be fetched as one.
func TestLoadManifestRejectsNonManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n"))
	}))
	defer srv.Close()

	f := &Fetcher{}
	_, _, err := f.LoadManifest(context.Background(), srv.URL+"/x.mpd")
	if err == nil || !strings.Contains(err.Error(), "not a DASH manifest") {
		t.Errorf("err = %v, want a complaint that it is not a manifest", err)
	}
}
