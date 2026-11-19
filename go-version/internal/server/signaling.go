package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tiann/scrcpy-webrtc-go/internal/signal"
)

// SignalConn adapts an encrypted MQTT room session to the Conn interface.
// Each viewer is identified by a session id (sid); all JSON frames carry it.
type SignalConn struct {
	room *signal.Room
	sid  string

	mu      sync.Mutex
	handler func([]byte, bool)
	ready   bool
	onGone  func()
	closeFn func()
	idle    *time.Timer
}

func (c *SignalConn) touch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idle != nil {
		c.idle.Stop()
	}
	c.idle = time.AfterFunc(90*time.Second, func() { c.Close(0, "") })
}

func (c *SignalConn) SendText(data string) error {
	var obj map[string]interface{}
	if json.Unmarshal([]byte(data), &obj) != nil {
		return nil
	}
	obj["sid"] = c.sid
	return c.room.Send(obj)
}

func (c *SignalConn) SendBinary([]byte) error {
	// Signaling has no WS video fallback; media is WebRTC-only.
	return nil
}

func (c *SignalConn) Close(code int, reason string) {
	c.mu.Lock()
	if !c.ready {
		c.mu.Unlock()
		return
	}
	c.ready = false
	if c.idle != nil {
		c.idle.Stop()
	}
	c.mu.Unlock()
	msg := map[string]interface{}{"sid": c.sid, "t": "bye"}
	if code != 0 {
		msg["code"] = code
	}
	_ = c.room.Send(msg)
	if c.onGone != nil {
		c.onGone()
	}
	if c.closeFn != nil {
		c.closeFn()
	}
}

func (c *SignalConn) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *SignalConn) SetMessageHandler(fn func([]byte, bool)) {
	c.mu.Lock()
	c.handler = fn
	c.mu.Unlock()
}

func (c *SignalConn) SetCloseHandler(fn func()) {
	c.mu.Lock()
	c.closeFn = fn
	c.mu.Unlock()
}

// deliver is called by the room dispatcher for messages addressed to this sid.
func (c *SignalConn) deliver(msg map[string]interface{}) {
	c.touch()
	c.mu.Lock()
	fn := c.handler
	c.mu.Unlock()
	if fn == nil {
		return
	}
	delete(msg, "sid")
	data, _ := json.Marshal(msg)
	fn(data, false)
}

func loadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		decoded, err := base64.RawURLEncoding.DecodeString(string(data))
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(key)), 0o600)
	}
	return key, nil
}

// StartSignal connects to MQTT brokers and routes encrypted viewer messages
// into sessions, mirroring startSignal() in server.mjs.
func (s *Server) StartSignal() error {
	key, err := loadOrCreateKey(s.cfg.SignalKeyFile)
	if err != nil {
		return err
	}
	room, err := signal.NewRoom(key, "host", s.cfg.SignalBrokers,
		func(format string, args ...interface{}) {
			fmt.Printf("[signal] "+format+"\n", args...)
		}, 10_000)
	if err != nil {
		return err
	}
	s.signalRoom = room

	var mu sync.Mutex
	conns := make(map[string]*SignalConn)

	room.OnMessage = func(msg map[string]interface{}) {
		sid, _ := msg["sid"].(string)
		if sid == "" {
			return
		}
		t, _ := msg["t"].(string)
		switch t {
		case "devices":
			given, _ := msg["token"].(string)
			if s.cfg.ViewNeedsToken && !tokenValid(given, s.cfg.Token) {
				_ = room.Send(map[string]interface{}{"sid": sid, "t": "devices", "error": "unauthorized"})
				return
			}
			devices := s.store.List()
			_ = room.Send(map[string]interface{}{"sid": sid, "t": "devices", "devices": devices})
		case "open":
			mu.Lock()
			if prev := conns[sid]; prev != nil {
				prev.Close(0, "")
			}
			serial, _ := msg["serial"].(string)
			token, _ := msg["token"].(string)
			authed := tokenValid(token, s.cfg.Token)
			conn := &SignalConn{room: room, sid: sid, ready: true}
			conn.onGone = func() {
				mu.Lock()
				delete(conns, sid)
				mu.Unlock()
			}
			conns[sid] = conn
			mu.Unlock()
			sess := NewSession(s, conn, serial, authed)
			s.registerSession(serial, sess)
			go sess.Run()
		case "bye":
			mu.Lock()
			if prev := conns[sid]; prev != nil {
				prev.Close(0, "")
			}
			mu.Unlock()
		default:
			mu.Lock()
			conn := conns[sid]
			mu.Unlock()
			if conn == nil {
				return
			}
			if t != "ping" {
				conn.deliver(msg)
			} else {
				conn.touch()
			}
		}
	}

	keyB64 := base64.RawURLEncoding.EncodeToString(key)
	link := fmt.Sprintf("%s#k=%s", s.cfg.PageURL, keyB64)
	if s.cfg.Token != "" {
		link += "&t=" + s.cfg.Token
	}
	fmt.Printf("\nsignaling ready — stable link (valid across restarts):\n  open:  %s\n  local: http://localhost:%d/\n\n", link, s.cfg.Port)
	return nil
}
