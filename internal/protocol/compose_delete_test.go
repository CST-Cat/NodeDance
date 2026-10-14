package protocol

import "testing"

func TestComposeDeletionRequiresConfirmationAndProjectResourceLock(t *testing.T) {
	ref := ComposeProjectRef{Name: "app", WorkingDirectory: "/srv/app", ConfigFiles: []string{"/srv/app/compose.yaml"}}
	ref.Key = ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	intent := TaskIntent{Action: TaskComposeDelete, ContainerID: ref.Key, Compose: &ComposeTaskSpec{Project: ref}}
	if ValidateTaskIntent(intent) == nil {
		t.Fatal("unconfirmed deletion accepted")
	}
	intent.DeleteConfirmed = true
	if err := ValidateTaskIntent(intent); err != nil {
		t.Fatal(err)
	}
	identity, err := TaskIdentity("task", "node", "intent", intent)
	if err != nil || identity.ResourceKey != "docker-compose-project:"+ref.Key {
		t.Fatal(identity, err)
	}
	intent.Compose.Project.Key = ComposeProjectKey("other", ref.WorkingDirectory, ref.ConfigFiles)
	if ValidateTaskIntent(intent) == nil {
		t.Fatal("mismatched project accepted")
	}
}
