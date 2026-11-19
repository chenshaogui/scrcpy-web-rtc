// Package adb implements the ADB over TCP protocol directly against adbd,
// without an external `adb` binary or adb-server. It is a Go port of the
// subset of @yume-chan/adb used by scrcpy-webrtc.
package adb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ADB command words. Stored as the integer whose little-endian encoding is
// the ASCII command (e.g. 0x48545541 -> "AUTH").
const (
	CmdAuth    uint32 = 0x48545541 // AUTH
	CmdClose   uint32 = 0x45534c43 // CLSE
	CmdConnect uint32 = 0x4e584e43 // CNXN
	CmdOkay    uint32 = 0x59414b4f // OKAY
	CmdOpen    uint32 = 0x4e45504f // OPEN
	CmdWrite   uint32 = 0x45545257 // WRTE
)

const (
	headerSize = 24
	// Version >= this omits checksums and the trailing NUL on service strings.
	versionOmitChecksum uint32 = 0x01000001
	initialVersion      uint32 = 0x01000001
	initialMaxPayload          = 1024 * 1024
)

// Features advertised in the CNXN banner. Only the ones we actually rely on
// are needed; advertising the full set matches @yume-chan and avoids adbd
// refusing reverse/sendrecv on newer devices.
const featureBanner = "host::features=shell_v2,cmd,stat_v2,ls_v2,fixed_push_mkdir,abb,fixed_push_symlink_timestamp,abb_exec,sendrecv_v2,sendrecv_v2_dry_run_send"

// Packet is one ADB protocol frame.
type Packet struct {
	Command       uint32
	Arg0, Arg1    uint32
	PayloadLength uint32
	Checksum      uint32
	Magic         uint32
	Payload       []byte
}

func checksum(p []byte) uint32 {
	var sum uint32
	for _, b := range p {
		sum += uint32(b)
	}
	return sum
}

func cmdName(c uint32) string {
	switch c {
	case CmdAuth:
		return "AUTH"
	case CmdClose:
		return "CLSE"
	case CmdConnect:
		return "CNXN"
	case CmdOkay:
		return "OKAY"
	case CmdOpen:
		return "OPEN"
	case CmdWrite:
		return "WRTE"
	default:
		return fmt.Sprintf("0x%08x", c)
	}
}

func (p Packet) String() string {
	return fmt.Sprintf("%s(arg0=%d arg1=%d len=%d)", cmdName(p.Command), p.Arg0, p.Arg1, p.PayloadLength)
}

// writePacket serializes one frame. During authentication (alwaysChecksum)
// the checksum is computed even on new-version links; afterwards it is 0.
func writePacket(w io.Writer, p Packet, version uint32, alwaysChecksum bool) error {
	var hdr [headerSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], p.Command)
	binary.LittleEndian.PutUint32(hdr[4:8], p.Arg0)
	binary.LittleEndian.PutUint32(hdr[8:12], p.Arg1)
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(p.Payload)))
	var cs uint32
	if alwaysChecksum || version < versionOmitChecksum {
		cs = checksum(p.Payload)
	}
	binary.LittleEndian.PutUint32(hdr[16:20], cs)
	binary.LittleEndian.PutUint32(hdr[20:24], p.Command^0xffffffff)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(p.Payload) > 0 {
		_, err := w.Write(p.Payload)
		return err
	}
	return nil
}

// readPacket parses one frame. It tolerates a zero or mismatched checksum the
// same way adbd does on modern links.
func readPacket(r io.Reader) (Packet, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Packet{}, err
	}
	p := Packet{
		Command:       binary.LittleEndian.Uint32(hdr[0:4]),
		Arg0:          binary.LittleEndian.Uint32(hdr[4:8]),
		Arg1:          binary.LittleEndian.Uint32(hdr[8:12]),
		PayloadLength: binary.LittleEndian.Uint32(hdr[12:16]),
		Checksum:      binary.LittleEndian.Uint32(hdr[16:20]),
		Magic:         binary.LittleEndian.Uint32(hdr[20:24]),
	}
	if p.Magic != (p.Command ^ 0xffffffff) {
		return Packet{}, fmt.Errorf("adb: bad magic for %s", cmdName(p.Command))
	}
	if p.PayloadLength > 64<<20 {
		return Packet{}, errors.New("adb: payload too large")
	}
	if p.PayloadLength > 0 {
		p.Payload = make([]byte, p.PayloadLength)
		if _, err := io.ReadFull(r, p.Payload); err != nil {
			return Packet{}, err
		}
	}
	return p, nil
}
