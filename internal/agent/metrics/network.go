package metrics

import (
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

type defaultRoute struct {
	name   string
	ipv6   bool
	metric uint32
}

func parseIPv4DefaultRoutes(data string) []defaultRoute {
	var routes []defaultRoute
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		metric, err := strconv.ParseUint(fields[6], 10, 32)
		if err != nil {
			continue
		}
		routes = append(routes, defaultRoute{name: fields[0], metric: uint32(metric)})
	}
	return routes
}

func parseIPv6DefaultRoutes(data string) []defaultRoute {
	var routes []defaultRoute
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[1] != "00" || strings.Trim(fields[0], "0") != "" {
			continue
		}
		metric, err := strconv.ParseUint(fields[5], 16, 32)
		if err != nil {
			continue
		}
		routes = append(routes, defaultRoute{name: fields[9], ipv6: true, metric: uint32(metric)})
	}
	return routes
}

func primaryInterfaces(routes []defaultRoute) map[string]bool {
	best := make(map[bool]uint32, 2)
	for _, route := range routes {
		if current, ok := best[route.ipv6]; !ok || route.metric < current {
			best[route.ipv6] = route.metric
		}
	}
	primary := make(map[string]bool, len(routes))
	for _, route := range routes {
		if route.metric == best[route.ipv6] {
			primary[route.name] = true
		}
	}
	return primary
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
