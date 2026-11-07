package agent

import (
	"net/netip"
	"testing"
)

func TestClassifyLineType(t *testing.T) {
	for _, tt := range []struct {
		name      string
		dmi       dmiInfo
		virt      string
		wireless  bool
		publicNIC bool
		want      string
	}{
		{name: "public_ip_on_nic_is_kvm", dmi: dmiInfo{sysVendor: "QEMU", productName: "Standard PC"}, publicNIC: true, want: "kvm"},
		{name: "vm_without_public_ip_stays_unknown", dmi: dmiInfo{sysVendor: "QEMU", productName: "Standard PC (Q35 + ICH9, 2009)"}, want: ""},
		{name: "ec2_without_public_ip_stays_unknown", dmi: dmiInfo{sysVendor: "Amazon EC2", productName: "c7i.xlarge"}, want: ""},
		{name: "oracle_cloud_without_public_ip_stays_unknown", dmi: dmiInfo{sysVendor: "AMD", productName: "Cloud Compute"}, want: ""},
		{name: "openvz_is_nat", dmi: dmiInfo{sysVendor: "OpenVZ"}, virt: "openvz", want: "nat"},
		{name: "lxc_is_nat", virt: "lxc", want: "nat"},
		{name: "container_beats_public_nic", virt: "lxc", publicNIC: true, want: "nat"},
		{name: "desktop_chassis_is_homelab", dmi: dmiInfo{sysVendor: "ASUSTeK COMPUTER INC.", productName: "ProArt X670E", chassisType: "3"}, want: "homelab"},
		{name: "laptop_chassis_is_homelab", dmi: dmiInfo{sysVendor: "LENOVO", productName: "ThinkPad X1", chassisType: "10"}, want: "homelab"},
		{name: "wireless_is_homelab", dmi: dmiInfo{sysVendor: "QEMU", productName: "Standard PC"}, wireless: true, want: "homelab"},
		{name: "bare_metal_without_public_ip_stays_unknown", dmi: dmiInfo{sysVendor: "Supermicro", productName: "X12STH"}, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyLineType(tt.dmi, tt.virt, tt.wireless, tt.publicNIC); got != tt.want {
				t.Fatalf("classifyLineType(%+v, virt=%q, wireless=%t, publicNIC=%t) = %q, want %q", tt.dmi, tt.virt, tt.wireless, tt.publicNIC, got, tt.want)
			}
		})
	}
}

func TestDetectLineTypeRuns(t *testing.T) {
	if got := DetectLineType(); !validDetectResult(got) {
		t.Fatalf("DetectLineType returned %q", got)
	}
}

func TestIsDedicatedLineAddress(t *testing.T) {
	for _, tt := range []struct {
		address string
		want    bool
	}{
		{address: "203.0.113.5", want: true},
		{address: "100.128.0.1", want: true},
		{address: "2606:4700::1111", want: true},
		{address: "10.0.0.5", want: false},
		{address: "172.16.5.5", want: false},
		{address: "192.168.1.1", want: false},
		{address: "100.64.0.1", want: false},
		{address: "100.127.255.254", want: false},
		{address: "127.0.0.1", want: false},
		{address: "169.254.3.7", want: false},
		{address: "fe80::1", want: false},
		{address: "fc00::1", want: false},
		{address: "ff02::1", want: false},
	} {
		if got := isDedicatedLineAddress(netip.MustParseAddr(tt.address)); got != tt.want {
			t.Fatalf("isDedicatedLineAddress(%s) = %t, want %t", tt.address, got, tt.want)
		}
	}
}

func validDetectResult(value string) bool {
	return value == "" || value == "kvm" || value == "nat" || value == "homelab"
}
