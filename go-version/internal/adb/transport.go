package adb

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Transport is one authenticated ADB connection to adbd. It multiplexes many
// logical sockets over a single TCP stream, matching @yume-chan's Dispatcher.
type Transport struct {
	conn    net.Conn
	writeMu sync.Mutex

	version    uint32
	maxPayload int

	mu          sync.Mutex
	nextLocalID uint32
	sockets     map[uint32]*Socket
	incoming    map[string]chan *Socket // reverse-tunnel service -> incoming sockets

	cred *CredentialStore
	done chan struct{}
	once sync.Once
}

// handshakeTimeout bounds the CNXN/AUTH exchange; a variable so tests can
// shorten it.
var handshakeTimeout = 10 * time.Second

// Dial connects to an adbd TCP daemon and completes CNXN/AUTH handshake.
func Dial(host string, port int, cred *CredentialStore, timeout time.Duration) (*Transport, error) {
	return DialContext(context.Background(), host, port, cred, timeout)
}

// DialContext is Dial with cancellation: if ctx is canceled mid-dial or
// mid-handshake, the underlying conn is closed immediately. This matters for
// single-client adbd — a viewer that disconnects while the handshake is still
// pending must not keep the adbd slot occupied until the handshake timeout,
// or the viewer's immediate reconnect fails against its own zombie dial.
func DialContext(ctx context.Context, host string, port int, cred *CredentialStore, timeout time.Duration) (*Transport, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	// Retry transient dial failures. A redroid adbd serves one client at a
	// time; when a previous session just closed, the kernel can briefly cache
	// an ICMP unreachable and return EHOSTUNREACH ("no route to host") for a
	// second or two even though the host and port are reachable (telnet works).
	// Without retry, one stale cache entry makes the connection fail.
	var conn net.Conn
	var err error
	const attempts = 5
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, err = net.DialTimeout("tcp", addr, timeout)
		if err == nil {
			break
		}
		if i < attempts-1 {
			time.Sleep(time.Duration(300*(i+1)) * time.Millisecond) // 300/600/900/1200ms
		}
	}
	if err != nil {
		return nil, fmt.Errorf("adb tcp connect %s: %w", addr, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	// Watch for cancellation until the handshake finishes; closing the conn
	// unblocks any pending readPacket right away.
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-watchDone:
		}
	}()
	t := &Transport{
		conn:        conn,
		version:     initialVersion,
		maxPayload:  initialMaxPayload,
		nextLocalID: 1,
		sockets:     make(map[uint32]*Socket),
		incoming:    make(map[string]chan *Socket),
		cred:        cred,
		done:        make(chan struct{}),
	}
	if err := t.handshake(); err != nil {
		close(watchDone)
		conn.Close()
		return nil, err
	}
	close(watchDone)
	if ctx.Err() != nil {
		// canceled in the gap between handshake success and watch stop
		conn.Close()
		return nil, ctx.Err()
	}
	go t.readLoop()
	return t, nil
}

