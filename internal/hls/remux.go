package hls

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ffmpegOnce sync.Once
	ffmpegPath string
)

// FFmpegPath returns the ffmpeg binary to use, or "" when none is installed.
// Looked up once: the answer does not change during a run.
func FFmpegPath() string {
	ffmpegOnce.Do(func() {
		if p := os.Getenv("DM_FFMPEG"); p != "" {
			if _, err := os.Stat(p); err == nil {
				ffmpegPath = p
				return
			}
		}
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = p
		}
	})
	return ffmpegPath
}

// remux converts an MPEG-TS file into MP4 without re-encoding.
//
// HLS segments concatenate into a valid .ts, but plenty of players and
// editors will not touch one. This is a stream copy, so it costs a disk pass
// rather than a transcode. Returns the new path, or "" when nothing was done.
func remux(ctx context.Context, tsPath string) (string, error) {
	if !strings.EqualFold(filepath.Ext(tsPath), ".ts") {
		return "", nil // already MP4 (fMP4 playlist), nothing to do
	}
	ff := FFmpegPath()
	if ff == "" {
		return "", nil // no ffmpeg: keeping the .ts is a fine outcome
	}

	mp4Path := strings.TrimSuffix(tsPath, filepath.Ext(tsPath)) + ".mp4"
	if _, err := os.Stat(mp4Path); err == nil {
		var err2 error
		mp4Path, err2 = uniquePath(filepath.Dir(mp4Path), filepath.Base(mp4Path))
		if err2 != nil {
			return "", err2
		}
	}

	// Remuxing a long video is still IO-bound work; give it room but do not
	// let a wedged ffmpeg hang the download queue forever.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, ff,
		"-hide_banner", "-loglevel", "error",
		"-i", tsPath,
		"-c", "copy",
		// ADTS AAC from a TS stream needs this filter to be legal in MP4.
		"-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart",
		"-y", mp4Path,
	)
	hideWindow(cmd)

	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(mp4Path)
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return "", fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	st, statErr := os.Stat(mp4Path)
	if statErr != nil || st.Size() == 0 {
		os.Remove(mp4Path)
		return "", fmt.Errorf("ffmpeg produced no output")
	}

	// Only drop the source once the result is known good.
	os.Remove(tsPath)
	return mp4Path, nil
}
