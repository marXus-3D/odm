package dash

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// State mirrors the byte-range engine's states so the UI can treat every
// kind of download the same way.
type State string

const (
	StateQueued      State = "queued"
	StateProbing     State = "probing"
	StateDownloading State = "downloading"
	StatePaused      State = "paused"
	StateRemuxing    State = "remuxing"
	StateDone        State = "done"
	StateError       State = "error"
)

// ErrPaused is a clean stop, not a failure.
var ErrPaused = errors.New("download paused")

// Stats is a snapshot for the UI.
type Stats struct {
	State         State   `json:"state"`
	Downloaded    int64   `json:"downloaded"`
	Total         int64   `json:"total"` // estimated; -1 when unknown
	SpeedBPS      float64 `json:"speedBps"`
	Conns         int     `json:"conns"`
	SegmentsDone  int     `json:"segmentsDone"`
	SegmentsTotal int     `json:"segmentsTotal"`
	Duration      float64 `json:"durationSeconds"`
	Variant       string  `json:"variant,omitempty"`
	Kind          string  `json:"kind"`
	Err           string  `json:"error,omitempty"`
}

// Download fetches one DASH manifest to one file.
//
// The shape follows the HLS engine deliberately: the manager drives both
// through the same job interface, and a download that reported its progress
// differently depending on the streaming format would show up as a bug in
// the UI rather than as a feature of the protocol.
type Download struct {
	ID       string
	URL      string
	Dir      string
	Filename string
	Headers  map[string]string

	Client      *http.Client
	Limiter     Limiter
	Concurrency int
	UserAgent   string

	// MaxRetries and Backoff bound how hard a flaky segment is chased
	// before the download gives up. Zero means the fetcher's defaults.
	MaxRetries int
	Backoff    time.Duration

	OnUpdate func(Stats)

	Path string // resolved output path

	mu      sync.Mutex
	video   *Track
	audio   *Track
	label   string
	cancel  context.CancelFunc
	paused  atomic.Bool
	state   atomic.Value
	errMsg  atomic.Value
	segDone atomic.Int64

	segTotal atomic.Int64
	written  atomic.Int64
	conns    atomic.Int32

	speedMu   sync.Mutex
	lastBytes int64
	lastTime  time.Time
	ema       float64
}

const sidecarSuffix = ".dmd"

// sidecar records how far each track got, so a paused download can resume.
//
// Two counters rather than one: the tracks are fetched one after the other
// into files of their own, and resuming the audio from the video's position
// would silently corrupt it.
type sidecar struct {
	Version    int    `json:"version"`
	URL        string `json:"url"`
	Variant    string `json:"variant,omitempty"`
	VideoDone  int    `json:"videoDone"`
	VideoBytes int64  `json:"videoBytes"`
	AudioDone  int    `json:"audioDone"`
	AudioBytes int64  `json:"audioBytes"`
}

// NewDownload prepares a manifest download.
func NewDownload(id, rawURL string) *Download {
	d := &Download{ID: id, URL: rawURL}
	d.state.Store(StateQueued)
	d.errMsg.Store("")
	return d
}

func (d *Download) State() State {
	s, _ := d.state.Load().(State)
	return s
}

// Pause stops the fetch; the sidecar lets a later Run pick it back up.
func (d *Download) Pause() {
	d.paused.Store(true)
	d.mu.Lock()
	c := d.cancel
	d.mu.Unlock()
	if c != nil {
		c()
	}
}

// Stats snapshots progress.
func (d *Download) Stats() Stats {
	errStr, _ := d.errMsg.Load().(string)
	done, total := int(d.segDone.Load()), int(d.segTotal.Load())

	written := d.written.Load()
	// The real byte total is unknowable until the last segment lands, so
	// extrapolate from what the average segment has cost so far. It is an
	// estimate and the UI should treat it as one.
	est := int64(-1)
	if done > 0 && total > 0 {
		est = int64(float64(written) / float64(done) * float64(total))
	}

	d.mu.Lock()
	var dur float64
	if d.video != nil {
		dur = d.video.Duration
	} else if d.audio != nil {
		dur = d.audio.Duration
	}
	label := d.label
	d.mu.Unlock()

	return Stats{
		State:         d.State(),
		Downloaded:    written,
		Total:         est,
		SpeedBPS:      d.rate(),
		Conns:         int(d.conns.Load()),
		SegmentsDone:  done,
		SegmentsTotal: total,
		Duration:      dur,
		Variant:       label,
		Kind:          "dash",
		Err:           errStr,
	}
}

