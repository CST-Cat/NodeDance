package probes

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/agent/networkpolicy"
	"net"
	"net/url"
	"time"
)

// Echo requests use a random payload and identifier, and accept only a reply
// from the resolved target with the matching payload. DNS failures stay DNS
// failures rather than being counted as packet loss.
func (e *Executor) checkICMP(ctx context.Context, target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	addresses, err := networkpolicy.Resolver(e.settings()).LookupIPAddr(ctx, u.Hostname())
	if err != nil {
		return err
	}
	if len(addresses) == 0 {
		return errors.New("target has no address")
	}
	ip := addresses[0]
	preference := e.settings().PreferIPVersion
	forced, _ := ctx.Value(probeFamilyKey{}).(string)
	if forced != "" {
		preference = forced
		filtered := addresses[:0]
		for _, a := range addresses {
			if (forced == "4") == (a.IP.To4() != nil) {
				filtered = append(filtered, a)
			}
		}
		if len(filtered) == 0 {
			return &net.DNSError{Err: "address family unavailable", Name: u.Hostname(), IsNotFound: true}
		}
		addresses = filtered
		ip = addresses[0]
	}
	for _, addr := range addresses {
		if (preference == "4" && addr.IP.To4() != nil) || (preference == "6" && addr.IP.To4() == nil) {
			ip = addr
			break
		}
	}
	network, listen, requestType, replyType := "ip4:icmp", "0.0.0.0", byte(8), byte(0)
	if ip.IP.To4() == nil {
		network, listen, requestType, replyType = "ip6:ipv6-icmp", "::", 128, 129
	}
	conn, err := net.ListenPacket(network, listen)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	packet := make([]byte, 24)
	packet[0] = requestType
	if _, err = rand.Read(packet[4:]); err != nil {
		return err
	}
	packet[6] = 0
	packet[7] = 1
	if requestType == 8 {
		binary.BigEndian.PutUint16(packet[2:4], icmpChecksum(packet))
	}
	destination := &net.IPAddr{IP: ip.IP, Zone: ip.Zone}
	if _, err = conn.WriteTo(packet, destination); err != nil {
		return err
	}
	buffer := make([]byte, 1500)
	for {
		n, source, err := conn.ReadFrom(buffer)
		if err != nil {
			return err
		}
		addr, ok := source.(*net.IPAddr)
		if !ok || !addr.IP.Equal(ip.IP) {
			continue
		}
		reply := buffer[:n]
		if len(reply) >= 20 && reply[0]>>4 == 4 {
			header := int(reply[0]&15) * 4
			if header > len(reply) {
				continue
			}
			reply = reply[header:]
		}
		if len(reply) < len(packet) || reply[0] != replyType || reply[1] != 0 {
			continue
		}
		if bytes.Equal(reply[4:len(packet)], packet[4:]) {
			return nil
		}
	}
}
func icmpChecksum(data []byte) uint16 {
	sum := uint32(0)
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) > 0 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 65535) + (sum >> 16)
	}
	return ^uint16(sum)
}
