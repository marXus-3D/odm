package dash

import (
	"errors"
	"math"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

// tracksOf parses a manifest and returns its two tracks, failing the test on
// any error. Most cases here care about the segments, not the plumbing.
func tracksOf(t *testing.T, xml, base string) (*Track, *Track) {
	t.Helper()
	m, err := Parse([]byte(xml), mustURL(t, base))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v, a, err := m.Tracks(mustURL(t, base))
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	return v, a
}

const templateDuration = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT30S">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="v1" bandwidth="800000" width="1280" height="720" codecs="avc1.64001f">
        <SegmentTemplate initialization="init-$RepresentationID$.m4s"
                         media="seg-$RepresentationID$-$Number%03d$.m4s"
                         startNumber="1" duration="10" timescale="1"/>
      </Representation>
      <Representation id="v2" bandwidth="2400000" width="1920" height="1080" codecs="hvc1.1.6.L120">
        <SegmentTemplate initialization="init-$RepresentationID$.m4s"
                         media="seg-$RepresentationID$-$Number%03d$.m4s"
                         startNumber="1" duration="10" timescale="1"/>
      </Representation>
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4" lang="en">
      <Representation id="a1" bandwidth="128000" codecs="mp4a.40.2">
        <SegmentTemplate initialization="init-$RepresentationID$.m4s"
                         media="seg-$RepresentationID$-$Number$.m4s"
                         startNumber="1" duration="10" timescale="1"/>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestTracksSegmentTemplateDuration(t *testing.T) {
	v, a := tracksOf(t, templateDuration, "https://cdn.example.com/dash/index.mpd")

	if v == nil || a == nil {
		t.Fatal("want both a video and an audio track")
	}
	// The highest bandwidth representation wins, the same rule HLS uses.
	if v.ID != "v2" || v.Height != 1080 {
		t.Errorf("video = %s (%dp), want v2 (1080p)", v.ID, v.Height)
	}
	if got, want := v.InitURI, "https://cdn.example.com/dash/init-v2.m4s"; got != want {
		t.Errorf("init = %q, want %q", got, want)
	}
	// 30 seconds of 10-second segments.
	if len(v.Segments) != 3 {
		t.Fatalf("video segments = %d, want 3", len(v.Segments))
	}
	// The %03d modifier has to be honoured or every URL is a 404.
	if got, want := v.Segments[0].URI, "https://cdn.example.com/dash/seg-v2-001.m4s"; got != want {
		t.Errorf("segment 1 = %q, want %q", got, want)
	}
	if got, want := v.Segments[2].URI, "https://cdn.example.com/dash/seg-v2-003.m4s"; got != want {
		t.Errorf("segment 3 = %q, want %q", got, want)
	}
	// No modifier means no padding.
	if got, want := a.Segments[0].URI, "https://cdn.example.com/dash/seg-a1-1.m4s"; got != want {
		t.Errorf("audio segment 1 = %q, want %q", got, want)
	}
	if a.Lang != "en" {
		t.Errorf("audio lang = %q, want en", a.Lang)
	}
}

const templateTimeline = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT20S">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate initialization="v/init.mp4" media="v/$Time$.m4s" timescale="90000">
        <SegmentTimeline>
          <S t="0" d="180000" r="2"/>
          <S d="90000"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v1" bandwidth="500000" width="640" height="360"/>
    </AdaptationSet>
  </Period>
</MPD>`

func TestTracksSegmentTimeline(t *testing.T) {
	v, a := tracksOf(t, templateTimeline, "https://cdn.example.com/x/manifest.mpd")
	if a != nil {
		t.Errorf("want no audio track, got %v", a.ID)
	}
	// r="2" means the entry repeats twice more: three segments, then one more.
	if len(v.Segments) != 4 {
		t.Fatalf("segments = %d, want 4", len(v.Segments))
	}
	// $Time$ advances by each segment's own duration, not by a fixed step.
	want := []string{
		"https://cdn.example.com/x/v/0.m4s",
		"https://cdn.example.com/x/v/180000.m4s",
		"https://cdn.example.com/x/v/360000.m4s",
		"https://cdn.example.com/x/v/540000.m4s",
	}
	for i, w := range want {
		if v.Segments[i].URI != w {
			t.Errorf("segment %d = %q, want %q", i+1, v.Segments[i].URI, w)
		}
	}
	// The template sat on the AdaptationSet, so the representation inherits it.
	if got, want := v.InitURI, "https://cdn.example.com/x/v/init.mp4"; got != want {
		t.Errorf("init = %q, want %q", got, want)
	}
}

const segmentList = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S">
  <Period>
    <AdaptationSet contentType="audio" mimeType="audio/mp4">
      <Representation id="a1" bandwidth="96000" codecs="mp4a.40.2">
        <SegmentList timescale="1" duration="4">
          <Initialization sourceURL="a/init.mp4"/>
          <SegmentURL media="a/1.m4s"/>
          <SegmentURL media="a/2.m4s"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestTracksSegmentList(t *testing.T) {
	v, a := tracksOf(t, segmentList, "https://cdn.example.com/s/m.mpd")
	if v != nil {
		t.Errorf("want no video track, got %v", v.ID)
	}
	if len(a.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(a.Segments))
	}
	if got, want := a.InitURI, "https://cdn.example.com/s/a/init.mp4"; got != want {
		t.Errorf("init = %q, want %q", got, want)
	}
	if got, want := a.Segments[1].URI, "https://cdn.example.com/s/a/2.m4s"; got != want {
		t.Errorf("segment 2 = %q, want %q", got, want)
	}
}

const segmentBase = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT60S">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="v1" bandwidth="1000000" width="1280" height="720">
        <BaseURL>video-720.mp4</BaseURL>
        <SegmentBase indexRange="900-1200">
          <Initialization range="0-899"/>
        </SegmentBase>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestTracksSegmentBase(t *testing.T) {
	v, _ := tracksOf(t, segmentBase, "https://cdn.example.com/b/m.mpd")
	if got, want := v.InitURI, "https://cdn.example.com/b/video-720.mp4"; got != want {
		t.Errorf("init = %q, want %q", got, want)
	}
	if !v.InitHasRange || v.InitStart != 0 || v.InitLength != 900 {
		t.Errorf("init range = (%v, %d, %d), want (true, 0, 900)",
			v.InitHasRange, v.InitStart, v.InitLength)
	}
	if len(v.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(v.Segments))
	}
	// The media picks up where the initialization segment left off.
	if !v.Segments[0].HasRange || v.Segments[0].RangeStart != 900 {
		t.Errorf("media range start = %d, want 900", v.Segments[0].RangeStart)
	}
}

// TestTracksBaseURLChain pins down that a BaseURL at each level composes
// rather than replacing the one above it.
func TestTracksBaseURLChain(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
  <BaseURL>https://media.example.com/root/</BaseURL>
  <Period>
    <BaseURL>period1/</BaseURL>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <BaseURL>video/</BaseURL>
      <Representation id="v1" bandwidth="100" width="320" height="180">
        <SegmentTemplate initialization="init.mp4" media="$Number$.m4s"
                         startNumber="1" duration="10" timescale="1"/>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`
	v, _ := tracksOf(t, doc, "https://cdn.example.com/ignored/m.mpd")
	if got, want := v.InitURI, "https://media.example.com/root/period1/video/init.mp4"; got != want {
		t.Errorf("init = %q, want %q", got, want)
	}
	if got, want := v.Segments[0].URI, "https://media.example.com/root/period1/video/1.m4s"; got != want {
		t.Errorf("segment = %q, want %q", got, want)
	}
}

