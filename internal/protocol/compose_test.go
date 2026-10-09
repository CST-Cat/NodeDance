package protocol

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestComposeProjectKeyBindsNameDirectoryAndOrderedFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "path with spaces; ' quote")
	files := []string{filepath.Join(directory, "compose.yaml"), filepath.Join(directory, "override.yaml")}
	key := ComposeProjectKey("demo", directory, files)
	if key == ComposeProjectKey("demo", directory+"-other", files) || key == ComposeProjectKey("demo", directory, []string{files[1], files[0]}) {
		t.Fatal("Compose source context key ignored project directory or ordered file list")
	}
	ref := ComposeProjectRef{Key: key, Name: "demo", WorkingDirectory: directory, ConfigFiles: files}
	if err := ValidateComposeProjectRef(ref); err != nil {
		t.Fatal(err)
	}
	ref.ConfigFiles = []string{files[1], files[0]}
	if err := ValidateComposeProjectRef(ref); err == nil {
		t.Fatal("reordered file list retained an old project key")
	}
}

func TestValidateComposeRequestBoundsPathsActionsAndProfiles(t *testing.T) {
	directory := t.TempDir()
	files := []string{filepath.Join(directory, "compose.yaml")}
	ref := ComposeProjectRef{Name: "demo", WorkingDirectory: directory, ConfigFiles: files}
	ref.Key = ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	valid := ComposeRequest{OperationID: "op-1", Action: ComposeUp, Project: ref,
		EnvFiles: []string{filepath.Join(directory, "one.env"), filepath.Join(directory, "two.env")}, Profiles: []string{"api", "worker.v2"}}
	if err := ValidateComposeRequest(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for name, request := range map[string]ComposeRequest{
		"relative-config": func() ComposeRequest {
			copy := valid
			copy.Project.ConfigFiles = []string{"compose.yaml"}
			copy.Project.Key = ComposeProjectKey(copy.Project.Name, copy.Project.WorkingDirectory, copy.Project.ConfigFiles)
			return copy
		}(),
		"relative-env": func() ComposeRequest { copy := valid; copy.EnvFiles = []string{".env"}; return copy }(),
		"bad-profile":  func() ComposeRequest { copy := valid; copy.Profiles = []string{"../outside"}; return copy }(),
		"bad-action":   func() ComposeRequest { copy := valid; copy.Action = ComposeAction("shell"); return copy }(),
		"list-target":  {OperationID: "list-1", Action: ComposeList, Project: ref},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateComposeRequest(request); err == nil {
				t.Fatal("invalid Compose request was accepted")
			}
		})
	}
	list := ComposeRequest{OperationID: "list-2", Action: ComposeList}
	if err := ValidateComposeRequest(list); err != nil {
		t.Fatalf("empty list request rejected: %v", err)
	}
	if !reflect.DeepEqual(valid.EnvFiles, []string{filepath.Join(directory, "one.env"), filepath.Join(directory, "two.env")}) {
		t.Fatal("test fixture unexpectedly changed")
	}
}

func TestComposeEditorStatusQueryMustBeReadOnlyAndBoundToAnOperation(t *testing.T) {
	directory := t.TempDir()
	files := []string{filepath.Join(directory, "compose.yaml")}
	project := ComposeProjectRef{Name: "demo", WorkingDirectory: directory, ConfigFiles: files}
	project.Key = ComposeProjectKey(project.Name, project.WorkingDirectory, project.ConfigFiles)
	request := ComposeRequest{OperationID: "00000000-0000-4000-8000-000000000002", Action: ComposeEditStatus, Project: project,
		Editor: &ComposeEditorInput{TargetOperationID: "00000000-0000-4000-8000-000000000001"}}
	if err := ValidateComposeRequest(request); err != nil {
		t.Fatalf("valid result query rejected: %v", err)
	}
	invalid := request
	invalid.Editor = &ComposeEditorInput{TargetOperationID: "../../outside"}
	if err := ValidateComposeRequest(invalid); err == nil {
		t.Fatal("result query accepted a path-like operation target")
	}
	invalid = request
	invalid.Editor = &ComposeEditorInput{TargetOperationID: request.Editor.TargetOperationID, PortEdits: []ComposePortEdit{{Service: "web"}}}
	if err := ValidateComposeRequest(invalid); err == nil {
		t.Fatal("read-only result query accepted a mutation")
	}
	success := ComposeResponse{OperationID: request.OperationID, Status: "succeeded", Verified: true, Editor: &ComposeEditorResult{}}
	if err := ValidateComposeResponse(success, request); err != nil {
		t.Fatalf("verified read-only result was rejected: %v", err)
	}
}
