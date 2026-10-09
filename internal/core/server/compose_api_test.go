package server

import (
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestComposeObservedPostconditionOnlyResolvesProvableStates(t *testing.T) {
	ref := protocol.ComposeProjectRef{Name: "demo", WorkingDirectory: "/srv/demo", ConfigFiles: []string{"/srv/demo/compose.yaml"}}
	ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	request := protocol.ComposeRequest{OperationID: "test-operation", Project: ref}
	project := protocol.ComposeProject{Ref: ref, ConfigAvailable: true, Services: []protocol.ComposeService{{
		Name: "web", Instances: []protocol.ComposeServiceInstance{{ContainerID: "container-1", ContainerName: "web-1", State: "exited"}},
	}}}
	tests := []struct {
		name     string
		action   protocol.ComposeAction
		projects []protocol.ComposeProject
		want     bool
	}{
		{name: "down absent", action: protocol.ComposeDown, want: true},
		{name: "down still present", action: protocol.ComposeDown, projects: []protocol.ComposeProject{project}, want: false},
		{name: "stop all exited", action: protocol.ComposeStop, projects: []protocol.ComposeProject{project}, want: true},
		{name: "stop has running instance", action: protocol.ComposeStop, projects: []protocol.ComposeProject{{Ref: ref, Services: []protocol.ComposeService{{Name: "web", Instances: []protocol.ComposeServiceInstance{{ContainerID: "container-1", ContainerName: "web-1", State: "running"}}}}}}, want: false},
		{name: "up inventory does not enumerate expected services", action: protocol.ComposeUp, projects: []protocol.ComposeProject{project}, want: false},
		{name: "start inventory does not enumerate expected services", action: protocol.ComposeStart, projects: []protocol.ComposeProject{project}, want: false},
		{name: "restart is not proven by current running state", action: protocol.ComposeRestart, projects: []protocol.ComposeProject{{Ref: ref, Services: []protocol.ComposeService{{Name: "web", Instances: []protocol.ComposeServiceInstance{{ContainerID: "container-1", ContainerName: "web-1", State: "running"}}}}}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request.Action = test.action
			if got := composeObservedPostcondition(request, test.projects); got != test.want {
				t.Fatalf("composeObservedPostcondition() = %t, want %t", got, test.want)
			}
		})
	}
}
