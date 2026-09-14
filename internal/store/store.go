// Package store persists the download list and user config between runs.
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record is one download as the daemon remembers it across restarts.
type Record struct {
	ID         string            `json:"id"`
	URL        string            `json:"url"`
	Kind       string            `json:"kind,omitempty"` // file (default) or hls
	FinalURL   string            `json:"finalUrl,omitempty"`
	Path       string            `json:"path,omitempty"`
	Filename   string            `json:"filename"`
	Size       int64             `json:"size"`
	Downloaded int64             `json:"downloaded"`
	State      string            `json:"state"`
	Error      string            `json:"error,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Dir        string            `json:"dir,omitempty"`
	MaxConns   int               `json:"maxConns,omitempty"`

	// QueueID is the queue this download waits in. Empty means the default
	// queue, which is how every record written before queues existed reads.
	QueueID string `json:"queueId,omitempty"`

	// Category groups the download for filing; Description is the user's
	// own note, both shown in the Download File Info dialog.
	Category    string    `json:"category,omitempty"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	Finished    time.Time `json:"finished,omitempty"`
}

// Config holds user-tunable daemon settings.
type Config struct {
	Dir           string `json:"dir"`
	MaxConns      int    `json:"maxConns"`
	MaxConcurrent int    `json:"maxConcurrent"`
	Port          int    `json:"port"`

	// LimitKBps caps total throughput across all downloads, in KiB/s.
	// Zero means unlimited.
	LimitKBps int `json:"limitKBps"`

	// Categories file finished downloads by type.
	Categories []Category `json:"categories"`

	// Queues are the named lines downloads wait in, each with its own limit
	// on how many run at once. MaxConcurrent above is the fallback used when
	// a queue does not name its own.
	Queues []Queue `json:"queues"`

	// StartWithWindows registers ODM to run at login.
	StartWithWindows bool `json:"startWithWindows"`

	// ShowStartDialog asks where to save before a download begins;
	// ShowCompleteDialog reports when one finishes. Both mirror IDM, and
	// both are switched off from inside their own dialog.
	ShowStartDialog    bool `json:"showStartDialog"`
	ShowCompleteDialog bool `json:"showCompleteDialog"`

	// ShowProgressDialog gives every running download its own small window
	// with a progress bar and pause and cancel buttons, the way IDM does.
	ShowProgressDialog bool `json:"showProgressDialog"`

	// Theme is "system", "light" or "dark".
	Theme string `json:"theme"`

	// ExtensionPromptDismissed stops the app nagging about the browser
	// extension.
	ExtensionPromptDismissed bool `json:"extensionPromptDismissed"`

	// OnComplete is what to do once every download has finished: none,
	// exit, sleep, hibernate, shutdown or restart. It is one-shot and
	// resets itself after firing, so a machine does not shut down every
	// time the queue happens to empty.
	OnComplete string `json:"onComplete"`

	// ConfigVersion records which defaults this file was written against,
	// so a changed default can be applied once to an existing install
	// instead of only to new ones. See migrate.
	ConfigVersion int `json:"configVersion"`
}

// currentConfigVersion is bumped whenever an existing config needs a value
// changed under it. Version 1 turned ShowStartDialog on: it shipped off,
// which left the browser extension and the app starting downloads with no
// dialog at all, and no clue that one existed.
const currentConfigVersion = 2

// DefaultConfig is used the first time the daemon starts.
func DefaultConfig(downloadDir string) Config {
	return Config{
		Dir:                downloadDir,
		MaxConns:           8,
		MaxConcurrent:      3,
		Port:               9111,
		LimitKBps:          0,
		Categories:         DefaultCategories(),
		Queues:             DefaultQueues(3),
		StartWithWindows:   false,
		ShowStartDialog:    true,
		ShowCompleteDialog: true,
		ShowProgressDialog: true,
		ConfigVersion:      currentConfigVersion,
		Theme:              "system",
		OnComplete:         "none",
	}
}

// Store is a small JSON-backed database. The whole download list lives in
// memory; disk is only a durability layer, so reads never touch the file.
type Store struct {
	dir string

	mu      sync.RWMutex
	records map[string]*Record
	cfg     Config

	// dirty coalesces rapid progress updates into one write per flush tick
	// instead of hammering the disk on every 500ms stats callback.
	dirty bool
}

// Dir returns the directory holding the daemon state.
func (s *Store) Dir() string { return s.dir }

