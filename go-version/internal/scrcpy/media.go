package scrcpy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	ptsConfig   uint64 = 1 << 63
	ptsKeyframe uint64 = 1 << 62
)

// MediaPacket is one decoded frame/configuration packet from a media stream.
type MediaPacket struct {
	Config   bool
	Keyframe bool
	PTS      uint64 // microseconds, flag bits cleared
	Data     []byte
}

// MediaReader parses the big-endian media stream framing:
//
//	u64 pts | u32 length | bytes
//
// bit63 marks a configuration packet, bit62 marks a keyframe.
type MediaReader struct {
	r io.Reader
}

func NewMediaReader(r io.Reader) *MediaReader {
	return &MediaReader{r: r}
}

// Next reads one packet. For raw mode (send_frame_meta=false) the caller
// should use io.ReadAll directly instead.
func (m *MediaReader) Next() (*MediaPacket, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(m.r, hdr[:]); err != nil {
		return nil, err
	}
	pts := binary.BigEndian.Uint64(hdr[0:8])
	length := binary.BigEndian.Uint32(hdr[8:12])
	if length > 64<<20 {
		return nil, errors.New("scrcpy: media packet too large")
	}
	data := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(m.r, data); err != nil {
			return nil, err
		}
	}
	return &MediaPacket{
		Config:   pts == ptsConfig,
		Keyframe: pts&ptsKeyframe != 0,
		PTS:      pts &^ (ptsConfig | ptsKeyframe),
		Data:     data,
	}, nil
}

// Codec IDs from the device metadata / audio stream header (big-endian u32).
const (
	CodecH264 uint32 = 0x68323634 // "h264"
	CodecH265 uint32 = 0x68323635 // "h265"
	CodecAV1  uint32 = 0x00617631 // "\0av1"

	AudioOpus uint32 = 0x6f707573 // "opus"
	AudioAAC  uint32 = 0x00616163 // "\0aac"
	AudioFLAC uint32 = 0x666c6163 // "flac"
	AudioRaw  uint32 = 0x00726177 // "\0raw"
)

func CodecName(id uint32) string {
	switch id {
	case CodecH264:
		return "h264"
	case CodecH265:
		return "h265"
	case CodecAV1:
		return "av1"
	default:
		return "unknown"
	}
}

func ReadAudioCodec(r io.Reader) (uint32, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, fmt.Errorf("scrcpy: read audio codec metadata: %w", err)
	}
	return binary.BigEndian.Uint32(hdr[:]), nil
}

func AudioCodecName(id uint32) string {
	switch id {
	case AudioOpus:
		return "opus"
	case AudioAAC:
		return "aac"
	case AudioFLAC:
		return "flac"
	case AudioRaw:
		return "raw"
	default:
		return "unknown"
	}
}
