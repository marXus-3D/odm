// Package manager owns the download queue: what runs now, what waits, and
// what the UI is told about it.
package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/marcus/dm/internal/engine"
	"github.com/marcus/dm/internal/hls"
	"github.com/marcus/dm/internal/store"
)

// Event is a change the UI should react to.
type Event struct {
	Type string       `json:"type"` // added | updated | removed
	Item store.Record `json:"item"`
}

// ErrNotFound is returned for an unknown download id.
var ErrNotFound = errors.New("download not found")

type entry struct {
	rec store.Record
	job job
}

// Manager runs downloads subject to a concurrency limit and broadcasts
// progress to subscribers.
type Manager struct {
	st  *store.Store
	ctx context.Context

	// limiter is shared by every download so the ceiling is a global one.
	limiter *engine.Limiter

	mu      sync.Mutex
	entries map[string]*entry
	queue   []string
	running int

	subMu sync.Mutex
	subs  map[chan Event]struct{}
}

// New restores the persisted list and prepares the queue.
func New(ctx context.Context, st *store.Store) *Manager {
	m := &Manager{
		st:      st,
		ctx:     ctx,
		entries: map[string]*entry{},
		subs:    map[chan Event]struct{}{},
		limiter: engine.NewLimiter(float64(st.Config().LimitKBps) * 1024),
	}
	for _, r := range st.List() {
		rec := r
		// Anything that was mid-flight when we last exited is resumable, not
		// running: the worker goroutines died with the process.
		switch rec.State {
		case string(engine.StateDownloading), string(engine.StateProbing),
			string(engine.StateQueued):
			rec.State = string(engine.StatePaused)
			st.Put(&rec)
		case string(hls.StateRemuxing):
			// The bytes were all fetched and the sidecar is gone; only the
			// MP4 conversion was interrupted. The .ts is intact and plays,
			// so this is done, not resumable.
			rec.State = string(engine.StateDone)
			st.Put(&rec)
		}
		m.entries[rec.ID] = &entry{rec: rec}
	}
	return m
}

// AddRequest is a request to queue a download. It is the manager's own type
// rather than engine.Request because Kind decides which engine runs it.
type AddRequest struct {
	URL      string
	Filename string
	Dir      string
	MaxConns int
	Headers  map[string]string
	Kind     string // "", "file" or "hls"; empty means detect from the URL
}

// Add queues a new download and returns its record.
func (m *Manager) Add(req AddRequest) (store.Record, error) {
	if req.URL == "" {
		return store.Record{}, errors.New("url is required")
	}
	cfg := m.st.Config()
	if req.Dir == "" {
		req.Dir = cfg.Dir
	}
	if req.MaxConns <= 0 {
		req.MaxConns = cfg.MaxConns
	}

	kind := DetectKind(req.URL)
	if req.Kind != "" {
		kind = req.Kind // the extension may have sniffed the content type
	}
	if kind == KindDASH {
		return store.Record{}, ErrDASHUnsupported
	}
	rec := store.Record{
		ID:       store.NewID(),
		URL:      req.URL,
		Kind:     kind,
		Filename: req.Filename,
		Headers:  req.Headers,
		Dir:      req.Dir,
		MaxConns: req.MaxConns,
		Size:     -1,
		State:    string(engine.StateQueued),
		Created:  time.Now(),
	}
	m.st.Put(&rec)

	m.mu.Lock()
	m.entries[rec.ID] = &entry{rec: rec}
	m.queue = append(m.queue, rec.ID)
	m.mu.Unlock()

	m.broadcast(Event{Type: "added", Item: rec})
	m.pump()
	return rec, nil
}

// Pause stops a running download; its bytes and sidecar stay on disk.
func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	j := e.job
	// Drop it from the pending queue if it never started.
	for i, qid := range m.queue {
		if qid == id {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			break
		}
	}
	m.mu.Unlock()

	if j != nil {
		j.Pause()
		return nil
	}
	m.setState(id, engine.StatePaused, "")
	return nil
}

// Resume re-queues a paused or failed download.
func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	switch engine.State(e.rec.State) {
	case engine.StateDownloading, engine.StateProbing, engine.StateQueued:
		m.mu.Unlock()
		return nil // already going
	}
	e.rec.State = string(engine.StateQueued)
	e.rec.Error = ""
	rec := e.rec
	m.queue = append(m.queue, id)
	m.mu.Unlock()

	m.st.Put(&rec)
	m.broadcast(Event{Type: "updated", Item: rec})
	m.pump()
	return nil
}

// Remove cancels a download and forgets it, optionally deleting the bytes
// already written along with the resume sidecar.
func (m *Manager) Remove(id string, deleteFile bool) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	j := e.job
	rec := e.rec
	delete(m.entries, id)
	for i, qid := range m.queue {
		if qid == id {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			break
		}
	}
	m.mu.Unlock()

	if j != nil {
		j.Pause()
	}
	m.st.Delete(id)

	if deleteFile && rec.Path != "" {
		// The sidecar always goes: leaving it behind would make a later
		// download to the same name resume into a stale plan.
		os.Remove(rec.Path + ".dm")
		os.Remove(rec.Path + ".dm.tmp")
		os.Remove(rec.Path + ".dmh")
		os.Remove(rec.Path + ".dmh.tmp")
		if err := os.Remove(rec.Path); err != nil && !os.IsNotExist(err) {
			// Best effort: the file may still be held open by a worker that
			// has not noticed the cancellation yet.
			go func(p string) {
				time.Sleep(2 * time.Second)
				os.Remove(p)
			}(rec.Path)
		}
	}
	m.broadcast(Event{Type: "removed", Item: rec})
	m.pump()
	return nil
}

