package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func NewToken() (raw string, digest []byte, err error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(buffer)
	digest = DigestToken(raw)
	return raw, digest, nil
}

func DigestToken(raw string) []byte {
	digest := sha256.Sum256([]byte(raw))
	return digest[:]
}
