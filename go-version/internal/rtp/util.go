package rtp

import (
	"crypto/rand"
	"encoding/binary"
)

func randomU32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.LittleEndian.Uint32(b[:])
}

// VideoTimestamp converts scrcpy microseconds (pts) to the 90kHz RTP clock.
func VideoTimestamp(pts uint64) uint32 {
	return uint32((pts * 9) / 100)
}

// AudioTimestamp converts scrcpy microseconds (pts) to the 48kHz RTP clock.
func AudioTimestamp(pts uint64) uint32 {
	return uint32((pts * 48) / 1000)
}
