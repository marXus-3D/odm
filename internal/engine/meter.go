package engine

import (
	"sync"
	"time"
)

// meter turns a monotonically increasing byte count into a smoothed rate.
// A raw delta/interval reading is far too jumpy to display, so we run it
// through an exponential moving average.
type meter struct {
	mu       sync.Mutex
	last     int64
	lastTime time.Time
	ema      float64
	started  bool
}

const emaAlpha = 0.3

// sample records the current total and returns the smoothed bytes/sec.
func (m *meter) sample(total int64) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if !m.started {
		m.started = true
		m.last, m.lastTime = total, now
		return 0
	}
	dt := now.Sub(m.lastTime).Seconds()
	if dt < 0.05 {
		return m.ema
	}
	inst := float64(total-m.last) / dt
	if inst < 0 {
		inst = 0
	}
	m.ema = emaAlpha*inst + (1-emaAlpha)*m.ema
	m.last, m.lastTime = total, now
	return m.ema
}

func (m *meter) rate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ema
}

// reset clears the window so a paused/resumed download doesn't report a
// phantom spike from the gap.
func (m *meter) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started, m.ema = false, 0
}
