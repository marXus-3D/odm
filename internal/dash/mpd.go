// Package dash parses MPEG-DASH manifests and downloads the media they
// describe.
//
// DASH is the other half of the "download this video" case. Where HLS hands
// out a playlist of segments that already carry audio and video together,
// DASH almost always keeps the two apart: one list of segments for the
// picture, another for the sound, and the player is expected to feed both
// into the same decoder. Nothing on disk is watchable until they are put
// back together, which is why this package ends in a mux rather than a
// concatenation.
package dash

import (
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MPD is the root of a manifest.
//
// Only the parts that bear on downloading a finished video are modelled.
// Everything to do with adaptive playback -- buffering rules, bandwidth
// estimation, the many ways a live window can slide -- exists to help a
// player keep up in real time, and is beside the point when the whole thing
// is being fetched as fast as the network allows.
type MPD struct {
	XMLName                   xml.Name `xml:"MPD"`
	Type                      string   `xml:"type,attr"`
	MediaPresentationDuration string   `xml:"mediaPresentationDuration,attr"`
	BaseURL                   []string `xml:"BaseURL"`
	Periods                   []Period `xml:"Period"`
}

// Period is one contiguous stretch of content. Films and episodes are a
// single period; ad-stitched streams are several.
type Period struct {
	ID             string          `xml:"id,attr"`
	Duration       string          `xml:"duration,attr"`
	Start          string          `xml:"start,attr"`
	BaseURL        []string        `xml:"BaseURL"`
	AdaptationSets []AdaptationSet `xml:"AdaptationSet"`
}

// AdaptationSet groups the representations of one track: every quality of
// the video, or every bitrate of one audio language.
type AdaptationSet struct {
	MimeType          string              `xml:"mimeType,attr"`
	ContentType       string              `xml:"contentType,attr"`
	Lang              string              `xml:"lang,attr"`
	Codecs            string              `xml:"codecs,attr"`
	BaseURL           []string            `xml:"BaseURL"`
	SegmentTemplate   *SegmentTemplate    `xml:"SegmentTemplate"`
	SegmentList       *SegmentList        `xml:"SegmentList"`
	ContentProtection []ContentProtection `xml:"ContentProtection"`
	Representations   []Representation    `xml:"Representation"`
}

// Representation is one encoding of a track at one quality.
type Representation struct {
	ID                string              `xml:"id,attr"`
	Bandwidth         int                 `xml:"bandwidth,attr"`
	Width             int                 `xml:"width,attr"`
	Height            int                 `xml:"height,attr"`
	Codecs            string              `xml:"codecs,attr"`
	MimeType          string              `xml:"mimeType,attr"`
	AudioSamplingRate string              `xml:"audioSamplingRate,attr"`
	BaseURL           []string            `xml:"BaseURL"`
	SegmentTemplate   *SegmentTemplate    `xml:"SegmentTemplate"`
	SegmentList       *SegmentList        `xml:"SegmentList"`
	SegmentBase       *SegmentBase        `xml:"SegmentBase"`
	ContentProtection []ContentProtection `xml:"ContentProtection"`
}

// ContentProtection marks a track as encrypted. Its presence is the whole
// signal that matters here: the keys live behind a licence server, so a
// protected track cannot be turned into a playable file no matter how many
// segments are fetched.
type ContentProtection struct {
	SchemeIDURI string `xml:"schemeIdUri,attr"`
	Value       string `xml:"value,attr"`
}

// SegmentTemplate builds segment URLs from a pattern rather than listing
// them. This is how nearly every real manifest is written, because a
// two-hour film is thousands of segments and spelling them all out would
// make the manifest larger than some of the media.
type SegmentTemplate struct {
	Initialization         string           `xml:"initialization,attr"`
	Media                  string           `xml:"media,attr"`
	StartNumber            *int             `xml:"startNumber,attr"`
	Duration               float64          `xml:"duration,attr"`
	Timescale              float64          `xml:"timescale,attr"`
	PresentationTimeOffset float64          `xml:"presentationTimeOffset,attr"`
	Timeline               *SegmentTimeline `xml:"SegmentTimeline"`
}

// SegmentTimeline spells out the duration of each segment, which is what a
// manifest uses when they are not all the same length.
type SegmentTimeline struct {
	S []TimelineEntry `xml:"S"`
}

// TimelineEntry is a run of segments: one of duration D starting at time T,
// repeated R more times.
type TimelineEntry struct {
	T *uint64 `xml:"t,attr"`
	D uint64  `xml:"d,attr"`
	R *int    `xml:"r,attr"`
}

// SegmentList is the long-hand form: every segment named individually.
type SegmentList struct {
	Timescale      float64      `xml:"timescale,attr"`
	Duration       float64      `xml:"duration,attr"`
	Initialization *URLType     `xml:"Initialization"`
	SegmentURLs    []SegmentURL `xml:"SegmentURL"`
}

// SegmentURL is one entry of a SegmentList.
type SegmentURL struct {
	Media      string `xml:"media,attr"`
	MediaRange string `xml:"mediaRange,attr"`
}

// SegmentBase describes a track held in a single file, where the segments
// are byte ranges rather than separate requests.
type SegmentBase struct {
	IndexRange     string   `xml:"indexRange,attr"`
	Timescale      float64  `xml:"timescale,attr"`
	Initialization *URLType `xml:"Initialization"`
}

// URLType is the <Initialization> element, which carries either its own URL
// or a byte range into the representation's BaseURL.
type URLType struct {
	SourceURL string `xml:"sourceURL,attr"`
	Range     string `xml:"range,attr"`
}

// Segment is one media chunk to fetch.
type Segment struct {
	URI string

	// A segment may be a byte range of a larger file rather than a request
	// of its own, which is how SegmentBase and SegmentList byte-range
	// manifests are packaged.
	HasRange    bool
	RangeStart  int64
	RangeLength int64
}

// Track is one downloadable stream: the initialization segment that opens
// the container, and the media segments that follow it.
type Track struct {
	Kind      string // "video" or "audio"
	ID        string
	Codecs    string
	Lang      string
	Bandwidth int
	Width     int
	Height    int

	InitURI      string
	InitHasRange bool
	InitStart    int64
	InitLength   int64

	Segments []Segment
	Duration float64 // seconds, 0 when the manifest does not say
}

// Label renders a track the way a quality picker would show it.
func (t Track) Label() string {
	var parts []string
	if t.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dp", t.Height))
	} else if t.Lang != "" {
		parts = append(parts, t.Lang)
	}
	if t.Bandwidth > 0 {
		parts = append(parts, fmt.Sprintf("%.1f Mbps", float64(t.Bandwidth)/1e6))
	}
	if len(parts) == 0 {
		return t.ID
	}
	return strings.Join(parts, " · ")
}