// trimBOM drops a UTF-8 byte-order mark. encoding/json rejects one outright,
// so without this a config.json that has been through Notepad, or any editor
// that adds a BOM, would fail to parse and every setting in it would be
// silently ignored.
func trimBOM(b []byte) []byte { return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}) }

// Open loads (or creates) the state directory.
func Open(dir string, defaultDownloadDir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	s := &Store{dir: dir, records: map[string]*Record{}}

	s.cfg = DefaultConfig(defaultDownloadDir)
	if b, err := os.ReadFile(s.configPath()); err == nil {
		b = trimBOM(b)
		// Booleans are decoded separately as pointers. Decoding them into a
		// plain Config cannot tell "absent" from "false", so a config file
		// written before a flag existed would silently turn it off.
		var flags struct {
			StartWithWindows         *bool `json:"startWithWindows"`
			ExtensionPromptDismissed *bool `json:"extensionPromptDismissed"`
			ShowStartDialog          *bool `json:"showStartDialog"`
			ShowCompleteDialog       *bool `json:"showCompleteDialog"`
			ShowProgressDialog       *bool `json:"showProgressDialog"`
		}
		json.Unmarshal(b, &flags)

		var c Config
		if json.Unmarshal(b, &c) == nil {
			if c.Dir != "" {
				s.cfg.Dir = c.Dir
			}
			if c.MaxConns > 0 {
				s.cfg.MaxConns = c.MaxConns
			}
			if c.MaxConcurrent > 0 {
				s.cfg.MaxConcurrent = c.MaxConcurrent
			}
			if c.Port > 0 {
				s.cfg.Port = c.Port
			}
			// Zero is meaningful here (unlimited), so it is always taken.
			if c.LimitKBps >= 0 {
				s.cfg.LimitKBps = c.LimitKBps
			}
			if len(c.Categories) > 0 {
				s.cfg.Categories = c.Categories
			}
			if len(c.Queues) > 0 {
				s.cfg.Queues = c.Queues
			}
			if c.Theme != "" {
				s.cfg.Theme = c.Theme
			}
			if flags.StartWithWindows != nil {
				s.cfg.StartWithWindows = *flags.StartWithWindows
			}
			if flags.ExtensionPromptDismissed != nil {
				s.cfg.ExtensionPromptDismissed = *flags.ExtensionPromptDismissed
			}
			if flags.ShowStartDialog != nil {
				s.cfg.ShowStartDialog = *flags.ShowStartDialog
			}
			if flags.ShowCompleteDialog != nil {
				s.cfg.ShowCompleteDialog = *flags.ShowCompleteDialog
			}
			if flags.ShowProgressDialog != nil {
				s.cfg.ShowProgressDialog = *flags.ShowProgressDialog
			}
			s.cfg.ConfigVersion = c.ConfigVersion
			s.migrate()
		}
	}

	if b, err := os.ReadFile(s.dbPath()); err == nil {
		var list []*Record
		if err := json.Unmarshal(trimBOM(b), &list); err == nil {
			for _, r := range list {
				s.records[r.ID] = r
			}
		}
	}
	return s, nil
}

// ExtensionSeen reports whether a browser extension has ever connected
// through the native messaging host.
func (s *Store) ExtensionSeen() bool {
	_, err := os.Stat(filepath.Join(s.dir, "extension_seen"))
	return err == nil
}

func (s *Store) dbPath() string { return filepath.Join(s.dir, "downloads.json") }

// migrate brings a config written against older defaults up to date and
// writes it straight back, so the change happens once instead of on every
// start. It runs under Open, before anything can read the config, and only
// ever moves forward.
func (s *Store) migrate() {
	if s.cfg.ConfigVersion >= currentConfigVersion {
		return
	}
	if s.cfg.ConfigVersion < 1 {
		// Shipped off by mistake. Anyone who turns it off from now on does
		// so against a config that already records version 1, so their
		// choice is never overwritten.
		s.cfg.ShowStartDialog = true
	}
	if s.cfg.ConfigVersion < 2 {
		// Queues arrived after this config was written. Seed the default one
		// from the single global limit it used to carry, so the machine
		// behaves exactly as it did before.
		if len(s.cfg.Queues) == 0 {
			s.cfg.Queues = DefaultQueues(s.cfg.MaxConcurrent)
		}
	}
	s.cfg.ConfigVersion = currentConfigVersion
	if b, err := json.MarshalIndent(s.cfg, "", "  "); err == nil {
		_ = writeAtomic(s.configPath(), b, 0o600)
	}
}

