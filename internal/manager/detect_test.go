package manager

import (
	"strings"
	"testing"
)

func TestDetectKind(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://cdn.example.com/vod/master.m3u8", KindHLS},
		{"https://cdn.example.com/vod/master.m3u8?token=abc", KindHLS},
		// A session parameter stuck on with a semicolon, and more path after
		// the playlist: both used to fall through to the file engine.
		{"https://cdn.example.com/hls/master.m3u8;s=9f3a", KindHLS},
		{"https://cdn.example.com/hls/index.m3u8/seg-1", KindHLS},
		{"https://cdn.example.com/get?src=https%3A%2F%2Fx%2Fa.m3u8", KindHLS},
		{"https://cdn.example.com/dash/video.mpd", KindDASH},
		{"https://cdn.example.com/dash/manifest.mpd?t=1", KindDASH},
		// Nothing in the URL says playlist; only the response can.
		{"https://cdn.example.com/api/video/hls", KindFile},
		{"https://cdn.example.com/files/setup.exe", KindFile},
		{"not a url at all", KindFile},
	}
	for _, c := range cases {
		if got := DetectKind(c.url); got != c.want {
			t.Errorf("DetectKind(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestKindFromResponse(t *testing.T) {
	playlist := []byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n")
	manifest := []byte(`<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011">`)
	mp4 := []byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	cases := []struct {
		name string
		ct   string
		head []byte
		want string
	}{
		{"apple type", "application/vnd.apple.mpegurl", nil, KindHLS},
		{"x-mpegurl with charset", "application/x-mpegURL; charset=utf-8", nil, KindHLS},
		{"audio variant", "audio/x-mpegurl", nil, KindHLS},
		// The case that mattered: a CDN calling a playlist a binary blob.
		{"octet-stream body wins", "application/octet-stream", playlist, KindHLS},
		{"text body wins", "text/plain", playlist, KindHLS},
		{"no type at all", "", playlist, KindHLS},
		{"dash type", "application/dash+xml", nil, KindDASH},
		{"dash body", "application/octet-stream", manifest, KindDASH},
		{"an actual video", "video/mp4", mp4, ""},
		{"a plain file", "application/octet-stream", mp4, ""},
		{"nothing to go on", "", nil, ""},
		// Neither a playlist nor a manifest, despite being XML.
		{"unrelated xml", "application/xml", []byte(`<?xml version="1.0"?><rss/>`), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := KindFromResponse(c.ct, c.head); got != c.want {
				t.Errorf("KindFromResponse(%q, %q) = %q, want %q", c.ct, c.head, got, c.want)
			}
		})
	}
}

func TestPlaylistName(t *testing.T) {
	cases := []struct{ in, url, want string }{
		// The playlist's own filename says nothing about the video.
		{"master.m3u8", "https://x/vod/master.m3u8", ""},
		{"index.m3u8", "https://x/vod/index.m3u8", ""},
		{"chunklist.m3u8", "https://x/chunklist.m3u8", ""},
		// A name off a signed endpoint is a verb, not a title.
		{"hls", "https://x/api/video/hls", ""},
		{"cdn", "https://x/file/abc/binary/cdn", ""},
		// Anything that looks like a real title is kept.
		{"Wallace and Gromit.m3u8", "https://x/Wallace and Gromit.m3u8", "Wallace and Gromit.m3u8"},
		{"episode-04", "https://x/series/episode-04", "episode-04"},
		{"", "https://x/vod/master.m3u8", ""},
	}
	for _, c := range cases {
		if got := playlistName(c.in, c.url); got != c.want {
			t.Errorf("playlistName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLooksLikeToken(t *testing.T) {
	// Machine-generated: no use as a filename.
	tokens := []string{
		"aHR0cHM6Ly9oZ3BsYXljZG4uY29tL3BsL21hc3Rlci5tM3U4",
		"H4sIAAAAAAAAAw3OW3KDIBQA0C1dEDNJ_5oxammlIxGI_AE3rfWV1DxMXH17VnDCl0eMAj2",
		"4210c548-5e5e-41ce-acd6-2d1ef27c4b08",
		"7f3a9c1e5b2d4f8a3b7c1e5d9f2a4b8c",
		"pbH9FgRk2xQ7mN4vB8kL2wZ",
	}
	for _, s := range tokens {
		if !looksLikeToken(s) {
			t.Errorf("looksLikeToken(%q) = false, want true", s)
		}
	}
	// Things a person would recognise, which must survive.
	names := []string{
		"Wallace and Gromit", "episode-04", "master.m3u8", "setup.exe",
		"How steel is made", "annual-report-2026.pdf", "video_1280.mp4",
		"Die Wiedervereinigung Deutschlands", "第一話",
		"a-very-long-hyphenated-english-title-with-no-digits",
	}
	for _, s := range names {
		if looksLikeToken(s) {
			t.Errorf("looksLikeToken(%q) = true, want false", s)
		}
	}
}

func TestTitleAsName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Wallace and Gromit", "Wallace and Gromit"},
		{"  spaced  ", "spaced"},
		// The site's own name is not part of the title.
		{"Frieren Episode 12 | AnimeSite", "Frieren Episode 12"},
		{"How steel is made - Fabrication", "How steel is made"},
		{"Episode 4 — SomeSite", "Episode 4"},
		// Too little would survive the cut, so it is left whole.
		{"S1 - The Beginning", "S1 - The Beginning"},
		{`bad<>:"|?*chars`, "bad_______chars"},
		{"a/b\\c", "c"}, // path separators cannot survive in a filename
		{"", ""},
		{"...", ""},
		{strings.Repeat("x", 300), strings.Repeat("x", 120)},
	}
	for _, c := range cases {
		if got := titleAsName(c.in); got != c.want {
			t.Errorf("titleAsName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLooksLikeYouTube(t *testing.T) {
	yes := []string{
		"https://www.youtube.com/watch?v=abc",
		"https://youtube.com/shorts/abc",
		"https://m.youtube.com/watch?v=abc",
		"https://youtu.be/abc",
		"https://www.youtube-nocookie.com/embed/abc",
	}
	no := []string{
		// Not the page: the media host, which is a different question.
		"https://rr3---sn-x.googlevideo.com/videoplayback?id=abc",
		"https://example.com/watch?v=abc",
		"https://notyoutube.com/watch?v=abc",
	}
	for _, u := range yes {
		if !looksLikeYouTube(u) {
			t.Errorf("looksLikeYouTube(%q) = false, want true", u)
		}
	}
	for _, u := range no {
		if looksLikeYouTube(u) {
			t.Errorf("looksLikeYouTube(%q) = true, want false", u)
		}
	}
}
