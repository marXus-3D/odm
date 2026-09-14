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

	"github.com/marXus-3D/odm/internal/engine"
	"github.com/marXus-3D/odm/internal/hls"
	"github.com/marXus-3D/odm/internal/power"
	"github.com/marXus-3D/odm/internal/store"
)

// Event is a change the UI should react to.
type Event struct {
	Type string       `json:"type"` // added | updated | removed
	Item store.Record `json:"item"`
}

// ErrNotFound is returned for an unknown download id.
var ErrNotFound = errors.New("download not found")

// StateConfirm means the download is waiting for the user to say where it
// should go. It is a manager-level state: the engine never sees these,
// because nothing has started.
const StateConfirm = "confirm"

type entry struct {
	rec store.Record
	job job
	// What the caller pinned down itself, and so what the probe must not
	// overwrite when it learns the real name of the file.
	nameGiven bool
	catGiven  bool
	dirGiven  bool
	// runQueue is the queue this download was started under. The record can
	// be moved to another queue while it runs, so the slot has to be given
	// back to the queue that lent it.
	runQueue string
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
	// queue is one ordered list of everything waiting, across all queues.
	// Keeping a single line preserves the order downloads were added in;
	// which of them may start is decided per queue in pump.
	queue []string
	// running counts what is in flight per queue id.
	running map[string]int

	subMu sync.Mutex
	subs  map[chan Event]struct{}

	onExit func()
}