// SetLimit changes the global throughput ceiling, in KiB/s. Zero is
// unlimited. Running downloads pick the new rate up immediately.
func (m *Manager) SetLimit(kbps int) {
	if kbps < 0 {
		kbps = 0
	}
	m.limiter.SetRate(float64(kbps) * 1024)
}

// List returns every known download, newest first.
func (m *Manager) List() []store.Record { return m.st.List() }

// Get returns one record.
func (m *Manager) Get(id string) (store.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return store.Record{}, ErrNotFound
	}
	return e.rec, nil
}

// pump starts queued downloads until the concurrency limit is reached.
func (m *Manager) pump() {
	cfg := m.st.Config()
	for {
		m.mu.Lock()
		if len(m.queue) == 0 || m.running >= cfg.MaxConcurrent {
			m.mu.Unlock()
			return
		}
		id := m.queue[0]
		m.queue = m.queue[1:]
		e, ok := m.entries[id]
		if !ok {
			m.mu.Unlock()
			continue
		}
		m.running++
		j := m.newJob(e.rec, func() { m.onUpdate(id) })
		e.job = j
		m.mu.Unlock()

		go m.run(id, j)
	}
}

func (m *Manager) run(id string, j job) {
	err := j.Run(m.ctx)

	m.mu.Lock()
	m.running--
	e, ok := m.entries[id]
	if !ok {
		// Removed while running.
		m.mu.Unlock()
		m.pump()
		return
	}
	e.job = nil
	e.rec.Path = j.OutPath()
	if e.rec.Filename == "" && e.rec.Path != "" {
		e.rec.Filename = filepath.Base(e.rec.Path)
	}
	if done, size, _ := j.Summary(); true {
		e.rec.Downloaded = done
		if size > 0 {
			e.rec.Size = size
		}
	}

	switch {
	case err == nil:
		e.rec.State = string(engine.StateDone)
		e.rec.Error = ""
		e.rec.Finished = time.Now()
	case errors.Is(err, engine.ErrPaused), errors.Is(err, hls.ErrPaused),
		errors.Is(err, context.Canceled):
		e.rec.State = string(engine.StatePaused)
		e.rec.Error = ""
	default:
		e.rec.State = string(engine.StateError)
		e.rec.Error = err.Error()
	}
	rec := e.rec
	m.mu.Unlock()

	m.st.Put(&rec)
	m.broadcast(Event{Type: "updated", Item: rec})
	m.pump()
}

func (m *Manager) onUpdate(id string) {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok || e.job == nil {
		m.mu.Unlock()
		return
	}
	done, size, state := e.job.Summary()
	e.rec.State = state
	e.rec.Downloaded = done
	if size > 0 {
		e.rec.Size = size
	}
	if p := e.job.OutPath(); p != "" {
		e.rec.Path = p
		if e.rec.Filename == "" {
			e.rec.Filename = filepath.Base(p)
		}
	}
	rec := e.rec
	m.mu.Unlock()

	m.st.Put(&rec)
	m.broadcast(Event{Type: "updated", Item: rec})
}

// Progress returns live engine stats for a running download; ok is false when
// the download is not currently active.
// Progress returns live stats for a running download. The concrete shape
// depends on the kind, so callers just serialize it.
func (m *Manager) Progress(id string) (any, bool) {
	m.mu.Lock()
	e, ok := m.entries[id]
	var j job
	if ok {
		j = e.job
	}
	m.mu.Unlock()
	if j == nil {
		return nil, false
	}
	return j.Snapshot(), true
}

func (m *Manager) setState(id string, s engine.State, errMsg string) {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	e.rec.State = string(s)
	e.rec.Error = errMsg
	rec := e.rec
	m.mu.Unlock()

	m.st.Put(&rec)
	m.broadcast(Event{Type: "updated", Item: rec})
}

// Subscribe returns a channel of events plus a function to stop listening.
func (m *Manager) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	m.subMu.Lock()
	m.subs[ch] = struct{}{}
	m.subMu.Unlock()

	return ch, func() {
		m.subMu.Lock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
		m.subMu.Unlock()
	}
}

// broadcast is non-blocking: a subscriber that cannot keep up loses the
// intermediate frames rather than stalling the whole engine.
func (m *Manager) broadcast(ev Event) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// PauseAll stops every running download so the process can exit cleanly with
// all resume state on disk.
func (m *Manager) PauseAll() {
	m.mu.Lock()
	jobs := make([]job, 0, len(m.entries))
	for _, e := range m.entries {
		if e.job != nil {
			jobs = append(jobs, e.job)
		}
	}
	m.mu.Unlock()
	for _, j := range jobs {
		j.Pause()
	}
}

// StartFlusher periodically persists the download list until ctx is done.
func (m *Manager) StartFlusher(ctx context.Context, every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				if err := m.st.Flush(); err != nil {
					fmt.Fprintf(os.Stderr, "dm: final flush: %v\n", err)
				}
				return
			case <-t.C:
				if err := m.st.Flush(); err != nil {
					fmt.Fprintf(os.Stderr, "dm: flush: %v\n", err)
				}
			}
		}
	}()
}
