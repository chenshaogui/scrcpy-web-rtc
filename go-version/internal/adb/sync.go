package adb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const syncMaxPacket = 64 * 1024

func syncID(s string) uint32 {
	if len(s) != 4 {
		panic("sync id must be 4 chars")
	}
	return binary.LittleEndian.Uint32([]byte(s))
}

// writeSyncHeader writes a 4-char ID + u32 LE length, optionally followed by data.
func writeSyncHeader(w io.Writer, id string, payloadLen int) error {
	var h [8]byte
	binary.LittleEndian.PutUint32(h[0:4], syncID(id))
	binary.LittleEndian.PutUint32(h[4:8], uint32(payloadLen))
	_, err := w.Write(h[:])
	return err
}

// readSyncResponse reads an ID + u32 LE length + payload.
func readSyncResponse(r io.Reader) (string, []byte, error) {
	var h [8]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return "", nil, err
	}
	idBytes := h[0:4]
	id := string(idBytes)
	length := binary.LittleEndian.Uint32(h[4:8])
	if length > 4<<20 {
		return "", nil, errors.New("adb sync: response too large")
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return "", nil, err
		}
	}
	return id, payload, nil
}

// PushFile pushes local data to a path on the device via the sync protocol,
// matching adb.sync().write() with default 0666 permissions.
func (t *Transport) PushFile(data []byte, remotePath string, mtime int64) error {
	s, err := t.OpenSocket("sync:")
	if err != nil {
		return err
	}
	defer s.Close()

	// mode = (file type 0o10 << 12) | 0o666 = 33206 (decimal)
	const mode = 33206
	arg := fmt.Sprintf("%s,%d", remotePath, mode)
	if err := writeSyncHeader(s, "SEND", len(arg)); err != nil {
		return err
	}
	if _, err := io.WriteString(s, arg); err != nil {
		return err
	}

	for start := 0; start < len(data); start += syncMaxPacket {
		end := start + syncMaxPacket
		if end > len(data) {
			end = len(data)
		}
		if err := writeSyncHeader(s, "DATA", end-start); err != nil {
			return err
		}
		if _, err := s.Write(data[start:end]); err != nil {
			return err
		}
	}
	if err := writeSyncHeader(s, "DONE", int(mtime)); err != nil {
		return err
	}

	// Read OKAY (id + 4 unused bytes) or FAIL.
	id, payload, err := readSyncResponse(s)
	if err != nil {
		return err
	}
	switch id {
	case "OKAY":
		return nil
	case "FAIL":
		return fmt.Errorf("adb sync push failed: %s", string(payload))
	default:
		return fmt.Errorf("adb sync push: unexpected response %q", id)
	}
}

// ReverseForward registers a local handler for deviceAddress and asks adbd to
// forward that device-side address to it. Returns the local address assigned.
func (t *Transport) ReverseForward(deviceAddress string) (string, error) {
	// Register the handler under the device address as key: scrcpy's server
	// opens localabstract:scrcpy_<scid>, and AcceptReverse looks it up by the
	// same string. The ADB OPEN service string from the device equals the local
	// address we install, so we use the device address verbatim as local key.
	localAddress := deviceAddress
	_ = t.RegisterReverseHandler(localAddress)

	service := fmt.Sprintf("reverse:forward:%s;%s", deviceAddress, localAddress)
	s, err := t.OpenSocket(service)
	if err != nil {
		return "", err
	}
	defer s.Close()
	out, err := io.ReadAll(s)
	if err != nil {
		return "", err
	}
	if len(out) < 4 || string(out[:4]) != "OKAY" {
		return "", fmt.Errorf("adb reverse failed: %s", string(out))
	}
	return localAddress, nil
}

// AcceptReverse waits for the device to open a socket to one of our registered
// reverse local addresses.
func (t *Transport) AcceptReverse(timeout time.Duration, localAddress string) (*Socket, error) {
	t.mu.Lock()
	ch := t.incoming[localAddress]
	t.mu.Unlock()
	if ch == nil {
		return nil, fmt.Errorf("adb: no reverse handler for %s", localAddress)
	}
	select {
	case s := <-ch:
		return s, nil
	case <-time.After(timeout):
		return nil, errors.New("adb: reverse accept timeout")
	case <-t.done:
		return nil, errors.New("adb: transport closed")
	}
}
