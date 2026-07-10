package cli

import "testing"

func TestProfileCommandsAlwaysSkipSelfUpdate(t *testing.T) {
	root := newRoot()
	profileCmd, _, err := root.Find([]string{"profile"})
	if err != nil {
		t.Fatal(err)
	}
	if !skipSelfUpdate(profileCmd) {
		t.Fatal("profile group must skip self-update")
	}
	for _, child := range profileCmd.Commands() {
		if !skipSelfUpdate(child) {
			t.Errorf("profile %s must skip self-update", child.Name())
		}
	}
}
