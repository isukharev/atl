package cli

import (
	"reflect"
	"testing"
)

func TestRepositoryCommandInventory(t *testing.T) {
	commands, err := RepositoryCommandInventory()
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) == 0 {
		t.Fatal("repository command inventory is empty")
	}
	for index, command := range commands {
		if command.Path == "" || index > 0 && commands[index-1].Path >= command.Path {
			t.Fatalf("inventory is empty, duplicated, or unsorted at %+v", command)
		}
		if command.EffectProfile == "" {
			t.Fatalf("command %q has no effect profile", command.Path)
		}
		registration, ok := commandRegistry.nodes[command.Path]
		if !ok || registration.traits&commandLeaf == 0 {
			t.Fatalf("inventory contains non-leaf %q", command.Path)
		}
		if command.Access == "read-only" && command.MutationProfile != "" {
			t.Fatalf("read-only command %q has mutation profile %q", command.Path, command.MutationProfile)
		}
		if command.Access == "mutating" && !validMutationProfile(mutationProfile(command.MutationProfile)) {
			t.Fatalf("mutating command %q has invalid profile %q", command.Path, command.MutationProfile)
		}
		if want := reviewedOutputModes(command.Path); !reflect.DeepEqual(command.OutputModes, want) {
			t.Fatalf("command %q output modes=%v want=%v", command.Path, command.OutputModes, want)
		}
	}

	commands[0].RequiredFlags = append(commands[0].RequiredFlags, "mutated")
	commands[0].OutputModes = append(commands[0].OutputModes, "mutated")
	again, err := RepositoryCommandInventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range again[0].RequiredFlags {
		if flag == "mutated" {
			t.Fatal("inventory exposes registry-owned flag storage")
		}
	}
	for _, mode := range again[0].OutputModes {
		if mode == "mutated" {
			t.Fatal("inventory exposes registry-owned output mode storage")
		}
	}
	if !reflect.DeepEqual(again, mustRepositoryCommandInventory(t)) {
		t.Fatal("repository command inventory is not deterministic")
	}
}

func mustRepositoryCommandInventory(t *testing.T) []RepositoryCommand {
	t.Helper()
	commands, err := RepositoryCommandInventory()
	if err != nil {
		t.Fatal(err)
	}
	return commands
}

func TestRepositoryFlagInventory(t *testing.T) {
	flags, err := RepositoryFlagInventory()
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) == 0 {
		t.Fatal("repository flag inventory is empty")
	}
	seen := map[string]RepositoryFlag{}
	previous := ""
	for _, flag := range flags {
		key := repositoryFlagKey(flag.Command, flag.Name)
		if flag.Name == "" || key <= previous {
			t.Fatalf("inventory is empty, duplicated, or unsorted at %+v", flag)
		}
		previous = key
		switch flag.Class {
		case RepositoryFlagVisible, RepositoryFlagHidden, RepositoryFlagDeprecated, RepositoryFlagFramework:
		default:
			t.Fatalf("flag has invalid class: %+v", flag)
		}
		seen[key] = flag
	}

	want := []RepositoryFlag{
		{Command: "", Name: "help", Class: RepositoryFlagFramework},
		{Command: "", Name: "output", Class: RepositoryFlagVisible},
		{Command: "", Name: "read-only", Class: RepositoryFlagVisible},
		{Command: "", Name: "verbose", Class: RepositoryFlagVisible},
		{Command: "", Name: "version", Class: RepositoryFlagVisible},
		{Command: "completion bash", Name: "no-descriptions", Class: RepositoryFlagVisible},
		{Command: "conf pull", Name: "help", Class: RepositoryFlagFramework},
		{Command: "conf pull", Name: "time-zone", Class: RepositoryFlagHidden},
	}
	for _, expected := range want {
		if got := seen[repositoryFlagKey(expected.Command, expected.Name)]; got != expected {
			t.Errorf("flag %q on %q = %+v, want %+v", expected.Name, expected.Command, got, expected)
		}
	}
	if _, duplicated := seen[repositoryFlagKey("conf pull", "output")]; duplicated {
		t.Fatal("root --output flag was duplicated onto a leaf")
	}

	again, err := RepositoryFlagInventory()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(flags, again) {
		t.Fatal("repository flag inventory is not deterministic")
	}
}