// LooksLikeManifest reports whether the bytes are an MPD at all.
//
// Content types on media CDNs are not worth trusting -- the same server that
// serves a manifest as application/dash+xml will serve the next one as
// octet-stream -- so the body is the only dependable check.
func LooksLikeManifest(b []byte) bool {
	s := b
	if len(s) > 2048 {
		s = s[:2048]
	}
	lower := strings.ToLower(string(s))
	if !strings.Contains(lower, "<mpd") {
		return false
	}
	return strings.Contains(lower, "urn:mpeg:dash") ||
		strings.Contains(lower, "mediapresentationduration") ||
		strings.Contains(lower, "<period")
}

// ErrLive is returned for a manifest with no fixed end. A live edge keeps
// moving, so there is no such thing as having downloaded all of it.
var ErrLive = fmt.Errorf("this is a live stream, which has no fixed end")

// ErrProtected is returned for a DRM-protected manifest. Fetching the
// segments would work and produce a file that cannot be played, which is a
// worse outcome than saying so.
var ErrProtected = fmt.Errorf(
	"this stream is DRM protected, so the segments cannot be assembled into " +
		"a playable file")

// Parse reads a manifest. base is the URL it was fetched from, which every
// relative URL inside it resolves against.
func Parse(b []byte, base *url.URL) (*MPD, error) {
	var m MPD
	if err := xml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("not a valid MPD: %w", err)
	}
	if len(m.Periods) == 0 {
		return nil, fmt.Errorf("manifest has no periods")
	}
	return &m, nil
}

