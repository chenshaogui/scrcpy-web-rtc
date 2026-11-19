// Package gateway is a Go port of fnos-app-native/proxy.mjs. It exposes the
// fnOS unified-gateway reverse proxy: a unix-socket listener that rewrites a
// path prefix, serves a single static page, and forwards /api/* (including
// WebSocket upgrades) to the inner HTTP server.
package gateway

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config mirrors the environment variables exported by cmd/main.
type Config struct {
	Socket  string // unix socket path to listen on
	Prefix  string // gateway path prefix, e.g. /app/scrcpy-remote
	Target  string // inner HTTP server, e.g. http://127.0.0.1:18010
	WebDir  string // directory holding index.html
	LogFile string // optional log file (stdout is always used too)
}

// Proxy is the gateway reverse proxy.
type Proxy struct {
	cfg      Config
	prefix   string
	target   *url.URL
	webDir   string
	logger   *log.Logger
	server   *http.Server
	listener net.Listener

	mu     sync.Mutex
	closed bool
}

var safeRelRe = regexp.MustCompile(`^[\w./-]+$`)

// New validates cfg and builds a Proxy.
func New(cfg Config) (*Proxy, error) {
	if cfg.Socket == "" {
		return nil, errors.New("gateway: Socket is required")
	}
	target, err := url.Parse(cfg.Target)
	if err != nil {
		return nil, fmt.Errorf("gateway: invalid target %q: %w", cfg.Target, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("gateway: target must be an absolute URL, got %q", cfg.Target)
	}
	writers := []io.Writer{os.Stdout}
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("gateway: open log file: %w", err)
		}
		writers = append(writers, f)
	}
	p := &Proxy{
		cfg:    cfg,
		prefix: strings.TrimRight(cfg.Prefix, "/"),
		target: target,
		webDir: cfg.WebDir,
		logger: log.New(io.MultiWriter(writers...), "", log.LstdFlags|log.LUTC),
	}
	// Node sets requestTimeout = 0 (unlimited); http.Server zero does the same.
	p.server = &http.Server{
		Handler:           http.HandlerFunc(p.serveHTTP),
		ReadHeaderTimeout: 60 * time.Second,
	}
	return p, nil
}

// ListenAndServe starts the unix-socket listener and blocks until closed.
func (p *Proxy) ListenAndServe() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return http.ErrServerClosed
	}
	p.mu.Unlock()

	if err := os.Remove(p.cfg.Socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gateway: remove stale socket: %w", err)
	}
	l, err := net.Listen("unix", p.cfg.Socket)
	if err != nil {
		return fmt.Errorf("gateway: listen %s: %w", p.cfg.Socket, err)
	}
	if err := os.Chmod(p.cfg.Socket, 0o666); err != nil {
		p.logf("chmod socket: %s", err)
	}
	p.mu.Lock()
	p.listener = l
	p.mu.Unlock()

	p.logf("gateway proxy listening on unix://%s prefix=%s target=%s web=%s",
		p.cfg.Socket, p.prefix, p.target, p.webDirOrNone())
	return p.server.Serve(l)
}

// Close stops the listener.
func (p *Proxy) Close() error {
	p.mu.Lock()
	p.closed = true
	l := p.listener
	p.mu.Unlock()
	if l != nil {
		return l.Close()
	}
	return nil
}

func (p *Proxy) webDirOrNone() string {
	if p.webDir == "" {
		return "(none)"
	}
	return p.webDir
}

func (p *Proxy) logf(format string, args ...any) {
	p.logger.Printf(format, args...)
}

// rewritePath strips the configured prefix. The bool is false when reqPath
// does not belong to the prefix at all.
func (p *Proxy) rewritePath(reqPath string) (string, bool) {
	if p.prefix == "" {
		return reqPath, true
	}
	if reqPath == p.prefix || reqPath == p.prefix+"/" {
		return "/", true
	}
	if strings.HasPrefix(reqPath, p.prefix+"/") {
		rest := reqPath[len(p.prefix):]
		if rest == "" {
			return "/", true
		}
		return rest, true
	}
	return "", false
}

func (p *Proxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	upPath, ok := p.rewritePath(r.URL.Path)
	if !ok {
		p.logf("HTTP REJECT path=%s", r.URL.RequestURI())
		rejectHTTP(w, http.StatusNotFound, "Not found")
		return
	}

	// Non-/api/* paths are served from the local page when possible; otherwise
	// they fall through to the upstream, matching proxy.mjs.
	if !strings.HasPrefix(upPath, "/api/") && p.serveStatic(w, r, upPath) {
		p.logf("HTTP %s %s -> static %s", r.Method, r.URL.RequestURI(), upPath)
		return
	}

	if isUpgrade(r) {
		p.handleUpgrade(w, r, upPath)
		return
	}

	p.logf("HTTP %s %s -> %s", r.Method, r.URL.RequestURI(), upPath)
	p.handleHTTP(w, r, upPath)
}

