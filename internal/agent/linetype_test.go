package agent

import "testing"

func TestClassifyLineType(t *testing.T) {
	for _, tt := range []struct {
		name     string
		dmi      dmiInfo
		wireless bool
		openvz   bool
		want     string
	}{
		{name: "qemu_vm", dmi: dmiInfo{sysVendor: "QEMU", productName: "Standard PC (Q35 + ICH9, 2009)"}, want: "kvm"},
		{name: "amazon_ec2", dmi: dmiInfo{sysVendor: "Amazon EC2", productName: "c7i.xlarge"}, want: "kvm"},
		{name: "vmware", dmi: dmiInfo{sysVendor: "VMware, Inc.", productName: "VMware Virtual Platform"}, want: "kvm"},
		{name: "oracle_cloud", dmi: dmiInfo{sysVendor: "AMD", productName: "Cloud Compute"}, want: "kvm"},
		{name: "desktop_chassis", dmi: dmiInfo{sysVendor: "ASUSTeK COMPUTER INC.", productName: "ProArt X670E", chassisType: "3"}, want: "homelab"},
		{name: "laptop_chassis", dmi: dmiInfo{sysVendor: "LENOVO", productName: "ThinkPad X1", chassisType: "10"}, want: "homelab"},
		{name: "wireless_override", dmi: dmiInfo{sysVendor: "QEMU", productName: "Standard PC"}, wireless: true, want: "homelab"},
		{name: "openvz_provider", dmi: dmiInfo{sysVendor: "OpenVZ"}, openvz: true, want: "nat"},
		{name: "unknown_bare_metal", dmi: dmiInfo{sysVendor: "Supermicro", productName: "X12STH"}, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyLineType(tt.dmi, tt.wireless, tt.openvz); got != tt.want {
				t.Fatalf("classifyLineType(%+v, wireless=%t, openvz=%t) = %q, want %q", tt.dmi, tt.wireless, tt.openvz, got, tt.want)
			}
		})
	}
}

func TestDetectLineTypeRuns(t *testing.T) {
	if got := DetectLineType(); !validDetectResult(got) {
		t.Fatalf("DetectLineType returned %q", got)
	}
}

func validDetectResult(value string) bool {
	return value == "" || value == "kvm" || value == "nat" || value == "homelab"
}
