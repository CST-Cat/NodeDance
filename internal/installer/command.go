// Package installer renders the root Agent installation commands shared by all UIs.
package installer

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var shellWord = regexp.MustCompile(`^[A-Za-z0-9:/._-]+$`)
var enrollmentToken = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidateServer(server string, development bool) error {
	_, err := origin(server, development)
	return err
}
func origin(server string, development bool) (string, error) {
	parsed, err := url.Parse(server)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("Agent Core URL must be an HTTPS origin")
	}
	if parsed.Scheme != "https" {
		ip := net.ParseIP(parsed.Hostname())
		if parsed.Scheme != "http" || !development || ip == nil || !ip.IsLoopback() {
			return "", errors.New("Agent HTTP requires development mode and a literal loopback address")
		}
	}
	parsed.Path = ""
	return parsed.String(), nil
}
func Command(server, token string, development bool) (string, error) {
	return command(server, token, "native", development)
}
func DockerCommand(server, token string, development bool) (string, error) {
	return command(server, token, "docker", development)
}
func command(server, token, mode string, development bool) (string, error) {
	server, err := origin(server, development)
	if err != nil {
		return "", err
	}
	if !enrollmentToken.MatchString(token) {
		return "", errors.New("invalid Agent enrollment token")
	}
	script := "curl -fsSL --proto =https --proto-redir =https --tlsv1.2 https://raw.githubusercontent.com/CST-Cat/NodeDance/main/scripts/install-agent.sh | sudo bash -s -- --mode " + mode + " --server " + quote(server) + " --token " + token
	if strings.HasPrefix(server, "http:") {
		script += " --dev"
	}
	return "bash -o pipefail -c " + quote(script), nil
}
func quote(s string) string {
	if shellWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
