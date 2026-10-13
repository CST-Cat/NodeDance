package tailscale

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"golang.org/x/crypto/ssh"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestSSHCancelInterruptsBlockedInstallInput(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			stream, requests, err := channel.Accept()
			if err != nil {
				return
			}
			go func() {
				for request := range requests {
					request.Reply(true, nil)

				}
			}()
			// Consume one byte to prove Start completed and stdin transmission began,
			// then stop reading so the remaining 8 MiB exceeds the SSH window.
			go func() {
				var one [1]byte
				if _, err := stream.Read(one[:]); err == nil {
					close(started)
				}
			}()
		}
	}()
	host, rawPort, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	remote, err := connectSSHOnPort(context.Background(), Peer{Identity: "fixture", Class: "linux", Online: true, IPs: []string{host}}, SSHCredentials{User: "root", Password: "fixture"}, ssh.FingerprintSHA256(signer.PublicKey()), NewStateStore(t.TempDir()), false, port)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := remote.Run(ctx, "fixture", make([]byte, 8<<20)); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("SSH command did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatal("uncertain send lost its classification", err)
		}
	case <-time.After(time.Second):
		remote.Close()
		<-done
		t.Fatal("SSH input ignored cancellation")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("SSH server did not stop")
	}
}
