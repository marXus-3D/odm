package engine

import (
	"encoding/json"
	"os"
	"time"
)

const (
	metaSuffix  = ".dm"
	metaVersion = 1
)

// Meta is the resume sidecar written next to the output file. It holds
// everything needed to pick a download back up in a later process, plus the
// validators used to prove the remote bytes haven't changed underneath us.
type Meta struct {
	Version      int               `json:"version"`
	URL          string            `json:"url"`
	FinalURL     string            `json:"finalUrl"`
	Path         string            `json:"path"`
	Size         int64             `json:"size"`
	Resumable    bool              `json:"resumable"`
	ETag         string            `json:"etag,omitempty"`
	LastModified string            `json:"lastModified,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Segments     []SegmentSnapshot `json:"segments"`
	Updated      time.Time         `json:"updated"`
}

// validator returns the strongest If-Range token available, preferring the
// ETag. An empty result means we can't prove freshness and must not resume.
func (m *Meta) validator() string {
	if m.ETag != "" {
		return m.ETag
	}
	return m.LastModified
}

func loadMeta(path string) (*Meta, error) {
	b, err := os.ReadFile(path + metaSuffix)
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Version != metaVersion {
		return nil, os.ErrInvalid
	}
	return &m, nil
}

// save writes the sidecar atomically: a torn metadata file during a crash is
// worse than no metadata file, because it would silently corrupt a resume.
func (m *Meta) save() error {
	m.Updated = time.Now()
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := m.Path + metaSuffix + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.Path+metaSuffix)
}

func removeMeta(path string) {
	os.Remove(path + metaSuffix)
	os.Remove(path + metaSuffix + ".tmp")
}

// matches reports whether a stored sidecar still describes the resource we
// just probed. Any disagreement means we start over rather than stitch two
// different files together.
func (m *Meta) matches(p *Probe) bool {
	if m.Size != p.Size || !p.Resumable || !m.Resumable {
		return false
	}
	if m.ETag != "" && p.ETag != "" {
		return m.ETag == p.ETag
	}
	if m.LastModified != "" && p.LastModified != "" {
		return m.LastModified == p.LastModified
	}
	// No validators on either side: size agreement is all we have.
	return m.Size > 0
}
