package sshconnection

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

func ValidHost(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 255 || strings.TrimSpace(value) != value || strings.HasPrefix(value, "-") ||
		strings.ContainsAny(value, "\x00\r\n\t /@\\'\";|&$`(){}[]<>!") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	if strings.Contains(value, ":") {
		address, zone, hasZone := strings.Cut(value, "%")
		if !hasZone || zone == "" || strings.Contains(zone, "%") || net.ParseIP(address) == nil {
			return false
		}
		for _, char := range zone {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '.' || char == '-') {
				return false
			}
		}
		return true
	}
	if len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}

func ValidUser(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 || strings.HasPrefix(value, "-") {
		return false
	}
	for index, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char == '_' ||
			index > 0 && (char >= '0' && char <= '9' || char == '.' || char == '-')) {
			return false
		}
	}
	return true
}

type Info struct {
	Status    string            `json:"status"`
	Reason    string            `json:"reason,omitempty"`
	Hosts     []string          `json:"hosts"`
	Ports     []int             `json:"ports"`
	Users     []string          `json:"users"`
	Host      string            `json:"host,omitempty"`
	Port      int               `json:"port,omitempty"`
	User      string            `json:"user,omitempty"`
	Commands  map[string]string `json:"commands"`
	Command   string            `json:"command,omitempty"`
	Source    string            `json:"source"`
	SampledAt time.Time         `json:"sampledAt"`
}

func Validate(v Info) error {
	if len(v.Hosts) > 32 || len(v.Ports) > 16 || len(v.Users) > 32 || !ValidHost(v.Host) || !ValidUser(v.User) || v.Port < 0 || v.Port > 65535 || len(v.Reason) > 128 {
		return errors.New("invalid SSH discovery")
	}
	for _, host := range v.Hosts {
		if host == "" || !ValidHost(host) {
			return errors.New("invalid SSH address")
		}
	}
	for _, user := range v.Users {
		if user == "" || !ValidUser(user) {
			return errors.New("invalid SSH user")
		}
	}
	for _, port := range v.Ports {
		if port < 1 || port > 65535 {
			return errors.New("invalid SSH port")
		}
	}
	if v.Status != "available" && v.Status != "unavailable" {
		return errors.New("invalid SSH discovery status")
	}
	return nil
}
func Resolve(v Info, host, user string, port int) (Info, error) {
	if err := Validate(v); err != nil {
		return Info{}, err
	}
	if !ValidHost(host) || !ValidUser(user) || port < 0 || port > 65535 {
		return Info{}, errors.New("invalid SSH override")
	}
	v.Source = "discovered"
	if host != "" {
		v.Host = host
		v.Source = "override"
	}
	if user != "" {
		v.User = user
		v.Source = "override"
	}
	if port > 0 && (host != "" || user != "" || port != 22) {
		v.Port = port
		v.Source = "override"
	}
	v.Command = ""
	v.Commands = map[string]string{}
	if v.Host != "" && v.Port > 0 {
		target := v.Host
		if v.User != "" {
			target = v.User + "@" + target
		}
		v.Command = fmt.Sprintf("ssh -p %d %s", v.Port, target)
		for _, candidate := range v.Users {
			v.Commands[candidate] = fmt.Sprintf("ssh -p %d %s@%s", v.Port, candidate, v.Host)
		}
		v.Status = "available"
		v.Reason = ""
	}
	return v, nil
}