// Tracks turns a manifest into the best video track and the best audio
// track it offers.
//
// "Best" is the highest bandwidth, the same rule the HLS side uses: a
// "download this video" action that quietly picked 480p when 1080p was on
// offer would be answering a question nobody asked.
//
// Either may be nil. A muxed manifest -- one AdaptationSet carrying both --
// comes back as the video track alone, with no audio to mux in.
func (m *MPD) Tracks(base *url.URL) (video, audio *Track, err error) {
	if strings.EqualFold(m.Type, "dynamic") {
		return nil, nil, ErrLive
	}

	// Only the first period is taken. Multi-period manifests are ad breaks
	// stitched into the film, and joining them needs each period's tracks to
	// agree on codec and timescale, which they are under no obligation to
	// do. Better to download the feature than to produce a file that plays
	// an advert and then stops.
	p := m.Periods[0]

	total := duration(m.MediaPresentationDuration)
	if d := duration(p.Duration); d > 0 {
		total = d
	}

	mpdBase := resolveAll(base, m.BaseURL)
	periodBase := resolveAll(mpdBase, p.BaseURL)

	for i := range p.AdaptationSets {
		as := &p.AdaptationSets[i]
		kind := trackKind(as)
		if kind == "" {
			continue // subtitles, thumbnails, anything not audio or video
		}
		if isProtected(as.ContentProtection) {
			return nil, nil, ErrProtected
		}

		best := bestRepresentation(as)
		if best == nil {
			continue
		}
		if isProtected(best.ContentProtection) {
			return nil, nil, ErrProtected
		}

		t, terr := buildTrack(as, best, kind, periodBase, total)
		if terr != nil {
			return nil, nil, terr
		}
		switch kind {
		case "video":
			if video == nil || t.Bandwidth > video.Bandwidth {
				video = t
			}
		case "audio":
			if audio == nil || t.Bandwidth > audio.Bandwidth {
				audio = t
			}
		}
	}

	if video == nil && audio == nil {
		return nil, nil, fmt.Errorf("manifest has no audio or video tracks")
	}
	return video, audio, nil
}

// trackKind works out whether an adaptation set is video or audio.
//
// contentType is the field meant for this and is regularly absent, so
// mimeType is the fallback, and the codec string is the last resort: a set
// that says nothing but carries avc1 is video whatever it forgot to
// declare.
func trackKind(as *AdaptationSet) string {
	switch strings.ToLower(as.ContentType) {
	case "video":
		return "video"
	case "audio":
		return "audio"
	}
	mime := strings.ToLower(as.MimeType)
	switch {
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	}
	// Fall back to whatever the representations declare.
	for _, r := range as.Representations {
		rm := strings.ToLower(r.MimeType)
		switch {
		case strings.HasPrefix(rm, "video/"):
			return "video"
		case strings.HasPrefix(rm, "audio/"):
			return "audio"
		}
		if r.Width > 0 || r.Height > 0 {
			return "video"
		}
	}
	codecs := strings.ToLower(as.Codecs)
	switch {
	case strings.Contains(codecs, "avc"), strings.Contains(codecs, "hev"),
		strings.Contains(codecs, "hvc"), strings.Contains(codecs, "vp9"),
		strings.Contains(codecs, "av01"):
		return "video"
	case strings.Contains(codecs, "mp4a"), strings.Contains(codecs, "opus"),
		strings.Contains(codecs, "ac-3"), strings.Contains(codecs, "ec-3"):
		return "audio"
	}
	return ""
}

