package hls

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireFFmpeg skips a test when ffmpeg is not installed. Remuxing is
// optional by design, so its absence is not a failure.
func requireFFmpeg(t *testing.T) string {
	t.Helper()
	ff := FFmpegPath()
	if ff == "" {
		t.Skip("ffmpeg is not installed; remux tests need it")
	}
	return ff
}

// makeTS builds a real MPEG-TS with ffmpeg: a colour-bar video and, when
// asked, an AAC tone. Synthesising a valid TS by hand is not practical, and
// a fake one would not exercise the code that matters.
func makeTS(t *testing.T, dir string, seconds int, audio string) string {
	t.Helper()
	ff := requireFFmpeg(t)
	out := filepath.Join(dir, "source.ts")

	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=320x240:rate=15:duration=%d", seconds),
	}
	if audio != "" {
		args = append(args,
			"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d", seconds),
			"-c:a", audio, "-b:a", "64k")
	}
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-f", "mpegts", "-y", out)

	cmd := exec.Command(ff, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build a test .ts (%v): %s", err, b)
	}
	return out
}

// probe returns ffprobe's view of a file's streams.
func probe(t *testing.T, path string) string {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	out, err := exec.Command(ffprobe, "-hide_banner", "-loglevel", "error",
		"-show_entries", "stream=codec_name,codec_type",
		"-show_entries", "format=format_name,duration",
		"-of", "default=noprint_wrappers=1", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %s: %v: %s", path, err, out)
	}
	return string(out)
}

func TestRemuxTSToMP4(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	ts := makeTS(t, dir, 2, "aac")

	tsInfo := probe(t, ts)
	if !strings.Contains(tsInfo, "mpegts") {
		t.Fatalf("test fixture is not a transport stream:\n%s", tsInfo)
	}

	mp4, err := remux(context.Background(), ts)
	if err != nil {
		t.Fatalf("remux: %v", err)
	}
	if mp4 == "" {
		t.Fatal("remux reported nothing to do for a .ts")
	}
	if filepath.Ext(mp4) != ".mp4" {
		t.Errorf("output %q is not an .mp4", mp4)
	}
	if _, err := os.Stat(ts); !os.IsNotExist(err) {
		t.Error("the source .ts should be removed once the .mp4 is in place")
	}
	if _, err := os.Stat(mp4 + ".part"); !os.IsNotExist(err) {
		t.Error("the .part scratch file was left behind")
	}

	info := probe(t, mp4)
	if !strings.Contains(info, "mp4") {
		t.Errorf("output is not an MP4:\n%s", info)
	}
	// A stream copy must preserve both streams; a botched one silently drops
	// the audio.
	if !strings.Contains(info, "h264") {
		t.Errorf("video stream missing:\n%s", info)
	}
	if !strings.Contains(info, "aac") {
		t.Errorf("audio stream missing:\n%s", info)
	}

	// faststart puts the moov atom before the media data.
	assertFaststart(t, mp4)
}

// assertFaststart checks that moov precedes mdat, which is what lets a
// player start without reading the whole file.
func assertFaststart(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	off, moovAt, mdatAt := 0, -1, -1
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		if size == 1 && off+16 <= len(data) {
			size = int(binary.BigEndian.Uint64(data[off+8 : off+16]))
		}
		if size < 8 {
			break
		}
		if typ == "moov" && moovAt < 0 {
			moovAt = off
		}
		if typ == "mdat" && mdatAt < 0 {
			mdatAt = off
		}
		off += size
	}
	if moovAt < 0 || mdatAt < 0 {
		t.Fatalf("expected both moov and mdat, got moov=%d mdat=%d", moovAt, mdatAt)
	}
	if moovAt > mdatAt {
		t.Errorf("moov at %d is after mdat at %d; -movflags +faststart did not apply",
			moovAt, mdatAt)
	}
}

// A transport stream carrying non-AAC audio makes ffmpeg reject the
// aac_adtstoasc bitstream filter. The fallback attempt without it must save
// the conversion.
func TestRemuxFallsBackForNonAACAudio(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	ts := makeTS(t, dir, 2, "mp2")

	mp4, err := remux(context.Background(), ts)
	if err != nil {
		t.Fatalf("remux with mp2 audio failed instead of falling back: %v", err)
	}
	info := probe(t, mp4)
	if !strings.Contains(info, "h264") {
		t.Errorf("video stream missing after fallback:\n%s", info)
	}
}

