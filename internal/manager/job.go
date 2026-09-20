package manager

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/marXus-3D/odm/internal/dash"
	"github.com/marXus-3D/odm/internal/engine"
	"github.com/marXus-3D/odm/internal/hls"
	"github.com/marXus-3D/odm/internal/store"
)

// job is what the queue actually runs. Byte-range downloads, HLS playlists
// and DASH manifests have different innards but the same lifecycle, so the
// manager only deals with this.
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

// dashJob adapts a manifest download.
type dashJob struct{ d *dash.Download }

func (j dashJob) Run(ctx context.Context) error { return j.d.Run(ctx) }
func (j dashJob) Pause()                        { j.d.Pause() }
func (j dashJob) OutPath() string               { return j.d.Path }
func (j dashJob) Snapshot() any                 { return j.d.Stats() }

func (j dashJob) Summary() (int64, int64, string) {
	s := j.d.Stats()
	return s.Downloaded, s.Total, string(s.State)
}

// Kind labels how a download will be fetched.
const (
	KindFile = "file"
	KindHLS  = "hls"
	KindDASH = "dash"
)

// isStream reports whether a kind is one of the segmented formats, where
// the URL names a document listing the media rather than the media itself.
// The two want the same handling for naming, sizing and categorising.
func isStream(kind string) bool {
	return kind == KindHLS || kind == KindDASH
}

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
	q := strings.ToLower(u.RawQuery)
	if hasExt(p, ".m3u8") || hasExt(p, ".m3u") {
		return KindHLS
	}
	// Some CDNs put the playlist in a query parameter instead.
	if hasExt(q, ".m3u8") {
		return KindHLS
	}
	// DASH is a refusal rather than a fallback, so a loose match here costs
	// the user a download that would otherwise have worked. Match only at a
	// boundary: ".mpd" must not be the start of a longer word.
	if hasExt(p, ".mpd") || hasExt(q, ".mpd") {
		return KindDASH
	}
	return KindFile
}

// hasExt reports whether s carries ext as an extension rather than as a
// fragment of some longer word.
//
// Not HasSuffix: the extension is regularly followed by something else,
// either a session parameter stuck on with a semicolon (/master.m3u8;s=abc)
// or more path after it (/master.m3u8/seg-1). Not Contains either, which
// would read "clip.mpdata" as a DASH manifest.
func hasExt(s, ext string) bool {
	for i := 0; i+len(ext) <= len(s); {
		j := strings.Index(s[i:], ext)
		if j < 0 {
			return false
		}
		end := i + j + len(ext)
		if end == len(s) || !isWordByte(s[end]) {
			return true
		}
		i += j + 1
	}
	return false
}

func isWordByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
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

	if rec.Kind == KindDASH {
		d := dash.NewDownload(rec.ID, rec.URL)
		d.Dir = rec.Dir
		d.Filename = rec.Filename
		d.Headers = rec.Headers
		d.Limiter = m.limiter
		d.Concurrency = rec.MaxConns
		d.Path = rec.Path
		d.OnUpdate = func(dash.Stats) { onUpdate() }
		return dashJob{d: d}
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
