package manager

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/marXus-3D/odm/internal/engine"
	"github.com/marXus-3D/odm/internal/hls"
	"github.com/marXus-3D/odm/internal/store"
)

// job is what the queue actually runs. Byte-range downloads and HLS playlist
// downloads have different innards but the same lifecycle, so the manager
// only deals with this.
type job interface {
	Run(ctx context.Context) error
	Pause()
	OutPath() string
	// Snapshot returns progress for the API. The concrete type differs by
	// kind, so the UI reads it defensively.
	Snapshot() any
	// Summary is the subset the download list needs.
	Summary() (downloaded, size int64, state string)
}

// rangeJob adapts the byte-range engine.
type rangeJob struct{ d *engine.Download }

func (j rangeJob) Run(ctx context.Context) error { return j.d.Run(ctx) }
func (j rangeJob) Pause()                        { j.d.Pause() }
func (j rangeJob) OutPath() string               { return j.d.Path }
func (j rangeJob) Snapshot() any                 { return j.d.Stats() }

func (j rangeJob) Summary() (int64, int64, string) {
	s := j.d.Stats()
	return s.Downloaded, s.Total, string(s.State)
}

// hlsJob adapts a playlist download.
type hlsJob struct{ d *hls.Download }

func (j hlsJob) Run(ctx context.Context) error { return j.d.Run(ctx) }
func (j hlsJob) Pause()                        { j.d.Pause() }
func (j hlsJob) OutPath() string               { return j.d.Path }
func (j hlsJob) Snapshot() any                 { return j.d.Stats() }

func (j hlsJob) Summary() (int64, int64, string) {
	s := j.d.Stats()
	return s.Downloaded, s.Total, string(s.State)
}

// Kind labels how a download will be fetched.
const (
	KindFile = "file"
	KindHLS  = "hls"
	KindDASH = "dash"
)

// ErrDASHUnsupported is returned rather than saving the manifest XML under a
// video filename, which is what treating a .mpd as a plain file would do.
var ErrDASHUnsupported = errors.New(
	"DASH (.mpd) streams are not supported yet; only HLS (.m3u8) playlists are")

// ErrNoFileBehindPage is for a page that plays video without serving one.
// There is nothing at the URL to fetch, so the request is refused with a
// reason instead of quietly saving the HTML of the page.
var ErrNoFileBehindPage = errors.New(
	"this is a video page, not a video file: the player assembles the " +
		"stream from separate audio and video parts, so there is no single " +
		"URL for ODM to fetch. Sites that serve an HLS (.m3u8) playlist do " +
		"work -- the extension offers those automatically")

// DetectKind guesses from the URL whether this is a streaming playlist.
//
// A cheap URL check is enough in practice: HLS URLs essentially always end
// in .m3u8, and the extension sends an explicit kind when it has sniffed the
// content type. Getting it wrong is not fatal either way -- the HLS loader
// rejects a body that is not a playlist, and a playlist fetched as a plain
// file would just save the text of the manifest.
func DetectKind(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return KindFile
	}
	p := strings.ToLower(u.Path)
	// Contains rather than HasSuffix: the extension is regularly followed by
	// something else, either a session parameter stuck on with a semicolon
	// (/master.m3u8;s=abc) or more path after it (/master.m3u8/seg-1).
	if strings.Contains(p, ".m3u8") || strings.Contains(p, ".m3u") {
		return KindHLS
	}
	// Some CDNs put the playlist in a query parameter instead.
	if strings.Contains(strings.ToLower(u.RawQuery), ".m3u8") {
		return KindHLS
	}
	if strings.Contains(p, ".mpd") || strings.Contains(strings.ToLower(u.RawQuery), ".mpd") {
		return KindDASH
	}
	return KindFile
}

// newJob builds the right kind of job for a record.
func (m *Manager) newJob(rec store.Record, onUpdate func()) job {
	if rec.Kind == KindHLS {
		d := hls.NewDownload(rec.ID, rec.URL)
		d.Dir = rec.Dir
		d.Filename = rec.Filename
		d.Headers = rec.Headers
		d.Limiter = m.limiter
		d.Concurrency = rec.MaxConns
		d.Path = rec.Path
		d.OnUpdate = func(hls.Stats) { onUpdate() }
		return hlsJob{d: d}
	}

	req := engine.Request{
		URL:      rec.URL,
		Headers:  rec.Headers,
		Filename: rec.Filename,
		Dir:      rec.Dir,
		MaxConns: rec.MaxConns,
	}
	d := engine.New(rec.ID, req, engine.Options{
		MaxConns: rec.MaxConns,
		Limiter:  m.limiter,
	})
	d.Path = rec.Path
	d.OnUpdate = func(engine.Stats) { onUpdate() }
	return rangeJob{d: d}
}
