//go:build windows

package hls

import (
	"os"
	"path/filepath"
)

// wellKnownFFmpeg lists the usual install locations, checked when ffmpeg is
// not on PATH.
//
// On Windows a PATH change only reaches processes started afterwards. The
// daemon is often launched by the browser through the native messaging host,
// so a browser that was already open when ffmpeg was installed hands us a
// stale PATH and the conversion silently never happens. Looking in the
// obvious places avoids telling the user to restart everything.
func wellKnownFFmpeg() []string {
	var dirs []string
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		dirs = append(dirs,
			filepath.Join(v, "Microsoft", "WinGet", "Links"),
			filepath.Join(v, "Programs", "ffmpeg", "bin"),
		)
	}
	for _, v := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if v != "" {
			dirs = append(dirs, filepath.Join(v, "ffmpeg", "bin"))
		}
	}
	if v := os.Getenv("ChocolateyInstall"); v != "" {
		dirs = append(dirs, filepath.Join(v, "bin"))
	} else if v := os.Getenv("ProgramData"); v != "" {
		dirs = append(dirs, filepath.Join(v, "chocolatey", "bin"))
	}
	if v := os.Getenv("SCOOP"); v != "" {
		dirs = append(dirs, filepath.Join(v, "shims"))
	} else if v, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(v, "scoop", "shims"))
	}

	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, filepath.Join(d, "ffmpeg.exe"))
	}
	return out
}
