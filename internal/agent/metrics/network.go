package metrics

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

type networkCounter struct {
	rx       uint64
	tx       uint64
	at       time.Time
	identity string
}

type interfaceFacts struct {
	up       bool
	loopback bool
}

type linkMetadata struct {
	ifindex    int
	iflink     int
	master     string
	kind       string
	lowerLinks []string
	upperLinks []string
}

func classifyInterface(name string, facts interfaceFacts, factsOK bool, link linkMetadata, linkErr error, masterKind string) (bool, string, bool) {
	lowerName := strings.ToLower(name)
	if lowerName == "lo" || facts.loopback || hasPrefix(lowerName, "lo:") {
		return false, "loopback", true
	}
	if hasPrefix(lowerName, "veth", "docker", "br-", "br0", "virbr", "cni", "flannel", "podman", "tun", "tap", "wg", "tailscale", "ifb", "dummy") {
		return false, "virtual_interface", true
	}
	if !factsOK {
		return false, "interface_metadata_unavailable", false
	}
	if !facts.up {
		return false, "interface_down", true
	}
	if linkErr != nil {
		return false, "topology_unavailable", false
	}
	if link.kind == "bridge" {
		return false, "bridge", true
	}
	if link.kind == "veth" || link.kind == "tunnel" {
		return false, "virtual_interface", true
	}
	if link.kind == "bond" {
		return true, "", true
	}
	if link.ifindex > 0 && link.iflink > 0 && link.ifindex != link.iflink {
		return false, "lower_or_virtual_interface", true
	}
	if len(link.lowerLinks) > 0 {
		return false, "upper_or_virtual_interface", true
	}
	if link.master != "" && masterKind != "bridge" {
		return false, "enslaved_interface", true
	}
	return true, "", true
}

func hasPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func linkKindForPath(name string, ifindex, iflink int, bridge, bond, tun bool) string {
	switch {
	case bridge:
		return "bridge"
	case bond:
		return "bond"
	case tun:
		return "tunnel"
	case ifindex > 0 && iflink > 0 && ifindex != iflink:
		return "veth"
	default:
		return ""
	}
}

func gopsutilInterfaceFacts(iface net.InterfaceStat) interfaceFacts {
	return interfaceFacts{
		up:       slices.Contains(iface.Flags, "up"),
		loopback: slices.Contains(iface.Flags, "loopback"),
	}
}

func stableInterfaceIdentity(iface net.InterfaceStat, link linkMetadata) (string, string) {
	index := iface.Index
	if link.ifindex > 0 {
		if index > 0 && index != link.ifindex {
			return "", "interface_identity_mismatch"
		}
		index = link.ifindex
	}
	mac := strings.ToLower(strings.TrimSpace(iface.HardwareAddr))
	if index <= 0 {
		return "", "interface_identity_unavailable"
	}
	identity := strconv.Itoa(index)
	if mac != "" {
		identity += "/" + mac
	}
	return identity, ""
}

func masterName(path string) string {
	return filepath.Base(path)
}
