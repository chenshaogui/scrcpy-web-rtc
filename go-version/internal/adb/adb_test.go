package adb

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("host::features=cmd")
	want := Packet{Command: CmdConnect, Arg0: 0x01000001, Arg1: 1024 * 1024, Payload: payload}
	if err := writePacket(&buf, want, initialVersion, false); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != headerSize+len(payload) {
		t.Fatalf("serialized size = %d", buf.Len())
	}
	got, err := readPacket(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != want.Command || got.Arg0 != want.Arg0 || got.Arg1 != want.Arg1 {
		t.Errorf("packet mismatch: %+v vs %+v", got, want)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("payload mismatch: %q", got.Payload)
	}
	if got.Magic != (got.Command ^ 0xffffffff) {
		t.Errorf("magic mismatch: %#x", got.Magic)
	}
}

func TestCommandWireBytes(t *testing.T) {
	// ADB commands are stored as the integer whose LE encoding is the ASCII.
	var buf bytes.Buffer
	_ = writePacket(&buf, Packet{Command: CmdAuth}, initialVersion, true)
	if !bytes.Equal(buf.Bytes()[:4], []byte("AUTH")) {
		t.Errorf("AUTH wire bytes = % x, want AUTH", buf.Bytes()[:4])
	}
}

func TestChecksum(t *testing.T) {
	if got := checksum([]byte{1, 2, 3}); got != 6 {
		t.Errorf("checksum = %d, want 6", got)
	}
}

func TestMincryptPublicKeyLayout(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := mincryptPublicKey(key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 524 {
		t.Fatalf("public key size = %d, want 524", len(blob))
	}
	// First u32 LE must be modulusLengthInWords = 64, last u32 LE the exponent.
	if got := binary.LittleEndian.Uint32(blob[0:4]); got != 64 {
		t.Errorf("modulusLengthInWords = %d, want 64", got)
	}
	if exp := binary.LittleEndian.Uint32(blob[520:524]); exp != 65537 {
		t.Errorf("exponent = %d, want 65537", exp)
	}
}

func TestSignaturePayloadLength(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cred := &CredentialStore{key: key, name: "test"}
	token := bytes.Repeat([]byte{0xab}, 20)
	sig, err := cred.SignaturePayload(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 256 {
		t.Fatalf("signature length = %d, want 256", len(sig))
	}
}
