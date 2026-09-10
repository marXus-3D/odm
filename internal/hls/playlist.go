// Package hls parses M3U8 playlists and downloads the media they describe.
//
// This is the "download this video" case: streaming sites almost never serve
// one file, they serve a playlist of a few hundred small segments. Fetching
// them one at a time is slow for exactly the reasons a segmented download is
// fast, so the fetcher pulls many at once and writes them in order.
package hls

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Variant is one quality level from a master playlist.
type Variant struct {
	URI        string `json:"uri"`
	Bandwidth  int    `json:"bandwidth"`
	Resolution string `json:"resolution,omitempty"`
	Codecs     string `json:"codecs,omitempty"`
	Name       string `json:"name,omitempty"`
	AudioGroup string `json:"audioGroup,omitempty"`
}

// Label renders a variant the way a quality picker would show it.
func (v Variant) Label() string {
	parts := []string{}
	if v.Resolution != "" {
		parts = append(parts, v.Resolution)
	} else if v.Name != "" {
		parts = append(parts, v.Name)
	}
	if v.Bandwidth > 0 {
		parts = append(parts, fmt.Sprintf("%.1f Mbps", float64(v.Bandwidth)/1e6))
	}
	if len(parts) == 0 {
		return v.URI
	}
	return strings.Join(parts, " · ")
}

// Rendition is an alternative audio or subtitle track (EXT-X-MEDIA).
type Rendition struct {
	Type     string `json:"type"` // AUDIO, SUBTITLES, ...
	GroupID  string `json:"groupId"`
	Name     string `json:"name"`
	Language string `json:"language,omitempty"`
	URI      string `json:"uri,omitempty"`
	Default  bool   `json:"default"`
}

// Master is a playlist of playlists.
type Master struct {
	Variants   []Variant
	Renditions []Rendition
}

// Key describes segment encryption.
type Key struct {
	Method string // NONE, AES-128, SAMPLE-AES
	URI    string
	IV     []byte // nil means derive from the media sequence number
}

// Segment is one media chunk.
type Segment struct {
	URI      string
	Duration float64
	Key      *Key // nil when unencrypted
	Sequence uint64

	// ByteRange support: a segment may be a slice of a larger resource.
	HasRange    bool
	RangeLength int64
	RangeStart  int64
}

// Media is a playlist of segments.
type Media struct {
	TargetDuration float64
	MediaSequence  uint64
	Segments       []Segment
	InitURI        string // EXT-X-MAP, the fMP4 initialization segment
	InitKey        *Key

	// The init segment may itself be a byte range of a larger file, which is
	// how Apple's own fMP4 examples are packaged: every segment, init
	// included, is a slice of one main.mp4.
	InitHasRange    bool
	InitRangeLength int64
	InitRangeStart  int64
	EndList         bool // false means a live stream, which has no fixed end
	Type            string
}

// Duration is the total playlist length in seconds.
func (m *Media) Duration() float64 {
	var d float64
	for _, s := range m.Segments {
		d += s.Duration
	}
	return d
}

// IsMaster reports whether the bytes look like a master playlist.
func IsMaster(b []byte) bool {
	return strings.Contains(string(b), "#EXT-X-STREAM-INF")
}

// LooksLikePlaylist reports whether the bytes are an M3U8 at all. Content
// types are unreliable on media CDNs, so sniffing the body is the only
// dependable check.
func LooksLikePlaylist(b []byte) bool {
	s := strings.TrimSpace(string(b))
	return strings.HasPrefix(s, "#EXTM3U")
}

