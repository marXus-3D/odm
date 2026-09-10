package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadWritesPlayableFile(t *testing.T) {
	const n = 15
	srv, _ := plainServer(t, n, 0)
	dir := t.TempDir()

	d := NewDownload("v1", srv.URL+"/index.m3u8")
	d.Dir = dir
	d.Concurrency = 5
	d.Remux = false // keep the test independent of whether ffmpeg exists

	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if d.State() != StateDone {
		t.Errorf("state = %q, want done", d.State())
	}
	got, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, expectedConcat(n)) {
		t.Fatalf("output mismatch: %d bytes on disk", len(got))
	}
	// index.m3u8 is a useless name, so the parent path segment is used.
	if base := filepath.Base(d.Path); filepath.Ext(base) != ".ts" {
		t.Errorf("output %q should be a .ts", base)
	}
	if _, err := os.Stat(d.Path + sidecarSuffix); !os.IsNotExist(err) {
		t.Error("sidecar should be gone once the download completes")
	}

	st := d.Stats()
	if st.SegmentsDone != n || st.SegmentsTotal != n {
		t.Errorf("segments = %d/%d, want %d/%d", st.SegmentsDone, st.SegmentsTotal, n, n)
	}
	if st.Duration < 59 || st.Duration > 61 {
		t.Errorf("duration = %v, want ~60s", st.Duration)
	}
}

func TestDownloadRejectsLiveStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No EXT-X-ENDLIST: a live stream.
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\ns0.ts\n")
	}))
	defer srv.Close()

	d := NewDownload("live", srv.URL+"/live.m3u8")
	d.Dir = t.TempDir()
	err := d.Run(context.Background())
	if !errors.Is(err, ErrLive) {
		t.Fatalf("got %v, want ErrLive", err)
	}
	if d.State() != StateError {
		t.Errorf("state = %q, want error", d.State())
	}
}

func TestDownloadPauseAndResume(t *testing.T) {
	const n = 40
	srv, _ := plainServer(t, n, 25*time.Millisecond)
	dir := t.TempDir()

	d := NewDownload("v2", srv.URL+"/index.m3u8")
	d.Dir = dir
	d.Concurrency = 3
	d.Remux = false

	go func() {
		time.Sleep(200 * time.Millisecond)
		d.Pause()
	}()
	err := d.Run(context.Background())
	if err != nil && !errors.Is(err, ErrPaused) {
		t.Fatalf("unexpected error: %v", err)
	}
	if err == nil {
		t.Skip("finished before the pause landed; timing-dependent")
	}

	partialSegs := d.Stats().SegmentsDone
	if partialSegs == 0 || partialSegs == n {
		t.Skipf("pause landed at %d/%d segments; nothing useful to resume", partialSegs, n)
	}
	sc, err := os.ReadFile(d.Path + sidecarSuffix)
	if err != nil {
		t.Fatalf("sidecar missing after pause: %v", err)
	}
	t.Logf("paused at %d/%d segments, sidecar: %s", partialSegs, n, sc)

	// A fresh object aimed at the same path must continue, not restart.
	d2 := NewDownload("v2", srv.URL+"/index.m3u8")
	d2.Dir = dir
	d2.Path = d.Path
	d2.Concurrency = 3
	d2.Remux = false
	if err := d2.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}

	got, err := os.ReadFile(d2.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, expectedConcat(n)) {
		t.Fatalf("resumed file does not match: got %d bytes, want %d",
			len(got), len(expectedConcat(n)))
	}
}

// A partially written trailing segment must be discarded on resume, or the
// stream is corrupt at the join.
func TestDownloadResumeTruncatesPartialTail(t *testing.T) {
	const n = 10
	srv, _ := plainServer(t, n, 0)
	dir := t.TempDir()

	d := NewDownload("v3", srv.URL+"/index.m3u8")
	d.Dir = dir
	d.Concurrency = 2
	d.Remux = false
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	path := d.Path

	// Rewind to segment 4 and add junk, as a crash mid-write would.
	var upto bytes.Buffer
	for i := 0; i < 4; i++ {
		upto.Write(segBody(i))
	}
	if err := os.WriteFile(path, append(upto.Bytes(), []byte("TORN")...), 0o644); err != nil {
		t.Fatal(err)
	}
	sc := fmt.Sprintf(
		`{"version":1,"url":%q,"segmentsDone":4,"segmentsTotal":%d,"bytesWritten":%d}`,
		srv.URL+"/index.m3u8", n, upto.Len())
	if err := os.WriteFile(path+sidecarSuffix, []byte(sc), 0o644); err != nil {
		t.Fatal(err)
	}

	d2 := NewDownload("v3", srv.URL+"/index.m3u8")
	d2.Dir = dir
	d2.Path = path
	d2.Remux = false
	if err := d2.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, expectedConcat(n)) {
		t.Fatal("the torn trailing bytes were not discarded before resuming")
	}
}

// A playlist that changed length since the sidecar was written must restart
// rather than splice two different videos together.
func TestDownloadRestartsWhenPlaylistChanged(t *testing.T) {
	const n = 8
	srv, _ := plainServer(t, n, 0)
	dir := t.TempDir()
	path := filepath.Join(dir, "video.ts")

	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc := fmt.Sprintf(
		`{"version":1,"url":%q,"segmentsDone":3,"segmentsTotal":999,"bytesWritten":5}`,
		srv.URL+"/index.m3u8")
	if err := os.WriteFile(path+sidecarSuffix, []byte(sc), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDownload("v4", srv.URL+"/index.m3u8")
	d.Dir = dir
	d.Path = path
	d.Remux = false
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, expectedConcat(n)) {
		t.Fatal("a changed playlist should restart from scratch")
	}
}

func TestNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://cdn.example.com/movies/heist/index.m3u8":  "heist",
		"https://cdn.example.com/movies/heist/master.m3u8": "heist",
		"https://cdn.example.com/v/my-talk.m3u8":           "my-talk",
		"https://cdn.example.com/chunklist.m3u8":           "chunklist",
		"https://cdn.example.com/":                         "video",
	}
	for in, want := range cases {
		if got := nameFromURL(in); got != want {
			t.Errorf("nameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDownloadFMP4GetsMp4Extension(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MAP:URI=\"init.mp4\"\n"+
			"#EXTINF:4.0,\ns0.m4s\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("INIT")) })
	mux.HandleFunc("/s0.m4s", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("SEG")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := NewDownload("v5", srv.URL+"/index.m3u8")
	d.Dir = t.TempDir()
	d.Remux = false
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if filepath.Ext(d.Path) != ".mp4" {
		t.Errorf("fMP4 output %q should be .mp4", d.Path)
	}
	got, _ := os.ReadFile(d.Path)
	// The initialization segment has to come first or the file is unplayable.
	if string(got) != "INITSEG" {
		t.Errorf("got %q, want the init segment followed by the media segment", got)
	}
}
