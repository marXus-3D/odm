package engine

import (
	"net"
	"net/http"
	"time"
)

// DefaultClient returns a transport tuned for many parallel range requests to
// a small number of hosts. The stock http.DefaultTransport caps idle
// connections per host at 2, which would serialize our segments.
func DefaultClient() *http.Client {
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// HTTP/2 multiplexes every stream onto one congestion window, which
		// defeats the point of opening several connections. Force 1.1.
		ForceAttemptHTTP2: false,
	}
	return &http.Client{Transport: t}
}

// applyHeaders copies caller-supplied headers (browser cookies, Referer, UA)
// onto a request, filling in a User-Agent when the caller had none.
func applyHeaders(req *http.Request, h map[string]string, ua string) {
	for k, v := range h {
		if v == "" {
			continue
		}
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", ua)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}
	// We handle our own decompression boundaries; a gzipped body would make
	// byte offsets meaningless.
	req.Header.Set("Accept-Encoding", "identity")
}
