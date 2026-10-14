package sshdiscovery

import (
	"github.com/CST-Cat/NodeDance/internal/sshconnection"
	"testing"
	"time"
)

const accounts = "root:x:0:0:root:/root:/bin/bash\nnodedance:x:1000:1000:user:/home/nodedance:/bin/bash\nservice:x:995:995:service:/var/lib/service:/bin/bash\nnobody:x:65534:65534:nobody:/:/usr/sbin/nologin\n"

func blank() sshconnection.Info {
	return sshconnection.Info{Status: "unavailable", Reason: "ssh_service_unavailable", Source: "discovered", SampledAt: time.Now(), Hosts: []string{}, Ports: []int{}, Users: []string{}}
}
func TestDiscoveryWithoutSavedConnectionOrConversation(t *testing.T) {
	sockets := `LISTEN 0 128 0.0.0.0:2222 0.0.0.0:* users:(("sshd",pid=100,fd=3))`
	v := observed(blank(), "permitrootlogin no\n", sockets, "", []string{"192.168.1.4", "100.111.176.125"}, accounts, "", nil)
	if v.Command != "ssh -p 2222 nodedance@100.111.176.125" || len(v.Users) != 1 || v.Source != "discovered" {
		t.Fatalf("failed automatic discovery: %+v", v)
	}
	// Socket activation can override the sshd_config port; do not invent 22.
	v = observed(blank(), "port 22\npermitrootlogin no\n", `LISTEN 0 128 0.0.0.0:2200 0.0.0.0:* users:(("systemd",pid=1,fd=3))`, "ActiveState=active\nListen=0.0.0.0:2200 (Stream)\n", []string{"203.0.113.1"}, accounts, "", nil)
	if v.Port != 2200 || v.Status != "available" {
		t.Fatalf("socket activation lost: %+v", v)
	}
	v = observed(blank(), "port 22\n", `LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("other-service",pid=3,fd=3))`, "", []string{"203.0.113.1"}, accounts, "", nil)
	if v.Command != "" || v.Status != "unavailable" {
		t.Fatal("non-SSH service guessed as SSH")
	}
}
func TestAccountsSessionsAndBoundEndpoints(t *testing.T) {
	sockets := `LISTEN 0 128 192.168.1.4:22 0.0.0.0:* users:(("sshd",pid=100,fd=3))
LISTEN 0 128 100.111.176.125:2222 0.0.0.0:* users:(("sshd",pid=100,fd=4))`
	extra := accounts + "other:x:1001:1001:other:/home/other:/bin/bash\n"
	v := observed(blank(), "permitrootlogin yes\n", sockets, "", []string{"192.168.1.4", "100.111.176.125"}, extra, "", []string{"other"})
	if v.User != "other" || v.Port != 2222 || v.Host != "100.111.176.125" {
		t.Fatalf("session or bound port lost: %+v", v)
	}
	v = observed(blank(), "permitrootlogin no\nallowgroups sshusers\ndenyusers other\n", sockets, "", []string{"192.168.1.4"}, extra, "sshusers:x:1000:\n", nil)
	if v.User != "nodedance" {
		t.Fatalf("login restrictions ignored: %+v", v)
	}
	v = observed(blank(), "permitrootlogin no\n", sockets, "", []string{"192.168.1.4"}, extra, "", nil)
	if v.User != "" || v.Command != "ssh -p 22 192.168.1.4" {
		t.Fatal("ambiguous login was invented", v)
	}
	for _, name := range []string{"docker0", "br-a", "veth1", "cni0"} {
		if !internalInterface(name) {
			t.Fatal("internal address accepted", name)
		}
	}
	if internalInterface("tailscale0") || internalInterface("eth0") {
		t.Fatal("reachable host interfaces excluded")
	}
}

func TestForwardedSSHSocketsAreNotServerListeners(t *testing.T) {
	sockets := `LISTEN 0 128 0.0.0.0:1000 0.0.0.0:* users:(("sshd",pid=200,fd=6))
LISTEN 0 128 0.0.0.0:2222 0.0.0.0:* users:(("sshd",pid=100,fd=3))`
	filtered := filterSessionSockets(sockets, map[int]bool{200: true})
	v := observed(blank(), "permitrootlogin no\n", filtered, "", []string{"203.0.113.1"}, accounts, "", nil)
	if v.Port != 2222 {
		t.Fatal("SSH remote forwarding was mistaken for daemon port", v)
	}
}

func TestExplicitAndDistributionLoginAccounts(t *testing.T) {
	sockets := `LISTEN 0 128 0.0.0.0:2222 0.0.0.0:* users:(("sshd",pid=100,fd=3))`
	v := observed(blank(), "permitrootlogin no\nallowusers service\n", sockets, "", []string{"203.0.113.1"}, accounts, "", nil)
	if v.User != "service" {
		t.Fatal("explicit SSH account ignored due to UID classification", v)
	}
	v = observed(blank(), "permitrootlogin no\n", sockets, "", []string{"203.0.113.1"}, "login:x:500:500:user:/home/login:/bin/bash\n", "", nil, 500)
	if v.User != "login" {
		t.Fatal("machine UID_MIN ignored", v)
	}
}

func TestOnlyRootDaemonOwnsDiscoveredSSHPort(t *testing.T) {
	sockets := `LISTEN 0 128 0.0.0.0:1000 0.0.0.0:* users:(("sshd",pid=200,fd=6))
LISTEN 0 128 0.0.0.0:2222 0.0.0.0:* users:(("sshd",pid=100,fd=3))`
	filtered := filterDaemonSockets(sockets, map[int]bool{100: true})
	v := observed(blank(), "permitrootlogin no\n", filtered, "", []string{"203.0.113.1"}, accounts, "", nil)
	if v.Port != 2222 {
		t.Fatal("unprivileged process name spoofed SSH service", v)
	}
}

func TestSocketActivationWildcardPresentation(t *testing.T) {
	v := observed(blank(), "permitrootlogin no\n", `LISTEN 0 128 *:2200 *:* users:(("systemd",pid=1,fd=3))`, "ActiveState=active\nListen=[::]:2200 (Stream)\n", []string{"100.64.0.8"}, accounts, "", nil)
	if v.Port != 2200 || v.Host != "100.64.0.8" {
		t.Fatal("active SSH socket was missed", v)
	}
}
