package server

import (
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/shirou/gopsutil/v4/host"
	"net"
	"net/http"
	"os"
	"runtime"
)

func (s *Server) handleDashboardContextAPI(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/v1/dashboard/core":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return true
		}
		info := dashboard.CoreInfo{Architecture: runtime.GOARCH, Version: s.version, Environment: "native", Addresses: []string{}, SampledAt: s.now().UTC()}
		info.Hostname, _ = os.Hostname()
		if _, err := os.Stat("/.dockerenv"); err == nil {
			info.Environment = "container"
		}
		if value, err := host.InfoWithContext(r.Context()); err == nil {
			info.Hostname = value.Hostname
			info.Platform = value.Platform
			info.PlatformVersion = value.PlatformVersion
			info.KernelVersion = value.KernelVersion
			info.Architecture = value.KernelArch
			info.BootTime = value.BootTime
			info.Uptime = value.Uptime
		}
		interfaces, _ := net.Interfaces()
		for _, iface := range interfaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addresses, _ := iface.Addrs()
			for _, address := range addresses {
				ip, _, err := net.ParseCIDR(address.String())
				if err == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && len(info.Addresses) < 16 {
					info.Addresses = append(info.Addresses, ip.String())
				}
			}
		}
		writeJSON(w, 200, info)
		return true
	case "/api/v1/dashboard/memo":
		if r.Method == http.MethodGet {
			value, err := s.dashboardPreferences.Memo(r.Context())
			if err != nil {
				http.Error(w, "memo unavailable", 500)
			} else {
				writeJSON(w, 200, value)
			}
			return true
		}
		if r.Method == http.MethodPut {
			var input dashboard.MemoUpdate
			if !decodeJSON(w, r, &input) {
				return true
			}
			value, err := s.dashboardPreferences.SaveMemo(r.Context(), input)
			switch {
			case errors.Is(err, dashboard.ErrInvalidMemo):
				http.Error(w, "invalid memo", 400)
			case errors.Is(err, dashboard.ErrMemoConflict):
				http.Error(w, "memo changed; reload before saving", 409)
			case err != nil:
				http.Error(w, "memo could not be saved", 500)
			default:
				writeJSON(w, 200, value)
			}
			return true
		}
		http.Error(w, "method not allowed", 405)
		return true
	}
	return false
}
