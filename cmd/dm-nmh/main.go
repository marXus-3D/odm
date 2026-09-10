// Command dm-nmh is the Chrome native messaging host.
//
// Chrome launches this process and speaks the native messaging protocol over
// stdin/stdout: a 4-byte native-endian length prefix followed by a JSON
// payload. The host itself holds no logic; it authenticates against the local
// daemon with the on-disk token and forwards requests, which keeps the secret
// out of the browser entirely.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/dm/internal/store"
)

// Chrome rejects anything larger than 1 MB coming from the host, and refuses
// to send more than 64 MB to it.
const (
	maxIncoming = 64 << 20
	maxOutgoing = 1 << 20
)

type request struct {
	Type     string            `json:"type"`
	URL      string            `json:"url,omitempty"`
	Filename string            `json:"filename,omitempty"`
	Referer  string            `json:"referer,omitempty"`
	Cookie   string            `json:"cookie,omitempty"`
	UA       string            `json:"userAgent,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	ID       string            `json:"id,omitempty"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func main() {
	// Anything written to stdout must be a framed message, so logs go to a
	// file. A stray Println here would corrupt the protocol and Chrome would
	// silently drop the connection.
	if lf, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer lf.Close()
		log.SetOutput(lf)
	} else {
		log.SetOutput(io.Discard)
	}
	log.SetPrefix("dm-nmh: ")
	log.SetFlags(log.LstdFlags)

	c := &client{}
	in := os.Stdin
	out := os.Stdout

	for {
		msg, err := readMessage(in)
		if errors.Is(err, io.EOF) {
			return // Chrome closed the port; normal shutdown.
		}
		if err != nil {
			log.Printf("read: %v", err)
			return
		}

		var req request
		if err := json.Unmarshal(msg, &req); err != nil {
			writeMessage(out, response{OK: false, Error: "bad json: " + err.Error()})
			continue
		}
		writeMessage(out, c.handle(req))
	}
}

func readMessage(r io.Reader) ([]byte, error) {
	var length uint32
	if err := binary.Read(r, binary.NativeEndian, &length); err != nil {
		return nil, err
	}
	if length > maxIncoming {
		return nil, fmt.Errorf("message of %d bytes exceeds the protocol limit", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeMessage(w io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"ok":false,"error":"failed to encode response"}`)
	}
	if len(b) > maxOutgoing {
		b = []byte(`{"ok":false,"error":"response too large"}`)
	}
	if err := binary.Write(w, binary.NativeEndian, uint32(len(b))); err != nil {
		log.Printf("write length: %v", err)
		return
	}
	if _, err := w.Write(b); err != nil {
		log.Printf("write body: %v", err)
	}
}

// client talks to the daemon, starting it on demand.
type client struct {
	base  string
	token string
	http  http.Client
}

func (c *client) handle(req request) response {
	if err := c.ensureDaemon(); err != nil {
		return response{OK: false, Error: err.Error()}
	}

	switch req.Type {
	case "ping":
		return response{OK: true, Data: map[string]string{"status": "ok", "base": c.base}}

	case "add":
		if req.URL == "" {
			return response{OK: false, Error: "url is required"}
		}
		body := map[string]any{
			"url":       req.URL,
			"filename":  req.Filename,
			"referer":   req.Referer,
			"cookie":    req.Cookie,
			"userAgent": req.UA,
			"headers":   req.Headers,
		}
		var out any
		if err := c.call(http.MethodPost, "/api/downloads", body, &out); err != nil {
			return response{OK: false, Error: err.Error()}
		}
		return response{OK: true, Data: out}

	case "state":
		var out any
		if err := c.call(http.MethodGet, "/api/state", nil, &out); err != nil {
			return response{OK: false, Error: err.Error()}
		}
		return response{OK: true, Data: out}

	case "pause", "resume":
		if req.ID == "" {
			return response{OK: false, Error: "id is required"}
		}
		var out any
		path := "/api/downloads/" + req.ID + "/" + req.Type
		if err := c.call(http.MethodPost, path, nil, &out); err != nil {
			return response{OK: false, Error: err.Error()}
		}
		return response{OK: true, Data: out}

	case "openUI":
		return response{OK: true, Data: map[string]string{"url": c.base + "/"}}

	default:
		return response{OK: false, Error: "unknown request type: " + req.Type}
	}
}

func (c *client) call(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-DM-Token", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("daemon returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxOutgoing)).Decode(out)
}

// ensureDaemon connects to a running daemon, starting one if needed.
func (c *client) ensureDaemon() error {
	if c.base != "" && c.ping() == nil {
		return nil
	}
	if err := c.loadCreds(); err == nil && c.ping() == nil {
		return nil
	}
	if err := c.startDaemon(); err != nil {
		return err
	}
	// Give the daemon a moment to bind its port before the first real call.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.loadCreds(); err == nil && c.ping() == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("the DM daemon did not start; check the dmd log")
}

func (c *client) loadCreds() error {
	dir := store.StateDir()
	tok, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		return err
	}
	c.token = strings.TrimSpace(string(tok))

	port := 9111
	if b, err := os.ReadFile(filepath.Join(dir, "port")); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
			port = p
		}
	}
	c.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	c.http.Timeout = 15 * time.Second
	return nil
}

func (c *client) ping() error {
	if c.base == "" || c.token == "" {
		return errors.New("no daemon address")
	}
	return c.call(http.MethodGet, "/api/ping", nil, nil)
}

// startDaemon launches dmd from beside this executable, detached so it
// outlives the browser session that spawned us.
func (c *client) startDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dmd := filepath.Join(filepath.Dir(self), daemonName)
	if _, err := os.Stat(dmd); err != nil {
		return fmt.Errorf("cannot find %s next to the native host: %w", daemonName, err)
	}
	cmd := exec.Command(dmd)
	cmd.Stdout, cmd.Stderr = nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	// Release the child so it is not tied to this short-lived process.
	go func() { _ = cmd.Wait() }()
	log.Printf("started daemon %s (pid %d)", dmd, cmd.Process.Pid)
	return nil
}

func logPath() string {
	return filepath.Join(store.StateDir(), "nmh.log")
}
