package hls

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

// State mirrors the byte-range engine's states so the UI can treat both
// kinds of download the same way.
type State string

const (
	StateQueued      State = "queued"
	StateProbing     State = "probing"
	StateDownloading State = "downloading"
	StatePaused      State = "paused"
	StateDone        State = "done"
	StateError       State = "error"
)

// ErrPaused is a clean stop, not a failure.
var ErrPaused = errors.New("download paused")

// ErrLive is returned for a playlist with no EXT-X-ENDLIST. Such a stream
// has no end, so "download the file" is not a well-defined request.
var ErrLive = errors.New("this is a live stream, which has no fixed end")

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

// Download fetches one HLS playlist to one file.
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

	// Remux converts the finished .ts into .mp4 when ffmpeg is on PATH.
	// Players are much happier with MP4, and it is a stream copy, not a
	// re-encode.
	Remux bool

	// PickVariant chooses a quality level; nil takes the highest bandwidth.
	PickVariant func(*Master) Variant

	OnUpdate func(Stats)

	Path string // resolved output path

	mu       sync.Mutex
	media    *Media
	variant  string
	cancel   context.CancelFunc
	paused   atomic.Bool
	state    atomic.Value
	errMsg   atomic.Value
	segDone  atomic.Int64
	segTotal atomic.Int64
	written  atomic.Int64
	conns    atomic.Int32

	speedMu   sync.Mutex
	lastBytes int64
	lastTime  time.Time
	ema       float64
}

const sidecarSuffix = ".dmh"

// sidecar records how far a playlist download got, so it can resume.
type sidecar struct {
	Version       int    `json:"version"`
	URL           string `json:"url"`
	Variant       string `json:"variant,omitempty"`
	SegmentsDone  int    `json:"segmentsDone"`
	SegmentsTotal int    `json:"segmentsTotal"`
	BytesWritten  int64  `json:"bytesWritten"`
	HasInit       bool   `json:"hasInit"`
}

// NewDownload prepares a playlist download.
func NewDownload(id, rawURL string) *Download {
	d := &Download{ID: id, URL: rawURL, Remux: true}
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

	var dur float64
	d.mu.Lock()
	if d.media != nil {
		dur = d.media.Duration()
	}
	variant := d.variant
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
		Variant:       variant,
		Kind:          "hls",
		Err:           errStr,
	}
}

// Run downloads the playlist and blocks until it finishes, fails or pauses.
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
	}

	media, master, err := f.LoadPlaylist(ctx, d.URL, d.PickVariant)
	if err != nil {
		return d.fail(err)
	}
	if !media.EndList {
		return d.fail(ErrLive)
	}

	d.mu.Lock()
	d.media = media
	if master != nil {
		v := master.Best()
		if d.PickVariant != nil {
			v = d.PickVariant(master)
		}
		d.variant = v.Label()
	}
	d.mu.Unlock()
	d.segTotal.Store(int64(len(media.Segments)))

	if d.Path == "" {
		p, err := d.resolvePath(media)
		if err != nil {
			return d.fail(err)
		}
		d.Path = p
	}

	skip, err := d.restore(media)
	if err != nil {
		return d.fail(err)
	}
	d.segDone.Store(int64(skip))

	file, err := os.OpenFile(d.Path, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return d.fail(err)
	}
	// Resuming appends after the bytes we already trust; a fresh start
	// truncates whatever was there.
	off := int64(0)
	if skip > 0 {
		off = d.written.Load()
	} else {
		if err := file.Truncate(0); err != nil {
			file.Close()
			return d.fail(err)
		}
	}
	if _, err := file.Seek(off, 0); err != nil {
		file.Close()
		return d.fail(err)
	}

	d.state.Store(StateDownloading)
	d.resetRate()

	f.OnProgress = func(p Progress) {
		d.segDone.Store(int64(p.SegmentsDone))
		d.conns.Store(int32(p.Conns))
		d.written.Store(off + p.BytesWritten)
		d.sample()
		d.saveSidecar(media, p.SegmentsDone)
		d.emit()
	}

	ferr := f.Fetch(ctx, media, file, skip)
	syncErr := file.Sync()
	file.Close()

	if ferr != nil {
		if d.paused.Load() || errors.Is(ferr, context.Canceled) {
			d.state.Store(StatePaused)
			d.saveSidecar(media, int(d.segDone.Load()))
			d.emit()
			return ErrPaused
		}
		return d.fail(ferr)
	}
	if syncErr != nil {
		return d.fail(syncErr)
	}

	os.Remove(d.Path + sidecarSuffix)

	if d.Remux {
		if newPath, err := remux(ctx, d.Path); err == nil && newPath != "" {
			d.Path = newPath
		} else if err != nil {
			// A failed remux is not a failed download: the .ts plays.
			d.errMsg.Store("saved as .ts (remux failed: " + err.Error() + ")")
		}
	}

	d.state.Store(StateDone)
	d.emit()
	return nil
}

