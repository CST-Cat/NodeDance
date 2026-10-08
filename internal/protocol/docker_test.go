package protocol

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDockerBatchRoundTripKeepsDistinctPortStatesAndComposeIdentity(t *testing.T) {
	now := time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)
	batch := DockerBatch{
		Sequence: 8, SnapshotID: 3, FullSnapshot: true, SnapshotIndex: 0, SnapshotFinal: true,
		Health: validDockerHealth(8, now),
		Changes: []DockerChange{{
			Sequence: 8, Action: DockerChangeUpsert, ContainerID: "container-1", ObservedAt: now,
			Container: &DockerContainer{
				ID: "container-1", Name: "nd-web", Image: "nginx", ImageID: "sha256:123", State: "running",
				Running: true, Health: DockerHealthNone, ObservedAt: now, Ports: []DockerPort{
					{ContainerPort: 80, Protocol: "tcp", Exposed: true,
						Configured: []DockerHostPort{{IP: "127.0.0.1", Port: ""}},
						Published:  []DockerHostPort{{IP: "127.0.0.1", Port: "49152"}}},
					{ContainerPort: 443, Protocol: "tcp", Exposed: true},
				},
				Networks: []DockerNetwork{{Name: "bridge", IPv4: "172.18.0.2", IPv6: "2001:db8::2"}},
				Mounts:   []DockerMount{{Type: "volume", Name: "web-data", Destination: "/data", ReadWrite: true}},
				Compose:  &DockerComposeIdentity{Project: "demo", Service: "web", WorkingDir: "/srv/demo", ConfigFiles: "/srv/demo/compose.yaml", ContainerNumber: "1"},
			},
		}},
	}
	payload, err := MarshalDockerBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalDockerBatch(payload)
	if err != nil {
		t.Fatal(err)
	}
	port80 := decoded.Changes[0].Container.Ports[0]
	if len(port80.Configured) != 1 || port80.Configured[0].Port != "" || len(port80.Published) != 1 || port80.Published[0].Port != "49152" {
		t.Fatalf("configured/dynamic and actual host bindings collapsed: %+v", port80)
	}
	port443 := decoded.Changes[0].Container.Ports[1]
	if !port443.Exposed || len(port443.Configured) != 0 || len(port443.Published) != 0 {
		t.Fatalf("EXPOSE was confused with a host binding: %+v", port443)
	}
	if decoded.Changes[0].Container.Compose == nil || decoded.Changes[0].Container.Compose.Project != "demo" {
		t.Fatalf("selected Compose identity missing: %+v", decoded.Changes[0].Container.Compose)
	}
}

func TestDockerBatchAcceptsUnavailableEmptySnapshotForCoreToTreatAsNonAuthoritative(t *testing.T) {
	now := time.Now().UTC()
	batch := DockerBatch{
		Sequence: 1, SnapshotID: 0, FullSnapshot: true, SnapshotIndex: 0, SnapshotFinal: true,
		Health: &DockerHealth{
			Sequence: 1, Availability: DockerAvailabilityUnavailable, SnapshotFresh: false,
			ErrorKind: "permission_denied", Reason: "Docker socket is not accessible", ObservedAt: now,
		},
	}
	payload, err := MarshalDockerBatch(batch)
	if err != nil {
		t.Fatalf("valid unavailable report rejected at protocol layer: %v", err)
	}
	if _, err := UnmarshalDockerBatch(payload); err != nil {
		t.Fatal(err)
	}
}

func TestDockerBatchRejectsMalformedOrOverLimitPayload(t *testing.T) {
	now := time.Now().UTC()
	base := DockerBatch{
		Sequence: 1,
		Changes: []DockerChange{{
			Sequence: 1, Action: DockerChangeUpsert, ContainerID: "a", ObservedAt: now,
			Container: &DockerContainer{ID: "a", Name: "a", State: "running", Health: DockerHealthNone, ObservedAt: now},
		}},
	}
	badPort := base
	badPort.Changes = append([]DockerChange(nil), base.Changes...)
	container := *base.Changes[0].Container
	container.Ports = []DockerPort{{ContainerPort: 80, Protocol: "tcp", Published: []DockerHostPort{{Port: ""}}}}
	badPort.Changes[0].Container = &container
	if _, err := MarshalDockerBatch(badPort); err == nil {
		t.Fatal("accepted an actual host publication without an allocated port")
	}

	tooLarge := base
	tooLarge.Changes = make([]DockerChange, MaxDockerBatchChanges)
	for index := range tooLarge.Changes {
		id := "container-" + strings.Repeat("x", 1) + string(rune('a'+index))
		tooLarge.Changes[index] = DockerChange{
			Sequence: 1, Action: DockerChangeUpsert, ContainerID: id, ObservedAt: now,
			Container: &DockerContainer{ID: id, Name: "n", Image: strings.Repeat("i", maxDockerTextBytes), ImageID: "image", State: "running", Health: DockerHealthNone, ObservedAt: now},
		}
	}
	if _, err := MarshalDockerBatch(tooLarge); err == nil {
		t.Fatal("accepted a Docker payload above the 64 KiB bound")
	}

	valid, err := MarshalDockerBatch(base)
	if err != nil {
		t.Fatal(err)
	}
	unknownField := bytes.Replace(valid, []byte(`"sequence":1`), []byte(`"sequence":1,"environment":["SECRET=value"]`), 1)
	if _, err := UnmarshalDockerBatch(unknownField); err == nil {
		t.Fatal("accepted an unapproved field that could expose environment data")
	}
	if _, err := UnmarshalDockerBatch(append(valid, []byte(` {}`)...)); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}

func TestDockerSnapshotShapeAndDuplicateContainerValidation(t *testing.T) {
	now := time.Now().UTC()
	if _, err := MarshalDockerBatch(DockerBatch{
		Sequence: 1, SnapshotID: 1, FullSnapshot: true, SnapshotIndex: 0,
		Health: validDockerHealth(1, now),
	}); err == nil {
		t.Fatal("accepted empty non-final snapshot")
	}
	container := &DockerContainer{ID: "a", Name: "a", State: "running", Health: DockerHealthNone, ObservedAt: now}
	change := DockerChange{Sequence: 1, Action: DockerChangeUpsert, ContainerID: "a", Container: container, ObservedAt: now}
	if _, err := MarshalDockerBatch(DockerBatch{Sequence: 1, Changes: []DockerChange{change, change}}); err == nil {
		t.Fatal("accepted duplicate container entries in one batch")
	}
}

func validDockerHealth(sequence uint64, now time.Time) *DockerHealth {
	return &DockerHealth{
		Sequence: sequence, Availability: DockerAvailabilityAvailable,
		EventsConnected: true, SnapshotFresh: true, ObservedAt: now,
	}
}
