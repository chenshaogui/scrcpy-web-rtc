package server

import (
	"encoding/binary"
	"testing"

	"github.com/tiann/scrcpy-webrtc-go/internal/scrcpy"
)

// captureConn records binary frames so tests can assert the WS video framing
// the page's WebCodecs decoder expects (tag/keyframe/pts header, 10 bytes).
type captureConn struct {
	binary [][]byte
	text   []string
}

func (c *captureConn) SendText(data string) error { c.text = append(c.text, data); return nil }
func (c *captureConn) SendBinary(data []byte) error {
	c.binary = append(c.binary, append([]byte(nil), data...))
	return nil
}
func (c *captureConn) Close(code int, reason string)          {}
func (c *captureConn) Ready() bool                            { return true }
func (c *captureConn) SetMessageHandler(fn func([]byte, bool)) {}
func (c *captureConn) SetCloseHandler(fn func())               {}

// The page reads pts with DataView.getBigUint64(2) (big-endian default), same
// as server.mjs writes it. A little-endian pts here byte-swaps into garbage
// timestamps for the WebCodecs decoder.
func TestSendVideoPacketPtsIsBigEndian(t *testing.T) {
	srv := New(&Config{})
	conn := &captureConn{}
	s := NewSession(srv, conn, "test-serial", true)

	const pts = 0x0102030405060708
	s.sendVideoPacket(&scrcpy.MediaPacket{PTS: pts, Keyframe: true, Data: []byte{0xAA, 0xBB}})
	if len(conn.binary) != 1 {
		t.Fatalf("expected 1 binary frame, got %d", len(conn.binary))
	}
	frame := conn.binary[0]
	if frame[0] != 1 || frame[1] != 1 {
		t.Fatalf("tag/keyframe bytes = %d/%d, want 1/1", frame[0], frame[1])
	}
	if got := binary.BigEndian.Uint64(frame[2:10]); got != pts {
		t.Fatalf("pts read big-endian = %#x, want %#x", got, uint64(pts))
	}
	if len(frame) != 12 || frame[10] != 0xAA || frame[11] != 0xBB {
		t.Fatalf("payload misplaced: %v", frame)
	}

	// configuration packets carry tag 0 and no pts
	s.sendVideoPacket(&scrcpy.MediaPacket{Config: true, Data: []byte{0x01}})
	cfg := conn.binary[1]
	if cfg[0] != 0 || len(cfg) != 11 {
		t.Fatalf("config frame = %v, want tag 0 + 1 payload byte", cfg)
	}
}