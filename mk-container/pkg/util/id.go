package util

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a 24-byte random id encoded in hex.
func NewID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
