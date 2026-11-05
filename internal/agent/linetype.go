package agent

import (
	"net"
	"os"
	"strings"
)

// DetectLineType classifies the node's line type at enrollment time: kvm for
// virtual machines, nat for provider container virtualization, homelab for
// desktop-form or wireless machines. An empty result means unknown; the
// operator can set the label on the node afterwards.
func DetectLineType() string {
	return classifyLineType(readDMI(), hasWirelessInterface(), isOpenVZ())
}

// classifyLineType is the pure decision core behind DetectLineType.
func classifyLineType(dmi dmiInfo, wireless, openvz bool) string {
	chassis := ""
	for _, part := range strings.Split(dmi.chassisType, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			chassis = part
			break
		}
	}
	markers := strings.ToLower(dmi.sysVendor + " " + dmi.productName + " " + dmi.boardName)
	virtual := strings.Contains(markers, "qemu") || strings.Contains(markers, "kvm") ||
		strings.Contains(markers, "vmware") || strings.Contains(markers, "virtualbox") ||
		strings.Contains(markers, "bochs") || strings.Contains(markers, "hyper-v") ||
		strings.Contains(markers, "xen") || strings.Contains(markers, "virtual machine") ||
		strings.Contains(markers, "amazon ec2") || strings.Contains(markers, "cloud compute")
	switch chassis {
	case "3", "4", "6", "7", "8", "9", "10", "11", "13", "30", "31", "32":
		return "homelab"
	}
	if wireless {
		return "homelab"
	}
	if virtual {
		return "kvm"
	}
	if openvz {
		return "nat"
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

func isOpenVZ() bool {
	if _, err := os.Stat("/proc/vz"); err == nil {
		return true
	}
	return false
}