func (s *Store) configPath() string { return filepath.Join(s.dir, "config.json") }
func (s *Store) tokenPath() string  { return filepath.Join(s.dir, "token") }

// Token returns the shared secret that authenticates API calls, generating it
// on first use. Without it any web page could POST downloads to our localhost
// port; see the api package for how it is enforced.
func (s *Store) Token() (string, error) {
	if b, err := os.ReadFile(s.tokenPath()); err == nil && len(b) >= 32 {
		return string(b), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.WriteFile(s.tokenPath(), []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// Config returns a copy of the current settings.
func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SetConfig merges non-empty fields and writes the settings through.
//
// Booleans cannot be merged this way, since false is indistinguishable from
// unset; use SetFlags for those.
func (s *Store) SetConfig(c Config) error {
	s.mu.Lock()
	if c.Dir != "" {
		s.cfg.Dir = c.Dir
	}
	if c.MaxConns > 0 {
		s.cfg.MaxConns = c.MaxConns
	}
	if c.MaxConcurrent > 0 {
		s.cfg.MaxConcurrent = c.MaxConcurrent
	}
	if c.Port > 0 {
		s.cfg.Port = c.Port
	}
	if c.LimitKBps >= 0 {
		s.cfg.LimitKBps = c.LimitKBps
	}
	if len(c.Categories) > 0 {
		s.cfg.Categories = c.Categories
	}
	if len(c.Queues) > 0 {
		s.cfg.Queues = c.Queues
	}
	if c.Theme != "" {
		s.cfg.Theme = c.Theme
	}
	if c.OnComplete != "" {
		s.cfg.OnComplete = c.OnComplete
	}
	cfg := s.cfg
	s.mu.Unlock()

	return s.writeConfig(cfg)
}

// writeConfig persists a config snapshot taken under the lock.
func (s *Store) writeConfig(cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.configPath(), b, 0o600)
}

// Flags are the boolean settings, passed around as a set so that turning
// one off is not mistaken for leaving it alone.
type Flags struct {
	StartWithWindows         *bool
	ShowStartDialog          *bool
	ShowCompleteDialog       *bool
	ShowProgressDialog       *bool
	ExtensionPromptDismissed *bool
}

// SetFlags updates whichever booleans are supplied and persists them.
func (s *Store) SetFlags(f Flags) error {
	s.mu.Lock()
	if f.StartWithWindows != nil {
		s.cfg.StartWithWindows = *f.StartWithWindows
	}
	if f.ShowStartDialog != nil {
		s.cfg.ShowStartDialog = *f.ShowStartDialog
	}
	if f.ShowCompleteDialog != nil {
		s.cfg.ShowCompleteDialog = *f.ShowCompleteDialog
	}
	if f.ShowProgressDialog != nil {
		s.cfg.ShowProgressDialog = *f.ShowProgressDialog
	}
	if f.ExtensionPromptDismissed != nil {
		s.cfg.ExtensionPromptDismissed = *f.ExtensionPromptDismissed
	}
	cfg := s.cfg
	s.mu.Unlock()

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.configPath(), b, 0o600)
}

// Put inserts or replaces a record and marks the DB dirty.
func (s *Store) Put(r *Record) {
	s.mu.Lock()
	cp := *r
	s.records[r.ID] = &cp
	s.dirty = true
	s.mu.Unlock()
}

// Get returns a copy of one record.
func (s *Store) Get(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	if !ok {
		return Record{}, false
	}
	return *r, true
}

// Delete drops a record.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.records, id)
	s.dirty = true
	s.mu.Unlock()
}

// List returns every record, newest first.
func (s *Store) List() []Record {
	s.mu.RLock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, *r)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Flush writes the download list to disk if anything changed.
func (s *Store) Flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	list := make([]*Record, 0, len(s.records))
	for _, r := range s.records {
		cp := *r
		list = append(list, &cp)
	}
	s.dirty = false
	s.mu.Unlock()

	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.dbPath(), b, 0o600)
}

// writeAtomic avoids leaving a truncated state file behind if we are killed
// mid-write, which would lose the whole download list.
func writeAtomic(path string, b []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NewID returns a short random identifier for a download.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("d%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// StateDir is where the daemon keeps its database, config and token.
func StateDir() string {
	if v := os.Getenv("DM_STATE_DIR"); v != "" {
		return v
	}
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		return filepath.Join(appdata, "odm")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".odm"
	}
	return filepath.Join(home, ".odm")
}