// Run downloads the manifest and blocks until it finishes, fails or pauses.
func (d *Download) Run(ctx context.Context) error {
	d.paused.Store(false)
	ctx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()
	defer cancel()

	d.state.Store(StateProbing)

	f := &Fetcher{
		Client:      d.Client,
		Headers:     d.Headers,
		UserAgent:   d.UserAgent,
		Limiter:     d.Limiter,
		Concurrency: d.Concurrency,
		MaxRetries:  d.MaxRetries,
		Backoff:     d.Backoff,
	}

	video, audio, err := f.LoadManifest(ctx, d.URL)
	if err != nil {
		return d.fail(err)
	}
	// Nothing on disk is playable until the two tracks are joined, so find
	// out now rather than after a gigabyte has been spent.
	if video != nil && audio != nil && !haveFFmpeg() {
		return d.fail(ErrNoFFmpeg)
	}
	// A manifest with only an audio adaptation set is a soundtrack, not a
	// video, but it is still a thing the user asked to download.
	if video == nil {
		video, audio = audio, nil
	}

	d.mu.Lock()
	d.video, d.audio = video, audio
	d.label = video.Label()
	d.mu.Unlock()

	total := len(video.Segments)
	if audio != nil {
		total += len(audio.Segments)
	}
	d.segTotal.Store(int64(total))

	if d.Path == "" {
		p, err := d.resolvePath()
		if err != nil {
			return d.fail(err)
		}
		d.Path = p
	}

	videoPart := d.Path + ".v.part"
	audioPart := d.Path + ".a.part"

	sc := d.restore()
	d.segDone.Store(int64(sc.VideoDone + sc.AudioDone))
	d.written.Store(sc.VideoBytes + sc.AudioBytes)

	d.state.Store(StateDownloading)
	d.resetRate()

	// The video first, then the audio, each with the full connection budget
	// to itself. Interleaving them would halve the parallelism on the track
	// that dominates the transfer for no gain in wall-clock time.
	if err := d.fetchTrack(ctx, f, video, videoPart, 0, &sc, true); err != nil {
		return d.stopOrFail(err, &sc)
	}
	if audio != nil {
		if err := d.fetchTrack(ctx, f, audio, audioPart,
			len(video.Segments), &sc, false); err != nil {
			return d.stopOrFail(err, &sc)
		}
	}

	// Joining a long film reports no byte progress, so give it a state of
	// its own rather than letting the UI show a download stalled at 100%.
	d.state.Store(StateRemuxing)
	d.emit()

	second := audioPart
	if audio == nil {
		second = ""
	}
	if err := mux(ctx, videoPart, second, d.Path); err != nil {
		return d.fail(err)
	}

	os.Remove(videoPart)
	os.Remove(audioPart)
	os.Remove(d.Path + sidecarSuffix)

	d.state.Store(StateDone)
	d.emit()
	return nil
}

// fetchTrack downloads one track into its own part file.
//
// doneBefore is how many segments of the track that ran before this one are
// already counted, so the two add up to the single figure the UI shows.
func (d *Download) fetchTrack(ctx context.Context, f *Fetcher, t *Track,
	partPath string, doneBefore int, sc *sidecar, isVideo bool) error {

	skip, bytesBefore := trackProgress(sc, isVideo)
	if skip >= len(t.Segments) {
		return nil // this track finished before the pause
	}

	// Everything banked for the other track. This track's own contribution
	// is re-derived below, so counting it here as well would double it --
	// which is exactly what a resumed download used to report.
	otherBytes := d.written.Load() - bytesBefore

	file, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Resuming appends after the bytes we already trust; a fresh start
	// truncates whatever was there, and forgets what the sidecar claimed.
	off := bytesBefore
	if skip == 0 {
		off = 0
		if err := file.Truncate(0); err != nil {
			return err
		}
	}
	if _, err := file.Seek(off, 0); err != nil {
		return err
	}
	setTrackProgress(sc, isVideo, skip, off)
	d.written.Store(otherBytes + off)

	f.OnProgress = func(p Progress) {
		setTrackProgress(sc, isVideo, p.SegmentsDone, off+p.BytesWritten)
		d.segDone.Store(int64(doneBefore + p.SegmentsDone))
		d.conns.Store(int32(p.Conns))
		d.written.Store(otherBytes + off + p.BytesWritten)
		d.sample()
		d.saveSidecar(sc)
		d.emit()
	}

	if err := f.Fetch(ctx, t, file, skip); err != nil {
		return err
	}
	return file.Sync()
}

// trackProgress reads one track's entry out of the sidecar.
func trackProgress(sc *sidecar, isVideo bool) (done int, bytes int64) {
	if isVideo {
		return sc.VideoDone, sc.VideoBytes
	}
	return sc.AudioDone, sc.AudioBytes
}

