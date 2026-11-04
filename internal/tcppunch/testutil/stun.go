package testutil

import (
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/pion/stun/v4"
)

func STUNServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
			go func() {
				defer conn.Close()
				header := make([]byte, 20)
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				length := int(binary.BigEndian.Uint16(header[2:4]))
				if length > 2048 {
					return
				}
				request := &stun.Message{Raw: make([]byte, 20+length)}
				copy(request.Raw, header)
				if _, err := io.ReadFull(conn, request.Raw[20:]); err != nil || request.Decode() != nil || request.Type != stun.BindingRequest {
					return
				}
				remote := conn.RemoteAddr().(*net.TCPAddr)
				response, err := stun.Build(request, stun.BindingSuccess, &stun.XORMappedAddress{IP: remote.IP, Port: remote.Port}, stun.Fingerprint)
				if err != nil {
					return
				}
				if _, err := conn.Write(response.Raw); err == nil {
					_, _ = io.Copy(io.Discard, conn)
				}
			}()
		}
	}()
	return "stun:" + listener.Addr().String()
}