func isProtected(cp []ContentProtection) bool {
	for _, c := range cp {
		// The mp4protection scheme only says the container is encrypted in
		// the CENC sense; on its own it is often paired with a real system
		// id. Either way a protected track cannot be assembled, so any
		// entry other than an empty one counts.
		if strings.TrimSpace(c.SchemeIDURI) != "" {
			return true
		}
	}
	return false
}

// bestRepresentation picks the highest-bandwidth encoding of a track.
func bestRepresentation(as *AdaptationSet) *Representation {
	var best *Representation
	for i := range as.Representations {
		r := &as.Representations[i]
		if best == nil || r.Bandwidth > best.Bandwidth {
			best = r
		}
	}
	return best
}

// buildTrack expands whichever addressing scheme the manifest uses into a
// flat list of segments.
func buildTrack(as *AdaptationSet, r *Representation, kind string,
	base *url.URL, total float64) (*Track, error) {

	t := &Track{
		Kind:      kind,
		ID:        r.ID,
		Codecs:    firstNonEmpty(r.Codecs, as.Codecs),
		Lang:      as.Lang,
		Bandwidth: r.Bandwidth,
		Width:     r.Width,
		Height:    r.Height,
		Duration:  total,
	}

	// A representation's own BaseURL is relative to the adaptation set's,
	// which is relative to the period's.
	repBase := resolveAll(resolveAll(base, as.BaseURL), r.BaseURL)

	switch {
	case r.SegmentTemplate != nil || as.SegmentTemplate != nil:
		tpl := mergeTemplate(as.SegmentTemplate, r.SegmentTemplate)
		return t, expandTemplate(t, tpl, r, repBase, total)

	case r.SegmentList != nil || as.SegmentList != nil:
		list := r.SegmentList
		if list == nil {
			list = as.SegmentList
		}
		return t, expandList(t, list, repBase)

	case r.SegmentBase != nil:
		return t, expandBase(t, r.SegmentBase, repBase)

	default:
		// No addressing scheme at all means the representation's BaseURL is
		// the whole track, in one file.
		if repBase == nil {
			return nil, fmt.Errorf("representation %s has no segments and no URL", r.ID)
		}
		t.Segments = []Segment{{URI: repBase.String()}}
		return t, nil
	}
}

// mergeTemplate layers a representation's template over the adaptation
// set's, which is how a manifest says "the same as the set, but with this
// one attribute changed".
func mergeTemplate(set, rep *SegmentTemplate) *SegmentTemplate {
	if set == nil {
		return rep
	}
	if rep == nil {
		return set
	}
	out := *set
	if rep.Initialization != "" {
		out.Initialization = rep.Initialization
	}
	if rep.Media != "" {
		out.Media = rep.Media
	}
	if rep.StartNumber != nil {
		out.StartNumber = rep.StartNumber
	}
	if rep.Duration > 0 {
		out.Duration = rep.Duration
	}
	if rep.Timescale > 0 {
		out.Timescale = rep.Timescale
	}
	if rep.PresentationTimeOffset > 0 {
		out.PresentationTimeOffset = rep.PresentationTimeOffset
	}
	if rep.Timeline != nil {
		out.Timeline = rep.Timeline
	}
	return &out
}

