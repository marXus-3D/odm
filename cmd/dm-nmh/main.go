// Command dm-nmh is the Chrome native messaging host.
//
// Chrome launches this process and speaks the native messaging protocol over
// stdin/stdout: a 4-byte native-endian length prefix followed by a JSON
// payload. The host holds no logic of its own; it authenticates against the
// local daemon with the on-disk token and forwards requests, which keeps that
// secret out of the browser entirely.
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/marXus-3D/dm/internal/client"
	"github.com/marXus-3D/dm/internal/store"
)

// Chrome refuses to accept more than 1 MB from a host, and will not send it
// more than 64 MB.
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
	Kind     string            `json:"kind,omitempty"`
	ID       string            `json:"id,omitempty"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func main() {
	// Anything on stdout must be a framed message, so logs go to a file. A
	// stray Println would corrupt the protocol and Chrome would silently drop
	// the connection.
	if lf, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer lf.Close()
		log.SetOutput(lf)
	} else {
		log.SetOutput(io.Discard)
	}
	log.SetPrefix("dm-nmh: ")

	h := &handler{}
	for {
		msg, err := readMessage(os.Stdin)
		if errors.Is(err, io.EOF) {
			return // Chrome closed the port; normal shutdown
		}
		if err != nil {
			log.Printf("read: %v", err)
			return
		}
		var req request
		if err := json.Unmarshal(msg, &req); err != nil {
			writeMessage(os.Stdout, response{OK: false, Error: "bad json: " + err.Error()})
			continue
		}
		writeMessage(os.Stdout, h.handle(req))
	}
}

type handler struct {
	c *client.Client
}

// connect reuses a live daemon connection across messages on the same port.
func (h *handler) connect() error {
	if h.c != nil && h.c.Ping() == nil {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c, err := client.Connect(filepath.Dir(self))
	if err != nil {
		return err
	}
	h.c = c
	markExtensionSeen()
	return nil
}

// markExtensionSeen records that a browser really did reach the host, so
// the app can stop telling the user to install the extension.
func markExtensionSeen() {
	path := filepath.Join(store.StateDir(), "extension_seen")
	now := time.Now().UTC().Format(time.RFC3339)
	if err := os.WriteFile(path, []byte(now), 0o600); err != nil {
		log.Printf("record extension: %v", err)
	}
}

func (h *handler) handle(req request) response {
	if err := h.connect(); err != nil {
		return response{OK: false, Error: err.Error()}
	}

	switch req.Type {
	case "ping":
		return response{OK: true, Data: map[string]string{"status": "ok", "base": h.c.Base}}

	case "openUI":
		return response{OK: true, Data: map[string]string{"url": h.c.Base + "/"}}

	case "add":
		if req.URL == "" {
			return response{OK: false, Error: "url is required"}
		}
		rec, err := h.c.Add(client.AddRequest{
			URL:      req.URL,
			Filename: req.Filename,
			Referer:  req.Referer,
			Cookie:   req.Cookie,
			UA:       req.UA,
			Headers:  req.Headers,
			Kind:     req.Kind,
		})
		if err != nil {
			return response{OK: false, Error: err.Error()}
		}
		return response{OK: true, Data: rec}

	case "state":
		st, err := h.c.State()
		if err != nil {
			return response{OK: false, Error: err.Error()}
		}
		return response{OK: true, Data: st}

	case "pause", "resume":
		if req.ID == "" {
			return response{OK: false, Error: "id is required"}
		}
		var err error
		if req.Type == "pause" {
			err = h.c.Pause(req.ID)
		} else {
			err = h.c.Resume(req.ID)
		}
		if err != nil {
			return response{OK: false, Error: err.Error()}
		}
		return response{OK: true, Data: map[string]string{"status": "ok"}}

	default:
		return response{OK: false, Error: "unknown request type: " + req.Type}
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

func logPath() string {
	return filepath.Join(store.StateDir(), "nmh.log")
}
