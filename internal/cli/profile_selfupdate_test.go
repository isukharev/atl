package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

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

func TestCobraDiagnosticBuiltinsSkipSelfUpdate(t *testing.T) {
	root := newRoot()
	for _, args := range [][]string{{"help"}, {"completion", "bash"}} {
		cmd, _, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		if !skipSelfUpdate(cmd) {
			t.Errorf("%s must skip self-update", cmd.CommandPath())
		}
	}
	if !skipSelfUpdate(&cobra.Command{Use: cobra.ShellCompRequestCmd}) {
		t.Error("hidden completion request must skip self-update")
	}
}
