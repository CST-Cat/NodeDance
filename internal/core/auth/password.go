package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// PasswordParameters is intentionally fixed for production so persisted hashes
// have a reviewable cost. The values fit within the Core's single-admin scope.
type PasswordParameters struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  int
	KeyLength   uint32
}

var DefaultPasswordParameters = PasswordParameters{
	MemoryKiB:   64 * 1024,
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}

const CurrentPasswordVersion = "argon2id-v1"

var ErrPasswordLength = errors.New("password must contain 12 to 1024 UTF-8 bytes")

func HashPassword(password string) (hash, salt []byte, err error) {
	if !validPasswordLength(password) {
		return nil, nil, ErrPasswordLength
	}
	params := DefaultPasswordParameters
	salt = make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	hash = argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyLength)
	return hash, salt, nil
}

func VerifyPassword(password string, expectedHash, salt []byte) bool {
	if !validPasswordLength(password) || len(expectedHash) != int(DefaultPasswordParameters.KeyLength) || len(salt) != DefaultPasswordParameters.SaltLength {
		return false
	}
	params := DefaultPasswordParameters
	candidate := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyLength)
	return subtle.ConstantTimeCompare(candidate, expectedHash) == 1
}

func VerifyPasswordVersion(version, password string, expectedHash, salt []byte) bool {
	if version != CurrentPasswordVersion {
		return false
	}
	return VerifyPassword(password, expectedHash, salt)
}

func validPasswordLength(password string) bool {
	return utf8.ValidString(password) && len(password) >= 12 && len(password) <= 1024
}
