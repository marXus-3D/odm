package engine

import (
	"net/http"
	"sync/atomic"
	"time"
)

// State of a download.
type State string

const (
	StateQueued      State = "queued"
	StateProbing     State = "probing"
	StateDownloading State = "downloading"
	StatePaused      State = "paused"
	StateDone        State = "done"
	StateError       State = "error"
)

// Request describes a download to perform. Headers carry the browser's
// cookies / Referer / User-Agent so our own sockets are authenticated the
// same way the originating tab was.
type Request struct {
	URL      string            `json:"url"`
	Method   string            `json:"method,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     []byte            `json:"body,omitempty"`
	Filename string            `json:"filename,omitempty"`
	Dir      string            `json:"dir,omitempty"`
	MaxConns int               `json:"maxConns,omitempty"`
}

// Probe is what we learn about a URL before committing to a strategy.
type Probe struct {
	FinalURL     string `json:"finalUrl"`
	Size         int64  `json:"size"` // -1 when the server won't say
	Resumable    bool   `json:"resumable"`
	Filename     string `json:"filename"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	Status       int    `json:"status"`
}

// Segment is one contiguous byte range assigned to one worker.
// cur and end are mutated concurrently: the owning worker advances cur, and a
// splitter goroutine may pull end backwards to donate the tail to an idle
// worker. Both are accessed via the atomic helpers below.
type Segment struct {
	ID    int   `json:"id"`
	Start int64 `json:"start"`
	cur   int64 // atomic: next byte offset to write
	end   int64 // atomic: exclusive upper bound

	// claimed guards against two workers picking up the same restored
	// segment; it says nothing about whether the range is complete.
	claimed atomic.Bool
}

// SegmentSnapshot is the serializable view of a Segment, used for resume
// metadata and for the UI.
type SegmentSnapshot struct {
	ID    int   `json:"id"`
	Start int64 `json:"start"`
	Cur   int64 `json:"cur"`
	End   int64 `json:"end"`
}

// Stats is a point-in-time view of progress.
type Stats struct {
	State      State             `json:"state"`
	Downloaded int64             `json:"downloaded"`
	Total      int64             `json:"total"`
	SpeedBPS   float64           `json:"speedBps"`
	ETA        time.Duration     `json:"eta"`
	Conns      int               `json:"conns"`
	Throttled  bool              `json:"throttled"`
	Segments   []SegmentSnapshot `json:"segments"`
	Err        string            `json:"error,omitempty"`
}

// Options tune the engine. Zero values fall back to the defaults below.
type Options struct {
	MaxConns     int           // parallel connections per download
	MinSplitSize int64         // don't split a range smaller than this
	ChunkSize    int           // read buffer size
	MaxRetries   int           // per-segment attempt limit
	RetryBackoff time.Duration // base backoff, doubled per attempt
	Timeout      time.Duration // per-connection idle timeout
	Client       *http.Client
	UserAgent    string

	// Limiter caps aggregate throughput. Share one across every download so
	// the ceiling is global rather than per connection. Nil means unlimited.
	Limiter *Limiter
}

const (
	DefaultMaxConns     = 8
	DefaultMinSplitSize = 1 << 20 // 1 MiB
	DefaultChunkSize    = 64 << 10
	DefaultMaxRetries   = 5
	DefaultRetryBackoff = 500 * time.Millisecond
	DefaultTimeout      = 30 * time.Second
	DefaultUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) DM/0.1"
)

func (o *Options) applyDefaults() {
	if o.MaxConns <= 0 {
		o.MaxConns = DefaultMaxConns
	}
	if o.MinSplitSize <= 0 {
		o.MinSplitSize = DefaultMinSplitSize
	}
	if o.ChunkSize <= 0 {
		o.ChunkSize = DefaultChunkSize
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = DefaultMaxRetries
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = DefaultRetryBackoff
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.UserAgent == "" {
		o.UserAgent = DefaultUserAgent
	}
	if o.Client == nil {
		o.Client = DefaultClient()
	}
}