// New restores the persisted list and prepares the queue.
func New(ctx context.Context, st *store.Store) *Manager {
	m := &Manager{
		st:      st,
		ctx:     ctx,
		entries: map[string]*entry{},
		running: map[string]int{},
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
		case StateConfirm:
			// Still waiting on the user; leave it alone.
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

	// Category overrides the one guessed from the filename; Description is
	// the user's note. Both come from the Download File Info dialog.
	Category    string
	Description string

	// QueueID picks the queue to wait in. Empty means the default.
	QueueID string

	// NoPrompt skips the Download File Info dialog for this one download,
	// however the setting is configured.
	NoPrompt bool
}

// filenameFromURL guesses a name from a URL, for choosing a category before
// the download has been probed.
func filenameFromURL(raw string) string {
	return engine.FilenameFromURL(raw)
}

// Add queues a new download and returns its record.
func (m *Manager) Add(req AddRequest) (store.Record, error) {
	if req.URL == "" {
		return store.Record{}, errors.New("url is required")
	}
	cfg := m.st.Config()
	if req.MaxConns <= 0 {
		req.MaxConns = cfg.MaxConns
	}

	// Work out the category from whatever name we can guess before the
	// probe: the caller's filename, else the tail of the URL. The Download
	// File Info dialog shows this and can override it.
	guess := req.Filename
	if guess == "" {
		guess = filenameFromURL(req.URL)
	}
	cat := cfg.CategoryFor(guess)
	if req.Category != "" {
		cat = cfg.CategoryByName(req.Category)
	}
	// Noted before the default fills it in, so the probe knows whether the
	// directory is the caller's choice or only ours.
	dirGiven := req.Dir != ""
	if !dirGiven {
		req.Dir = cfg.DirFor(cat)
	}

	kind := DetectKind(req.URL)
	if req.Kind != "" {
		kind = req.Kind // the extension may have sniffed the content type
	}
	if kind == KindDASH {
		return store.Record{}, ErrDASHUnsupported
	}
	q := cfg.QueueByID(req.QueueID)
	rec := store.Record{
		ID:          store.NewID(),
		QueueID:     q.ID,
		URL:         req.URL,
		Kind:        kind,
		Filename:    req.Filename,
		Headers:     req.Headers,
		Dir:         req.Dir,
		MaxConns:    req.MaxConns,
		Category:    cat.Name,
		Description: req.Description,
		Size:        -1,
		State:       string(engine.StateQueued),
		Created:     time.Now(),
	}
	// With the Download File Info dialog switched on, a new download waits
	// for the user instead of starting. The extension goes through this
	// same path, so a browser download gets the dialog too.
	confirm := cfg.ShowStartDialog && !req.NoPrompt
	if confirm {
		// Ask the server what the file is called before asking the user
		// where to put it. Without this the dialog can only offer the tail
		// of the URL, and whatever it offers is what gets saved, so a
		// signed CDN link became a file with a token for a name and no
		// extension. resolve flips the state to StateConfirm when it is
		// done, or when it gives up.
		rec.State = string(engine.StateProbing)
	}
	m.st.Put(&rec)

	m.mu.Lock()
	m.entries[rec.ID] = &entry{
		rec:       rec,
		nameGiven: req.Filename != "",
		catGiven:  req.Category != "",
		dirGiven:  dirGiven,
	}
	if !confirm {
		m.queue = append(m.queue, rec.ID)
	}
	m.mu.Unlock()

	m.broadcast(Event{Type: "added", Item: rec})
	if confirm {
		go m.resolve(rec.ID)
	} else {
		m.pump()
	}
	return rec, nil
}

// resolveTimeout caps the wait before the Download File Info dialog opens
// with whatever we know. A dead host must not leave the dialog unopened.
const resolveTimeout = 20 * time.Second

// resolve fills in the name and size of a download that is waiting for the
// dialog, then hands it to the user.
//
// This is the step that makes the name right. The probe follows redirects
// and reads Content-Disposition, which is where the real filename lives for
// anything served from a CDN or behind a token endpoint; the URL path
// frequently has no name in it at all.
func (m *Manager) resolve(id string) {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok || e.rec.State != string(engine.StateProbing) {
		m.mu.Unlock()
		return
	}
	rec := e.rec
	m.mu.Unlock()

	var name string
	var size int64
	// A playlist URL names the video after its own directory; there is no
	// file behind it to ask, so skip straight to the dialog.
	if rec.Kind == KindFile {
		ctx, cancel := context.WithTimeout(m.ctx, resolveTimeout)
		p, err := engine.DoProbe(ctx, engine.Request{
			URL:      rec.URL,
			Headers:  rec.Headers,
			Filename: rec.Filename,
		}, engine.Options{})
		cancel()
		if err == nil {
			name, size = p.Filename, p.Size
		}
	}

	cfg := m.st.Config()
	m.mu.Lock()
	e, ok = m.entries[id]
	if !ok || e.rec.State != string(engine.StateProbing) {
		m.mu.Unlock()
		return // cancelled, or answered by hand, while we were asking
	}
	if name != "" && !e.nameGiven {
		e.rec.Filename = name
		// The category follows the extension, and it was guessed from the
		// URL a moment ago with no extension to go on.
		if !e.catGiven {
			cat := cfg.CategoryFor(name)
			e.rec.Category = cat.Name
			if !e.dirGiven {
				e.rec.Dir = cfg.DirFor(cat)
			}
		}
	}
	if size > 0 {
		e.rec.Size = size
	}
	e.rec.State = StateConfirm
	rec = e.rec
	m.mu.Unlock()

	m.st.Put(&rec)
	m.broadcast(Event{Type: "updated", Item: rec})
}

// Confirmation is the answer to the Download File Info dialog.
type Confirmation struct {
	Dir         string
	Filename    string
	Category    string
	Description string
	// QueueID moves the download to another queue before it starts. Empty
	// leaves it where it is.
	QueueID string
	// Start now, or leave it paused for later.
	Start bool
}

// Confirm applies the dialog's answer and either starts the download or
// parks it.
func (m *Manager) Confirm(id string, c Confirmation) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if e.rec.State != StateConfirm {
		m.mu.Unlock()
		return nil // already answered
	}
	if c.Dir != "" {
		e.rec.Dir = c.Dir
	}
	if c.Filename != "" {
		e.rec.Filename = c.Filename
	}
	if c.Category != "" {
		e.rec.Category = c.Category
	}
	e.rec.Description = c.Description
	if c.QueueID != "" {
		e.rec.QueueID = m.st.Config().QueueByID(c.QueueID).ID
	}
	if c.Start {
		e.rec.State = string(engine.StateQueued)
		m.queue = append(m.queue, id)
	} else {
		e.rec.State = string(engine.StatePaused)
	}
	rec := e.rec
	m.mu.Unlock()

	m.st.Put(&rec)
	m.broadcast(Event{Type: "updated", Item: rec})
	if c.Start {
		m.pump()
	}
	return nil
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
		os.Remove(rec.Path + ".odm")
		os.Remove(rec.Path + ".odm.tmp")
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

// ResumeAll re-queues everything that is not already running or finished.
func (m *Manager) ResumeAll() int {
	m.mu.Lock()
	var ids []string
	for id, e := range m.entries {
		switch e.rec.State {
		case string(engine.StateDownloading), string(engine.StateProbing),
			string(engine.StateQueued), string(engine.StateDone), StateConfirm:
			continue
		}
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.Resume(id)
	}
	return len(ids)
}

// StopAll pauses everything and empties the pending queue, so nothing
// starts back up on its own. Pause All only stops what is running; this is
// the "and stay stopped" version.
func (m *Manager) StopAll() int {
	m.mu.Lock()
	queued := m.queue
	m.queue = nil
	var running []job
	for _, e := range m.entries {
		if e.job != nil {
			running = append(running, e.job)
		}
	}
	m.mu.Unlock()

	for _, id := range queued {
		m.setState(id, engine.StatePaused, "")
	}
	for _, j := range running {
		j.Pause()
	}
	return len(queued) + len(running)
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

// pump starts waiting downloads until every queue is at its limit.
//
// The waiting list is shared, so a queue that is full does not block the
// others: the scan skips past its downloads and starts the first one behind
// them whose own queue has room.
func (m *Manager) pump() {
	cfg := m.st.Config()
	for {
		m.mu.Lock()
		idx, qid := -1, ""
		for i, id := range m.queue {
			e, ok := m.entries[id]
			if !ok {
				// Removed while waiting; drop it on the way past.
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				idx = -2 // rescan, the slice moved under us
				break
			}
			q := cfg.QueueByID(e.rec.QueueID)
			if m.running[q.ID] < q.MaxConcurrent {
				idx, qid = i, q.ID
				break
			}
		}
		if idx == -2 {
			m.mu.Unlock()
			continue
		}
		if idx < 0 {
			m.mu.Unlock()
			return
		}

		id := m.queue[idx]
		m.queue = append(m.queue[:idx], m.queue[idx+1:]...)
		e := m.entries[id]
		m.running[qid]++
		e.runQueue = qid
		j := m.newJob(e.rec, func() { m.onUpdate(id) })
		e.job = j
		m.mu.Unlock()

		go m.run(id, qid, j)
	}
}

func (m *Manager) run(id, qid string, j job) {
	err := j.Run(m.ctx)

	m.mu.Lock()
	// The slot goes back to the queue that lent it, named here rather than
	// read off the entry: the download may have been moved to another queue,
	// or removed outright, while it was running.
	m.running[qid]--
	if e, ok := m.entries[id]; ok {
		e.runQueue = ""
	}
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
	m.maybeFinishAction()
}

// maybeFinishAction runs the configured "when everything is done" action
// once nothing is left running or queued.
//
// It is one-shot: the setting resets before the action fires, so the
// machine does not shut itself down every time the list happens to empty.
func (m *Manager) maybeFinishAction() {
	cfg := m.st.Config()
	act := power.Action(cfg.OnComplete)
	if act == "" || act == power.None || !power.Valid(act) {
		return
	}

	m.mu.Lock()
	busy := len(m.queue) > 0
	for _, n := range m.running {
		if n > 0 {
			busy = true
			break
		}
	}
	if !busy {
		for _, e := range m.entries {
			switch e.rec.State {
			case string(engine.StateDownloading), string(engine.StateProbing),
				string(engine.StateQueued), string(hls.StateRemuxing):
				busy = true
			}
			if busy {
				break
			}
		}
	}
	m.mu.Unlock()
	if busy {
		return
	}

	// Clear it first, so a failure to sleep does not leave the machine
	// trying again on every subsequent completion.
	if err := m.st.SetConfig(store.Config{OnComplete: string(power.None)}); err != nil {
		fmt.Fprintf(os.Stderr, "odm: clear on-complete: %v\n", err)
	}
	m.broadcast(Event{Type: "finished-all", Item: store.Record{State: string(act)}})

	if act == power.ExitDM {
		if m.onExit != nil {
			go m.onExit()
		}
		return
	}
	go func() {
		if err := power.Do(act); err != nil {
			fmt.Fprintf(os.Stderr, "odm: %s: %v\n", act, err)
		}
	}()
}

// SetExitFunc supplies the callback used by the "Exit ODM" completion
// action, which only the daemon knows how to perform.
func (m *Manager) SetExitFunc(f func()) { m.onExit = f }

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
					fmt.Fprintf(os.Stderr, "odm: final flush: %v\n", err)
				}
				return
			case <-t.C:
				if err := m.st.Flush(); err != nil {
					fmt.Fprintf(os.Stderr, "odm: flush: %v\n", err)
				}
			}
		}
	}()
}