func setTrackProgress(sc *sidecar, isVideo bool, done int, bytes int64) {
	if isVideo {
		sc.VideoDone, sc.VideoBytes = done, bytes
		return
	}
	sc.AudioDone, sc.AudioBytes = done, bytes
}

// stopOrFail turns a cancelled fetch into a pause and anything else into a
// failure. A paused download keeps its part files; that is the whole point.
func (d *Download) stopOrFail(err error, sc *sidecar) error {
	if d.paused.Load() || errors.Is(err, context.Canceled) {
		d.state.Store(StatePaused)
		d.saveSidecar(sc)
		d.emit()
		return ErrPaused
	}
	return d.fail(err)
}

// resolvePath picks the output filename. Everything this engine produces is
// an MP4, because that is what the mux writes.
func (d *Download) resolvePath() (string, error) {
	name := d.Filename
	if name == "" {
		name = nameFromURL(d.URL)
	}
	if e := path.Ext(name); e == ".mpd" || e == "" {
		name = strings.TrimSuffix(name, e) + ".mp4"
	}
	dir := d.Dir
	if dir == "" {
		dir = "."
	}
	return uniquePath(dir, sanitize(name))
}

// nameFromURL guesses a title from the manifest URL. Manifests are usually
// called index.mpd or manifest.mpd, in which case the parent directory is
// the more useful name.
func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "video"
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	base := ""
	if len(segs) > 0 {
		base = segs[len(segs)-1]
	}
	stem := strings.TrimSuffix(base, ".mpd")
	generic := map[string]bool{
		"index": true, "manifest": true, "master": true, "stream": true,
		"index_web": true, "playlist": true, "": true,
	}
	if generic[strings.ToLower(stem)] && len(segs) > 1 {
		stem = segs[len(segs)-2]
	}
	if stem == "" {
		stem = "video"
	}
	return stem
}

// restore reads the sidecar, which is how a paused download knows where it
// was. A sidecar for a different URL is ignored: the file it describes is
// not the one being asked for now.
func (d *Download) restore() sidecar {
	fresh := sidecar{Version: 1, URL: d.URL}
	b, err := os.ReadFile(d.Path + sidecarSuffix)
	if err != nil {
		return fresh
	}
	var sc sidecar
	if json.Unmarshal(b, &sc) != nil || sc.URL != d.URL || sc.Version != 1 {
		return fresh
	}
	// A part file that went missing takes its progress with it.
	if !fileAtLeast(d.Path+".v.part", sc.VideoBytes) {
		return fresh
	}
	if sc.AudioDone > 0 && !fileAtLeast(d.Path+".a.part", sc.AudioBytes) {
		sc.AudioDone, sc.AudioBytes = 0, 0
	}
	return sc
}

// fileAtLeast reports whether a part file still holds the bytes the sidecar
// claims. Anything shorter has been truncated or replaced, and appending to
// it would produce a corrupt track.
func fileAtLeast(p string, n int64) bool {
	if n == 0 {
		return true
	}
	st, err := os.Stat(p)
	return err == nil && st.Size() >= n
}

func (d *Download) saveSidecar(sc *sidecar) {
	sc.Version = 1
	sc.URL = d.URL
	d.mu.Lock()
	sc.Variant = d.label
	d.mu.Unlock()
	b, err := json.Marshal(sc)
	if err != nil {
		return
	}
	os.WriteFile(d.Path+sidecarSuffix, b, 0o644)
}

func (d *Download) fail(err error) error {
	d.state.Store(StateError)
	d.errMsg.Store(err.Error())
	d.emit()
	return err
}

func (d *Download) emit() {
	if d.OnUpdate != nil {
		d.OnUpdate(d.Stats())
	}
}

func (d *Download) resetRate() {
	d.speedMu.Lock()
	d.lastBytes = d.written.Load()
	d.lastTime = time.Now()
	d.ema = 0
	d.speedMu.Unlock()
}

// sample folds the latest byte count into a smoothed rate. Segment
// downloads land in bursts, so an instantaneous figure swings wildly.
func (d *Download) sample() {
	now := time.Now()
	d.speedMu.Lock()
	defer d.speedMu.Unlock()
	if d.lastTime.IsZero() {
		d.lastTime, d.lastBytes = now, d.written.Load()
		return
	}
	elapsed := now.Sub(d.lastTime).Seconds()
	if elapsed < 0.25 {
		return
	}
	cur := d.written.Load()
	rate := float64(cur-d.lastBytes) / elapsed
	if d.ema == 0 {
		d.ema = rate
	} else {
		d.ema = 0.7*d.ema + 0.3*rate
	}
	d.lastBytes, d.lastTime = cur, now
}

func (d *Download) rate() float64 {
	d.speedMu.Lock()
	defer d.speedMu.Unlock()
	return d.ema
}
