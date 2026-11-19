package adb

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

// Auth types sent as the arg0 of an AUTH packet.
const (
	AuthToken     uint32 = 1
	AuthSignature uint32 = 2
	AuthPublicKey uint32 = 3
)

// SHA-1 DigestInfo prefix (OID 1.3.14.3.2.26 + NULL + OCTET STRING len 20).
// ADB treats the 20-byte token as if it were already a SHA-1 digest.
var sha1DigestInfo = []byte{
	0x30, 0x21, 0x30, 0x09, 0x06, 0x05, 0x2b, 0x0e, 0x03, 0x02, 0x1a,
	0x05, 0x00, 0x04, 0x14,
}

// CredentialStore persists a PKCS#8 RSA-2048 private key and produces the two
// AUTH payloads adbd expects.
type CredentialStore struct {
	path string
	key  *rsa.PrivateKey
	name string
}

func NewCredentialStore(path, name string) *CredentialStore {
	if name == "" {
		name = "scrcpy-webrtc"
	}
	return &CredentialStore{path: path, name: name}
}

func (c *CredentialStore) load() (*rsa.PrivateKey, error) {
	if c.key != nil {
		return c.key, nil
	}
	if c.path != "" {
		if data, err := os.ReadFile(c.path); err == nil && len(data) > 0 {
			if key, err := x509.ParsePKCS8PrivateKey(data); err == nil {
				if rsaKey, ok := key.(*rsa.PrivateKey); ok {
					c.key = rsaKey
					return c.key, nil
				}
			}
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	c.key = key
	if c.path != "" {
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err == nil {
			_ = os.WriteFile(c.path, der, 0o600)
		}
	}
	return key, nil
}

// SignaturePayload signs the 20-byte token using PKCS#1 v1.5 with the token
// placed directly in the DigestInfo (no SHA-1 hashing of the token).
func (c *CredentialStore) SignaturePayload(token []byte) ([]byte, error) {
	key, err := c.load()
	if err != nil {
		return nil, err
	}
	if len(token) != 20 {
		return nil, fmt.Errorf("adb: auth token is %d bytes, want 20", len(token))
	}
	k := (key.N.BitLen() + 7) / 8
	di := append(append([]byte{}, sha1DigestInfo...), token...)
	if len(di)+3 > k {
		return nil, errors.New("adb: token too long for RSA block")
	}
	block := make([]byte, k)
	block[0], block[1] = 0x00, 0x01
	for i := 2; i < k-len(di)-1; i++ {
		block[i] = 0xff
	}
	block[k-len(di)-1] = 0x00
	copy(block[k-len(di):], di)

	m := new(big.Int).SetBytes(block)
	sig := new(big.Int).Exp(m, key.D, key.N)
	out := make([]byte, k)
	sig.FillBytes(out)
	return out, nil
}

// PublicKeyPayload builds the Android "mincrypt" public key blob, base64
// encodes it, appends " <name>" and a NUL terminator (not PEM).
func (c *CredentialStore) PublicKeyPayload() ([]byte, error) {
	key, err := c.load()
	if err != nil {
		return nil, err
	}
	blob, err := mincryptPublicKey(key.PublicKey)
	if err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(blob)
	out := []byte(encoded)
	if c.name != "" {
		out = append(out, ' ')
		out = append(out, c.name...)
	}
	out = append(out, 0x00)
	return out, nil
}

// mincryptPublicKey produces the 524-byte little-endian android_pubkey:
//
//	u32 modulusLengthInWords(=64) | i32 n0inv | 256B modulus n (LE) |
//	256B rr = R^2 mod n (LE) | u32 exponent
func mincryptPublicKey(pub rsa.PublicKey) ([]byte, error) {
	if pub.N.BitLen() != 2048 || pub.E != 65537 {
		return nil, fmt.Errorf("adb: unsupported RSA key (%d bits, e=%d)", pub.N.BitLen(), pub.E)
	}
	const size = 256
	blob := make([]byte, 4+4+size+size+4)

	// n, little-endian
	nLE := make([]byte, size)
	nBytes := pub.N.Bytes()
	for i := range nBytes {
		nLE[i] = nBytes[len(nBytes)-1-i]
	}
	copy(blob[8:8+size], nLE)

	binary.LittleEndian.PutUint32(blob[0:4], 64) // modulusLengthInWords

	// n0inv = -n^-1 mod 2^32
	n0 := uint32(0)
	for i := 0; i < 4; i++ {
		n0 |= uint32(nLE[i]) << (8 * i)
	}
	nInv := modInverse32(n0)
	binary.LittleEndian.PutUint32(blob[4:8], uint32(0)-nInv)

	// rr = 2^(2*bitlen) mod n, little-endian
	two := big.NewInt(2)
	exp := big.NewInt(int64(2 * pub.N.BitLen()))
	r2 := new(big.Int).Exp(two, exp, pub.N)
	rrLE := make([]byte, size)
	b := r2.Bytes()
	for i := range b {
		rrLE[i] = b[len(b)-1-i]
	}
	copy(blob[8+size:8+2*size], rrLE)

	binary.LittleEndian.PutUint32(blob[8+2*size:], uint32(pub.E))
	return blob, nil
}

// modInverse32 returns x^-1 mod 2^32 via Newton iteration.
func modInverse32(x uint32) uint32 {
	inv := uint32(1)
	for i := 0; i < 5; i++ {
		inv = inv * (2 - x*inv)
	}
	return inv
}
