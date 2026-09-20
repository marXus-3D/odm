//go:build !windows

package ffmpeg

// wellKnownFFmpeg lists fallback locations for systems where PATH is
// reliable enough that there is little to add.
func wellKnown() []string {
	return []string{
		"/usr/bin/ffmpeg",
		"/usr/local/bin/ffmpeg",
		"/opt/homebrew/bin/ffmpeg",
	}
}
