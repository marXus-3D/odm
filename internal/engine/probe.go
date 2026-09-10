package engine

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// DoProbe learns the size, range support and filename for a URL.
//
// It uses GET with "Range: bytes=0-0" rather than HEAD: signed CDN URLs and a
// fair number of app servers either reject HEAD outright or answer it with
// headers that disagree with the real GET.
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
	req.Header.Set("Range", "bytes=0-0")

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
	// our zero-length probe; trust an explicit "bytes" only when we know size.
	if !p.Resumable && p.Size > 0 &&
		strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes") {
		p.Resumable = true
	}

	p.Filename = pickFilename(r.Filename, resp)
	return p, nil
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

// pickFilename resolves the name to save as, in order of trust:
// caller override, Content-Disposition, URL path, then a generic fallback.
func pickFilename(override string, resp *http.Response) string {
	if override != "" {
		return SanitizeFilename(override)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			// filename* (RFC 5987) wins over plain filename when both exist.
			if v := params["filename*"]; v != "" {
				if dec, err := decodeExtValue(v); err == nil && dec != "" {
					return SanitizeFilename(dec)
				}
			}
			if v := params["filename"]; v != "" {
				return SanitizeFilename(v)
			}
		}
	}
	if u := resp.Request.URL; u != nil {
		if base := path.Base(u.Path); base != "" && base != "/" && base != "." {
			if unesc, err := url.PathUnescape(base); err == nil {
				base = unesc
			}
			return SanitizeFilename(base)
		}
	}
	return "download"
}

// decodeExtValue decodes an RFC 5987 ext-value: charset'lang'pct-encoded.
func decodeExtValue(v string) (string, error) {
	parts := strings.SplitN(v, "'", 3)
	if len(parts) != 3 {
		return url.PathUnescape(v)
	}
	dec, err := url.PathUnescape(parts[2])
	if err != nil {
		return "", err
	}
	return dec, nil
}