// expandTemplate turns a SegmentTemplate into the list of URLs it stands
// for.
func expandTemplate(t *Track, tpl *SegmentTemplate, r *Representation,
	base *url.URL, total float64) error {

	if tpl.Media == "" {
		return fmt.Errorf("representation %s has a segment template with no media pattern", r.ID)
	}
	timescale := tpl.Timescale
	if timescale <= 0 {
		timescale = 1
	}
	start := 1
	if tpl.StartNumber != nil {
		start = *tpl.StartNumber
	}

	if tpl.Initialization != "" {
		t.InitURI = resolveRef(base, substitute(tpl.Initialization, r, start, 0))
	}

	switch {
	case tpl.Timeline != nil && len(tpl.Timeline.S) > 0:
		// The timeline spells out every run of segments, so the count and
		// each segment's start time come straight out of it.
		number := start
		var tNow uint64
		first := true
		for _, s := range tpl.Timeline.S {
			if s.T != nil {
				tNow = *s.T
			} else if first {
				tNow = 0
			}
			first = false
			repeat := 0
			if s.R != nil {
				repeat = *s.R
			}
			if repeat < 0 {
				// A negative r means "until the end of the period", which
				// only a live manifest writes. Work out how many fit.
				if total <= 0 || s.D == 0 {
					return fmt.Errorf("representation %s repeats segments without a period duration", r.ID)
				}
				end := uint64(total * timescale)
				repeat = 0
				for tNow+uint64(repeat+1)*s.D < end {
					repeat++
				}
			}
			for i := 0; i <= repeat; i++ {
				t.Segments = append(t.Segments, Segment{
					URI: resolveRef(base, substitute(tpl.Media, r, number, tNow)),
				})
				tNow += s.D
				number++
			}
		}

	case tpl.Duration > 0:
		// Every segment is the same length, so the count falls out of the
		// total running time.
		if total <= 0 {
			return fmt.Errorf("representation %s needs a period duration to count its segments", r.ID)
		}
		per := tpl.Duration / timescale
		if per <= 0 {
			return fmt.Errorf("representation %s has a zero segment duration", r.ID)
		}
		n := int(math.Ceil(total/per - 1e-9))
		if n <= 0 {
			return fmt.Errorf("representation %s works out to no segments", r.ID)
		}
		var tNow uint64
		for i := 0; i < n; i++ {
			t.Segments = append(t.Segments, Segment{
				URI: resolveRef(base, substitute(tpl.Media, r, start+i, tNow)),
			})
			tNow += uint64(tpl.Duration)
		}

	default:
		return fmt.Errorf("representation %s has neither a timeline nor a segment duration", r.ID)
	}
	return nil
}

// expandList reads a SegmentList, where the manifest names each segment.
func expandList(t *Track, list *SegmentList, base *url.URL) error {
	if list.Initialization != nil {
		if list.Initialization.SourceURL != "" {
			t.InitURI = resolveRef(base, list.Initialization.SourceURL)
		} else if base != nil {
			t.InitURI = base.String()
		}
		if rng := list.Initialization.Range; rng != "" {
			t.InitStart, t.InitLength = parseRange(rng)
			t.InitHasRange = t.InitLength > 0
		}
	}
	for _, s := range list.SegmentURLs {
		seg := Segment{URI: resolveRef(base, s.Media)}
		if s.Media == "" && base != nil {
			seg.URI = base.String()
		}
		if s.MediaRange != "" {
			seg.RangeStart, seg.RangeLength = parseRange(s.MediaRange)
			seg.HasRange = seg.RangeLength > 0
		}
		t.Segments = append(t.Segments, seg)
	}
	if len(t.Segments) == 0 {
		return fmt.Errorf("segment list is empty")
	}
	return nil
}

// expandBase handles a track held in one file.
//
// The segment index that would let this be fetched in pieces lives inside
// the file itself, in a sidx box, and reading it means a range request and a
// box parser for no gain: the file is one resource and the ordinary file
// engine could fetch it. Treating it as a single segment keeps that simple
// and correct.
func expandBase(t *Track, sb *SegmentBase, base *url.URL) error {
	if base == nil {
		return fmt.Errorf("a single-file representation needs a BaseURL")
	}
	if sb.Initialization != nil {
		t.InitURI = base.String()
		if sb.Initialization.SourceURL != "" {
			t.InitURI = resolveRef(base, sb.Initialization.SourceURL)
		}
		if rng := sb.Initialization.Range; rng != "" {
			t.InitStart, t.InitLength = parseRange(rng)
			t.InitHasRange = t.InitLength > 0
		}
	}
	// The whole file, init included, is the one thing to fetch. When the
	// init range was given separately it has already been written, so the
	// media starts after it.
	seg := Segment{URI: base.String()}
	if t.InitHasRange {
		seg.HasRange = true
		seg.RangeStart = t.InitStart + t.InitLength
		seg.RangeLength = math.MaxInt32 // to the end; the server clamps it
	}
	t.Segments = []Segment{seg}
	return nil
}

