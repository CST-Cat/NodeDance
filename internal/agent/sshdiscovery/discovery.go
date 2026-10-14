// Package sshdiscovery observes the host SSH service; it never changes SSH or reads credentials.
package sshdiscovery

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/sshconnection"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type listener struct {
	host string
	port int
}

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	var output limitedBuffer
	c.Stdout = &output
	c.Stderr = &limitedBuffer{}
	err := c.Run()
	return output.Bytes(), err
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, errors.New("SSH observation exceeds limit")
	}
	return b.Buffer.Write(p)
}
func Discover(ctx context.Context) sshconnection.Info {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	v := sshconnection.Info{Status: "unavailable", Reason: "ssh_service_unavailable", Source: "discovered", SampledAt: time.Now().UTC(), Hosts: []string{}, Ports: []int{}, Users: []string{}}
	if os.Geteuid() != 0 {
		v.Reason = "root_required"
		return v
	}
	// -T applies sshd_config Include files without starting or modifying the daemon.
	config, _ := command(ctx, "/usr/sbin/sshd", "-T")
	sockets, err := command(ctx, "ss", "-H", "-lntp")
	if err != nil {
		fallback, cancel := context.WithTimeout(ctx, 2*time.Second)
		connections, e := gnet.ConnectionsWithContext(fallback, "tcp")
		cancel()
		if e != nil {
			v.Reason = "ssh_listener_read_failed"
			return v
		}
		var lines []string
		for _, connection := range connections {
			if connection.Status != "LISTEN" || connection.Pid <= 0 {
				continue
			}
			comm, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(int(connection.Pid)), "comm"))
			if !strings.HasPrefix(strings.TrimSpace(string(comm)), "sshd") {
				continue
			}
			lines = append(lines, "LISTEN 0 0 "+net.JoinHostPort(connection.Laddr.IP, strconv.Itoa(int(connection.Laddr.Port)))+" *:* users:((\"sshd\",pid="+strconv.Itoa(int(connection.Pid))+"))")
		}
		sockets = []byte(strings.Join(lines, "\n"))
	}
	socketUnits, _ := command(ctx, "systemctl", "show", "ssh.socket", "sshd.socket", "--property=ActiveState", "--property=Listen", "--no-pager")
	var addresses []string
	if interfaces, e := net.Interfaces(); e == nil {
		for _, iface := range interfaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || internalInterface(iface.Name) {
				continue
			}
			values, _ := iface.Addrs()
			for _, value := range values {
				ip, _, e := net.ParseCIDR(value.String())
				if e == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast() {
					addresses = append(addresses, ip.String())
				}
			}
		}
	}
	minUID := 1000
	loginDefs, _ := os.ReadFile("/etc/login.defs")
	for _, line := range strings.Split(string(loginDefs), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == "UID_MIN" {
			if n, e := strconv.Atoi(f[1]); e == nil && n > 0 {
				minUID = n
			}
		}
	}
	accounts, _ := os.ReadFile("/etc/passwd")
	groups, _ := os.ReadFile("/etc/group")
	var sessions []string
	sessionPIDs := map[int]bool{}
	daemonPIDs := map[int]bool{}
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		if _, e := strconv.Atoi(entry.Name()); e != nil {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if !strings.HasPrefix(strings.TrimSpace(string(comm)), "sshd") {
			continue
		}
		cmd, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		title := strings.SplitN(string(cmd), "\x00", 2)[0]
		_, after, ok := strings.Cut(title, ": ")
		if !ok {
			if strings.TrimSpace(string(comm)) == "sshd" && rootProcess(entry.Name()) {
				pid, _ := strconv.Atoi(entry.Name())
				daemonPIDs[pid] = true
			}
			continue
		}
		name := strings.Fields(strings.SplitN(after, "@", 2)[0])
		if len(name) > 0 && sshconnection.ValidUser(name[0]) {
			pid, _ := strconv.Atoi(entry.Name())
			sessionPIDs[pid] = true
			if strings.Contains(after, "@") {
				sessions = append(sessions, name[0])
			}
		} else if strings.TrimSpace(string(comm)) == "sshd" && rootProcess(entry.Name()) {
			pid, _ := strconv.Atoi(entry.Name())
			daemonPIDs[pid] = true
		}
	}
	return observed(v, string(config), filterDaemonSockets(filterSessionSockets(string(sockets), sessionPIDs), daemonPIDs), string(socketUnits), addresses, string(accounts), string(groups), sessions, minUID)
}
func observed(v sshconnection.Info, config, sockets, units string, addresses []string, passwd, groups string, sessions []string, minimumUID ...int) sshconnection.Info {
	minUID := 1000
	if len(minimumUID) > 0 {
		minUID = minimumUID[0]
	}
	options := map[string][]string{}
	for _, line := range strings.Split(config, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 {
			options[fields[0]] = append(options[fields[0]], fields[1:]...)
		}
	}
	activated := map[string]bool{}
	for _, unit := range strings.Split(units, "\n\n") {
		if !strings.Contains(unit, "ActiveState=active") {
			continue
		}
		for _, line := range strings.Split(unit, "\n") {
			if strings.HasPrefix(line, "Listen=") {
				endpoint := strings.Fields(strings.TrimPrefix(line, "Listen="))
				if len(endpoint) > 0 {
					activated[endpoint[0]] = true
				}
			}
		}
	}
	var listeners []listener
	for _, line := range strings.Split(sockets, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		endpoint := fields[3]
		if !strings.Contains(line, "(\"sshd\"") && !strings.Contains(line, "(\"sshd-session\"") && !activatedEndpoint(endpoint, activated) {
			continue
		}
		host, raw, e := net.SplitHostPort(endpoint)
		if e != nil {
			continue
		}
		port, e := strconv.Atoi(raw)
		if e != nil || port < 1 || port > 65535 {
			continue
		}
		listeners = append(listeners, listener{host, port})
	}
	if len(listeners) == 0 {
		return v
	}
	portSet := map[int]bool{}
	hostSet := map[string]bool{}
	for _, l := range listeners {
		for _, address := range addresses {
			ip := net.ParseIP(address)
			bound := net.ParseIP(l.host)
			wildcard := l.host == "*" || bound != nil && bound.IsUnspecified()
			if !wildcard && !ip.Equal(bound) {
				continue
			}
			if bound != nil && bound.IsUnspecified() && (bound.To4() != nil) != (ip.To4() != nil) {
				continue
			}
			hostSet[address] = true
			portSet[l.port] = true
		}
	}
	for host := range hostSet {
		v.Hosts = append(v.Hosts, host)
	}
	sort.Slice(v.Hosts, func(i, j int) bool {
		a, b := addressRank(v.Hosts[i]), addressRank(v.Hosts[j])
		if a != b {
			return a < b
		}
		return v.Hosts[i] < v.Hosts[j]
	})
	for port := range portSet {
		v.Ports = append(v.Ports, port)
	}
	sort.Ints(v.Ports)
	if len(v.Hosts) > 32 {
		v.Hosts = v.Hosts[:32]
	}
	if len(v.Ports) > 16 {
		v.Ports = v.Ports[:16]
	}
	groupNames := map[string][]string{}
	for _, line := range strings.Split(groups, "\n") {
		f := strings.Split(line, ":")
		if len(f) != 4 {
			continue
		}
		for _, user := range strings.Split(f[3], ",") {
			groupNames[user] = append(groupNames[user], f[0])
		}
		groupNames["gid:"+f[2]] = append(groupNames["gid:"+f[2]], f[0])
	}
	nonroot := []string{}
	allowed := map[string]bool{}
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(line, ":")
		if len(f) != 7 || !sshconnection.ValidUser(f[0]) || f[0] == "" {
			continue
		}
		uid, e := strconv.Atoi(f[2])
		if e != nil || (uid != 0 && (uid < minUID || uid == 65534) && !contains(sessions, f[0]) && !pattern(f[0], options["allowusers"])) || uid < 0 || f[6] == "" || strings.HasSuffix(f[6], "/nologin") || strings.HasSuffix(f[6], "/false") {
			continue
		}
		if uid == 0 && (contains(options["permitrootlogin"], "no") || len(options["permitrootlogin"]) == 0 && !contains(sessions, f[0])) {
			continue
		}
		names := append(append([]string{}, groupNames[f[0]]...), groupNames["gid:"+f[3]]...)
		if denied(f[0], names, options) {
			continue
		}
		v.Users = append(v.Users, f[0])
		allowed[f[0]] = true
		if uid != 0 {
			nonroot = append(nonroot, f[0])
		}
	}
	sort.Strings(v.Users)
	if len(v.Users) > 32 {
		v.Users = v.Users[:32]
	}
	active := map[string]bool{}
	for _, user := range sessions {
		if allowed[user] {
			active[user] = true
		}
	}
	if len(active) == 1 {
		for user := range active {
			v.User = user
		}
	} else if len(nonroot) == 1 {
		v.User = nonroot[0]
	} else if len(v.Users) == 1 {
		v.User = v.Users[0]
	}
	if len(v.Hosts) == 0 || len(v.Ports) == 0 {
		v.Reason = "ssh_listener_local_only"
		return v
	}
	v.Host = v.Hosts[0]
	for _, l := range listeners {
		ip, bound := net.ParseIP(v.Host), net.ParseIP(l.host)
		wildcard := l.host == "*" || bound != nil && bound.IsUnspecified()
		if !wildcard && !ip.Equal(bound) {
			continue
		}
		if bound != nil && bound.IsUnspecified() && (bound.To4() != nil) != (ip.To4() != nil) {
			continue
		}
		if v.Port == 0 || l.port < v.Port {
			v.Port = l.port
		}
	}
	v.Status = "available"
	v.Reason = ""
	resolved, e := sshconnection.Resolve(v, "", "", 0)
	if e != nil {
		v.Status = "unavailable"
		v.Reason = "invalid_ssh_observation"
		return v
	}
	return resolved
}
func addressRank(value string) int {
	ip := net.ParseIP(value)
	if ip.To4() != nil {
		if ip[12] == 100 && ip[13] >= 64 && ip[13] <= 127 {
			return 1
		}
		if !ip.IsPrivate() {
			return 0
		}
		return 3
	}
	if !ip.IsPrivate() {
		return 2
	}
	return 4
}
func internalInterface(name string) bool {
	for _, prefix := range []string{"docker", "veth", "br-", "virbr", "podman", "cni", "flannel", "ifb", "dummy"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func contains(values []string, v string) bool {
	for _, value := range values {
		if value == v {
			return true
		}
	}
	return false
}
func pattern(value string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.SplitN(p, "@", 2)[0]
		if ok, _ := filepath.Match(p, value); ok {
			return true
		}
	}
	return false
}
func groupMatch(names, patterns []string) bool {
	for _, name := range names {
		if pattern(name, patterns) {
			return true
		}
	}
	return false
}
func denied(user string, groups []string, options map[string][]string) bool {
	return pattern(user, options["denyusers"]) || len(options["allowusers"]) > 0 && !pattern(user, options["allowusers"]) || groupMatch(groups, options["denygroups"]) || len(options["allowgroups"]) > 0 && !groupMatch(groups, options["allowgroups"])
}

var socketPID = regexp.MustCompile(`pid=(\d+)`)

func filterSessionSockets(sockets string, sessions map[int]bool) string {
	var lines []string
	for _, line := range strings.Split(sockets, "\n") {
		ids := socketPID.FindAllStringSubmatch(line, -1)
		onlySessions := len(ids) > 0
		for _, id := range ids {
			pid, _ := strconv.Atoi(id[1])
			if !sessions[pid] {
				onlySessions = false
				break
			}
		}
		if !onlySessions {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func rootProcess(pid string) bool {
	status, e := os.ReadFile(filepath.Join("/proc", pid, "status"))
	if e != nil {
		return false
	}
	for _, line := range strings.Split(string(status), "\n") {
		f := strings.Fields(line)
		if len(f) > 2 && f[0] == "Uid:" {
			return f[2] == "0"
		}
	}
	return false
}
func filterDaemonSockets(sockets string, daemons map[int]bool) string {
	var lines []string
	for _, line := range strings.Split(sockets, "\n") {
		if !strings.Contains(line, "(\"sshd\"") && !strings.Contains(line, "(\"sshd-session\"") {
			lines = append(lines, line)
			continue
		}
		for _, id := range socketPID.FindAllStringSubmatch(line, -1) {
			pid, _ := strconv.Atoi(id[1])
			if daemons[pid] {
				lines = append(lines, line)
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

func activatedEndpoint(endpoint string, active map[string]bool) bool {
	if active[endpoint] {
		return true
	}
	host, port, e := net.SplitHostPort(endpoint)
	if e != nil {
		return false
	}
	for candidate := range active {
		other, p, e := net.SplitHostPort(candidate)
		if e != nil || p != port {
			continue
		}
		ip := net.ParseIP(other)
		if host == "*" && (other == "*" || ip != nil && ip.IsUnspecified()) {
			return true
		}
	}
	return false
}
