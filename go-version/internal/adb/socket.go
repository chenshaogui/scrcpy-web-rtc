package adb

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Socket is one logical ADB stream multiplexed over a Transport.
type Socket struct {
	t        *Transport
	localID  uint32
	remoteID uint32
	opened   atomic.Bool
	openCh   chan struct{}

	mu       sync.Mutex
	chunks   [][]byte
	readCh   chan struct{}
	readDone bool
	err      error

	writeMu    sync.Mutex
	credits    int // available write bytes; -1 means one-packet-at-a-time (empty OKAY)
	writeReady chan struct{}

	closeOnce sync.Once
}

func newSocket(t *Transport, localID, remoteID uint32) *Socket {
	return &Socket{
		t:          t,
		localID:    localID,
		remoteID:   remoteID,
		openCh:     make(chan struct{}),
		readCh:     make(chan struct{}, 1),
		writeReady: make(chan struct{}, 1),
	}
}

// ack is called by the transport when an OKAY arrives.
func (s *Socket) ack(bytes uint32) {
	s.writeMu.Lock()
	if bytes == 0 {
		// Empty OKAY = "one more packet is allowed" (delayed ack disabled).
		s.credits = -1
	} else if s.credits >= 0 {
		s.credits += int(bytes)
	}
	s.writeMu.Unlock()
	// Wake a writer blocked waiting for credit immediately. Polling with a
	// 200ms timer here made pushing a multi-MB jar take many seconds and stall.
	select {
	case s.writeReady <- struct{}{}:
	default:
	}
}

// enqueue buffers data received via WRTE.
func (s *Socket) enqueue(data []byte) {
	s.mu.Lock()
	if s.readDone {
		s.mu.Unlock()
		return
	}
	cp := append([]byte(nil), data...)
	s.chunks = append(s.chunks, cp)
	s.mu.Unlock()
	select {
	case s.readCh <- struct{}{}:
	default:
	}
}

// Read implements io.Reader over the incoming WRTE chunks.
func (s *Socket) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.chunks) > 0 {
			c := s.chunks[0]
			n := copy(p, c)
			if n == len(c) {
				s.chunks = s.chunks[1:]
			} else {
				s.chunks[0] = c[n:]
			}
			s.mu.Unlock()
			return n, nil
		}
		if s.readDone {
			err := s.err
			s.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
		s.mu.Unlock()
		<-s.readCh
	}
}

// Write sends data as one or more WRTE packets, honoring the write window.
func (s *Socket) Write(data []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	total := len(data)
	for start := 0; start < total; {
		// We open with arg1=0 (delayed ack disabled), so adbd grants one packet
		// at a time via empty OKAY packets. Wait for that credit; ack() signals
		// writeReady so we wake immediately instead of polling on a timer.
		for s.credits == 0 {
			s.writeMu.Unlock()
			select {
			case <-s.t.done:
				s.writeMu.Lock()
				return start, errors.New("adb: transport closed")
			case <-s.writeReady:
			case <-time.After(10 * time.Second):
				// safety net in case an OKAY is lost
			}
			s.writeMu.Lock()
		}
		end := start + s.t.maxPayload
		if end > total {
			end = total
		}
		chunk := data[start:end]
		if err := s.t.sendPacket(Packet{
			Command: CmdWrite,
			Arg0:    s.localID,
			Arg1:    s.remoteID,
			Payload: chunk,
		}); err != nil {
			return start, err
		}
		if s.credits > 0 {
			s.credits -= len(chunk)
		} else {
			// -1 (empty OKAY mode): consume the single-packet credit.
			s.credits = 0
		}
		start = end
	}
	return total, nil
}

func (s *Socket) closeFromPeer() {
	s.mu.Lock()
	s.readDone = true
	s.mu.Unlock()
	select {
	case s.readCh <- struct{}{}:
	default:
	}
}

// Close sends CLSE and marks the socket done locally.
func (s *Socket) Close() error {
	s.closeOnce.Do(func() {
		_ = s.t.sendPacket(Packet{Command: CmdClose, Arg0: s.localID, Arg1: s.remoteID})
		s.closeFromPeer()
	})
	return nil
}
