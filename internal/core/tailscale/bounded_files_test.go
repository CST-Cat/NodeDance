package tailscale

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryArtifactsRejectsUnsafeSignatureFiles(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	arch := filepath.Join(dir, "linux-arm64")
	if err := os.Mkdir(arch, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(arch, "nodedance-agent")
	binary := []byte("signed fixture")
	if err := os.WriteFile(path, binary, 0600); err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(private, binary)
	loader := DirectoryArtifacts{Directory: dir, PublicKeyBase64: base64.StdEncoding.EncodeToString(public)}
	for _, encoded := range [][]byte{signature, []byte(base64.StdEncoding.EncodeToString(signature))} {
		if err := os.WriteFile(path+".sig", encoded, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loader.Load("linux", "arm64"); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"oversized", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			os.Remove(path + ".sig")
			var err error
			switch kind {
			case "oversized":
				err = os.WriteFile(path+".sig", []byte(strings.Repeat(" ", 5000)+base64.StdEncoding.EncodeToString(signature)), 0600)
			case "symlink":
				err = os.Symlink(path, path+".sig")
			case "fifo":
				err = unix.Mkfifo(path+".sig", 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load("linux", "arm64"); err == nil {
				t.Fatal("unsafe signature accepted")
			}
		})
	}
}

func TestDeploymentStateBoundsAreSymmetric(t *testing.T) {
	store := NewStateStore(t.TempDir())
	oversized := State{Pins: map[string]string{"peer": strings.Repeat("x", maxDeploymentStateBytes)}}
	if err := store.Save(oversized); err == nil {
		t.Fatal("oversized state saved")
	}
	if err := store.Pin("peer", "SHA256:fixture"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read()
	if err != nil || state.Pins["peer"] != "SHA256:fixture" {
		t.Fatal(state, err)
	}
	if err := os.WriteFile(store.path, []byte(strings.Repeat(" ", maxDeploymentStateBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(); err == nil {
		t.Fatal("oversized state read")
	}
}
