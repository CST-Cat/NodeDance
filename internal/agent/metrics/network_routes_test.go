package metrics

import (
	"reflect"
	"testing"
)

func TestParseIPv4DefaultRoutes(t *testing.T) {
	data := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0101910A\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"eth0\t0001910A\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n" +
		"broken line\n"
	routes := parseIPv4DefaultRoutes(data)
	want := []defaultRoute{
		{name: "eth0", metric: 100},
		{name: "wlan0", metric: 600},
	}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %+v, want %+v", routes, want)
	}
}

func TestParseIPv6DefaultRoutes(t *testing.T) {
	data := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000000 00000000 00000003 eth0\n" +
		"20010000000000000000000000000000 20 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000000 00000000 00000003 eth0\n"
	routes := parseIPv6DefaultRoutes(data)
	want := []defaultRoute{
		{name: "eth0", ipv6: true, metric: 1024},
	}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %+v, want %+v", routes, want)
	}
}

func TestPrimaryInterfacesSelectLowestMetricPerFamily(t *testing.T) {
	routes := []defaultRoute{
		{name: "eth0", metric: 100},
		{name: "wlan0", metric: 600},
		{name: "eth0", ipv6: true, metric: 200},
		{name: "wg0", ipv6: true, metric: 50},
	}
	primary := primaryInterfaces(routes)
	want := map[string]bool{"eth0": true, "wg0": true}
	if !reflect.DeepEqual(primary, want) {
		t.Fatalf("primary = %v, want %v", primary, want)
	}
}

func TestPrimaryInterfacesEqualMetricsAndEmpty(t *testing.T) {
	routes := []defaultRoute{
		{name: "eth0", metric: 100},
		{name: "eth1", metric: 100},
	}
	primary := primaryInterfaces(routes)
	if !primary["eth0"] || !primary["eth1"] || len(primary) != 2 {
		t.Fatalf("equal-metric defaults must all be primary: %v", primary)
	}
	if got := primaryInterfaces(nil); len(got) != 0 {
		t.Fatalf("no routes must yield no primary interfaces: %v", got)
	}
}

func TestPrimaryInterfacesSingleVethContainer(t *testing.T) {
	routes := parseIPv4DefaultRoutes("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0100000A\t0003\t0\t0\t0\t00000000\t0\t0\t0\n")
	primary := primaryInterfaces(routes)
	if !primary["eth0"] || len(primary) != 1 {
		t.Fatalf("the only default-route interface must be primary: %v", primary)
	}
}
