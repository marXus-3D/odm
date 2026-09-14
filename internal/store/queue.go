package store

import (
	"fmt"
	"strings"
)

// A queue is a named line of downloads with its own limit on how many run at
// once. One queue for a game and another for videos lets a big install crawl
// along on a single connection while episodes keep arriving three at a time.
//
// Every download belongs to exactly one queue. A record with no queue, which
// is every record written before queues existed, belongs to the default one.
type Queue struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// MaxConcurrent is how many of this queue's downloads may run at once.
	// It is clamped to at least one when the scheduler reads it, so a
	// hand-edited zero cannot wedge a queue shut.
	MaxConcurrent int `json:"maxConcurrent"`
}

// DefaultQueueID is the queue every download lands in unless told otherwise.
// It cannot be deleted or renamed away: something has to catch the downloads
// that arrive from the browser without a queue in mind.
const DefaultQueueID = "main"

// DefaultQueues is the starting set.
func DefaultQueues(maxConcurrent int) []Queue {
	if maxConcurrent < 1 {
		maxConcurrent = 3
	}
	return []Queue{{ID: DefaultQueueID, Name: "Main", MaxConcurrent: maxConcurrent}}
}

// QueueByID returns the queue a download belongs to, falling back to the
// default. It always returns a usable queue, so callers never have to guard
// against a record pointing at a queue that has since been deleted.
func (c Config) QueueByID(id string) Queue {
	if id == "" {
		id = DefaultQueueID
	}
	for _, q := range c.Queues {
		if q.ID == id {
			if q.MaxConcurrent < 1 {
				q.MaxConcurrent = 1
			}
			return q
		}
	}
	for _, q := range c.Queues {
		if q.ID == DefaultQueueID {
			if q.MaxConcurrent < 1 {
				q.MaxConcurrent = 1
			}
			return q
		}
	}
	return Queue{ID: DefaultQueueID, Name: "Main", MaxConcurrent: 3}
}

// QueueByName finds a queue by its display name, case-insensitively, which is
// what the command line and the dialogs take from the user.
func (c Config) QueueByName(name string) (Queue, bool) {
	for _, q := range c.Queues {
		if strings.EqualFold(q.Name, name) {
			return q, true
		}
	}
	return Queue{}, false
}

// queueID turns a display name into a stable id. Ids are what records store,
// so renaming a queue must not orphan its downloads.
func queueID(name string, existing []Queue) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "queue"
	}
	id := base
	for n := 2; ; n++ {
		taken := false
		for _, q := range existing {
			if q.ID == id {
				taken = true
				break
			}
		}
		if !taken {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

// AddQueue creates a queue and returns it.
func (s *Store) AddQueue(name string, maxConcurrent int) (Queue, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Queue{}, fmt.Errorf("a queue needs a name")
	}
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	s.mu.Lock()
	for _, q := range s.cfg.Queues {
		if strings.EqualFold(q.Name, name) {
			s.mu.Unlock()
			return Queue{}, fmt.Errorf("a queue called %q already exists", name)
		}
	}
	q := Queue{ID: queueID(name, s.cfg.Queues), Name: name, MaxConcurrent: maxConcurrent}
	s.cfg.Queues = append(s.cfg.Queues, q)
	cfg := s.cfg
	s.mu.Unlock()

	return q, s.writeConfig(cfg)
}

// UpdateQueue renames a queue or changes its limit. Empty name or a
// non-positive limit leaves that field alone.
func (s *Store) UpdateQueue(id, name string, maxConcurrent int) (Queue, error) {
	name = strings.TrimSpace(name)

	s.mu.Lock()
	idx := -1
	for i, q := range s.cfg.Queues {
		if q.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return Queue{}, fmt.Errorf("no queue with id %q", id)
	}
	if name != "" {
		for i, q := range s.cfg.Queues {
			if i != idx && strings.EqualFold(q.Name, name) {
				s.mu.Unlock()
				return Queue{}, fmt.Errorf("a queue called %q already exists", name)
			}
		}
		s.cfg.Queues[idx].Name = name
	}
	if maxConcurrent > 0 {
		s.cfg.Queues[idx].MaxConcurrent = maxConcurrent
	}
	q := s.cfg.Queues[idx]
	cfg := s.cfg
	s.mu.Unlock()

	return q, s.writeConfig(cfg)
}

// RemoveQueue deletes a queue. Its downloads move to the default queue
// rather than disappearing with it.
func (s *Store) RemoveQueue(id string) error {
	if id == DefaultQueueID {
		return fmt.Errorf("the default queue cannot be removed")
	}

	s.mu.Lock()
	idx := -1
	for i, q := range s.cfg.Queues {
		if q.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no queue with id %q", id)
	}
	s.cfg.Queues = append(s.cfg.Queues[:idx], s.cfg.Queues[idx+1:]...)
	cfg := s.cfg
	for _, r := range s.records {
		if r.QueueID == id {
			r.QueueID = DefaultQueueID
		}
	}
	s.dirty = true
	s.mu.Unlock()

	return s.writeConfig(cfg)
}

// Queues returns the configured queues.
func (s *Store) Queues() []Queue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Queue, len(s.cfg.Queues))
	copy(out, s.cfg.Queues)
	return out
}

// SetRecordQueue moves one download to another queue.
func (s *Store) SetRecordQueue(id, queueID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return fmt.Errorf("no download with id %q", id)
	}
	found := false
	for _, q := range s.cfg.Queues {
		if q.ID == queueID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no queue with id %q", queueID)
	}
	r.QueueID = queueID
	s.dirty = true
	return nil
}
