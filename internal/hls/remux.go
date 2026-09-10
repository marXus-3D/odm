package hls

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ffmpegMu   sync.Mutex
	ffmpegPath string
)

// FFmpegPath returns the ffmpeg binary to use, or "" when none is installed.
//
// A successful lookup is cached, a failed one is not: the daemon is
// long-lived and may well be running when ffmpeg gets installed, and
// remembering "no ffmpeg" forever would mean never noticing.
func FFmpegPath() string {
	ffmpegMu.Lock()
	defer ffmpegMu.Unlock()
	if ffmpegPath != "" {
		return ffmpegPath
	}
	if p := os.Getenv("DM_FFMPEG"); p != "" {
		if _, err := os.Stat(p); err == nil {
			ffmpegPath = p
			return ffmpegPath
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		ffmpegPath = p
		return ffmpegPath
	}
	// Not on PATH. That often just means this process was started before
	// ffmpeg was installed, so check where installers actually put it.
	for _, cand := range wellKnownFFmpeg() {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			ffmpegPath = cand
			return ffmpegPath
		}
	}
	return ffmpegPath
}

// remuxTimeout bounds the conversion. It is a stream copy, so even a long
// film is minutes, but a wedged ffmpeg must not pin the download queue.
const remuxTimeout = 30 * time.Minute

// remux converts an MPEG-TS file into MP4 without re-encoding.
//
// HLS segments concatenate into a valid .ts, but plenty of players and
// editors will not open one. This is a stream copy: a disk pass, not a
// transcode. Returns the new path, or "" when there was nothing to do.
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
	// Write somewhere else and rename on success, so a crash or a killed
	// ffmpeg never leaves a truncated file sitting there looking finished.
	partPath := mp4Path + ".part"
	os.Remove(partPath)

	ctx, cancel := context.WithTimeout(ctx, remuxTimeout)
	defer cancel()

	// aac_adtstoasc is required to put ADTS AAC from a transport stream into
	// MP4, but ffmpeg refuses the filter outright when the audio is anything
	// else (AC-3, MP2, or no audio at all). Rather than probe the file first,
	// try the common case and fall back.
	err := runFFmpeg(ctx, ff, tsPath, partPath, true)
	if err != nil && ctx.Err() == nil {
		os.Remove(partPath)
		if err2 := runFFmpeg(ctx, ff, tsPath, partPath, false); err2 != nil {
			os.Remove(partPath)
			return "", err // report the first failure; it is the informative one
		}
	} else if err != nil {
		os.Remove(partPath)
		return "", err
	}

	st, statErr := os.Stat(partPath)
	if statErr != nil || st.Size() == 0 {
		os.Remove(partPath)
		return "", errors.New("ffmpeg produced no output")
	}
	if err := os.Rename(partPath, mp4Path); err != nil {
		os.Remove(partPath)
		return "", err
	}

	// Only drop the source once the result is on disk under its real name.
	os.Remove(tsPath)
	return mp4Path, nil
}

// runFFmpeg performs one conversion attempt.
func runFFmpeg(ctx context.Context, ff, in, out string, adtsFilter bool) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-fflags", "+genpts", // TS from HLS often has gaps in its timestamps
		"-i", in,
		"-c", "copy",
		"-map", "0",
	}
	if adtsFilter {
		args = append(args, "-bsf:a", "aac_adtstoasc")
	}
	args = append(args,
		"-movflags", "+faststart", // moov atom first, so it streams
		// The output is written as <name>.mp4.part, and ffmpeg picks the
		// muxer from the extension unless told otherwise: without this it
		// fails with "Unable to choose an output format".
		"-f", "mp4",
		"-y", out,
	)

	cmd := exec.CommandContext(ctx, ff, args...)
	hideWindow(cmd)

	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ffmpeg timed out after %s", remuxTimeout)
		}
		msg := strings.TrimSpace(string(outBytes))
		if len(msg) > 400 {
			msg = msg[:400] + "..."
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg: %s", msg)
	}
	return nil
}
