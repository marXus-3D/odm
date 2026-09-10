// Package store persists the download list and user config between runs.
package store

import (
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
	Created    time.Time         `json:"created"`
	Finished   time.Time         `json:"finished,omitempty"`
}

// Config holds user-tunable daemon settings.
type Config struct {
	Dir           string `json:"dir"`
	MaxConns      int    `json:"maxConns"`
	MaxConcurrent int    `json:"maxConcurrent"`
	Port          int    `json:"port"`
}

// DefaultConfig is used the first time the daemon starts.
func DefaultConfig(downloadDir string) Config {
	return Config{
		Dir:           downloadDir,
		MaxConns:      8,
		MaxConcurrent: 3,
		Port:          9111,
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

// Open loads (or creates) the state directory.
func Open(dir string, defaultDownloadDir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	s := &Store{dir: dir, records: map[string]*Record{}}

	s.cfg = DefaultConfig(defaultDownloadDir)
	if b, err := os.ReadFile(s.configPath()); err == nil {
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
		}
	}

	if b, err := os.ReadFile(s.dbPath()); err == nil {
		var list []*Record
		if err := json.Unmarshal(b, &list); err == nil {
			for _, r := range list {
				s.records[r.ID] = r
			}
		}
	}
	return s, nil
}

func (s *Store) dbPath() string     { return filepath.Join(s.dir, "downloads.json") }
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

// SetConfig replaces the settings and writes them through immediately.
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
		return filepath.Join(appdata, "dm")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".dm"
	}
	return filepath.Join(home, ".dm")
}
