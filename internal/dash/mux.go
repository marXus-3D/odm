package dash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/marXus-3D/odm/internal/ffmpeg"
)

// muxTimeout bounds the final step. It is a stream copy, so even a long film
// is minutes, but a wedged ffmpeg must not pin the download queue.
const muxTimeout = 30 * time.Minute

// ErrNoFFmpeg is returned when a manifest has separate audio and video and
// there is no ffmpeg to put them back together.
//
// Downloading anyway would leave two files, neither of which is the video
// the user asked for, so this is refused up front rather than after the
// bytes have been spent.
var ErrNoFFmpeg = errors.New(
	"this stream keeps its audio and video apart and ffmpeg is needed to " +
		"join them; install ffmpeg and try again")

// haveFFmpeg reports whether the final step can be performed at all.
func haveFFmpeg() bool { return ffmpeg.Path() != "" }

// mux writes one playable MP4 from the track files.
//
// audioPath may be empty, which is the muxed-manifest case: the video file
// already carries the sound and only needs repackaging. Returns nothing but
// an error, because the caller already knows the output path.
func mux(ctx context.Context, videoPath, audioPath, outPath string) error {
	ff := ffmpeg.Path()
	if ff == "" {
		return ErrNoFFmpeg
	}

	// Write somewhere else and rename on success, so a crash or a killed
	// ffmpeg never leaves a truncated file sitting there looking finished.
	partPath := outPath + ".part"
	os.Remove(partPath)

	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()

	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", videoPath}
	if audioPath != "" {
		args = append(args, "-i", audioPath)
	}
	args = append(args, "-c", "copy")
	if audioPath != "" {
		// Name the streams explicitly. Left to itself ffmpeg takes the best
		// stream of each type across all inputs, which is usually right and
		// occasionally picks a stray track out of the video file.
		args = append(args, "-map", "0:v:0", "-map", "1:a:0")
	} else {
		args = append(args, "-map", "0")
	}
	args = append(args,
		"-movflags", "+faststart", // moov atom first, so it streams
		// The output is written as <name>.mp4.part, and ffmpeg picks the
		// muxer from the extension unless told otherwise: without this it
		// fails with "Unable to choose an output format".
		"-f", "mp4",
		"-y", partPath,
	)

	cmd := exec.CommandContext(ctx, ff, args...)
	ffmpeg.Hide(cmd)

	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(partPath)
		if ctx.Err() != nil {
			return fmt.Errorf("ffmpeg timed out after %s", muxTimeout)
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

	st, statErr := os.Stat(partPath)
	if statErr != nil || st.Size() == 0 {
		os.Remove(partPath)
		return errors.New("ffmpeg produced no output")
	}
	if err := os.Rename(partPath, outPath); err != nil {
		os.Remove(partPath)
		return err
	}
	return nil
}