func TestRemuxLeavesNonTSAlone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "already.mp4")
	if err := os.WriteFile(p, []byte("not really an mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := remux(context.Background(), p)
	if err != nil {
		t.Fatalf("remux: %v", err)
	}
	if out != "" {
		t.Errorf("remux returned %q for a non-.ts input; it should do nothing", out)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("the input file was touched")
	}
}

func TestRemuxKeepsSourceWhenFFmpegFails(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	// Valid extension, garbage contents: ffmpeg cannot make an MP4 of this.
	ts := filepath.Join(dir, "broken.ts")
	if err := os.WriteFile(ts, bytes.Repeat([]byte{0x11, 0x22}, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := remux(context.Background(), ts)
	if err == nil {
		t.Fatalf("expected an error, got output %q", out)
	}
	// The download is still good; losing it because a conversion failed
	// would be much worse than shipping a .ts.
	if _, statErr := os.Stat(ts); statErr != nil {
		t.Error("the source .ts was deleted even though the remux failed")
	}
	if _, statErr := os.Stat(strings.TrimSuffix(ts, ".ts") + ".mp4.part"); !os.IsNotExist(statErr) {
		t.Error("a .part file was left behind after a failed remux")
	}
}

func TestRemuxRespectsCancellation(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	ts := makeTS(t, dir, 2, "aac")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before we start

	if _, err := remux(ctx, ts); err == nil {
		t.Fatal("expected a cancelled context to abort the remux")
	}
	if _, err := os.Stat(ts); err != nil {
		t.Error("the source was removed despite the remux being cancelled")
	}
}

// End to end: an HLS playlist of real TS segments, downloaded and converted
// by the daemon's own code path.
func TestHLSDownloadRemuxesToMP4(t *testing.T) {
	requireFFmpeg(t)
	srcDir := t.TempDir()
	full := makeTS(t, srcDir, 6, "aac")

	// Chop the transport stream into segments on packet boundaries, which is
	// what a real packager produces.
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	const pkt = 188
	perSeg := ((len(data) / 3) / pkt) * pkt
	if perSeg < pkt {
		t.Skip("test stream too small to segment")
	}
	var segs [][]byte
	for off := 0; off < len(data); off += perSeg {
		end := off + perSeg
		if end > len(data) {
			end = len(data)
		}
		segs = append(segs, data[off:end])
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := range segs {
			fmt.Fprintf(w, "#EXTINF:2.0,\nseg%d.ts\n", i)
		}
		fmt.Fprint(w, "#EXT-X-ENDLIST\n")
	})
	for i, b := range segs {
		i, b := i, b
		mux.HandleFunc(fmt.Sprintf("/seg%d.ts", i), func(w http.ResponseWriter, r *http.Request) {
			w.Write(b)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	outDir := t.TempDir()
	d := NewDownload("remux-e2e", srv.URL+"/index.m3u8")
	d.Dir = outDir
	d.Concurrency = 3
	d.Remux = true

	var sawRemuxing bool
	d.OnUpdate = func(s Stats) {
		if s.State == StateRemuxing {
			sawRemuxing = true
		}
	}

	start := time.Now()
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Logf("downloaded and remuxed in %s -> %s", time.Since(start).Round(time.Millisecond), d.Path)

	if filepath.Ext(d.Path) != ".mp4" {
		t.Fatalf("final path %q is not an .mp4; remux did not run", d.Path)
	}
	if !sawRemuxing {
		t.Error("the remuxing state was never reported to the UI")
	}
	if st := d.Stats(); st.State != StateDone {
		t.Errorf("final state = %q, want done", st.State)
	}
	if e := d.Stats().Err; e != "" {
		t.Errorf("unexpected error on a successful remux: %s", e)
	}

	// No .ts and no scratch file should survive.
	leftovers, _ := filepath.Glob(filepath.Join(outDir, "*"))
	for _, f := range leftovers {
		if strings.HasSuffix(f, ".ts") || strings.HasSuffix(f, ".part") ||
			strings.HasSuffix(f, sidecarSuffix) {
			t.Errorf("leftover file after remux: %s", f)
		}
	}

	info := probe(t, d.Path)
	if !strings.Contains(info, "h264") || !strings.Contains(info, "aac") {
		t.Errorf("remuxed file lost a stream:\n%s", info)
	}
	if !strings.Contains(info, "mp4") {
		t.Errorf("output is not MP4:\n%s", info)
	}
}