// TestTracksRefusals covers the two manifests that must not be downloaded.
// Both would otherwise produce a file: a live stream that never ends, and a
// DRM stream whose segments assemble into something unplayable.
func TestTracksRefusals(t *testing.T) {
	const live = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic">
  <Period><AdaptationSet contentType="video" mimeType="video/mp4">
    <Representation id="v" bandwidth="1"/>
  </AdaptationSet></Period>
</MPD>`
	const drm = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet contentType="video" mimeType="video/mp4">
    <ContentProtection schemeIdUri="urn:uuid:EDEF8BA9-79D6-4ACE-A3C8-27DCD51D21ED"/>
    <Representation id="v" bandwidth="1">
      <SegmentTemplate initialization="i.mp4" media="$Number$.m4s" duration="10" timescale="1"/>
    </Representation>
  </AdaptationSet></Period>
</MPD>`

	for _, c := range []struct {
		name string
		doc  string
		want error
	}{
		{"live", live, ErrLive},
		{"drm", drm, ErrProtected},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := Parse([]byte(c.doc), nil)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, _, err := m.Tracks(mustURL(t, "https://x/m.mpd")); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// TestTrackKindFallback covers an adaptation set that declares neither
// contentType nor a usable mimeType, which plenty of packagers emit.
func TestTrackKindFallback(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
  <Period>
    <AdaptationSet>
      <Representation id="v" bandwidth="900" width="1920" height="1080">
        <SegmentTemplate initialization="i.mp4" media="$Number$.m4s" duration="10" timescale="1"/>
      </Representation>
    </AdaptationSet>
    <AdaptationSet codecs="mp4a.40.2">
      <Representation id="a" bandwidth="128">
        <SegmentTemplate initialization="ai.mp4" media="a$Number$.m4s" duration="10" timescale="1"/>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`
	v, a := tracksOf(t, doc, "https://x/m.mpd")
	if v == nil || v.ID != "v" {
		t.Errorf("video track not found by its resolution")
	}
	if a == nil || a.ID != "a" {
		t.Errorf("audio track not found by its codec string")
	}
}

func TestSubstitute(t *testing.T) {
	r := &Representation{ID: "v1", Bandwidth: 800000}
	cases := []struct {
		pattern string
		number  int
		time    uint64
		want    string
	}{
		{"$RepresentationID$/seg$Number$.m4s", 7, 0, "v1/seg7.m4s"},
		{"seg-$Number%05d$.m4s", 42, 0, "seg-00042.m4s"},
		{"t/$Time$.m4s", 0, 180000, "t/180000.m4s"},
		{"$Bandwidth$/x.m4s", 0, 0, "800000/x.m4s"},
		// "$$" is an escaped dollar, not a placeholder.
		{"a$$b.m4s", 1, 0, "a$b.m4s"},
		// An unknown placeholder is left alone so the failure names it.
		{"$Nonsense$.m4s", 1, 0, "$Nonsense$.m4s"},
		{"plain.m4s", 1, 0, "plain.m4s"},
	}
	for _, c := range cases {
		if got := substitute(c.pattern, r, c.number, c.time); got != c.want {
			t.Errorf("substitute(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

func TestDuration(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"PT30S", 30},
		{"PT1H30M", 5400},
		{"PT1H30M15.5S", 5415.5},
		{"PT0H2M10.080S", 130.08},
		{"P1DT2H", 93600},
		// M means months before the T and minutes after it.
		{"P2M", 2 * 30 * 24 * 3600},
		{"", 0},
		{"nonsense", 0},
	}
	for _, c := range cases {
		if got := duration(c.in); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("duration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		in          string
		start, size int64
	}{
		{"0-899", 0, 900},
		{"900-1200", 900, 301},
		{"  12 - 15 ", 12, 4},
		{"garbage", 0, 0},
		{"5-1", 5, 0}, // an end before the start is not a range
	}
	for _, c := range cases {
		start, size := parseRange(c.in)
		if start != c.start || size != c.size {
			t.Errorf("parseRange(%q) = (%d, %d), want (%d, %d)",
				c.in, start, size, c.start, c.size)
		}
	}
}

func TestLooksLikeManifest(t *testing.T) {
	yes := []string{
		`<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period/></MPD>`,
		`<MPD mediaPresentationDuration="PT10S">`,
	}
	no := []string{
		"#EXTM3U\n#EXT-X-VERSION:3\n",
		`<?xml version="1.0"?><rss><channel/></rss>`,
		"",
	}
	for _, s := range yes {
		if !LooksLikeManifest([]byte(s)) {
			t.Errorf("LooksLikeManifest(%.30q) = false, want true", s)
		}
	}
	for _, s := range no {
		if LooksLikeManifest([]byte(s)) {
			t.Errorf("LooksLikeManifest(%.30q) = true, want false", s)
		}
	}
}
