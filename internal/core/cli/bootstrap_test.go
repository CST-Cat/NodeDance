package cli

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestBootstrapTerminalPrompts(t *testing.T) {
	for _, input := range [][2]string{{"", ""}, {"custom-admin", "hidden-strong-password"}} {
		t.Run(input[0], func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			type result struct {
				username, password string
				err                error
			}
			done := make(chan result, 1)
			go func() { u, p, e := promptInitialAdministrator(slave, slave); done <- result{u, p, e} }()
			chunks := make(chan string, 100)
			go func() {
				buffer := make([]byte, 1024)
				for {
					n, e := master.Read(buffer)
					if n > 0 {
						chunks <- string(buffer[:n])
					}
					if e != nil {
						return
					}
				}
			}()
			var transcript strings.Builder
			await := func(text string) {
				t.Helper()
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				for {
					select {
					case chunk := <-chunks:
						transcript.WriteString(chunk)
						if strings.Contains(transcript.String(), text) {
							return
						}
					case <-timer.C:
						t.Fatalf("missing prompt %q", text)
					}
				}
			}
			await("Administrator username")
			io.WriteString(master, input[0]+"\r")
			await("Administrator password")
			io.WriteString(master, input[1]+"\r")
			select {
			case result := <-done:
				if result.err != nil || result.username != input[0] || result.password != input[1] {
					t.Fatal("incorrect prompt result")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("prompt did not finish")
			}
			if input[1] != "" && strings.Contains(transcript.String(), input[1]) {
				t.Fatal("password echoed")
			}
		})
	}
}
func TestBootstrapWithoutTerminalDoesNotReadStdin(t *testing.T) {
	username, password, err := promptInitialAdministrator(strings.NewReader("ignored"), io.Discard)
	if err != nil || username != "" || password != "" {
		t.Fatal("unattended startup did not select random generation")
	}
}