func rejectHTTP(w http.ResponseWriter, code int, message string) {
	body := message + "\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func rejectUpgrade(conn net.Conn, code int, message string) {
	reason := "Bad Gateway"
	if code == http.StatusForbidden {
		reason = "Forbidden"
	}
	body := message + "\n"
	_, _ = fmt.Fprintf(conn,
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\n"+
			"Content-Length: %d\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n%s",
		code, reason, len(body), body)
}

// serveStatic serves a single safe path from WebDir. It returns false when the
// request should be forwarded upstream instead.
func (p *Proxy) serveStatic(w http.ResponseWriter, r *http.Request, upPath string) bool {
	if p.webDir == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	safe := strings.TrimRight(upPath, "/")
	rel := "index.html"
	if safe != "" && safe != "/" {
		rel = strings.TrimLeft(safe, "/")
	}
	if strings.Contains(rel, "..") || strings.Contains(rel, `\`) || !safeRelRe.MatchString(rel) {
		return false
	}
	file := filepath.Join(p.webDir, filepath.FromSlash(rel))
	st, err := os.Stat(file)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	contentType := "application/octet-stream"
	if strings.HasSuffix(rel, ".html") {
		contentType = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	// ServeContent handles HEAD, Range and If-Modified-Since for us.
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
	return true
}

func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request, upPath string) {
	rp := &httputil.ReverseProxy{
		Director: func(out *http.Request) {
			out.URL.Scheme = p.target.Scheme
			out.URL.Host = p.target.Host
			out.URL.Path = upPath
			out.URL.RawPath = ""
			out.URL.RawQuery = r.URL.RawQuery
			out.Host = p.target.Host
			out.Header.Del("Proxy-Connection")
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			p.logf("HTTP ERR %s %s", upPath, err)
			rejectHTTP(rw, http.StatusBadGateway, "Bad Gateway: "+err.Error())
		},
	}
	rp.ServeHTTP(w, r)
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// handleUpgrade manually tunnels a WebSocket upgrade: Hijack the client,
// replay the request line + headers to the upstream, then pipe both ways.
func (p *Proxy) handleUpgrade(w http.ResponseWriter, r *http.Request, upPath string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		p.logf("WS ERR %s hijack unsupported", upPath)
		rejectHTTP(w, http.StatusInternalServerError, "hijack unsupported")
		return
	}
	clientConn, clientRW, err := hj.Hijack()
	if err != nil {
		p.logf("WS ERR %s hijack: %s", upPath, err)
		return
	}
	defer clientConn.Close()

	addr := p.target.Host
	if p.target.Port() == "" {
		port := "80"
		if p.target.Scheme == "https" {
			port = "443"
		}
		addr = net.JoinHostPort(p.target.Hostname(), port)
	}
	var upConn net.Conn
	if p.target.Scheme == "https" {
		upConn, err = tls.Dial("tcp", addr, nil)
	} else {
		upConn, err = net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if err != nil {
		p.logf("WS ERR %s dial: %s", upPath, err)
		rejectUpgrade(clientConn, http.StatusBadGateway, "Bad Gateway: "+err.Error())
		return
	}
	defer upConn.Close()

	// Preserve the query string when replaying the upgrade request: the WebRTC
	// stream endpoint receives the device serial via ?serial=... and the token
	// via ?token=... . Dropping it here makes the backend see an empty serial.
	requestTarget := upPath
	if r.URL.RawQuery != "" {
		requestTarget += "?" + r.URL.RawQuery
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s HTTP/%d.%d\r\n", r.Method, requestTarget, r.ProtoMajor, r.ProtoMinor)
	for k, vs := range r.Header {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "proxy-") || lower == "connection" || lower == "host" {
			continue
		}
		for _, v := range vs {
			fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
		}
	}
	sb.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&sb, "Host: %s\r\n\r\n", p.target.Host)
	if _, err := io.WriteString(upConn, sb.String()); err != nil {
		p.logf("WS ERR %s write request: %s", upPath, err)
		return
	}

	p.logf("WS UPGRADE %s -> %s", r.URL.RequestURI(), upPath)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upConn, clientRW) // drains bufio first, then the conn
		if c, ok := upConn.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, upConn)
		if c, ok := clientConn.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
	}()
	wg.Wait()
}
