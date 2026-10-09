package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateFileRootCommandUsesAgentPathPolicy(t *testing.T) {
	base := t.TempDir()
	valid := filepath.Join(base, "valid")
	if err := os.Mkdir(valid, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(base, "symlink")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "existing directory", path: valid},
		{name: "missing directory", path: filepath.Join(base, "missing"), wantErr: true},
		{name: "symlink directory", path: symlink, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), []string{"validate-file-root", "--file-root", test.path}, nil, &stdout, &stderr)
			if (err != nil) != test.wantErr {
				t.Fatalf("validate-file-root error=%v, wantErr=%v", err, test.wantErr)
			}
			if test.wantErr && stdout.Len() != 0 {
				t.Fatalf("invalid root produced success output %q", stdout.String())
			}
			if !test.wantErr && stdout.String() != "Agent file root is valid\n" {
				t.Fatalf("valid root output=%q", stdout.String())
			}
		})
	}
}