// SetQueue moves a download to another queue. A waiting download simply
// starts under the new queue's limit next time the scheduler looks; one that
// is already running is left alone, because stopping it to re-queue would
// throw away its connections for no gain.
func (m *Manager) SetQueue(id, queueID string) error {
	cfg := m.st.Config()
	q := cfg.QueueByID(queueID)
	if q.ID != queueID && queueID != "" {
		return fmt.Errorf("no queue with id %q", queueID)
	}

	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	e.rec.QueueID = q.ID
	rec := e.rec
	m.mu.Unlock()

	m.st.Put(&rec)
	m.broadcast(Event{Type: "updated", Item: rec})
	m.pump()
	return nil
}

// Queues returns the configured queues with a live count of what is running
// and waiting in each, which is what the UI lists.
type QueueStatus struct {
	store.Queue
	Running int `json:"running"`
	Waiting int `json:"waiting"`
}

// QueueStatuses reports every queue and how busy it is.
func (m *Manager) QueueStatuses() []QueueStatus {
	cfg := m.st.Config()
	m.mu.Lock()
	defer m.mu.Unlock()

	waiting := map[string]int{}
	for _, id := range m.queue {
		if e, ok := m.entries[id]; ok {
			waiting[cfg.QueueByID(e.rec.QueueID).ID]++
		}
	}
	out := make([]QueueStatus, 0, len(cfg.Queues))
	for _, q := range cfg.Queues {
		out = append(out, QueueStatus{Queue: q, Running: m.running[q.ID], Waiting: waiting[q.ID]})
	}
	return out
}

// Kick asks the scheduler to look again, after something outside the manager
// changed a queue's limit or membership.
func (m *Manager) Kick() { m.pump() }
