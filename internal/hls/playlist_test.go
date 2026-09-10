package hls

import (
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("bad base url: %v", err)
	}
	return u
}

const masterPlaylist = `#EXTM3U
#EXT-X-VERSION:4
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.42c01e,mp4a.40.2",AUDIO="aac"
low/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2",AUDIO="aac"
mid/index.m3u8
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=4500000,BANDWIDTH=5000000,RESOLUTION=1920x1080
https://cdn.example.com/high/index.m3u8
`

func TestParseMaster(t *testing.T) {
	base := mustURL(t, "https://example.com/video/master.m3u8")
	m, err := ParseMaster(strings.NewReader(masterPlaylist), base)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Variants) != 3 {
		t.Fatalf("got %d variants, want 3", len(m.Variants))
	}

	// Relative URIs must resolve against the playlist, not the site root.
	if got, want := m.Variants[0].URI, "https://example.com/video/low/index.m3u8"; got != want {
		t.Errorf("variant 0 uri = %q, want %q", got, want)
	}
	// An absolute URI is left alone.
	if got, want := m.Variants[2].URI, "https://cdn.example.com/high/index.m3u8"; got != want {
		t.Errorf("variant 2 uri = %q, want %q", got, want)
	}
	// CODECS contains a comma inside quotes; naive splitting breaks here.
	if got, want := m.Variants[1].Codecs, "avc1.4d401f,mp4a.40.2"; got != want {
		t.Errorf("codecs = %q, want %q", got, want)
	}
	if got := m.Variants[0].AudioGroup; got != "aac" {
		t.Errorf("audio group = %q, want aac", got)
	}

	// AVERAGE-BANDWIDTH is the better estimate when present.
	if got := m.Variants[2].Bandwidth; got != 4500000 {
		t.Errorf("bandwidth = %d, want the average 4500000", got)
	}
	if best := m.Best(); best.Resolution != "1920x1080" {
		t.Errorf("Best() = %q, want the 1080p variant", best.Resolution)
	}

	if len(m.Renditions) != 1 {
		t.Fatalf("got %d renditions, want 1", len(m.Renditions))
	}
	r := m.Renditions[0]
	if r.Type != "AUDIO" || r.Language != "en" || !r.Default {
		t.Errorf("rendition parsed wrong: %+v", r)
	}
	if got, want := r.URI, "https://example.com/video/audio/en.m3u8"; got != want {
		t.Errorf("rendition uri = %q, want %q", got, want)
	}
}

func TestIsMaster(t *testing.T) {
	if !IsMaster([]byte(masterPlaylist)) {
		t.Error("master playlist not recognised")
	}
	if IsMaster([]byte(mediaPlaylist)) {
		t.Error("media playlist misread as a master")
	}
	if !LooksLikePlaylist([]byte(mediaPlaylist)) {
		t.Error("media playlist should look like a playlist")
	}
	if LooksLikePlaylist([]byte("<html><body>nope")) {
		t.Error("html should not look like a playlist")
	}
}

const mediaPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:9.009,
seg0.ts
#EXTINF:9.009,
seg1.ts
#EXTINF:3.003,
https://cdn.example.com/seg2.ts
#EXT-X-ENDLIST
`

func TestParseMedia(t *testing.T) {
	base := mustURL(t, "https://example.com/video/index.m3u8")
	m, err := ParseMedia(strings.NewReader(mediaPlaylist), base)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Segments) != 3 {
		t.Fatalf("got %d segments, want 3", len(m.Segments))
	}
	if !m.EndList {
		t.Error("EXT-X-ENDLIST not detected; this would be treated as live")
	}
	if m.TargetDuration != 10 {
		t.Errorf("target duration = %v, want 10", m.TargetDuration)
	}
	if got, want := m.Segments[0].URI, "https://example.com/video/seg0.ts"; got != want {
		t.Errorf("segment 0 uri = %q, want %q", got, want)
	}
	// Sequence numbers continue from EXT-X-MEDIA-SEQUENCE; the default IV
	// depends on getting this right.
	if got := m.Segments[0].Sequence; got != 7 {
		t.Errorf("segment 0 sequence = %d, want 7", got)
	}
	if got := m.Segments[2].Sequence; got != 9 {
		t.Errorf("segment 2 sequence = %d, want 9", got)
	}
	if d := m.Duration(); d < 21 || d > 21.1 {
		t.Errorf("duration = %v, want ~21.021", d)
	}
	for i, s := range m.Segments {
		if s.Key != nil {
			t.Errorf("segment %d should be unencrypted", i)
		}
	}
}

const encryptedPlaylist = `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MAP:URI="init.mp4"
#EXT-X-KEY:METHOD=AES-128,URI="https://keys.example.com/k1",IV=0x0123456789ABCDEF0123456789ABCDEF
#EXTINF:6.0,
seg0.m4s
#EXTINF:6.0,
seg1.m4s
#EXT-X-KEY:METHOD=NONE
#EXTINF:6.0,
seg2.m4s
#EXT-X-ENDLIST
`

func TestParseMediaEncryption(t *testing.T) {
	base := mustURL(t, "https://example.com/v/index.m3u8")
	m, err := ParseMedia(strings.NewReader(encryptedPlaylist), base)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := m.InitURI, "https://example.com/v/init.mp4"; got != want {
		t.Errorf("EXT-X-MAP uri = %q, want %q", got, want)
	}
	if m.Segments[0].Key == nil || m.Segments[0].Key.Method != "AES-128" {
		t.Fatalf("segment 0 should be AES-128, got %+v", m.Segments[0].Key)
	}
	if got := len(m.Segments[0].Key.IV); got != 16 {
		t.Errorf("IV length = %d, want 16", got)
	}
	if m.Segments[1].Key == nil {
		t.Error("the key should carry forward to segment 1")
	}
	// METHOD=NONE turns encryption off for everything after it.
	if m.Segments[2].Key != nil {
		t.Errorf("segment 2 should be unencrypted after METHOD=NONE, got %+v", m.Segments[2].Key)
	}
}

const byteRangePlaylist = `#EXTM3U
#EXT-X-TARGETDURATION:10
#EXT-X-VERSION:4
#EXTINF:10.0,
#EXT-X-BYTERANGE:75232@0
video.ts
#EXTINF:10.0,
#EXT-X-BYTERANGE:82112
video.ts
#EXTINF:10.0,
#EXT-X-BYTERANGE:69864
video.ts
#EXT-X-ENDLIST
`

func TestParseMediaByteRange(t *testing.T) {
	m, err := ParseMedia(strings.NewReader(byteRangePlaylist),
		mustURL(t, "https://example.com/v/index.m3u8"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []struct{ start, length int64 }{
		{0, 75232},
		{75232, 82112},
		{157344, 69864},
	}
	for i, w := range want {
		s := m.Segments[i]
		if !s.HasRange {
			t.Fatalf("segment %d has no byte range", i)
		}
		// A range without "@" continues where the previous one ended.
		if s.RangeStart != w.start || s.RangeLength != w.length {
			t.Errorf("segment %d range = %d@%d, want %d@%d",
				i, s.RangeLength, s.RangeStart, w.length, w.start)
		}
	}
}

func TestParseMediaRejectsEmpty(t *testing.T) {
	_, err := ParseMedia(strings.NewReader("#EXTM3U\n#EXT-X-ENDLIST\n"), nil)
	if err == nil {
		t.Fatal("a playlist with no segments should be an error")
	}
}

func TestParseAttrsQuotedCommas(t *testing.T) {
	got := parseAttrs(`BANDWIDTH=100,CODECS="a,b,c",NAME="Hi, there",DEFAULT=YES`)
	if got["CODECS"] != "a,b,c" {
		t.Errorf("CODECS = %q", got["CODECS"])
	}
	if got["NAME"] != "Hi, there" {
		t.Errorf("NAME = %q", got["NAME"])
	}
	if got["BANDWIDTH"] != "100" || got["DEFAULT"] != "YES" {
		t.Errorf("attrs = %v", got)
	}
}

func TestLiveStreamDetected(t *testing.T) {
	live := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\ns0.ts\n"
	m, err := ParseMedia(strings.NewReader(live), nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.EndList {
		t.Error("a playlist with no ENDLIST is live and must not claim otherwise")
	}
}
