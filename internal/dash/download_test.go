package dash

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marXus-3D/odm/internal/ffmpeg"
)

// makeStream builds a real DASH stream on disk with ffmpeg and returns the
// directory holding it.
//
// Generated rather than recorded: a checked-in fixture of a few hundred
// segments would dwarf the rest of the repository, and a synthetic stream
// exercises the same packaging a CDN serves.
func makeStream(t *testing.T) string {
	t.Helper()
	ff := ffmpeg.Path()
	if ff == "" {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc=duration=4:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10",
		"-c:a", "aac",
		"-f", "dash", "-seg_duration", "1", "-use_template", "1",
		// Forward slashes, even on Windows. ffmpeg's DASH muxer works out
		// where to put the segments by looking for the last "/" in the
		// output path, so a path full of backslashes leaves it writing them
		// to the working directory -- which during a test is the package
		// source directory.
		filepath.ToSlash(filepath.Join(dir, "manifest.mpd")),
	)
	// Belt and braces for the same hazard: anything that still escapes the
	// output path lands in the temporary directory rather than the repo.
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot package DASH (%v): %s", err, out)
	}
	return dir
}

// playable reports whether a file really holds the streams it should, by
// asking ffmpeg to decode them. A muxed file of the right size that will not
// open is the exact failure this engine has to avoid, and only a decoder
// can tell the difference.
func playable(t *testing.T, path string, wantAudio bool) error {
	t.Helper()
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-i", path, "-map", "0:v:0"}
	if wantAudio {
		args = append(args, "-map", "0:a:0")
	}
	args = append(args, "-f", "null", "-")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg.Path(), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("ffmpeg said: %s", out)
	}
	return err
}

// TestDownloadEndToEnd is the whole engine: parse a real manifest, fetch two
// tracks that are packaged apart, and mux them into one file that plays.
func TestDownloadEndToEnd(t *testing.T) {
	streamDir := makeStream(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(streamDir)))
	defer srv.Close()

	out := t.TempDir()
	d := NewDownload("t1", srv.URL+"/manifest.mpd")
	d.Dir = out
	d.Concurrency = 4

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	st := d.Stats()
	if st.State != StateDone {
		t.Errorf("state = %q, want done", st.State)
	}
	if st.SegmentsTotal == 0 || st.SegmentsDone != st.SegmentsTotal {
		t.Errorf("segments = %d/%d, want all of them",
			st.SegmentsDone, st.SegmentsTotal)
	}
	if st.Kind != "dash" {
		t.Errorf("kind = %q, want dash", st.Kind)
	}

	info, err := os.Stat(d.Path)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("output file is empty")
	}
	// The point of the whole exercise: both tracks made it into one file.
	if err := playable(t, d.Path, true); err != nil {
		t.Errorf("the muxed file does not decode as video+audio: %v", err)
	}

	// The part files and the sidecar are working state and must not be left
	// sitting next to the finished video.
	for _, leftover := range []string{
		d.Path + ".v.part", d.Path + ".a.part", d.Path + sidecarSuffix,
	} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("%s was left behind", filepath.Base(leftover))
		}
	}
}

