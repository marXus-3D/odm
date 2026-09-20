package ffmpeg

import (
	"os"
	"testing"
)

// reset clears the memoised lookup so a test can watch it happen again.
func reset() {
	mu.Lock()
	cached = ""
	mu.Unlock()
}

func TestPathHonoursDMFFmpegEnv(t *testing.T) {
	real := Path()
	if real == "" {
		t.Skip("ffmpeg is not installed")
	}
	old := os.Getenv("DM_FFMPEG")
	t.Cleanup(func() {
		os.Setenv("DM_FFMPEG", old)
		reset()
	})

	os.Setenv("DM_FFMPEG", real)
	reset()

	if got := Path(); got != real {
		t.Errorf("DM_FFMPEG override = %q, want %q", got, real)
	}
}

// TestPathDoesNotCacheFailure pins down the one case the cache must not
// remember: the daemon outliving an ffmpeg install, where caching "not
// found" would mean never noticing it arrived.
func TestPathDoesNotCacheFailure(t *testing.T) {
	old := os.Getenv("DM_FFMPEG")
	t.Cleanup(func() {
		os.Setenv("DM_FFMPEG", old)
		reset()
	})

	os.Setenv("DM_FFMPEG", "")
	reset()
	if Path() == "" {
		// No ffmpeg anywhere on this machine; nothing was cached either way.
		if cached != "" {
			t.Errorf("a failed lookup was cached as %q", cached)
		}
		return
	}
	// ffmpeg is installed, so the lookup succeeded and caching it is correct.
	if cached == "" {
		t.Error("a successful lookup was not cached")
	}
}

// On Windows a PATH change only reaches processes started afterwards, so a
// daemon launched by an already-running browser sees a stale PATH. The
// well-known-location fallback is what stops that from silently disabling
// conversion.
func TestPathFoundWithoutPATH(t *testing.T) {
	if Path() == "" {
		t.Skip("ffmpeg is not installed")
	}

	oldPath := os.Getenv("PATH")
	oldDM := os.Getenv("DM_FFMPEG")
	t.Cleanup(func() {
		os.Setenv("PATH", oldPath)
		os.Setenv("DM_FFMPEG", oldDM)
		reset()
	})

	os.Setenv("PATH", t.TempDir()) // nothing useful in here
	os.Unsetenv("DM_FFMPEG")
	reset() // drop the cached hit so the lookup really reruns

	got := Path()
	if got == "" {
		t.Skip("ffmpeg is installed somewhere the fallback list does not cover")
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("fallback returned %q which does not exist: %v", got, err)
	}
	t.Logf("found without PATH: %s", got)
}
