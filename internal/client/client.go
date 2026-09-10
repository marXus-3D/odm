// Package client talks to a running dmd over loopback HTTP, starting one if
// needed. Both the CLI and the browser native messaging host use it.
package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/dm/internal/store"
)

// Client is a connection to the daemon.
type Client struct {
	Base  string
	Token string
	http  http.Client
}

// AddRequest mirrors the daemon's POST /api/downloads body.
type AddRequest struct {
	URL      string            `json:"url"`
	Filename string            `json:"filename,omitempty"`
	Dir      string            `json:"dir,omitempty"`
	MaxConns int               `json:"maxConns,omitempty"`
	Referer  string            `json:"referer,omitempty"`
	Cookie   string            `json:"cookie,omitempty"`
	UA       string            `json:"userAgent,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Kind     string            `json:"kind,omitempty"`
}

// State is the daemon's view of the world.
type State struct {
	Downloads []store.Record `json:"downloads"`
	Config    store.Config   `json:"config"`
}

// Discover reads the token and port the daemon wrote to its state directory.
// It does not check that the daemon is actually up; call Ping for that.
func Discover() (*Client, error) {
	dir := store.StateDir()
	tok, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		return nil, fmt.Errorf("no DM state found in %s: %w", dir, err)
	}
	port := 9111
	if b, err := os.ReadFile(filepath.Join(dir, "port")); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
			port = p
		}
	}
	return &Client{
		Base:  fmt.Sprintf("http://127.0.0.1:%d", port),
		Token: strings.TrimSpace(string(tok)),
		http:  http.Client{Timeout: 20 * time.Second},
	}, nil
}

// Connect returns a live client, launching the daemon if it is not answering.
// exeDir is where to look for the daemon binary; empty means beside the
// current executable.
func Connect(exeDir string) (*Client, error) {
	if c, err := Discover(); err == nil && c.Ping() == nil {
		return c, nil
	}
	if err := StartDaemon(exeDir); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := Discover(); err == nil && c.Ping() == nil {
			return c, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, errors.New("the DM daemon did not come up; check nmh.log in the state directory")
}

// StartDaemon launches dmd detached so it outlives whatever spawned it.
func StartDaemon(exeDir string) error {
	if exeDir == "" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		exeDir = filepath.Dir(self)
	}
	dmd := filepath.Join(exeDir, DaemonName)
	if _, err := os.Stat(dmd); err != nil {
		return fmt.Errorf("cannot find %s in %s: %w", DaemonName, exeDir, err)
	}
	cmd := exec.Command(dmd)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	go func() { _ = cmd.Wait() }() // release the child
	return nil
}

func (c *Client) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-DM-Token", c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func (c *Client) Ping() error { return c.do(http.MethodGet, "/api/ping", nil, nil) }

func (c *Client) State() (*State, error) {
	var s State
	if err := c.do(http.MethodGet, "/api/state", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) Add(req AddRequest) (*store.Record, error) {
	var rec store.Record
	if err := c.do(http.MethodPost, "/api/downloads", req, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (c *Client) Pause(id string) error {
	return c.do(http.MethodPost, "/api/downloads/"+id+"/pause", nil, nil)
}

func (c *Client) Resume(id string) error {
	return c.do(http.MethodPost, "/api/downloads/"+id+"/resume", nil, nil)
}

func (c *Client) Remove(id string, deleteFile bool) error {
	q := ""
	if deleteFile {
		q = "?deleteFile=true"
	}
	return c.do(http.MethodDelete, "/api/downloads/"+id+q, nil, nil)
}

func (c *Client) Reveal(id string) error {
	return c.do(http.MethodPost, "/api/downloads/"+id+"/reveal", nil, nil)
}

func (c *Client) Open(id string) error {
	return c.do(http.MethodPost, "/api/downloads/"+id+"/open", nil, nil)
}

// Progress returns live engine stats; it fails when the download is idle.
func (c *Client) Progress(id string) (map[string]any, error) {
	var out map[string]any
	if err := c.do(http.MethodGet, "/api/downloads/"+id+"/progress", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Shutdown asks the daemon to stop gracefully.
func (c *Client) Shutdown() error {
	return c.do(http.MethodPost, "/api/shutdown", nil, nil)
}

func (c *Client) SetConfig(cfg store.Config) (*store.Config, error) {
	var out store.Config
	if err := c.do(http.MethodPut, "/api/config", cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