// TestDownloadResumes covers the pause path end to end: a download stopped
// part way must pick up where it left off and still produce a playable file
// rather than one with a seam in it.
func TestDownloadResumes(t *testing.T) {
	streamDir := makeStream(t)

	// Serve normally until the download is under way, then fail everything
	// so the first Run stops part way through.
	//
	// armed is what stops the trap resetting itself: without it the counter
	// is still over the threshold on the second run and every request trips
	// the failure again.
	var armed, blocking atomic.Bool
	var served atomic.Int32
	armed.Store(true)

	files := http.FileServer(http.Dir(streamDir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blocking.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if armed.Load() && served.Add(1) > 4 {
			blocking.Store(true)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer srv.Close()

	out := t.TempDir()
	d := NewDownload("t2", srv.URL+"/manifest.mpd")
	d.Dir = out
	d.Concurrency = 1
	d.MaxRetries = 1
	d.Backoff = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := d.Run(ctx); err == nil {
		t.Skip("the stream was small enough to finish before the server failed")
	}
	path := d.Path
	if _, err := os.Stat(path + sidecarSuffix); err != nil {
		t.Fatalf("no sidecar was written, so nothing could resume: %v", err)
	}

	// Let the rest through and run again against the same output path.
	armed.Store(false)
	blocking.Store(false)
	d2 := NewDownload("t2", srv.URL+"/manifest.mpd")
	d2.Dir = out
	d2.Path = path
	d2.Concurrency = 4
	if err := d2.Run(ctx); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := playable(t, d2.Path, true); err != nil {
		t.Errorf("the resumed file does not decode as video+audio: %v", err)
	}
}

// TestRestoreRejectsTruncatedPart pins down the guard that stops a resume
// from appending onto a part file that no longer holds what the sidecar
// claims. Appending there produces a file of plausible size that will not
// play, which is the worst failure this engine has.
func TestRestoreRejectsTruncatedPart(t *testing.T) {
	dir := t.TempDir()
	d := NewDownload("t3", "https://cdn.example/m.mpd")
	d.Path = filepath.Join(dir, "video.mp4")

	sc := sidecar{
		Version: 1, URL: d.URL,
		VideoDone: 10, VideoBytes: 5000,
		AudioDone: 4, AudioBytes: 800,
	}
	b, _ := json.Marshal(sc)
	os.WriteFile(d.Path+sidecarSuffix, b, 0o644)

	// A video part file shorter than the sidecar claims invalidates
	// everything: the audio counters are no use without it.
	os.WriteFile(d.Path+".v.part", make([]byte, 100), 0o644)
	os.WriteFile(d.Path+".a.part", make([]byte, 800), 0o644)
	if got := d.restore(); got.VideoDone != 0 || got.AudioDone != 0 {
		t.Errorf("restore() = %+v, want everything reset", got)
	}

	// A good video part with a short audio part only costs the audio.
	os.WriteFile(d.Path+".v.part", make([]byte, 5000), 0o644)
	os.WriteFile(d.Path+".a.part", make([]byte, 10), 0o644)
	got := d.restore()
	if got.VideoDone != 10 {
		t.Errorf("VideoDone = %d, want 10 kept", got.VideoDone)
	}
	if got.AudioDone != 0 || got.AudioBytes != 0 {
		t.Errorf("audio = (%d, %d), want reset", got.AudioDone, got.AudioBytes)
	}
}

// TestRestoreIgnoresOtherURL covers a sidecar left by a different download
// that happened to land on the same filename.
func TestRestoreIgnoresOtherURL(t *testing.T) {
	dir := t.TempDir()
	d := NewDownload("t4", "https://cdn.example/new.mpd")
	d.Path = filepath.Join(dir, "video.mp4")

	b, _ := json.Marshal(sidecar{
		Version: 1, URL: "https://cdn.example/old.mpd",
		VideoDone: 9, VideoBytes: 1234,
	})
	os.WriteFile(d.Path+sidecarSuffix, b, 0o644)
	os.WriteFile(d.Path+".v.part", make([]byte, 1234), 0o644)

	if got := d.restore(); got.VideoDone != 0 {
		t.Errorf("restore() = %+v, want a fresh start for a different URL", got)
	}
}

func TestNameFromURL(t *testing.T) {
	cases := []struct{ url, want string }{
		// A generic manifest name is no name; the directory above it is.
		{"https://cdn.example/sacdn/dash/3620534_1080_h265/index_web.mpd", "3620534_1080_h265"},
		{"https://cdn.example/a/b/manifest.mpd", "b"},
		{"https://cdn.example/a/b/index.mpd", "b"},
		// A real name is kept.
		{"https://cdn.example/a/the-feature.mpd", "the-feature"},
		{"https://cdn.example/", "video"},
	}
	for _, c := range cases {
		if got := nameFromURL(c.url); got != c.want {
			t.Errorf("nameFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestResolvePathAlwaysMP4(t *testing.T) {
	dir := t.TempDir()
	d := NewDownload("t5", "https://cdn.example/a/index_web.mpd")
	d.Dir = dir

	p, err := d.resolvePath()
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}
	if filepath.Ext(p) != ".mp4" {
		t.Errorf("path = %q, want a .mp4 (the mux writes nothing else)", p)
	}
}
