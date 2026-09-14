package engine

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestFilenameFromDisposition(t *testing.T) {
	cases := []struct {
		name string
		cd   string
		want string
	}{
		{"quoted", `attachment; filename="report.pdf"`, "report.pdf"},
		{"unquoted", `attachment; filename=report.pdf`, "report.pdf"},
		{"star wins", `attachment; filename="fallback.bin"; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf`, "résumé.pdf"},
		{"star only", `attachment; filename*=UTF-8''%E6%98%A0%E7%94%BB.mp4`, "映画.mp4"},
		{"rfc 2047 word", `attachment; filename="=?UTF-8?B?w6l0w6kubXA0?="`, "été.mp4"},
		{"percent encoded plain", `attachment; filename="my%20file.zip"`, "my file.zip"},
		// mime.ParseMediaType rejects all three of these outright, which is
		// why there is a regexp fallback at all.
		{"unquoted with spaces", `attachment; filename=my holiday.mp4`, "my holiday.mp4"},
		{"raw utf-8", "attachment; filename=\"日本.mkv\"", "日本.mkv"},
		{"windows path", `attachment; filename="C:\dl\clip.mp4"`, `C:\dl\clip.mp4`},
		{"inline no name", `inline`, ""},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FilenameFromDisposition(c.cd); got != c.want {
				t.Errorf("FilenameFromDisposition(%q) = %q, want %q", c.cd, got, c.want)
			}
		})
	}
}

func TestWithExtension(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ct   string
		want string
	}{
		{"missing", "videoplayback", "video/mp4", "videoplayback.mp4"},
		{"missing, vague type", "videoplayback", "application/octet-stream", "videoplayback"},
		{"missing, no type", "blob", "", "blob"},
		{"kept", "setup.exe", "application/octet-stream", "setup.exe"},
		{"kept over wrong type", "archive.zip", "text/html", "archive.zip"},
		{"script replaced", "download.php", "application/zip", "download.zip"},
		{"script kept when type is vague", "download.php", "application/octet-stream", "download.php"},
		{"charset ignored", "page", "text/plain; charset=utf-8", "page.txt"},
		// A trailing date or hash is not an extension.
		{"date tail", "clip.2024-05-01", "video/mp4", "clip.2024-05-01.mp4"},
		{"double extension", "src.tar.gz", "application/gzip", "src.tar.gz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withExtension(c.in, c.ct); got != c.want {
				t.Errorf("withExtension(%q, %q) = %q, want %q", c.in, c.ct, got, c.want)
			}
		})
	}
}

func TestFilenameFromURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			// The name is four segments from the end; "cdn" is a routing verb.
			"name mid-path, verb on the end",
			"https://media-cdn.atlassian.com/file/f6233603-074b-4422-affc-a558b986e565" +
				"/artifact/video_1280.mp4/binary/cdn?client=247fea4f&max-age=2592000&token=eyJhbGciOiJIUzI1NiJ9.abc",
			"video_1280.mp4",
		},
		{"plain", "https://example.com/files/setup.exe", "setup.exe"},
		{"query is ignored", "https://example.com/files/setup.exe?t=123", "setup.exe"},
		{"escaped", "https://example.com/files/my%20song.mp3", "my song.mp3"},
		{"trailing slash", "https://example.com/files/clip.mkv/", "clip.mkv"},
		{
			"presigned disposition",
			"https://s3.amazonaws.com/b/9f8e7d?response-content-disposition=attachment%3B%20filename%3D%22Q3%20report.pdf%22",
			"Q3 report.pdf",
		},
		{"filename parameter", "https://example.com/dl?id=99&filename=album.zip", "album.zip"},
		{"parameter without an extension is not a name", "https://example.com/dl?name=42", "dl"},
		{"nothing to go on", "https://example.com/d/7f3a9c1e", "7f3a9c1e"},
		{"script endpoint", "https://example.com/get.php?id=12", "get.php"},
		// A dotted path component is not a filename.
		{"version segment", "https://example.com/v1.2/binary", "binary"},
		{"no path at all", "https://example.com/", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FilenameFromURL(c.url); got != c.want {
				t.Errorf("FilenameFromURL = %q, want %q", got, c.want)
			}
		})
	}
}

// TestProbeFilename covers the whole chain through a real response, which is
// where the pieces have to agree: a CDN URL with no name in its path, a
// redirect, and a Content-Disposition that only the fallback parser reads.
func TestProbeFilename(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		override string
		headers  map[string]string
		want     string
	}{
		{
			name:    "disposition beats path",
			path:    "/videoplayback",
			headers: map[string]string{"Content-Disposition": `attachment; filename="Holiday.mp4"`},
			want:    "Holiday.mp4",
		},
		{
			name:    "nameless path takes the type",
			path:    "/videoplayback",
			headers: map[string]string{"Content-Type": "video/mp4"},
			want:    "videoplayback.mp4",
		},
		{
			name:    "token path takes the type",
			path:    "/d/7f3a9c1e5b2d4f8a",
			headers: map[string]string{"Content-Type": "application/zip"},
			want:    "7f3a9c1e5b2d4f8a.zip",
		},
		{
			name:    "root path",
			path:    "/",
			headers: map[string]string{"Content-Type": "application/pdf"},
			want:    "download.pdf",
		},
		{
			name:     "override wins",
			path:     "/videoplayback",
			override: "mine.mp4",
			headers:  map[string]string{"Content-Disposition": `attachment; filename="theirs.mp4"`},
			want:     "mine.mp4",
		},
		{
			name:     "override gains an extension",
			path:     "/videoplayback",
			override: "mine",
			headers:  map[string]string{"Content-Type": "video/mp4"},
			want:     "mine.mp4",
		},
		{
			name:    "path name is kept",
			path:    "/files/setup.exe",
			headers: map[string]string{"Content-Type": "application/octet-stream"},
			want:    "setup.exe",
		},
		{
			name:    "escaped path is decoded",
			path:    "/files/my%20song.mp3",
			headers: map[string]string{"Content-Type": "audio/mpeg"},
			want:    "my song.mp3",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			p, err := DoProbe(context.Background(),
				Request{URL: srv.URL + c.path, Filename: c.override}, Options{})
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			if p.Filename != c.want {
				t.Errorf("filename = %q, want %q", p.Filename, c.want)
			}
		})
	}
}

// TestProbeFilenameAfterRedirect checks the name comes from where the
// request ended up, not from where it started.
func TestProbeFilenameAfterRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cdn/album.zip", http.StatusFound)
	})
	mux.HandleFunc("/cdn/album.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p, err := DoProbe(context.Background(), Request{URL: srv.URL + "/get.php?id=12"}, Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if p.Filename != "album.zip" {
		t.Errorf("filename = %q, want album.zip", p.Filename)
	}
}

// TestDownloadNamesFromContentType is the end-to-end version of the bug:
// a URL with no name and no Content-Disposition used to be saved under the
// last path segment with no extension.
func TestDownloadNamesFromContentType(t *testing.T) {
	body := deterministicBody(4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "", time.Unix(1700000000, 0), bytes.NewReader(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := New("t", Request{URL: srv.URL + "/videoplayback", Dir: dir}, Options{MaxConns: 2})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := filepath.Base(d.Path); got != "videoplayback.mp4" {
		t.Errorf("saved as %q, want videoplayback.mp4", got)
	}
}
