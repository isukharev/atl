package cli

import "testing"

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
	}

	commands[0].RequiredFlags = append(commands[0].RequiredFlags, "mutated")
	again, err := RepositoryCommandInventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range again[0].RequiredFlags {
		if flag == "mutated" {
			t.Fatal("inventory exposes registry-owned flag storage")
		}
	}
}
