package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

var ErrUsername = errors.New("username must contain 3 to 80 ASCII letters, digits, underscores or hyphens")

func ValidUsername(value string) bool {
	if len(value) < 3 || len(value) > 80 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// InitialCredentials generates each omitted field independently using the OS CSPRNG.
// Usernames have 128 random bits; passwords have 256 random bits.
func InitialCredentials(username, password string) (string, string, error) {
	if username == "" {
		var bytes [16]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return "", "", err
		}
		username = "nd_" + base64.RawURLEncoding.EncodeToString(bytes[:])
	}
	if !ValidUsername(username) {
		return "", "", ErrUsername
	}
	if password == "" {
		var err error
		password, _, err = NewToken()
		if err != nil {
			return "", "", err
		}
	}
	if !validPasswordLength(password) {
		return "", "", ErrPasswordLength
	}
	return username, password, nil
}
