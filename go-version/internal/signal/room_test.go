package signal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestAESGCMEncryption(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"t":"rtc-offer","sdp":"v=0..."}`)
	nonce := bytes.Repeat([]byte{0x01}, 12)
	sealed := gcm.Seal(append([]byte(nil), nonce...), nonce, plaintext, nil)
	if len(sealed) != 12+len(plaintext)+16 {
		t.Fatalf("sealed length = %d", len(sealed))
	}
	// Room.recv requires at least 29 bytes (12 + 16 + 1).
	if len(sealed) < 29 {
		t.Fatal("sealed payload shorter than room minimum")
	}
	opened, err := gcm.Open(nil, sealed[:12], sealed[12:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Errorf("round trip mismatch: %q", opened)
	}
}

func TestB64uRoundTrip(t *testing.T) {
	data := []byte{0xfb, 0xff, 0x00, 0x10}
	encoded := b64uEncode(data)
	decoded, err := b64uDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Errorf("b64u mismatch: % x vs % x", decoded, data)
	}
	if bytes.ContainsAny([]byte(encoded), "+/=") {
		t.Errorf("b64u contains non-URL-safe chars: %s", encoded)
	}
}

func TestMQTTPacketLengthEncoding(t *testing.T) {
	// A body of 200 bytes needs a 2-byte remaining length: 200 % 128 = 72 (+0x80), 1.
	pkt := mqttPacket(0x30, make([]byte, 200))
	if pkt[0] != 0x30 {
		t.Fatalf("type = %#x", pkt[0])
	}
	if pkt[1] != 0xc8 || pkt[2] != 0x01 {
		t.Errorf("length bytes = %#x %#x, want c8 01", pkt[1], pkt[2])
	}
	if len(pkt) != 3+200 {
		t.Errorf("packet length = %d", len(pkt))
	}
}
