package tailscale

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

type recordingRemote struct {
	command string
	input   []byte
	output  []byte
	err     error
	calls   int
}

func (r *recordingRemote) Run(_ context.Context, command string, input []byte) ([]byte, error) {
	r.command, r.input, r.calls = command, append([]byte(nil), input...), r.calls+1
	return append([]byte(nil), r.output...), r.err
}
func (*recordingRemote) Close() error { return nil }

func encodeBase64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

func mustPublicKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public
}