// ParseMaster reads a master playlist. base is used to resolve relative URIs.
func ParseMaster(r io.Reader, base *url.URL) (*Master, error) {
	m := &Master{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	var pending *Variant
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			attrs := parseAttrs(line[len("#EXT-X-STREAM-INF:"):])
			v := Variant{
				Bandwidth:  atoiSafe(firstOf(attrs, "AVERAGE-BANDWIDTH", "BANDWIDTH")),
				Resolution: attrs["RESOLUTION"],
				Codecs:     attrs["CODECS"],
				Name:       attrs["NAME"],
				AudioGroup: attrs["AUDIO"],
			}
			pending = &v

		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			attrs := parseAttrs(line[len("#EXT-X-MEDIA:"):])
			rd := Rendition{
				Type:     attrs["TYPE"],
				GroupID:  attrs["GROUP-ID"],
				Name:     attrs["NAME"],
				Language: attrs["LANGUAGE"],
				Default:  strings.EqualFold(attrs["DEFAULT"], "YES"),
			}
			if u := attrs["URI"]; u != "" {
				rd.URI = resolve(base, u)
			}
			m.Renditions = append(m.Renditions, rd)

		case strings.HasPrefix(line, "#"):
			// Any other tag is not needed to pick a variant.

		default:
			if pending != nil {
				pending.URI = resolve(base, line)
				m.Variants = append(m.Variants, *pending)
				pending = nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(m.Variants) == 0 {
		return nil, fmt.Errorf("master playlist has no variant streams")
	}
	return m, nil
}

// ParseMedia reads a media playlist.
func ParseMedia(r io.Reader, base *url.URL) (*Media, error) {
	m := &Media{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	var (
		curKey   *Key
		duration float64
		hasRange bool
		rangeLen int64
		rangeOff int64
		nextOff  int64
		seq      uint64
		seqSet   bool
	)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			v := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.Index(v, ","); i >= 0 {
				v = v[:i]
			}
			duration, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)

		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			m.TargetDuration, _ = strconv.ParseFloat(
				strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64)

		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			n, _ := strconv.ParseUint(
				strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")), 10, 64)
			m.MediaSequence, seq, seqSet = n, n, true

		case strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE:"):
			m.Type = strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-PLAYLIST-TYPE:"))

		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			k, err := parseKey(line[len("#EXT-X-KEY:"):], base)
			if err != nil {
				return nil, err
			}
			curKey = k

		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs := parseAttrs(line[len("#EXT-X-MAP:"):])
			if u := attrs["URI"]; u != "" {
				m.InitURI = resolve(base, u)
				m.InitKey = curKey
				// BYTERANGE here is an attribute, not the EXT-X-BYTERANGE
				// tag. Ignoring it would download the whole container as the
				// initialization segment.
				if br := attrs["BYTERANGE"]; br != "" {
					m.InitRangeLength, m.InitRangeStart = parseByteRange(br, 0)
					m.InitHasRange = m.InitRangeLength > 0
				}
			}

		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			hasRange = true
			rangeLen, rangeOff = parseByteRange(
				strings.TrimPrefix(line, "#EXT-X-BYTERANGE:"), nextOff)

		case line == "#EXT-X-ENDLIST":
			m.EndList = true

		case strings.HasPrefix(line, "#"):
			// Unhandled tag.

		default:
			if !seqSet {
				seqSet = true
			}
			s := Segment{
				URI:      resolve(base, line),
				Duration: duration,
				Key:      curKey,
				Sequence: seq,
			}
			if hasRange {
				s.HasRange = true
				s.RangeLength = rangeLen
				s.RangeStart = rangeOff
				nextOff = rangeOff + rangeLen
			} else {
				nextOff = 0
			}
			m.Segments = append(m.Segments, s)
			seq++
			duration, hasRange, rangeLen, rangeOff = 0, false, 0, 0
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(m.Segments) == 0 {
		return nil, fmt.Errorf("media playlist has no segments")
	}
	return m, nil
}

// parseKey reads an EXT-X-KEY attribute list.
func parseKey(s string, base *url.URL) (*Key, error) {
	attrs := parseAttrs(s)
	method := strings.ToUpper(attrs["METHOD"])
	if method == "" || method == "NONE" {
		return nil, nil // encryption explicitly turned off from here on
	}
	k := &Key{Method: method}
	if u := attrs["URI"]; u != "" {
		k.URI = resolve(base, u)
	}
	if iv := attrs["IV"]; iv != "" {
		iv = strings.TrimPrefix(strings.TrimPrefix(iv, "0x"), "0X")
		b, err := hex.DecodeString(iv)
		if err != nil {
			return nil, fmt.Errorf("bad IV %q: %w", iv, err)
		}
		k.IV = b
	}
	return k, nil
}

// parseByteRange reads "length[@offset]"; a missing offset continues from
// the end of the previous segment.
func parseByteRange(s string, prevEnd int64) (length, start int64) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "@"); i >= 0 {
		length, _ = strconv.ParseInt(s[:i], 10, 64)
		start, _ = strconv.ParseInt(s[i+1:], 10, 64)
		return length, start
	}
	length, _ = strconv.ParseInt(s, 10, 64)
	return length, prevEnd
}

// parseAttrs reads an HLS attribute list: KEY=VALUE pairs separated by
// commas, where a quoted value may itself contain commas.
func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	var key, val strings.Builder
	inKey, inQuote := true, false

	flush := func() {
		k := strings.TrimSpace(key.String())
		if k != "" {
			out[strings.ToUpper(k)] = strings.TrimSpace(val.String())
		}
		key.Reset()
		val.Reset()
		inKey = true
	}

	for _, r := range s {
		switch {
		case inKey && r == '=':
			inKey = false
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			flush()
		case inKey:
			key.WriteRune(r)
		default:
			val.WriteRune(r)
		}
	}
	flush()
	return out
}

// resolve turns a possibly relative playlist URI into an absolute one.
func resolve(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if base == nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// Best returns the highest-bandwidth variant, which is what a "download this
// video" action should pick unless the user says otherwise.
func (m *Master) Best() Variant {
	best := m.Variants[0]
	for _, v := range m.Variants[1:] {
		if v.Bandwidth > best.Bandwidth {
			best = v
		}
	}
	return best
}
