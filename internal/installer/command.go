// Package installer renders the root Agent installer command shared by all UIs.
package installer

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

func Command(server, displayName string, development bool) (string, error) {
	parsed, err := url.Parse(server)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("Agent Core URL must be an HTTPS origin")
	}
	dev := false
	if parsed.Scheme != "https" {
		ip := net.ParseIP(parsed.Hostname())
		if parsed.Scheme != "http" || !development || ip == nil || !ip.IsLoopback() {
			return "", errors.New("Agent HTTP requires development mode and a literal loopback address")
		}
		dev = true
	}
	name := strings.TrimSpace(displayName)
	if name == "" || len(name) > 320 || strings.ContainsAny(name, "\r\n\x00") {
		return "", errors.New("Agent display name must be a nonempty single line")
	}
	parsed.Path = ""
	script := `releases=$(curl --fail --silent --show-error --location --proto =https --proto-redir =https --tlsv1.2 "https://api.github.com/repos/CST-Cat/NodeDance/releases?per_page=100") || { echo 'Could not list Agent releases.' >&2; exit 1; }; tag=$(printf '%s\n' "$releases" | awk -F'"' '$2 == "tag_name" && $4 ~ /^agent-v[0-9]+\.[0-9]+\.[0-9]+$/ { print $4; exit }'); [[ "$tag" =~ ^agent-v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'No published agent-vMAJOR.MINOR.PATCH release was found.' >&2; exit 1; }; curl --fail --silent --show-error --location --proto =https --proto-redir =https --tlsv1.2 "https://github.com/CST-Cat/NodeDance/releases/download/$tag/install-agent.sh" | sudo bash -s -- --release-tag "$tag" --server ` + quote(parsed.String()) + " --display-name " + quote(name)
	if dev {
		script += " --dev"
	}
	return "bash -o pipefail -c " + quote(script), nil
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
