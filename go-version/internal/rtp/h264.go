// Package rtp implements the RTP packetization needed by the WebRTC media
// tracks, ported from the werift-based implementation in webrtc.mjs.
package rtp

import (
	"github.com/pion/rtp"
)

const (
	// VideoPT and AudioPT are the fixed RTP payload types used in the offer.
	VideoPT byte = 96
	AudioPT byte = 111
	RTPMTU       = 1200
)

// StreamState tracks SSRC/sequence for one RTP stream.
type StreamState struct {
	Seq  uint16
	SSRC uint32
}

func NewStreamState() *StreamState {
	return &StreamState{SSRC: randomU32()}
}

// Packet builds one RTP packet for a payload.
func (s *StreamState) Packet(pt byte, payload []byte, timestamp uint32, marker bool) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    pt,
			SequenceNumber: s.Seq,
			Timestamp:      timestamp,
			SSRC:           s.SSRC,
			Marker:         marker,
		},
		Payload: payload,
	}
	s.Seq++
	return p
}

// H264Packetizer implements RFC 6184 single-NAL / FU-A packetization from
// Annex-B access units.
type H264Packetizer struct {
	StreamState
	configNalus [][]byte
}

func NewH264Packetizer() *H264Packetizer {
	return &H264Packetizer{StreamState: *NewStreamState()}
}

func (h *H264Packetizer) SetConfig(data []byte) {
	h.configNalus = splitAnnexB(data)
}

// Packetize returns the RTP packets for one access unit.
func (h *H264Packetizer) Packetize(data []byte, timestamp uint32, keyframe bool) []*rtp.Packet {
	nalus := splitAnnexB(data)
	if keyframe && len(h.configNalus) > 0 {
		nalus = append(append([][]byte{}, h.configNalus...), nalus...)
	}
	var packets []*rtp.Packet
	for i, nalu := range nalus {
		last := i == len(nalus)-1
		if len(nalu) <= RTPMTU {
			packets = append(packets, h.Packet(VideoPT, nalu, timestamp, last))
			continue
		}
		indicator := (nalu[0] & 0xe0) | 28
		nalType := nalu[0] & 0x1f
		for offset := 1; offset < len(nalu); {
			end := offset + RTPMTU - 2
			if end > len(nalu) {
				end = len(nalu)
			}
			start := offset == 1
			fin := end == len(nalu)
			var fuHeader byte
			if start {
				fuHeader |= 0x80
			}
			if fin {
				fuHeader |= 0x40
			}
			fuHeader |= nalType
			payload := make([]byte, 0, 2+(end-offset))
			payload = append(payload, indicator, fuHeader)
			payload = append(payload, nalu[offset:end]...)
			packets = append(packets, h.Packet(VideoPT, payload, timestamp, last && fin))
			offset = end
		}
	}
	return packets
}

// splitAnnexB splits 00 00 01 / 00 00 00 01 delimited NAL units.
func splitAnnexB(data []byte) [][]byte {
	var nalus [][]byte
	start := -1
	for i := 0; i+2 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			if start != -1 {
				end := i
				if i > 0 && data[i-1] == 0 {
					end = i - 1
				}
				nalus = append(nalus, data[start:end])
			}
			start = i + 3
			i += 2
		}
	}
	if start != -1 {
		nalus = append(nalus, data[start:])
	}
	return nalus
}
