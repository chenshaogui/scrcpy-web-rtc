package adb

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// mockAdbd simulates an adbd that does NOT pre-trust our key: it sends a token,
// rejects our signature (sends another token), then accepts our public key.
// This mirrors redroid on first connect and verifies our auth state machine
// falls back from signature to public key.
func mockAdbd(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				// Read CNXN.
				pkt, err := readPacket(conn)
				if err != nil {
					return
				}
				if pkt.Command != CmdConnect {
					return
				}
				// Reply CNXN.
				_ = writePacket(conn, Packet{Command: CmdConnect, Arg0: initialVersion, Arg1: initialMaxPayload}, initialVersion, true)
				// This mock drives a fresh AUTH challenge sequence to test the
				// client's signature->publickey fallback directly.
				token := bytes.Repeat([]byte{0x5a}, 20)
				_ = writePacket(conn, Packet{Command: CmdAuth, Arg0: AuthToken, Payload: token}, initialVersion, true)
				auth, err := readPacket(conn)
				if err != nil {
					return
				}
				if auth.Arg0 != AuthSignature {
					t.Errorf("first response = %d, want AuthSignature", auth.Arg0)
				}
				// Reject signature: send a new token.
				token2 := bytes.Repeat([]byte{0xa5}, 20)
				_ = writePacket(conn, Packet{Command: CmdAuth, Arg0: AuthToken, Payload: token2}, initialVersion, true)
				auth2, err := readPacket(conn)
				if err != nil {
					return
				}
				if auth2.Arg0 != AuthPublicKey {
					t.Errorf("second response = %d, want AuthPublicKey (fallback)", auth2.Arg0)
				}
				if len(auth2.Payload) != 524 {
					t.Errorf("public key payload = %d bytes, want 524", len(auth2.Payload))
				}
				if got := binary.LittleEndian.Uint32(auth2.Payload[:4]); got != 64 {
					t.Errorf("pubkey words = %d, want 64", got)
				}
				// Accept: reply CNXN.
				_ = writePacket(conn, Packet{Command: CmdConnect, Arg0: initialVersion, Arg1: initialMaxPayload}, initialVersion, true)
				// Keep the connection open briefly for the client to finish.
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestAuthFallbackToPublicKey(t *testing.T) {
	addr, stop := mockAdbd(t)
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	cred := NewCredentialStore("", "test")
	transport, err := Dial(host, port, cred, 3*time.Second)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer transport.Close()
	if transport.version != initialVersion {
		t.Errorf("version = %#x", transport.version)
	}
}

// A device that pre-trusts our key: signature is accepted immediately with CNXN.
func mockAdbdTrusted(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, err := readPacket(conn) // CNXN
				if err != nil {
					return
				}
				_ = writePacket(conn, Packet{Command: CmdConnect, Arg0: initialVersion, Arg1: initialMaxPayload}, initialVersion, true)
				token := bytes.Repeat([]byte{0x11}, 20)
				_ = writePacket(conn, Packet{Command: CmdAuth, Arg0: AuthToken, Payload: token}, initialVersion, true)
				auth, err := readPacket(conn)
				if err != nil {
					return
				}
				if auth.Arg0 != AuthSignature {
					t.Errorf("trusted device: response = %d, want AuthSignature", auth.Arg0)
				}
				// Accept signature -> CNXN.
				_ = writePacket(conn, Packet{Command: CmdConnect, Arg0: initialVersion, Arg1: initialMaxPayload}, initialVersion, true)
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestAuthTrustedSignature(t *testing.T) {
	addr, stop := mockAdbdTrusted(t)
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	transport, err := Dial(host, port, NewCredentialStore("", "test2"), 3*time.Second)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer transport.Close()
}

// A busy single-client adbd (held by another scrcpy-webrtc instance) accepts
// the TCP connection but never answers CNXN. Dial must fail with a clear
// timeout instead of blocking forever and hanging the viewer at "connecting".
func TestHandshakeSilentAdbdTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Accept and stay silent, holding the connection open.
			go func() {
				buf := make([]byte, 256)
				for {
					if _, err := conn.Read(buf); err != nil {
						conn.Close()
						return
					}
				}
			}()
		}
	}()

	old := handshakeTimeout
	handshakeTimeout = 300 * time.Millisecond
	defer func() { handshakeTimeout = old }()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	done := make(chan error, 1)
	go func() {
		_, err := Dial(host, port, NewCredentialStore("", "test-silent"), 3*time.Second)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Dial against a silent adbd should fail, got nil")
		}
		if !strings.Contains(err.Error(), "handshake timeout") {
			t.Fatalf("error = %v, want handshake timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dial blocked forever against a silent adbd (no read deadline)")
	}
}

// Canceling the context mid-handshake must unblock the dial immediately
// (viewer tapped 断开 while "connecting…") so the adbd slot is freed for the
// reconnect, not held until the handshake timeout.
func TestDialContextCancelUnblocksHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 256)
				for {
					if _, err := conn.Read(buf); err != nil {
						conn.Close()
						return
					}
				}
			}()
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err = DialContext(ctx, host, port, NewCredentialStore("", "test-cancel"), 3*time.Second)
	if err == nil {
		t.Fatal("canceled dial should fail, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v to unblock the handshake, want immediate", elapsed)
	}
}
