package gateway

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRewritePath(t *testing.T) {
	p := &Proxy{prefix: "/app/scrcpy-remote"}
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/app/scrcpy-remote", "/", true},
		{"/app/scrcpy-remote/", "/", true},
		{"/app/scrcpy-remote/api/devices", "/api/devices", true},
		{"/other/path", "", false},
	}
	for _, tt := range tests {
		got, ok := p.rewritePath(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("rewritePath(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestServeStaticAndTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>hello</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{webDir: dir}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	if !p.serveStatic(w, r, "/") {
		t.Fatal("expected index.html to be served")
	}
	if body := w.Body.String(); body != "<h1>hello</h1>" {
		t.Errorf("unexpected body %q", body)
	}

	// A traversal-style path must not be served from the static dir.
	r = httptest.NewRequest(http.MethodGet, "/../../etc/passwd", nil)
	w = httptest.NewRecorder()
	if p.serveStatic(w, r, "/../../etc/passwd") {
		t.Error("path traversal must be rejected")
	}
}

func TestHTTPProxy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/devices" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("X-From", "upstream")
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()

	p, err := New(Config{
		Socket: filepath.Join(t.TempDir(), "gw.sock"),
		Prefix: "/app/scrcpy-remote",
		Target: up.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/app/scrcpy-remote/api/devices", nil)
	w := httptest.NewRecorder()
	p.serveHTTP(w, req)
	if got := w.Header().Get("X-From"); got != "upstream" {
		t.Errorf("X-From = %q", got)
	}
	if got := w.Body.String(); got != "ok" {
		t.Errorf("body = %q", got)
	}
}

func TestWebSocketUpgradeTunnel(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/stream" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Test") != "yes" {
			t.Errorf("X-Test = %q", r.Header.Get("X-Test"))
		}
		// The gateway must forward ?serial=...&token=... to upstream. This is
		// how the backend learns which device (serial) to stream.
		if !strings.Contains(r.URL.RawQuery, "serial=192.168.3.113%3A5555") ||
			!strings.Contains(r.URL.RawQuery, "token=abc") {
			t.Errorf("upstream RawQuery = %q (query lost in upgrade)", r.URL.RawQuery)
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("upstream hijack unsupported")
			return
		}
		c, rw, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		line, _ := rw.Reader.ReadString('\n')
		_, _ = io.WriteString(c, "echo:"+line)
	}))
	defer up.Close()

	sock := filepath.Join(t.TempDir(), "gw.sock")
	p, err := New(Config{Socket: sock, Prefix: "/app/x", Target: up.URL})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.ListenAndServe() }()
	defer p.Close()

	// Wait for the unix socket to accept connections.
	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("unix", sock)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("gateway socket never became ready: %v", err)
	}
	defer conn.Close()

	_, _ = io.WriteString(conn,
		"GET /app/x/api/stream?serial=192.168.3.113%3A5555&token=abc HTTP/1.1\r\nHost: example\r\n"+
			"Upgrade: websocket\r\nConnection: Upgrade\r\nX-Test: yes\r\n\r\n"+
			"ping\n")

	var out strings.Builder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "echo:ping") {
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, readErr := conn.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if readErr != nil {
			if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
				continue
			}
			t.Fatal(readErr)
		}
	}
	got := out.String()
	if !strings.Contains(got, "101 Switching Protocols") {
		t.Errorf("missing 101 response: %q", got)
	}
	if !strings.Contains(got, "echo:ping") {
		t.Errorf("tunnel did not echo payload: %q", got)
	}
}
