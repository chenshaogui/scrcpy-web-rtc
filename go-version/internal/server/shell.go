package server

import (
	"encoding/base64"
	"sync"

	"github.com/tiann/scrcpy-webrtc-go/internal/adb"
)

// shellSession holds one device shell PTY.
type shellSession struct {
	sock *adb.Socket
	mu   sync.Mutex
}

func (s *Session) handleShell(msg map[string]interface{}) {
	t, _ := msg["t"].(string)
	switch t {
	case "shell-start":
		if !s.srv.cfg.Shell {
			s.sendShell(map[string]interface{}{"t": "shell-error", "message": "shell disabled (start server with --shell)"})
			return
		}
		if !s.authed {
			s.sendShell(map[string]interface{}{"t": "shell-error", "message": "unauthorized: open the page with ?token="})
			return
		}
		s.startShell(msg)
	case "shell-in":
		d, _ := msg["d"].(string)
		if raw, err := base64.StdEncoding.DecodeString(d); err == nil {
			s.mu.Lock()
			sh := s.shell
			s.mu.Unlock()
			if sh != nil && sh.sock != nil {
				_ = adb.WriteShellPacket(sh.sock, adb.ShellStdin, raw)
			}
		}
	case "shell-resize":
		rows, _ := num(msg["rows"])
		cols, _ := num(msg["cols"])
		payload := []byte(formatSize(int(rows), int(cols)))
		s.mu.Lock()
		sh := s.shell
		s.mu.Unlock()
		if sh != nil && sh.sock != nil {
			_ = adb.WriteShellPacket(sh.sock, adb.ShellResize, payload)
		}
	case "shell-stop":
		s.stopShell()
	}
}

func (s *Session) startShell(msg map[string]interface{}) {
	transport, err := s.store.DialSerial(s.serial)
	if err != nil {
		s.sendShell(map[string]interface{}{"t": "shell-error", "message": err.Error()})
		return
	}
	sock, err := transport.OpenShell("xterm-256color")
	if err != nil {
		transport.Close()
		s.sendShell(map[string]interface{}{"t": "shell-error", "message": err.Error()})
		return
	}
	sh := &shellSession{sock: sock}
	s.mu.Lock()
	s.stopShellLocked()
	s.shell = sh
	s.shellTransport = transport
	s.mu.Unlock()

	if rows, ok := num(msg["rows"]); ok {
		if cols, ok := num(msg["cols"]); ok {
			_ = adb.WriteShellPacket(sock, adb.ShellResize, []byte(formatSize(int(rows), int(cols))))
		}
	}
	s.log("shell started")
	s.sendShell(map[string]interface{}{"t": "shell-ready"})

	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := sock.Read(buf)
			if n > 0 {
				// Skip the 5-byte shell v2 header; forward stdout/stderr payloads.
				if n >= 5 {
					id := buf[0]
					length := int(uint32(buf[1]) | uint32(buf[2])<<8 | uint32(buf[3])<<16 | uint32(buf[4])<<24)
					if id == adb.ShellStdout || id == adb.ShellStderr {
						if 5+length <= n {
							payload := buf[5 : 5+length]
							s.sendShell(map[string]interface{}{
								"t": "shell-out", "d": base64.StdEncoding.EncodeToString(payload),
							})
						}
					} else if id == adb.ShellExit && length >= 1 {
						code := buf[5]
						s.sendShell(map[string]interface{}{"t": "shell-exit", "code": code})
						s.stopShell()
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

func (s *Session) stopShell() {
	s.mu.Lock()
	s.stopShellLocked()
	s.mu.Unlock()
}

func (s *Session) stopShellLocked() {
	if s.shell != nil && s.shell.sock != nil {
		_ = adb.WriteShellPacket(s.shell.sock, adb.ShellCloseStdin, nil)
		_ = s.shell.sock.Close()
	}
	if s.shellTransport != nil {
		_ = s.shellTransport.Close()
	}
	s.shell = nil
	s.shellTransport = nil
}

func formatSize(rows, cols int) string {
	return intToStr(rows) + "x" + intToStr(cols) + ",0x0\x00"
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
