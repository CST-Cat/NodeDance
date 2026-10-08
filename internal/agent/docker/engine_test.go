package docker

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNormalizeInspectKeepsDockerFactsSeparated(t *testing.T) {
	observed := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	raw := []byte(`{
	  "Id":"container-id-1",
	  "Name":"/nd-web",
	  "Image":"sha256:image",
	  "Created":"2026-10-08T19:00:00Z",
	  "RestartCount":2,
	  "State":{"Status":"running","Running":true,"StartedAt":"2026-10-08T19:01:00Z","FinishedAt":"0001-01-01T00:00:00Z","ExitCode":0,"Health":{"Status":"starting"}},
	  "Config":{"Image":"nginx:alpine","Env":["PASSWORD=do-not-forward"],"Healthcheck":{"Test":["CMD-SHELL","true"]},"Labels":{"com.example.secret":"not-forwarded","com.docker.compose.project":"demo","com.docker.compose.service":"web","com.docker.compose.project.working_dir":"/srv/demo","com.docker.compose.project.config_files":"compose.yaml:compose.prod.yaml","com.docker.compose.container-number":"1","com.docker.compose.oneoff":"False","com.docker.compose.version":"2.40.0"},"ExposedPorts":{"80/tcp":{},"53/udp":{},"443/tcp":{}}},
	  "HostConfig":{"NetworkMode":"bridge","PublishAllPorts":false,"PortBindings":{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}],"53/udp":[{"HostIp":"::","HostPort":""}]}},
	  "NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}],"53/udp":[{"HostIp":"::","HostPort":"18053"}]},"Networks":{"bridge":{"NetworkID":"network-id","IPAddress":"172.18.0.2","GlobalIPv6Address":"2001:db8::2","Gateway":"172.18.0.1","IPv6Gateway":"2001:db8::1","Aliases":["web","frontend"]}}},
	  "Mounts":[{"Type":"volume","Name":"nd-data","Source":"/var/lib/docker/volumes/nd-data/_data","Destination":"/data","Driver":"local","Mode":"rw","Propagation":"","RW":true}]
	}`)

	container, err := normalizeInspect(raw, observed)
	if err != nil {
		t.Fatal(err)
	}
	if container.Name != "nd-web" || container.Image != "nginx:alpine" || container.ImageID != "sha256:image" {
		t.Fatalf("identity was not normalized: %+v", container)
	}
	if container.State != "running" || !container.Running || container.Health != HealthStarting || !container.HealthcheckConfigured {
		t.Fatalf("state and health must remain distinct: %+v", container)
	}
	if container.StartedAt == nil || container.FinishedAt != nil || container.CreatedAt == nil {
		t.Fatalf("zero Docker times must be unknown: %+v", container)
	}
	if len(container.Ports) != 3 {
		t.Fatalf("wanted three configured ports, got %+v", container.Ports)
	}
	ports := map[string]Port{}
	for _, port := range container.Ports {
		ports[portKey(port)] = port
	}
	if got := ports["80/tcp"]; !got.Exposed || len(got.Configured) != 1 || got.Configured[0].Port != "" || len(got.Published) != 1 || got.Published[0].Port != "18080" {
		t.Fatalf("TCP requested and actual bindings were conflated: %+v", got)
	}
	if got := ports["53/udp"]; !got.Exposed || len(got.Configured) != 1 || got.Configured[0].Port != "" || len(got.Published) != 1 || got.Published[0].IP != "::" {
		t.Fatalf("UDP dynamic binding was not retained: %+v", got)
	}
	if got := ports["443/tcp"]; !got.Exposed || len(got.Configured) != 0 || len(got.Published) != 0 {
		t.Fatalf("EXPOSE-only port was presented as published: %+v", got)
	}
	if len(container.Networks) != 1 || container.Networks[0].IPv4 != "172.18.0.2" || container.Networks[0].IPv6 != "2001:db8::2" {
		t.Fatalf("network addresses were lost: %+v", container.Networks)
	}
	if len(container.Mounts) != 1 || container.Mounts[0].Name != "nd-data" || !container.Mounts[0].ReadWrite {
		t.Fatalf("mount was not normalized: %+v", container.Mounts)
	}
	if container.Compose == nil || container.Compose.Project != "demo" || container.Compose.Service != "web" || container.Compose.WorkingDir != "/srv/demo" || container.Compose.ContainerNumber != "1" {
		t.Fatalf("Compose identity labels were not selected: %+v", container.Compose)
	}
	encodedBytes, err := json.Marshal(container)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(encodedBytes)
	if strings.Contains(encoded, "do-not-forward") || strings.Contains(encoded, "not-forwarded") {
		t.Fatalf("secret or arbitrary labels escaped the normalized model: %s", encoded)
	}
}

func TestNormalizeInspectHostNetworkNeverReportsPublishedMapping(t *testing.T) {
	raw := []byte(`{"Id":"hosted","Name":"/nd-host","Config":{"ExposedPorts":{"80/tcp":{}},"Healthcheck":null},"HostConfig":{"NetworkMode":"host","PortBindings":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]}},"State":{"Status":"running","Running":true},"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]},"Networks":{"host":{"IPAddress":"127.0.0.1"}}}}`)
	container, err := normalizeInspect(raw, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !container.HostNetwork || container.Health != HealthNone || container.HealthcheckConfigured {
		t.Fatalf("Host Network or absent healthcheck was not represented: %+v", container)
	}
	if len(container.Ports) != 1 || len(container.Ports[0].Configured) != 1 || len(container.Ports[0].Published) != 0 {
		t.Fatalf("Host Network produced a fake published port: %+v", container.Ports)
	}
}

func TestNormalizeInspectUnknownTimesAndHealthcheck(t *testing.T) {
	raw := []byte(`{"Id":"created-only","Name":"/nd-created","Created":"2026-10-08T19:00:00Z","Config":{"Healthcheck":{"Test":["CMD-SHELL","true"]}},"State":{"Status":"created","Running":false,"StartedAt":"0001-01-01T00:00:00Z","FinishedAt":"0001-01-01T00:00:00Z"}}`)
	container, err := normalizeInspect(raw, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if container.CreatedAt == nil || container.StartedAt != nil || container.FinishedAt != nil || container.ExitCode != nil {
		t.Fatalf("never-started container times must remain unknown: %+v", container)
	}
	if container.Health != HealthUnknown || !container.HealthcheckConfigured {
		t.Fatalf("health must remain unknown until Docker reports a check result: %+v", container)
	}
}

func TestStoppedContainerDoesNotReportPublishedPorts(t *testing.T) {
	raw := []byte(`{"Id":"stopped","Name":"/nd-stopped","Config":{"ExposedPorts":{"80/tcp":{}}},"HostConfig":{"NetworkMode":"bridge","PortBindings":{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}]}},"State":{"Status":"exited","Running":false,"StartedAt":"2026-10-08T19:00:00Z","FinishedAt":"2026-10-08T19:01:00Z","ExitCode":0},"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}]}}}`)
	container, err := normalizeInspect(raw, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if container.State != "exited" || container.ExitCode == nil || *container.ExitCode != 0 {
		t.Fatalf("stopped state was not represented accurately: %+v", container)
	}
	if len(container.Ports) != 1 || len(container.Ports[0].Configured) != 1 || len(container.Ports[0].Published) != 0 {
		t.Fatalf("stopped container was presented as an active host service: %+v", container.Ports)
	}
}

func portKey(port Port) string {
	return strings.Join([]string{strconv.Itoa(int(port.ContainerPort)), port.Protocol}, "/")
}
