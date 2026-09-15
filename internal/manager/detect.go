package manager

import (
	"bytes"
	"mime"
	"net/url"
	"strings"

	"github.com/marXus-3D/odm/internal/hls"
)

// Telling a video from a playlist that lists one.
//
// DetectKind reads the URL, which is free but often wrong: a streaming site
// signs its playlist URL and the .m3u8 disappears into a query parameter, a
// path segment with a semicolon after it, or nothing at all. Downloading a
// playlist as a file saves a couple of kilobytes of text named .m3u8 and
// calls it done, which is exactly what it used to do.
//
// So the answer is taken from the response instead, once the probe has one:
// the content type when it is one of the playlist types, and failing that
// the first bytes of the body, which is the only thing a CDN cannot get
// wrong.

// playlistTypes are the content types that mean HLS. Servers disagree on
// which of them is correct, so all of them are accepted.
var playlistTypes = map[string]bool{
	"application/vnd.apple.mpegurl": true,
	"application/x-mpegurl":         true,
	"application/mpegurl":           true,
	"audio/mpegurl":                 true,
	"audio/x-mpegurl":               true,
	"video/x-mpegurl":               true,
	"vnd.apple.mpegurl":             true,
}

// dashTypes are the content types that mean a DASH manifest.
var dashTypes = map[string]bool{
	"application/dash+xml":      true,
	"video/vnd.mpeg.dash.mpd":   true,
	"application/vnd.mpeg.dash": true,
}

// KindFromResponse classifies what the server actually sent. It returns ""
// when the response is an ordinary file, or looks like one.
func KindFromResponse(contentType string, head []byte) string {
	ct := contentType
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		ct = parsed
	} else if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))

	switch {
	case playlistTypes[ct]:
		return KindHLS
	case dashTypes[ct]:
		return KindDASH
	}

	// The body is the last word. A playlist always opens with #EXTM3U, and
	// nothing else does.
	if hls.LooksLikePlaylist(head) {
		return KindHLS
	}
	if looksLikeDASH(head) {
		return KindDASH
	}
	return ""
}

// looksLikeDASH reports whether the bytes open an MPEG-DASH manifest. The
// namespace is what distinguishes it from any other XML.
func looksLikeDASH(head []byte) bool {
	// Only the opening tag matters, and a manifest starts with either the
	// XML declaration or the element itself.
	b := head
	if len(b) > 1024 {
		b = b[:1024]
	}
	lower := bytes.ToLower(b)
	if !bytes.Contains(lower, []byte("<mpd")) {
		return false
	}
	return bytes.Contains(lower, []byte("urn:mpeg:dash")) ||
		bytes.Contains(lower, []byte("mediapresentationduration")) ||
		bytes.Contains(lower, []byte("<period"))
}

// playlistName decides what to call an HLS download whose name was taken
// from the playlist itself. "master.m3u8" and "index.m3u8" say nothing about
// the video, and nor does the name of a signed endpoint, so in those cases
// the name is dropped and the HLS engine names the file from the URL it
// really came from.
func playlistName(name, rawURL string) string {
	if name == "" {
		return ""
	}
	stem := strings.ToLower(name)
	if i := strings.LastIndex(stem, "."); i > 0 {
		ext := stem[i+1:]
		stem = stem[:i]
		// A name that is only the playlist's own filename is no name.
		if ext == "m3u8" || ext == "m3u" {
			switch stem {
			case "master", "index", "playlist", "manifest", "media", "main",
				"chunklist", "prog_index", "hls", "video", "stream":
				return ""
			}
			return name
		}
	}
	// No extension at all means the name came off a signed endpoint, where
	// the last path segment is a verb rather than a title.
	if !strings.Contains(name, ".") {
		switch stem {
		case "hls", "stream", "playlist", "manifest", "video", "master",
			"index", "cdn", "download", "dl", "get", "file", "binary":
			return ""
		}
	}
	return name
}

// looksLikeYouTube reports whether a URL is a YouTube watch page, which ODM
// cannot download: there is no file behind it, only a player that assembles
// one. Recognising it is worth doing so the user gets told that rather than
// a download that fails for no stated reason.
func looksLikeYouTube(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	switch host {
	case "youtube.com", "youtube-nocookie.com", "youtu.be", "music.youtube.com":
		return true
	}
	return false
}