// resolvePath picks the output filename. fMP4 playlists already carry MP4
// data, so they get .mp4; everything else is MPEG-TS until remuxed.
func (d *Download) resolvePath(media *Media) (string, error) {
	name := d.Filename
	if name == "" {
		name = nameFromURL(d.URL)
	}
	ext := ".ts"
	if media.InitURI != "" {
		ext = ".mp4"
	}
	if e := path.Ext(name); e == ".m3u8" || e == ".m3u" || e == "" {
		name = strings.TrimSuffix(name, e) + ext
	}
	dir := d.Dir
	if dir == "" {
		dir = "."
	}
	return uniquePath(dir, sanitize(name))
}

// nameFromURL guesses a title from the playlist URL. Playlist files are
// usually called index.m3u8 or master.m3u8, in which case the parent
// directory is the more useful name.
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
	stem := strings.TrimSuffix(strings.TrimSuffix(base, ".m3u8"), ".m3u")
	generic := map[string]bool{
		"index": true, "master": true, "playlist": true,
		"manifest": true, "prog_index": true, "chunklist": true, "": true,
	}
	if generic[strings.ToLower(stem)] && len(segs) > 1 {
		stem = segs[len(segs)-2]
	}
	if stem == "" {
		stem = "video"
	}
	return stem
}

// restore reads the sidecar and reports how many segments to skip.
func (d *Download) restore(media *Media) (int, error) {
	b, err := os.ReadFile(d.Path + sidecarSuffix)
	if err != nil {
		return 0, nil // nothing to resume
	}
	var sc sidecar
	if json.Unmarshal(b, &sc) != nil || sc.Version != 1 {
		return 0, nil
	}
	// A playlist whose length changed is a different playlist as far as
	// resuming is concerned; starting over beats producing a spliced file.
	if sc.URL != d.URL || sc.SegmentsTotal != len(media.Segments) {
		return 0, nil
	}
	if sc.SegmentsDone <= 0 || sc.SegmentsDone > len(media.Segments) {
		return 0, nil
	}

	st, err := os.Stat(d.Path)
	if err != nil || st.Size() < sc.BytesWritten {
		return 0, nil // the file is not what the sidecar describes
	}
	// Drop anything written past the last segment boundary we recorded: a
	// partially written segment would corrupt the stream.
	if st.Size() > sc.BytesWritten {
		if err := os.Truncate(d.Path, sc.BytesWritten); err != nil {
			return 0, err
		}
	}
	d.written.Store(sc.BytesWritten)
	return sc.SegmentsDone, nil
}

func (d *Download) saveSidecar(media *Media, done int) {
	sc := sidecar{
		Version:       1,
		URL:           d.URL,
		SegmentsDone:  done,
		SegmentsTotal: len(media.Segments),
		BytesWritten:  d.written.Load(),
		HasInit:       media.InitURI != "",
	}
	d.mu.Lock()
	sc.Variant = d.variant
	d.mu.Unlock()

	b, err := json.Marshal(sc)
	if err != nil {
		return
	}
	tmp := d.Path + sidecarSuffix + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, d.Path+sidecarSuffix)
	}
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

// --- speed metering ---------------------------------------------------------

func (d *Download) resetRate() {
	d.speedMu.Lock()
	d.lastBytes, d.lastTime, d.ema = d.written.Load(), time.Now(), 0
	d.speedMu.Unlock()
}

func (d *Download) sample() {
	d.speedMu.Lock()
	defer d.speedMu.Unlock()
	now := time.Now()
	if d.lastTime.IsZero() {
		d.lastBytes, d.lastTime = d.written.Load(), now
		return
	}
	dt := now.Sub(d.lastTime).Seconds()
	if dt < 0.25 {
		return
	}
	cur := d.written.Load()
	inst := float64(cur-d.lastBytes) / dt
	if inst < 0 {
		inst = 0
	}
	d.ema = 0.3*inst + 0.7*d.ema
	d.lastBytes, d.lastTime = cur, now
}

func (d *Download) rate() float64 {
	d.speedMu.Lock()
	defer d.speedMu.Unlock()
	return d.ema
}
