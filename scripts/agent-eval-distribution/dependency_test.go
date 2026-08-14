package main

import "testing"

func TestDependencyIdentityBindsModuleClosureOnly(t *testing.T) {
	base := map[string][]byte{
		"internal/agenteval/go.mod":      []byte("module example.test/agent-eval\n\ngo 1.26.6\n"),
		"internal/agenteval/go.sum":      []byte("example.test/dependency v1.2.3 h1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=\n"),
		"internal/agenteval/ignored.txt": []byte("not part of dependency closure\n"),
	}
	first, err := dependencyIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	if !validDigest(first) {
		t.Fatalf("dependency identity is not a canonical digest: %q", first)
	}
	changedSum := cloneByteMap(base)
	changedSum["internal/agenteval/go.sum"] = []byte("example.test/dependency v1.2.4 h1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb=\n")
	changed, err := dependencyIdentity(changedSum)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("go.sum mutation did not change the dependency identity")
	}
	changedModule := cloneByteMap(base)
	changedModule["internal/agenteval/go.mod"] = []byte("module example.test/agent-eval\n\ngo 1.26.7\n")
	changed, err = dependencyIdentity(changedModule)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("go.mod mutation did not change the dependency identity")
	}
	compatibilityOnly := cloneByteMap(base)
	compatibilityOnly["compatibility-bundle.json"] = []byte("changed compatibility")
	changed, err = dependencyIdentity(compatibilityOnly)
	if err != nil {
		t.Fatal(err)
	}
	if changed != first {
		t.Fatal("non-module mutation changed the dependency identity")
	}
	if _, err := dependencyIdentity(map[string][]byte{"internal/agenteval/go.mod": base["internal/agenteval/go.mod"]}); err == nil {
		t.Fatal("dependency identity accepted a selection without go.sum")
	}
	duplicate := cloneByteMap(base)
	duplicate["go.mod"] = duplicate["internal/agenteval/go.mod"]
	if _, err := dependencyIdentity(duplicate); err == nil {
		t.Fatal("dependency identity accepted multiple module roots")
	}
}

func cloneByteMap(input map[string][]byte) map[string][]byte {
	output := make(map[string][]byte, len(input))
	for name, data := range input {
		output[name] = append([]byte(nil), data...)
	}
	return output
}
