// Package installer renders the root Agent installation commands shared by all UIs.
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

// DockerCommand installs the root Agent into a Docker container using the same
// one-time enrollment as the native installer. No credential enters argv or env.
func DockerCommand(server, displayName string, development bool) (string, error) {
	if _, err := Command(server, displayName, development); err != nil {
		return "", err
	}
	parsed, _ := url.Parse(server)
	parsed.Path = ""
	script := `set -Eeuo pipefail; [[ "$(uname -s)" == Linux ]] || { echo 'Linux rootful Docker is required.' >&2; exit 1; }; sudo -v; [[ ! -e /var/lib/nodedance-agent/agent.json && ! -L /var/lib/nodedance-agent ]] || { echo 'Agent state already exists; stop the old Agent and preserve its state before replacing it.' >&2; exit 1; }; if sudo -n docker container inspect nodedance-agent >/dev/null 2>&1; then echo 'Agent container already exists.' >&2; exit 1; fi; if command -v systemctl >/dev/null && systemctl is-active --quiet nodedance-agent.service; then echo 'Stop the existing systemd Agent before Docker enrollment.' >&2; exit 1; fi; releases=$(curl --fail --silent --show-error --location --proto =https --proto-redir =https --tlsv1.2 "https://api.github.com/repos/CST-Cat/NodeDance/releases?per_page=100"); tag=$(printf '%s\n' "$releases" | awk -F'"' '$2 == "tag_name" && $4 ~ /^agent-v[0-9]+\.[0-9]+\.[0-9]+$/ { print $4; exit }'); [[ "$tag" =~ ^agent-v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'No published Agent release was found.' >&2; exit 1; }; image="ghcr.io/cst-cat/nodedance-agent:$tag"; sudo -n docker pull "$image"; sudo -n install -d -o root -g root -m 0700 /var/lib/nodedance-agent; IFS= read -r -s -p 'Enrollment token: ' token < /dev/tty; printf '\n' > /dev/tty; printf '%s\n' "$token" | sudo -n docker run --rm -i --privileged --pid=host --network=host --mount type=bind,src=/,dst=/host,readonly --mount type=bind,src=/var/lib/nodedance-agent,dst=/var/lib/nodedance-agent "$image" enroll --server ` + quote(parsed.String()) + ` --token-stdin --config /var/lib/nodedance-agent/agent.json`
	if parsed.Scheme == "http" {
		script += " --dev"
	}
	script += `; unset token; sudo -n docker run -d --name nodedance-agent --restart unless-stopped --stop-timeout 30 --privileged --pid=host --network=host --mount type=bind,src=/,dst=/host,readonly --mount type=bind,src=/var/lib/nodedance-agent,dst=/var/lib/nodedance-agent "$image"`
	return "bash -o pipefail -c " + quote(script), nil
}
