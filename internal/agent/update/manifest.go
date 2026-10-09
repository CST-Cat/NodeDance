// Package update verifies signed Agent releases and stages them for the
// separately supervised update helper.
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const MaxArtifactBytes = 128 << 20

var (
	versionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+]([0-9A-Za-z.-]+))?$`)
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// TrustedPublicKeyBase64 is set by the release build with -ldflags. The
	// repository intentionally contains no private release key or test key.
	TrustedPublicKeyBase64 string
)

type Manifest struct {
	FormatVersion int    `json:"formatVersion"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Architecture  string `json:"architecture"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	MinProtocol   int    `json:"minProtocol"`
	MaxProtocol   int    `json:"maxProtocol"`
	CoreMin       string `json:"coreMinVersion,omitempty"`
	CoreMax       string `json:"coreMaxVersion,omitempty"`
	Signature     string `json:"signature"`
}

type signedFields struct {
	FormatVersion int    `json:"formatVersion"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Architecture  string `json:"architecture"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	MinProtocol   int    `json:"minProtocol"`
	MaxProtocol   int    `json:"maxProtocol"`
	CoreMin       string `json:"coreMinVersion,omitempty"`
	CoreMax       string `json:"coreMaxVersion,omitempty"`
}

func (m Manifest) signingBytes() ([]byte, error) {
	return json.Marshal(signedFields{m.FormatVersion, m.Version, m.OS, m.Architecture, m.SHA256, m.Size,
		m.MinProtocol, m.MaxProtocol, m.CoreMin, m.CoreMax})
}

func (m Manifest) Validate() error {
	if m.FormatVersion != 1 || !validVersion(m.Version) {
		return errors.New("release manifest version or format is invalid")
	}
	if m.OS != "linux" || (m.Architecture != "amd64" && m.Architecture != "arm64") {
		return errors.New("release manifest must target linux/amd64 or linux/arm64")
	}
	if !digestPattern.MatchString(m.SHA256) || m.Size < 1 || m.Size > MaxArtifactBytes {
		return errors.New("release manifest digest or artifact size is invalid")
	}
	if m.MinProtocol < 1 || m.MaxProtocol < m.MinProtocol || m.MaxProtocol > 1000 {
		return errors.New("release manifest protocol range is invalid")
	}
	if m.CoreMin != "" && !validVersion(m.CoreMin) || m.CoreMax != "" && !validVersion(m.CoreMax) {
		return errors.New("release manifest Core version range is invalid")
	}
	if m.CoreMin != "" && m.CoreMax != "" && compareVersion(m.CoreMin, m.CoreMax) > 0 {
		return errors.New("release manifest Core version range is reversed")
	}
	signature, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("release manifest signature is malformed")
	}
	return nil
}

func (m Manifest) Verify(publicKey ed25519.PublicKey) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("Agent update trust key is not configured")
	}
	signature, _ := base64.StdEncoding.DecodeString(m.Signature)
	data, err := m.signingBytes()
	if err != nil || !ed25519.Verify(publicKey, data, signature) {
		return errors.New("release manifest signature verification failed")
	}
	return nil
}

func Sign(m Manifest, privateKey ed25519.PrivateKey) (Manifest, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Manifest{}, errors.New("release signing key is invalid")
	}
	m.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	data, err := m.signingBytes()
	if err != nil {
		return Manifest{}, err
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data))
	return m, nil
}

func PublicKeyFromBase64(encoded string) (ed25519.PublicKey, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("Agent update trust key is not configured or malformed")
	}
	return ed25519.PublicKey(key), nil
}

func RuntimeCompatibility(m Manifest, currentAgent, coreVersion string, protocol int) error {
	if m.OS != runtime.GOOS || m.Architecture != runtime.GOARCH {
		return fmt.Errorf("release architecture %s/%s does not match this Agent", m.OS, m.Architecture)
	}
	if protocol < m.MinProtocol || protocol > m.MaxProtocol {
		return errors.New("release does not support the negotiated Agent protocol")
	}
	if m.CoreMin != "" && (!validVersion(coreVersion) || compareVersion(coreVersion, m.CoreMin) < 0) {
		return errors.New("release requires a newer Core version")
	}
	if m.CoreMax != "" && (!validVersion(coreVersion) || compareVersion(coreVersion, m.CoreMax) > 0) {
		return errors.New("release does not support this Core version")
	}
	if validVersion(currentAgent) && compareVersion(m.Version, currentAgent) <= 0 {
		return errors.New("release must be newer than the installed Agent")
	}
	return nil
}

func validVersion(value string) bool { return versionPattern.MatchString(value) }

func compareVersion(left, right string) int {
	left = strings.TrimPrefix(left, "v")
	right = strings.TrimPrefix(right, "v")
	left, _, _ = strings.Cut(left, "+")
	right, _, _ = strings.Cut(right, "+")
	leftCore, leftPre, leftHasPre := strings.Cut(left, "-")
	rightCore, rightPre, rightHasPre := strings.Cut(right, "-")
	leftParts := strings.Split(leftCore, ".")
	rightParts := strings.Split(rightCore, ".")
	for i := 0; i < 3; i++ {
		l, _ := strconv.Atoi(leftParts[i])
		r, _ := strconv.Atoi(rightParts[i])
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	if leftHasPre != rightHasPre {
		if leftHasPre {
			return -1
		}
		return 1
	}
	if leftHasPre {
		leftIDs, rightIDs := strings.Split(leftPre, "."), strings.Split(rightPre, ".")
		for i := 0; i < len(leftIDs) && i < len(rightIDs); i++ {
			l, le := strconv.Atoi(leftIDs[i])
			r, re := strconv.Atoi(rightIDs[i])
			if le == nil && re == nil {
				if l < r {
					return -1
				}
				if l > r {
					return 1
				}
				continue
			}
			if le == nil && re != nil {
				return -1
			}
			if le != nil && re == nil {
				return 1
			}
			if leftIDs[i] < rightIDs[i] {
				return -1
			}
			if leftIDs[i] > rightIDs[i] {
				return 1
			}
		}
		if len(leftIDs) < len(rightIDs) {
			return -1
		}
		if len(leftIDs) > len(rightIDs) {
			return 1
		}
	}
	return 0
}

func Digest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
