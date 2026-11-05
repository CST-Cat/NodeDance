package agent

import "testing"

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

func validDetectResult(value string) bool {
	return value == "" || value == "kvm" || value == "nat" || value == "homelab"
}
