package sshconnection

import (
	"testing"
	"time"
)

func TestResolveOverridesAndSafeCommand(t *testing.T) {
	v := Info{Status: "available", Hosts: []string{"2001:db8::1"}, Ports: []int{2222}, Users: []string{"detected"}, Host: "2001:db8::1", Port: 2222, User: "detected", Source: "discovered", SampledAt: time.Now()}
	got, err := Resolve(v, "", "", 22)
	if err != nil || got.Command != "ssh -p 2222 detected@2001:db8::1" {
		t.Fatal("empty display preference masks discovered port", got, err)
	}
	got, err = Resolve(v, "example.com", "saved_user", 22)
	if err != nil || got.Command != "ssh -p 22 saved_user@example.com" || got.Source != "override" {
		t.Fatal(got, err)
	}
	for _, host := range []string{"-oProxyCommand=bad", "example.com;id", "name$(id)"} {
		if _, err := Resolve(v, host, "", 22); err == nil {
			t.Fatal("unsafe command accepted", host)
		}
	}
	v.User = "user;id"
	if _, err := Resolve(v, "", "", 0); err == nil {
		t.Fatal("unsafe Agent observation accepted")
	}
}
