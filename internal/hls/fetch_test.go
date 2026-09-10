package hls

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// segBody builds recognisable content for segment i.
func segBody(i int) []byte {
	b := make([]byte, 1024+i)
	for j := range b {
		b[j] = byte(i*31 + j)
	}
	return b
}

func expectedConcat(n int) []byte {
	var buf bytes.Buffer
	for i := 0; i < n; i++ {
		buf.Write(segBody(i))
	}
	return buf.Bytes()
}

// plainServer serves an unencrypted media playlist of n segments.
func plainServer(t *testing.T, n int, delay time.Duration) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(w, "#EXTINF:4.0,\nseg%d.ts\n", i)
		}
		fmt.Fprint(w, "#EXT-X-ENDLIST\n")
	})
	for i := 0; i < n; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/seg%d.ts", i), func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			if delay > 0 {
				time.Sleep(delay)
			}
			w.Write(segBody(i))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestFetchWritesSegmentsInOrder(t *testing.T) {
	const n = 25
	// A delay makes out-of-order completion likely: without ordered writes
	// the output would be scrambled.
	srv, hits := plainServer(t, n, 15*time.Millisecond)

	f := &Fetcher{Concurrency: 8}
	media, master, err := f.LoadPlaylist(context.Background(), srv.URL+"/index.m3u8", nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if master != nil {
		t.Error("this is a media playlist, not a master")
	}
	if len(media.Segments) != n {
		t.Fatalf("got %d segments, want %d", len(media.Segments), n)
	}

	var out bytes.Buffer
	if err := f.Fetch(context.Background(), media, &out, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !bytes.Equal(out.Bytes(), expectedConcat(n)) {
		t.Fatal("output is not the segments concatenated in playlist order")
	}
	if got := atomic.LoadInt32(hits); got != n {
		t.Errorf("server saw %d segment requests, want %d", got, n)
	}
}

func TestFetchIsActuallyConcurrent(t *testing.T) {
	const n = 24
	var mu sync.Mutex
	cur, peak := 0, 0

	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(w, "#EXTINF:4.0,\nseg%d.ts\n", i)
		}
		fmt.Fprint(w, "#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
		w.Write([]byte("x"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Fetcher{Concurrency: 6}
	media, _, err := f.LoadPlaylist(context.Background(), srv.URL+"/index.m3u8", nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := f.Fetch(context.Background(), media, &bytes.Buffer{}, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	mu.Lock()
	got := peak
	mu.Unlock()
	if got < 3 {
		t.Errorf("peak concurrency was %d; segments were fetched almost serially", got)
	}
	if got > 6 {
		t.Errorf("peak concurrency was %d, above the configured limit of 6", got)
	}
}

func TestFetchResumeSkipsLeadingSegments(t *testing.T) {
	const n = 12
	srv, hits := plainServer(t, n, 0)
	f := &Fetcher{Concurrency: 4}
	media, _, err := f.LoadPlaylist(context.Background(), srv.URL+"/index.m3u8", nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	var out bytes.Buffer
	const skip = 5
	if err := f.Fetch(context.Background(), media, &out, skip); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	var want bytes.Buffer
	for i := skip; i < n; i++ {
		want.Write(segBody(i))
	}
	if !bytes.Equal(out.Bytes(), want.Bytes()) {
		t.Fatal("resumed output does not match the remaining segments")
	}
	if got := atomic.LoadInt32(hits); got != n-skip {
		t.Errorf("fetched %d segments, want %d -- already-downloaded ones were refetched",
			got, n-skip)
	}
}

// encryptedServer serves an AES-128 playlist, encrypting on the fly.
func encryptedServer(t *testing.T, n int, explicitIV bool) *httptest.Server {
	t.Helper()
	key := []byte("0123456789abcdef")
	iv := []byte("ABCDEFGHIJKLMNOP")

	mux := http.NewServeMux()
	mux.HandleFunc("/key.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Write(key)
	})
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n")
		if explicitIV {
			fmt.Fprintf(w, "#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x%x\n", iv)
		} else {
			fmt.Fprint(w, "#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n")
		}
		for i := 0; i < n; i++ {
			fmt.Fprintf(w, "#EXTINF:4.0,\nseg%d.ts\n", i)
		}
		fmt.Fprint(w, "#EXT-X-ENDLIST\n")
	})
	for i := 0; i < n; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/seg%d.ts", i), func(w http.ResponseWriter, r *http.Request) {
			segIV := iv
			if !explicitIV {
				// Mirror the spec's default: the media sequence number as a
				// 128-bit big-endian value.
				segIV = make([]byte, 16)
				binary.BigEndian.PutUint64(segIV[8:], uint64(i))
			}
			w.Write(encryptAES(t, key, segIV, segBody(i)))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func encryptAES(t *testing.T, key, iv, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pad := block.BlockSize() - len(plain)%block.BlockSize()
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out
}

func TestFetchDecryptsAES128(t *testing.T) {
	const n = 8
	for _, explicitIV := range []bool{true, false} {
		name := "explicit IV"
		if !explicitIV {
			name = "IV from sequence number"
		}
		t.Run(name, func(t *testing.T) {
			srv := encryptedServer(t, n, explicitIV)
			f := &Fetcher{Concurrency: 4}
			media, _, err := f.LoadPlaylist(context.Background(), srv.URL+"/index.m3u8", nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			var out bytes.Buffer
			if err := f.Fetch(context.Background(), media, &out, 0); err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if !bytes.Equal(out.Bytes(), expectedConcat(n)) {
				t.Fatal("decrypted output does not match the plaintext segments")
			}
		})
	}
}

func TestFetchMasterPicksHighestBandwidth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=500000,RESOLUTION=640x360\nlow.m3u8\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080\nhigh.m3u8\n")
	})
	for _, name := range []string{"low", "high"} {
		name := name
		mux.HandleFunc("/"+name+".m3u8", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\n%s.ts\n#EXT-X-ENDLIST\n", name)
		})
	}
	mux.HandleFunc("/low.ts", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("LOW")) })
	mux.HandleFunc("/high.ts", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("HIGH")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Fetcher{}
	media, master, err := f.LoadPlaylist(context.Background(), srv.URL+"/master.m3u8", nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if master == nil || len(master.Variants) != 2 {
		t.Fatalf("master not returned properly: %+v", master)
	}
	var out bytes.Buffer
	if err := f.Fetch(context.Background(), media, &out, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if out.String() != "HIGH" {
		t.Errorf("got %q, want the 1080p variant", out.String())
	}

	// An explicit chooser must override the default.
	media2, _, err := f.LoadPlaylist(context.Background(), srv.URL+"/master.m3u8",
		func(m *Master) Variant { return m.Variants[0] })
	if err != nil {
		t.Fatalf("load with picker: %v", err)
	}
	var out2 bytes.Buffer
	if err := f.Fetch(context.Background(), media2, &out2, 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if out2.String() != "LOW" {
		t.Errorf("picker ignored: got %q, want LOW", out2.String())
	}
}

func TestFetchSurfacesSegmentFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n"+
			"#EXTINF:4.0,\ns0.ts\n#EXTINF:4.0,\nmissing.ts\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/s0.ts", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/missing.ts", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Fetcher{Concurrency: 2, MaxRetries: 1, Backoff: time.Millisecond}
	media, _, err := f.LoadPlaylist(context.Background(), srv.URL+"/index.m3u8", nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = f.Fetch(context.Background(), media, &bytes.Buffer{}, 0)
	if err == nil {
		t.Fatal("a missing segment must fail the download, not truncate it silently")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("segment 2 of 2")) {
		t.Errorf("error should identify the segment, got: %v", err)
	}
}

func TestLoadPlaylistRejectsNonPlaylist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>not a playlist</body></html>"))
	}))
	defer srv.Close()

	f := &Fetcher{}
	if _, _, err := f.LoadPlaylist(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("expected an error for a non-playlist body")
	}
}

func TestFetchRespectsContextCancellation(t *testing.T) {
	srv, _ := plainServer(t, 60, 40*time.Millisecond)
	f := &Fetcher{Concurrency: 4}
	media, _, err := f.LoadPlaylist(context.Background(), srv.URL+"/index.m3u8", nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- f.Fetch(ctx, media, &bytes.Buffer{}, 0) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation to stop the fetch")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fetch did not stop on cancellation; goroutines are wedged")
	}
}
