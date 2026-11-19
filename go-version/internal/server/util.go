package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
)

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func tokenValid(provided, expected string) bool {
	if expected == "" {
		return true
	}
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
