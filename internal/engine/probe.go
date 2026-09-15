package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DoProbe learns the size, range support and filename for a URL.
//
// It uses a short ranged GET rather than HEAD: signed CDN URLs and a fair
// number of app servers either reject HEAD outright or answer it with
// headers that disagree with the real GET. The few bytes it does read are
// kept in Probe.Head, which is how the caller tells a video apart from a
// playlist listing one.
func DoProbe(ctx context.Context, r Request, o Options) (*Probe, error) {
	o.applyDefaults()

	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	var body *bytes.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	} else {
		body = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, r.URL, body)
	if err != nil {
		return nil, fmt.Errorf("probe: bad request: %w", err)
	}
	applyHeaders(req, r.Headers, o.UserAgent)
	// A short range rather than a single byte, so the first bytes of the
	// response come back with the headers. That is the only dependable way
	// to tell a video from a playlist describing one: media CDNs serve
	// M3U8 from URLs with no hint of it and label them octet-stream.
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", HeadSize-1))

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe: %w", err)
	}
	defer func() {
		resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("probe: server returned %s", resp.Status)
	}

	p := &Probe{
		FinalURL:     resp.Request.URL.String(),
		Size:         -1,
		Status:       resp.StatusCode,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		ContentType:  resp.Header.Get("Content-Type"),
	}

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// "Content-Range: bytes 0-0/12345" is the authoritative total size.
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			p.Size = total
			p.Resumable = true
		}
	case http.StatusOK:
		// Server ignored the Range header: single stream only.
		if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil && n >= 0 {
			p.Size = n
		}
		p.Resumable = false
	}

	// A server may claim ranges via Accept-Ranges even if it answered 200 for
	// our short probe; trust an explicit "bytes" only when we know the size.
	if !p.Resumable && p.Size > 0 &&
		strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes") {
		p.Resumable = true
	}

	p.Head = readHead(ctx, resp.Body)

	p.Filename = pickFilename(r.Filename, resp)
	return p, nil
}

// headTimeout bounds the wait for the first bytes of the body.
//
// It has to be bounded here rather than left to the transport: the client
// has no overall deadline, deliberately, because a download may legitimately
// take hours, and the transport's timeouts cover connecting and the response
// headers but not reading the body. A server that answers and then goes
// silent would otherwise wedge the probe, and with it the download, forever.
// A variable so a test can shorten it.
var headTimeout = 5 * time.Second

// readHead reads the first bytes of a response, giving up rather than
// waiting. The bytes are a hint about what the response is; nothing depends
// on having them.
func readHead(ctx context.Context, body io.Reader) []byte {
	got := make(chan []byte, 1)
	go func() {
		// A read error still yields whatever arrived before it.
		b, _ := io.ReadAll(io.LimitReader(body, HeadSize))
		got <- b
	}()

	t := time.NewTimer(headTimeout)
	defer t.Stop()
	select {
	case b := <-got:
		return b
	case <-t.C:
		return nil
	case <-ctx.Done():
		return nil
	}
	// The goroutine is not leaked: the caller closes the body on return,
	// which unblocks the read.
}

// parseContentRangeTotal pulls 12345 out of "bytes 0-0/12345".
// A "*" total means the server doesn't know the length.
func parseContentRangeTotal(v string) (int64, bool) {
	i := strings.LastIndex(v, "/")
	if i < 0 {
		return 0, false
	}
	total := strings.TrimSpace(v[i+1:])
	if total == "" || total == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// pickFilename resolves the name to save as, in order of trust: caller
// override, Content-Disposition, the path of the final URL, then a generic
// fallback. Whatever comes out is then held against the Content-Type, so a
// name taken from a URL like /videoplayback still ends in .mp4 rather than
// in nothing at all. See naming.go.
func pickFilename(override string, resp *http.Response) string {
	ct := resp.Header.Get("Content-Type")
	if override != "" {
		return withExtension(SanitizeFilename(override), ct)
	}
	if name := FilenameFromDisposition(resp.Header.Get("Content-Disposition")); name != "" {
		return withExtension(SanitizeFilename(name), ct)
	}
	// resp.Request.URL is the URL after redirects: a download link that
	// bounces through a token endpoint to the real file is named by the
	// file, not by the endpoint.
	if base := FilenameFromParsedURL(resp.Request.URL); base != "" {
		if name := SanitizeFilename(base); name != "download" {
			return withExtension(name, ct)
		}
	}
	return withExtension("download", ct)
}
