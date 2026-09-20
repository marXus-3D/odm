// Package ffmpeg locates the ffmpeg binary and runs it out of sight.
//
// Both media engines need it for the last step -- HLS to turn a transport
// stream into MP4, DASH to put the separate audio and video tracks back
// together -- and finding it is fiddly enough on Windows to be worth having
// in one place.
package ffmpeg

import (
	"os"
	"os/exec"
	"sync"
)

var (
	mu     sync.Mutex
	cached string
)

// Path returns the ffmpeg binary to use, or "" when none is installed.
//
// A successful lookup is cached, a failed one is not: the daemon is
// long-lived and may well be running when ffmpeg gets installed, and
// remembering "no ffmpeg" forever would mean never noticing.
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	if cached != "" {
		return cached
	}
	if p := os.Getenv("DM_FFMPEG"); p != "" {
		if _, err := os.Stat(p); err == nil {
			cached = p
			return cached
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		cached = p
		return cached
	}
	// Not on PATH. That often just means this process was started before
	// ffmpeg was installed, so check where installers actually put it.
	for _, cand := range wellKnown() {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			cached = cand
			return cached
		}
	}
	return cached
}