// substitute fills in the $...$ placeholders of a template.
//
// The format modifier ($Number%05d$) pads the value, which matters: a server
// that named its segments seg-00001.m4s will not answer to seg-1.m4s.
func substitute(pattern string, r *Representation, number int, timeVal uint64) string {
	var out strings.Builder
	for i := 0; i < len(pattern); {
		if pattern[i] != '$' {
			out.WriteByte(pattern[i])
			i++
			continue
		}
		end := strings.IndexByte(pattern[i+1:], '$')
		if end < 0 {
			out.WriteString(pattern[i:])
			break
		}
		token := pattern[i+1 : i+1+end]
		i += end + 2

		if token == "" {
			out.WriteByte('$') // "$$" is an escaped dollar
			continue
		}
		name, format := token, ""
		if j := strings.IndexByte(token, '%'); j >= 0 {
			name, format = token[:j], token[j:]
		}
		switch name {
		case "RepresentationID":
			out.WriteString(r.ID)
		case "Number":
			out.WriteString(formatNum(uint64(number), format))
		case "Time":
			out.WriteString(formatNum(timeVal, format))
		case "Bandwidth":
			out.WriteString(formatNum(uint64(r.Bandwidth), format))
		default:
			// An unknown placeholder is left as it was found, so the failure
			// is a 404 naming the pattern rather than a silent wrong URL.
			out.WriteString("$" + token + "$")
		}
	}
	return out.String()
}

// formatNum applies a printf-style width from a template placeholder.
func formatNum(v uint64, format string) string {
	if format == "" {
		return strconv.FormatUint(v, 10)
	}
	// Only the integer forms are legal here, and d is the only one in use.
	if !strings.HasSuffix(format, "d") {
		return strconv.FormatUint(v, 10)
	}
	return fmt.Sprintf(format, v)
}

// parseRange reads a "start-end" byte range into an offset and a length.
func parseRange(s string) (start, length int64) {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '-')
	if i < 0 {
		return 0, 0
	}
	start, _ = strconv.ParseInt(strings.TrimSpace(s[:i]), 10, 64)
	end, err := strconv.ParseInt(strings.TrimSpace(s[i+1:]), 10, 64)
	if err != nil || end < start {
		return start, 0
	}
	return start, end - start + 1
}

// duration reads an ISO 8601 duration, the form MPDs use for running times.
func duration(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || !strings.HasPrefix(s, "P") {
		return 0
	}
	s = s[1:]
	var total float64
	timePart := false
	num := strings.Builder{}
	for _, r := range s {
		switch {
		case r == 'T':
			timePart = true
			num.Reset()
		case (r >= '0' && r <= '9') || r == '.':
			num.WriteRune(r)
		default:
			v, err := strconv.ParseFloat(num.String(), 64)
			num.Reset()
			if err != nil {
				continue
			}
			switch r {
			case 'Y':
				total += v * 365 * 24 * 3600
			case 'D':
				total += v * 24 * 3600
			case 'H':
				total += v * 3600
			case 'M':
				// M is months before the T and minutes after it.
				if timePart {
					total += v * 60
				} else {
					total += v * 30 * 24 * 3600
				}
			case 'S':
				total += v
			case 'W':
				total += v * 7 * 24 * 3600
			}
		}
	}
	return total
}

// TotalDuration is the running time the manifest declares, in seconds.
func (m *MPD) TotalDuration() time.Duration {
	return time.Duration(duration(m.MediaPresentationDuration) * float64(time.Second))
}

// resolveAll walks a chain of BaseURL elements. Each one is relative to the
// one above it, and the innermost wins.
func resolveAll(base *url.URL, refs []string) *url.URL {
	out := base
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		u, err := url.Parse(ref)
		if err != nil {
			continue
		}
		if out == nil {
			out = u
			continue
		}
		out = out.ResolveReference(u)
	}
	return out
}

// resolveRef turns a possibly relative segment URL into an absolute one.
func resolveRef(base *url.URL, ref string) string {
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