func (t *Transport) handshake() error {
	// Send CNXN with correct checksum (auth phase always computes it).
	if err := writePacket(t.conn, Packet{
		Command: CmdConnect,
		Arg0:    t.version,
		Arg1:    uint32(t.maxPayload),
		Payload: []byte(featureBanner),
	}, t.version, true); err != nil {
		return err
	}

	deadline := time.Now().Add(handshakeTimeout)
	// Enforce the deadline on the socket itself. A busy single-client adbd
	// (e.g. held by another server instance on a different host) can accept
	// the TCP connection and then stay silent — without a read deadline the
	// first readPacket blocks forever and the viewer hangs at "connecting"
	// with no error. Cleared again once the handshake completes.
	_ = t.conn.SetReadDeadline(deadline)
	defer func() { _ = t.conn.SetReadDeadline(time.Time{}) }()
	// ADB auth is a state machine mirroring @yume-chan's authenticators:
	//   1. For each token the device sends, try signing with our private key and
	//      reply AUTH_SIGNATURE. If the device already trusts this key it replies
	//      with CNXN and we are done.
	//   2. If the device sends ANOTHER token after our signature, it means the
	//      signature was rejected (key not in adb_keys yet) — fall back to
	//      AUTH_PUBLIC_KEY so the user/device can authorize it. redroid in
	//      particular only trusts keys presented this way on first connect.
	// Critically: a "successfully generated" signature is NOT proof of trust;
	// only a CNXN reply is. So we must advance state on the NEXT token, not on
	// whether signing returned bytes.
	authStage := 0 // 0 = signature, >=1 = public key
	for time.Now().Before(deadline) {
		pkt, err := readPacket(t.conn)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return errors.New("adb handshake timeout: device adbd accepted the connection but stayed silent — it is likely busy with another client (single-client adbd, e.g. another scrcpy-webrtc instance), or this host key is not authorized")
			}
			return fmt.Errorf("adb handshake read: %w", err)
		}
		switch pkt.Command {
		case CmdConnect:
			t.version = min32(t.version, pkt.Arg0)
			t.maxPayload = int(min32(uint32(t.maxPayload), pkt.Arg1))
			return nil // authenticated
		case CmdAuth:
			if pkt.Arg0 != AuthToken {
				continue
			}
			var payload []byte
			var authType uint32
			if authStage == 0 {
				// First token: try signature. If our key is trusted by the
				// device, this yields a CNXN next.
				sig, err := t.cred.SignaturePayload(pkt.Payload)
				if err != nil {
					return err
				}
				payload, authType = sig, AuthSignature
				authStage = 1
			} else {
				// Signature was rejected (device re-issued a token): present
				// the public key. Device will either accept it (CNXN) or ask
				// for another token; re-sending the public key is harmless.
				pub, err := t.cred.PublicKeyPayload()
				if err != nil {
					return err
				}
				payload, authType = pub, AuthPublicKey
			}
			if err := writePacket(t.conn, Packet{
				Command: CmdAuth, Arg0: authType, Payload: payload,
			}, t.version, true); err != nil {
				return err
			}
		default:
			return fmt.Errorf("adb handshake: unexpected %s", pkt)
		}
	}
	return errors.New("adb handshake timeout (device adbd busy with another client, or this host key is not authorized)")
}

func min32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

func (t *Transport) sendPacket(p Packet) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return writePacket(t.conn, p, t.version, false)
}

// readLoop demultiplexes incoming packets to sockets.
func (t *Transport) readLoop() {
	defer t.Close()
	for {
		pkt, err := readPacket(t.conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				// network errors just end the transport
			}
			return
		}
		t.handlePacket(pkt)
	}
}

