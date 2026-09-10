package engine

import "sync/atomic"

// Cur is the next byte offset this segment will write.
func (s *Segment) Cur() int64 { return atomic.LoadInt64(&s.cur) }

// End is the exclusive upper bound. It only ever moves down, when a splitter
// donates this segment's tail to an idle worker.
func (s *Segment) End() int64 { return atomic.LoadInt64(&s.end) }

func (s *Segment) advance(n int64) { atomic.AddInt64(&s.cur, n) }

func (s *Segment) setEnd(v int64) { atomic.StoreInt64(&s.end, v) }

// Remaining is how many bytes are still owed. Clamped at zero: a worker can
// briefly overshoot End by up to one read buffer when a split lands mid-read.
func (s *Segment) Remaining() int64 {
	if r := s.End() - s.Cur(); r > 0 {
		return r
	}
	return 0
}

// Progress is bytes actually contributed by this segment, clamped so an
// overshoot past a freshly-lowered End can't inflate the total.
func (s *Segment) Progress() int64 {
	c, e := s.Cur(), s.End()
	if c > e {
		c = e
	}
	if c < s.Start {
		return 0
	}
	return c - s.Start
}

func (s *Segment) snapshot() SegmentSnapshot {
	c, e := s.Cur(), s.End()
	if c > e {
		c = e
	}
	return SegmentSnapshot{ID: s.ID, Start: s.Start, Cur: c, End: e}
}

func newSegment(id int, start, cur, end int64) *Segment {
	return &Segment{ID: id, Start: start, cur: cur, end: end}
}
