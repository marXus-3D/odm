package manager

import "testing"

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
