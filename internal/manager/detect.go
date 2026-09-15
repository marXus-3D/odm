package manager

import (
	"bytes"
	"mime"
	"net/url"
	"path"
	"strings"

	"github.com/marXus-3D/odm/internal/engine"
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
	if looksLikeToken(name) {
		return ""
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

// titleAsName turns a page title into a filename, or "" when there is
// nothing usable in it. The browser strips the worst of it already, but the
// API and the command line are callers too.
func titleAsName(title string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		return ""
	}
	s = trimSiteSuffix(s)
	s = engine.SanitizeFilename(s)
	if s == "download" { // what SanitizeFilename returns for nothing usable
		return ""
	}
	// A title is prose, so it can be long. Leave room for " (2)" and an
	// extension without running into the path limit.
	if len(s) > 120 {
		s = strings.TrimSpace(s[:120])
	}
	if s == "" {
		return ""
	}
	return s
}

// trimSiteSuffix drops the site's own name off the end of a page title.
// "Frieren Episode 12 | AnimeSite" is a title and a brand, and only the
// first half belongs in a filename.
//
// The browser does this too, before sending the title, but the API and the
// command line are callers as well and they send it raw.
func trimSiteSuffix(s string) string {
	for _, sep := range []string{" | ", " — ", " – ", " - "} {
		// At least a few characters have to survive, or a title like
		// "S1 - The Beginning" would be cut down to nothing useful.
		if i := strings.Index(s, sep); i >= 8 {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

// extensionOf returns the extension of a name, dot included, or "".
func extensionOf(name string) string {
	ext := path.Ext(name)
	if len(ext) > 1 && len(ext) <= 8 {
		return ext
	}
	return ""
}

// looksLikeToken reports whether a name is machine-generated rather than
// something a person would recognise: a base64 blob, a hash, a UUID. These
// turn up as the last path segment of every signed URL, and using one as a
// filename is barely better than using nothing.
func looksLikeToken(name string) bool {
	stem := strings.TrimSuffix(name, path.Ext(name))
	if len(stem) < 20 {
		return false
	}
	// A title has spaces or punctuation a generator would not emit.
	if strings.ContainsAny(stem, " ()[]'!,&") {
		return false
	}
	var digits, letters, upper, other int
	for _, r := range stem {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z':
			letters++
		case r >= 'A' && r <= 'Z':
			letters++
			upper++
		case r == '-' || r == '_' || r == '=' || r == '+' || r == '.' || r == '%':
			other++
		default:
			// Anything else -- a letter with an accent, a CJK character --
			// says this is a real title.
			return false
		}
	}
	if digits+letters+other != len([]rune(stem)) {
		return false
	}
	// A UUID: hex and dashes, nothing else.
	if isHexish(stem) {
		return true
	}
	// Base64 and friends: a long run with digits mixed in and, usually, both
	// cases present. Real titles that long are words, which have neither.
	return digits > 0 && (upper > 0 || digits*4 >= len(stem))
}

// isHexish reports whether a string is only hex digits and dashes, which is
// what a UUID or a checksum looks like.
func isHexish(s string) bool {
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		case r == '-':
		default:
			return false
		}
	}
	return digits > 0
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
