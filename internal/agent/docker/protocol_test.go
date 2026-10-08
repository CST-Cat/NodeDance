package docker

import (
	"reflect"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestToProtocolBatchPreservesNormalizedDockerFields(t *testing.T) {
	now := time.Date(2026, 10, 8, 18, 30, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	exitCode := 0
	batch := Batch{
		Sequence: 4, SnapshotID: 2, FullSnapshot: true, SnapshotIndex: 0, SnapshotFinal: true,
		Health: &Health{
			Sequence: 4, Availability: EngineAvailable, EventsConnected: true,
			LastSuccessAt: &now, LastSnapshotAt: &started, SnapshotFresh: true, ObservedAt: now,
		},
		Changes: []Change{{
			Sequence: 4, Action: ChangeUpsert, ContainerID: "id-1", ObservedAt: now,
			Container: &Container{
				ID: "id-1", Name: "web", Image: "nginx", ImageID: "sha256:abc", State: "running",
				Running: true, Health: HealthHealthy, HealthcheckConfigured: true,
				CreatedAt: &started, StartedAt: &started, ExitCode: &exitCode, RestartCount: 2,
				Compose: &ComposeIdentity{Project: "demo", Service: "web", WorkingDir: "/srv/demo", ConfigFiles: "/srv/demo/compose.yaml", ContainerNumber: "1", Version: "2"},
				Ports: []Port{{ContainerPort: 80, Protocol: "tcp", Exposed: true,
					Configured: []HostPort{{IP: "127.0.0.1", Port: ""}},
					Published:  []HostPort{{IP: "127.0.0.1", Port: "49201"}}}},
				Networks:   []Network{{Name: "bridge", IPv4: "172.18.0.2", IPv6: "2001:db8::2", Aliases: []string{"web", "frontend"}}},
				Mounts:     []Mount{{Type: "volume", Name: "data", Source: "/var/lib/docker/volumes/data", Destination: "/data", ReadWrite: true}},
				ObservedAt: now,
			},
		}},
	}
	got, err := ToProtocolBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.MarshalDockerBatch(got)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := protocol.UnmarshalDockerBatch(payload)
	if err != nil {
		t.Fatal(err)
	}
	container := decoded.Changes[0].Container
	if container.Compose == nil || container.Compose.Project != "demo" || container.Compose.Service != "web" {
		t.Fatalf("Compose identity was not mapped: %+v", container.Compose)
	}
	if container.Ports[0].Configured[0].Port != "" || container.Ports[0].Published[0].Port != "49201" {
		t.Fatalf("port states were not preserved: %+v", container.Ports[0])
	}
	if !reflect.DeepEqual(container.Networks[0].Aliases, []string{"web", "frontend"}) || container.Mounts[0].Destination != "/data" {
		t.Fatalf("network or mount fields were not mapped: networks=%+v mounts=%+v", container.Networks, container.Mounts)
	}
	if decoded.Health == nil || decoded.Health.Availability != protocol.DockerAvailabilityAvailable || decoded.Health.LastSuccessAt == nil {
		t.Fatalf("Docker health was not mapped: %+v", decoded.Health)
	}
}

func TestToProtocolBatchClonesMutableFields(t *testing.T) {
	now := time.Now().UTC()
	container := Container{
		ID: "id", Name: "web", State: "running", Health: HealthNone, ObservedAt: now,
		Ports:    []Port{{ContainerPort: 80, Protocol: "tcp", Configured: []HostPort{{IP: "127.0.0.1"}}}},
		Networks: []Network{{Name: "bridge", Aliases: []string{"web"}}},
	}
	input := Batch{Sequence: 1, Changes: []Change{{Sequence: 1, Action: ChangeUpsert, ContainerID: "id", Container: &container, ObservedAt: now}}}
	got, err := ToProtocolBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	got.Changes[0].Container.Networks[0].Aliases[0] = "changed"
	got.Changes[0].Container.Ports[0].Configured[0].IP = "changed"
	if container.Networks[0].Aliases[0] != "web" || container.Ports[0].Configured[0].IP != "127.0.0.1" {
		t.Fatal("protocol DTO aliases internal Agent cache memory")
	}
}
