package agent

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
)

func DetectLineType() string {
	return classifyLineType(readDMI(), detectVirt(), hasWirelessInterface(), hasPublicInterfaceAddress())
}

func classifyLineType(dmi dmiInfo, virt string, wireless, publicNIC bool) string {
	switch firstChassisType(dmi.chassisType) {
	case "3", "4", "6", "7", "8", "9", "10", "11", "13", "30", "31", "32":
		return "homelab"
	}
	if wireless {
		return "homelab"
	}
	if virt == "openvz" || virt == "lxc" || virt == "lxc-libvirt" {
		return "nat"
	}
	if strings.Contains(strings.ToLower(dmi.sysVendor), "openvz") {
		return "nat"
	}
	if publicNIC {
		return "kvm"
	}
	return ""
}

func firstChassisType(value string) string {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			return part
		}
	}
	return ""
}

func detectVirt() string {
	if out, err := exec.Command("systemd-detect-virt", "--container").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	if _, err := os.Stat("/proc/vz"); err == nil {
		return "openvz"
	}
	return ""
}

type dmiInfo struct {
	sysVendor   string
	productName string
	boardName   string
	chassisType string
}

func readDMI() dmiInfo {
	var info dmiInfo
	for _, entry := range []struct {
		field *string
		path  string
	}{
		{&info.sysVendor, "/sys/class/dmi/id/sys_vendor"},
		{&info.productName, "/sys/class/dmi/id/product_name"},
		{&info.boardName, "/sys/class/dmi/id/board_name"},
		{&info.chassisType, "/sys/class/dmi/id/chassis_type"},
	} {
		if data, err := os.ReadFile(entry.path); err == nil {
			*entry.field = strings.TrimSpace(string(data))
		}
	}
	return info
}

func hasWirelessInterface() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		if len(iface.Name) > 2 && strings.HasPrefix(iface.Name, "wl") {
			return true
		}
	}
	return false
}

func hasPublicInterfaceAddress() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				if peer, err := netip.ParseAddr(address.String()); err == nil {
					prefix = netip.PrefixFrom(peer, peer.BitLen())
				} else {
					continue
				}
			}
			if isDedicatedLineAddress(prefix.Addr()) {
				return true
			}
		}
	}
	return false
}

var dedicatedLineExclusions = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func isDedicatedLineAddress(ip netip.Addr) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return false
	}
	for _, excluded := range dedicatedLineExclusions {
		if excluded.Contains(ip) {
			return false
		}
	}
	return true
}
