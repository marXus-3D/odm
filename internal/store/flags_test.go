package store

import (
	"path/filepath"
	"testing"
)

// The boolean settings are split off into Flags on purpose: false and unset
// look the same over JSON, so a merging setter can never turn one off. These
// tests pin that division down, because from the outside a setting that
// silently refuses to switch off is indistinguishable from a broken toggle.

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir, filepath.Join(dir, "downloads"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return st, dir
}

func TestSetFlagsTurnsSettingsOff(t *testing.T) {
	st, dir := open(t)

	// The defaults have all three dialogs on, which is what makes "off" the
	// interesting direction.
	if cfg := st.Config(); !cfg.ShowStartDialog || !cfg.ShowCompleteDialog || !cfg.ShowProgressDialog {
		t.Fatalf("expected the dialogs to start on, got %+v", cfg)
	}

	off := false
	if err := st.SetFlags(Flags{
		ShowStartDialog:          &off,
		ShowCompleteDialog:       &off,
		ShowProgressDialog:       &off,
		ExtensionPromptDismissed: &off,
	}); err != nil {
		t.Fatalf("set flags: %v", err)
	}

	cfg := st.Config()
	if cfg.ShowStartDialog || cfg.ShowCompleteDialog || cfg.ShowProgressDialog {
		t.Errorf("dialogs did not switch off: %+v", cfg)
	}

	// And it has to survive a restart, or the toggle only appears to work
	// until the daemon is next started.
	again, err := Open(dir, filepath.Join(dir, "downloads"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	cfg = again.Config()
	if cfg.ShowStartDialog || cfg.ShowCompleteDialog || cfg.ShowProgressDialog {
		t.Errorf("dialogs came back on after a restart: %+v", cfg)
	}
}

func TestSetFlagsLeavesUnnamedFlagsAlone(t *testing.T) {
	st, _ := open(t)

	off := false
	if err := st.SetFlags(Flags{ShowStartDialog: &off}); err != nil {
		t.Fatalf("set flags: %v", err)
	}
	// Only one flag was named, so the others must not have moved.
	cfg := st.Config()
	if cfg.ShowStartDialog {
		t.Error("ShowStartDialog should be off")
	}
	if !cfg.ShowCompleteDialog || !cfg.ShowProgressDialog {
		t.Errorf("an unnamed flag was changed: %+v", cfg)
	}

	on := true
	if err := st.SetFlags(Flags{ShowStartDialog: &on}); err != nil {
		t.Fatalf("set flags: %v", err)
	}
	if !st.Config().ShowStartDialog {
		t.Error("ShowStartDialog should be back on")
	}
}

// TestSetConfigDoesNotCarryFlags documents the division rather than a
// defect: SetConfig merges the non-empty fields of a Config, which cannot
// include booleans. Callers that want to change one use SetFlags.
func TestSetConfigDoesNotCarryFlags(t *testing.T) {
	st, _ := open(t)

	// A zero-valued bool in a Config means "not supplied", so this must
	// leave the dialogs exactly as they were.
	if err := st.SetConfig(Config{Dir: `C:\Downloads\odm`, MaxConns: 4}); err != nil {
		t.Fatalf("set config: %v", err)
	}
	cfg := st.Config()
	if !cfg.ShowStartDialog || !cfg.ShowCompleteDialog || !cfg.ShowProgressDialog {
		t.Errorf("SetConfig cleared a flag it was not given: %+v", cfg)
	}
	// The fields it can carry still take effect.
	if cfg.Dir != `C:\Downloads\odm` {
		t.Errorf("Dir = %q, want the new one", cfg.Dir)
	}
	if cfg.MaxConns != 4 {
		t.Errorf("MaxConns = %d, want 4", cfg.MaxConns)
	}
}
