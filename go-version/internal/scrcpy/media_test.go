package scrcpy

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestReadAudioCodec(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], AudioOpus)

	stream := bytes.NewBuffer(header[:])
	var frameHeader [12]byte
	binary.BigEndian.PutUint64(frameHeader[:8], ptsConfig)
	binary.BigEndian.PutUint32(frameHeader[8:], 2)
	stream.Write(frameHeader[:])
	stream.Write([]byte{0x01, 0x02})

	codec, err := ReadAudioCodec(stream)
	if err != nil {
		t.Fatal(err)
	}
	if codec != AudioOpus {
		t.Fatalf("ReadAudioCodec() = %#x, want %#x", codec, AudioOpus)
	}
	packet, err := NewMediaReader(stream).Next()
	if err != nil {
		t.Fatal(err)
	}
	if !packet.Config || !bytes.Equal(packet.Data, []byte{0x01, 0x02}) {
		t.Fatalf("audio packet after codec metadata = %#v", packet)
	}
}

func TestReadAudioCodecRejectsTruncatedHeader(t *testing.T) {
	_, err := ReadAudioCodec(bytes.NewReader([]byte{0x6f, 0x70}))
	if err == nil {
		t.Fatal("ReadAudioCodec() accepted a truncated header")
	}
}

func TestAudioCodecName(t *testing.T) {
	for id, want := range map[uint32]string{
		AudioOpus: "opus",
		AudioAAC:  "aac",
		AudioFLAC: "flac",
		AudioRaw:  "raw",
	} {
		if got := AudioCodecName(id); got != want {
			t.Errorf("AudioCodecName(%#x) = %q, want %q", id, got, want)
		}
	}
}