func (t *Transport) handlePacket(pkt Packet) {
	switch pkt.Command {
	case CmdOpen:
		// Device initiated a socket (reverse tunnel connection). arg0 = remote id.
		service := string(bytes.TrimRight(pkt.Payload, "\x00"))
		t.mu.Lock()
		ch, ok := t.incoming[service]
		t.mu.Unlock()
		if !ok {
			_ = t.sendPacket(Packet{Command: CmdClose, Arg1: pkt.Arg0})
			return
		}
		localID := t.allocID()
		s := newSocket(t, localID, pkt.Arg0)
		// A reverse socket is already open from our side once we send OKAY.
		// Grant the initial one-packet write window (same state that ack(0)
		// gives a forward socket) and mark it opened, otherwise the first
		// control WRITE waits forever for credits and touches never reach adbd.
		s.writeMu.Lock()
		s.credits = -1
		s.writeMu.Unlock()
		s.opened.Store(true)
		close(s.openCh)
		// Register the socket before sending OKAY. The device can send WRTE
		// immediately after seeing OKAY, and CmdWrite/CmdClose are routed by
		// looking up t.sockets[pkt.Arg1] (our local id). Without this entry all
		// reverse-tunnel payloads — including scrcpy's video meta — are dropped.
		t.mu.Lock()
		t.sockets[localID] = s
		t.mu.Unlock()
		select {
		case ch <- s:
			_ = t.sendPacket(Packet{Command: CmdOkay, Arg0: localID, Arg1: pkt.Arg0})
		default:
			t.mu.Lock()
			delete(t.sockets, localID)
			t.mu.Unlock()
			_ = t.sendPacket(Packet{Command: CmdClose, Arg1: pkt.Arg0})
		}
	case CmdOkay:
		// Response to our OPEN (arg0=remote id, arg1=local id) or a write ack.
		t.mu.Lock()
		s := t.sockets[pkt.Arg1]
		t.mu.Unlock()
		if s == nil {
			return
		}
		if !s.opened.Load() {
			s.remoteID = pkt.Arg0
			if len(pkt.Payload) >= 4 {
				s.ack(binary.LittleEndian.Uint32(pkt.Payload[:4]))
			} else {
				s.ack(0) // signals unlimited (per-write ack mode)
			}
			s.opened.Store(true)
			close(s.openCh)
		} else {
			if len(pkt.Payload) >= 4 {
				s.ack(binary.LittleEndian.Uint32(pkt.Payload[:4]))
			} else {
				s.ack(0)
			}
		}
	case CmdWrite:
		t.mu.Lock()
		s := t.sockets[pkt.Arg1]
		t.mu.Unlock()
		if s == nil {
			return
		}
		s.enqueue(pkt.Payload)
		// ACK back, swapping arg0/arg1. Echo 4-byte LE byte count.
		var ack [4]byte
		binary.LittleEndian.PutUint32(ack[:], uint32(len(pkt.Payload)))
		_ = t.sendPacket(Packet{Command: CmdOkay, Arg0: pkt.Arg1, Arg1: pkt.Arg0, Payload: ack[:]})
	case CmdClose:
		t.mu.Lock()
		s := t.sockets[pkt.Arg1]
		delete(t.sockets, pkt.Arg1)
		t.mu.Unlock()
		if s != nil {
			s.closeFromPeer()
		}
	}
}

func (t *Transport) allocID() uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := t.nextLocalID
	t.nextLocalID++
	return id
}

// OpenSocket opens a new logical socket for the given service string.
func (t *Transport) OpenSocket(service string) (*Socket, error) {
	localID := t.allocID()
	s := newSocket(t, localID, 0)
	t.mu.Lock()
	t.sockets[localID] = s
	t.mu.Unlock()

	payload := []byte(service)
	if t.version < versionOmitChecksum {
		payload = append(payload, 0)
	}
	if err := t.sendPacket(Packet{
		Command: CmdOpen, Arg0: localID, Payload: payload,
	}); err != nil {
		return nil, err
	}

	select {
	case <-s.openCh:
		return s, nil
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("adb: open %q timeout", service)
	case <-t.done:
		return nil, errors.New("adb: transport closed")
	}
}

// RegisterReverseHandler registers a channel to receive sockets that the
// device opens to localAddress (used by scrcpy's abstract socket).
func (t *Transport) RegisterReverseHandler(localAddress string) chan *Socket {
	ch := make(chan *Socket, 4)
	t.mu.Lock()
	t.incoming[localAddress] = ch
	t.mu.Unlock()
	return ch
}

func (t *Transport) Close() error {
	var err error
	t.once.Do(func() {
		close(t.done)
		err = t.conn.Close()
	})
	return err
}

// RunCommand opens a service, writes data (optional), reads all output until
// close, and returns it. Mirrors spawnWaitText / one-shot shell commands.
func (t *Transport) RunCommand(service string, data []byte) ([]byte, error) {
	s, err := t.OpenSocket(service)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if len(data) > 0 {
		if _, err := s.Write(data); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(s)
}
