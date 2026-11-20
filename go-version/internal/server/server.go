package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tiann/scrcpy-webrtc-go/internal/adb"
	"github.com/tiann/scrcpy-webrtc-go/internal/rtc"
	"github.com/tiann/scrcpy-webrtc-go/internal/signal"
)

// Server owns the HTTP server, device store, and the live session registry.
type Server struct {
	cfg   *Config
	store *adb.DeviceStore

	upgrader websocket.Upgrader

	sessionsMu sync.Mutex
	sessions   map[string]*Session // serial -> session (one per device)

	turnMu    sync.Mutex
	turnCache []rtc.ICEServer
	turnUntil time.Time

	signalRoom *signal.Room
}

func New(cfg *Config) *Server {
	store := adb.NewDeviceStore("", cfg.VarDir)
	s := &Server{
		cfg: cfg, store: store,
		sessions: make(map[string]*Session),
		upgrader: websocket.Upgrader{
			CheckOrigin:  func(r *http.Request) bool { return true },
			Subprotocols: []string{},
		},
	}
	return s
}

func (s *Server) unregisterSession(serial string, sess *Session) bool {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if s.sessions[serial] == sess {
		delete(s.sessions, serial)
		return true
	}
	return false
}

// registerSession evicts the previous viewer for the device, then registers.
func (s *Server) registerSession(serial string, sess *Session) {
	if serial == "" {
		return
	}
	s.sessionsMu.Lock()
	prev := s.sessions[serial]
	s.sessions[serial] = sess
	s.sessionsMu.Unlock()
	if prev != nil {
		prev.conn.Close(4000, "replaced by a new session")
		time.Sleep(400 * time.Millisecond)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Serve starts HTTP/WS listeners. It blocks until the context is canceled.
func (s *Server) Serve(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", s.handleDevices)
	mux.HandleFunc("/api/adb-connect", s.handleADBConnect)
	mux.HandleFunc("/api/adb-disconnect", s.handleADBDisconnect)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/", s.handleStatic)

	host := s.cfg.Host
	if host == "" {
		host = "0.0.0.0"
	}
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", host, s.cfg.Port), Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	return srv.ListenAndServe()
}

func (s *Server) authRequest(r *http.Request) bool {
	if !s.cfg.ViewNeedsToken {
		return true
	}
	given := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		given = strings.TrimPrefix(h, "Bearer ")
	} else {
		given = r.URL.Query().Get("token")
	}
	return tokenValid(given, s.cfg.Token)
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "authorization")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.authRequest(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	devices := s.store.List()
	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) handleADBConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "authorization")
	if !s.authRequest(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	port := atoi(r.URL.Query().Get("port"))
	if ip == "" || port < 1 || port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ip or port"})
		return
	}
	if _, err := s.store.Connect(fmt.Sprintf("%s:%d", ip, port)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already connected") {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "already": true})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (s *Server) handleADBDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "authorization")
	if !s.authRequest(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	serial := strings.TrimSpace(r.URL.Query().Get("serial"))
	if serial == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing serial"})
		return
	}
	removed := s.store.Disconnect(serial)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "removed": removed})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	// Token may arrive as a subprotocol (browsers can't set WS headers).
	given := ""
	for _, p := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, part := range strings.Split(p, ",") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "scrcpy-webrtc.token.") {
				given = strings.TrimPrefix(part, "scrcpy-webrtc.token.")
			}
		}
	}
	if given == "" {
		given = r.URL.Query().Get("token")
	}
	authed := tokenValid(given, s.cfg.Token)
	if s.cfg.ViewNeedsToken && !authed {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// Echo the token subprotocol back so the browser handshake succeeds.
	if given != "" {
		s.upgrader.Subprotocols = []string{"scrcpy-webrtc.token." + given}
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := NewWSConn(ws)
	serial := r.URL.Query().Get("serial")
	sess := NewSession(s, conn, serial, authed)
	s.registerSession(serial, sess)
	sess.Run()
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PageURL != "" && !isLocalRequest(r) {
		// Interstitial pointing to the hosted frontend (token/server prefilled).
		target := s.cfg.PageURL
		if u, err := parseURL(target); err == nil {
			q := u.Query()
			q.Set("server", r.Host)
			if token := r.URL.Query().Get("token"); token != "" {
				q.Set("token", token)
			}
			u.RawQuery = q.Encode()
			target = u.String()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, interstitialHTML(target))
		return
	}
	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}
	clean := filepath.Clean(strings.TrimPrefix(path, "/"))
	if strings.HasPrefix(clean, "..") {
		http.NotFound(w, r)
		return
	}
	file := filepath.Join(s.cfg.PublicDir, clean)
	data, err := os.ReadFile(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(clean))
	switch ext {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	_, _ = w.Write(data)
}

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

func isLocalRequest(r *http.Request) bool {
	host := r.Host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" ||
		strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "172.")
}

func interstitialHTML(target string) string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8"><title>scrcpy-webrtc</title></head>` +
		`<body style="font-family:system-ui;background:#0f1115;color:#e6e8ec;padding:2rem">` +
		`<h1>scrcpy-webrtc</h1><p>Open the console from the CDN:</p>` +
		`<a style="color:#7ea6f5" href="` + target + `">` + target + `</a></body></html>`
}

// logf mirrors Session.log's plain-stdout style for server-level events.
func (s *Server) logf(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

// turnServers resolves TURN servers (static or Cloudflare on demand).
// Without TURN a remote viewer (FN Connect relay, different NAT) has no media
// path at all once P2P fails — the WS canvas fallback needs WebCodecs, which
// the fnOS desktop WebView lacks.
func (s *Server) turnServers() []rtc.ICEServer {
	if s.cfg.TURNTarget == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(s.cfg.TURNTarget), "turn:") ||
		strings.HasPrefix(strings.ToLower(s.cfg.TURNTarget), "turns:") {
		user, pass, _ := strings.Cut(s.cfg.TURNAuth, ":")
		urls := strings.Split(s.cfg.TURNTarget, ",")
		for i := range urls {
			urls[i] = strings.TrimSpace(urls[i])
		}
		return []rtc.ICEServer{{URLs: urls, Username: user, Credential: pass}}
	}
	if strings.EqualFold(s.cfg.TURNTarget, "cloudflare") {
		return s.cloudflareTurnServers()
	}
	return nil
}

// cloudflareTurnServers mints short-lived TURN credentials from the Cloudflare
// Calls API (same endpoint/shape as server.mjs), cached for half the TTL.
func (s *Server) cloudflareTurnServers() []rtc.ICEServer {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if len(s.turnCache) > 0 && time.Now().Before(s.turnUntil) {
		return s.turnCache
	}
	keyID := os.Getenv("CF_TURN_KEY_ID")
	apiToken := os.Getenv("CF_TURN_API_TOKEN")
	if keyID == "" || apiToken == "" {
		s.logf("turn: cloudflare requested but CF_TURN_KEY_ID / CF_TURN_API_TOKEN are not set")
		return nil
	}
	body := strings.NewReader(`{"ttl":86400}`)
	req, err := http.NewRequest(http.MethodPost,
		"https://rtc.live.cloudflare.com/v1/turn/keys/"+keyID+"/credentials/generate-ice-servers", body)
	if err != nil {
		s.logf("turn: %s", err)
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		s.logf("turn: cloudflare credentials: %s", err)
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		s.logf("turn: cloudflare credentials: HTTP %d", res.StatusCode)
		return nil
	}
	var payload struct {
		ICEServers []rtc.ICEServer `json:"iceServers"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&payload); err != nil {
		s.logf("turn: cloudflare credentials: %s", err)
		return nil
	}
	// keep only turn(s): urls — the page has STUN built in — and drop the :53
	// variants, which browsers refuse to use (same filter as server.mjs)
	var servers []rtc.ICEServer
	for _, srv := range payload.ICEServers {
		var urls []string
		for _, u := range srv.URLs {
			low := strings.ToLower(u)
			if (strings.HasPrefix(low, "turn:") || strings.HasPrefix(low, "turns:")) &&
				!strings.Contains(u, ":53") {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			servers = append(servers, rtc.ICEServer{URLs: urls, Username: srv.Username, Credential: srv.Credential})
		}
	}
	if len(servers) == 0 {
		s.logf("turn: no turn(s): urls in cloudflare response")
		return nil
	}
	s.turnCache = servers
	s.turnUntil = time.Now().Add(12 * time.Hour) // half the ttl
	return servers
}
