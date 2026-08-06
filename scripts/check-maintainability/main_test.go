package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRepositoryMaintainabilityRatchets(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	if _, err := validateRepositoryManifest(root); err != nil {
		t.Fatal(err)
	}
}

func TestRunReportsPhysicalFileAndFunctionSpans(t *testing.T) {
	root := writeMaintainabilityFixture(t)
	var output bytes.Buffer
	if err := run(root, &output); err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode report: %v\n%s", err, output.String())
	}
	if got.Status != "ok" || got.TimingMode != "observe" || got.TimingObservations != 2 {
		t.Fatalf("unexpected report: %+v", got)
	}
	if len(got.Measurements) != 15 {
		t.Fatalf("measurements=%d want 15", len(got.Measurements))
	}
	if got.Measurements[0].Lines != 3 || got.Measurements[1].Lines != 1 {
		t.Fatalf("app measurements=%+v want file=3 function=1", got.Measurements[:2])
	}
}

func TestMaintainabilityRatchetsRejectGrowthAndRemovedControl(t *testing.T) {
	t.Run("file growth", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		m := readFixtureManifest(t, root)
		m.Hotspots[0].MaxLines = 2
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "exceeds reviewed maximum")
	})

	t.Run("function growth", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		path := filepath.Join(root, "internal", "app", "a.go")
		writeTestFile(t, path, "package app\n\nfunc appHotspot() {\n\tprintln(\"one\")\n\tprintln(\"two\")\n}\n")
		m := readFixtureManifest(t, root)
		m.Hotspots[1].MaxLines = 3
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "exceeds reviewed maximum")
	})

	t.Run("package growth", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		m := readFixtureManifest(t, root)
		m.PackageTotals[0].MaxLines = 2
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "exceeds reviewed maximum")
	})

	t.Run("function removed", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		writeTestFile(t, filepath.Join(root, "internal", "app", "a.go"), "package app\n")
		assertMaintainabilityError(t, root, "function was not found")
	})
}

func TestMaintainabilityManifestRejectsInvalidRows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifest)
		want   string
	}{
		{name: "owner mapping", mutate: func(m *manifest) { m.Hotspots[len(m.Hotspots)-1].Owner = "unknown" }, want: "invalid owner/path mapping"},
		{name: "empty rationale", mutate: func(m *manifest) { m.Hotspots[0].Rationale = " " }, want: "empty rationale"},
		{name: "nonpositive maximum", mutate: func(m *manifest) { m.Hotspots[0].MaxLines = 0 }, want: "must be positive"},
		{name: "duplicate", mutate: func(m *manifest) { m.Hotspots = append(m.Hotspots, m.Hotspots[len(m.Hotspots)-1]) }, want: "duplicate hotspot"},
		{name: "unsorted", mutate: func(m *manifest) { m.Hotspots[0], m.Hotspots[2] = m.Hotspots[2], m.Hotspots[0] }, want: "must be sorted"},
		{name: "missing owner kind", mutate: func(m *manifest) { m.Hotspots = m.Hotspots[1:] }, want: "must have selected file and function"},
		{name: "owner declaration drift", mutate: func(m *manifest) { m.Owners[0].PathPrefixes = []string{"internal/"} }, want: "reviewed sorted owner/path mapping"},
		{name: "legacy evaluator owner prefix", mutate: func(m *manifest) { m.Owners[3].PathPrefixes = append(m.Owners[3].PathPrefixes, "scripts/agent-eval/") }, want: "reviewed sorted owner/path mapping"},
		{name: "missing package owner", mutate: func(m *manifest) { m.PackageTotals = m.PackageTotals[1:] }, want: "one row for each"},
		{name: "duplicate package owner", mutate: func(m *manifest) {
			m.PackageTotals[1].Owner = m.PackageTotals[0].Owner
			m.PackageTotals[1].Path = m.PackageTotals[0].Path
		}, want: "duplicated"},
		{name: "package owner mapping", mutate: func(m *manifest) { m.PackageTotals[0].Path = "internal/cli/" }, want: "invalid owner/path mapping"},
		{name: "package subtree", mutate: func(m *manifest) { m.PackageTotals[0].Path = "internal/app/subpackage/" }, want: "invalid owner/path mapping"},
		{name: "package traversal", mutate: func(m *manifest) { m.PackageTotals[0].Path = "internal/app/../cli/" }, want: "invalid owner/path mapping"},
		{name: "package empty rationale", mutate: func(m *manifest) { m.PackageTotals[0].Rationale = " " }, want: "positive maximum and rationale"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeMaintainabilityFixture(t)
			m := readFixtureManifest(t, root)
			test.mutate(&m)
			writeFixtureManifest(t, root, m)
			assertMaintainabilityError(t, root, test.want)
		})
	}
}

func TestMaintainabilityManifestRejectsUnsafePaths(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		m := readFixtureManifest(t, root)
		m.Hotspots[0].Path = "internal/app/missing.go"
		m.Hotspots[1].Path = "internal/app/missing.go"
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "inspect path component")
	})

	t.Run("non Go", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		writeTestFile(t, filepath.Join(root, "internal", "app", "a.txt"), "not Go\n")
		m := readFixtureManifest(t, root)
		m.Hotspots[0].Path = "internal/app/a.txt"
		m.Hotspots[1].Path = "internal/app/a.txt"
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "production Go file")
	})

	t.Run("test file", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		writeTestFile(t, filepath.Join(root, "internal", "app", "a_test.go"), "package app\n\nfunc appHotspot() {}\n")
		m := readFixtureManifest(t, root)
		m.Hotspots[0].Path = "internal/app/a_test.go"
		m.Hotspots[1].Path = "internal/app/a_test.go"
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "production Go file")
	})

	t.Run("symlink", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		target := filepath.Join(root, "internal", "app", "target.go")
		writeTestFile(t, target, "package app\n\nfunc appHotspot() {}\n")
		link := filepath.Join(root, "internal", "app", "link.go")
		if err := os.Symlink("target.go", link); err != nil {
			t.Fatal(err)
		}
		m := readFixtureManifest(t, root)
		m.Hotspots[0].Path = "internal/app/link.go"
		m.Hotspots[1].Path = "internal/app/link.go"
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "symlinked paths are not allowed")
	})
}

func TestTimingObservationsAreObserveOnlyAndReferenceMakeTargets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifest)
		want   string
	}{
		{name: "enforcement mode", mutate: func(m *manifest) { m.Timing.Mode = "enforce" }, want: "observe-only"},
		{name: "missing target", mutate: func(m *manifest) { m.Timing.Observations[0].MakeTarget = "agent-eval-race-missing" }, want: "missing Make target"},
		{name: "invalid source", mutate: func(m *manifest) { m.Timing.Observations[0].Source = "local" }, want: "invalid hosted source evidence"},
		{name: "invalid revision", mutate: func(m *manifest) { m.Timing.Observations[0].Revision = "main" }, want: "invalid revision"},
		{name: "invalid time", mutate: func(m *manifest) { m.Timing.Observations[0].ObservedAt = "today" }, want: "invalid observed_at"},
		{name: "invalid duration", mutate: func(m *manifest) { m.Timing.Observations[0].DurationSeconds = 0 }, want: "positive duration"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeMaintainabilityFixture(t)
			m := readFixtureManifest(t, root)
			test.mutate(&m)
			writeFixtureManifest(t, root, m)
			assertMaintainabilityError(t, root, test.want)
		})
	}

	t.Run("runtime threshold field", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		path := filepath.Join(root, manifestPath)
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		contents = bytes.Replace(contents, []byte(`"mode": "observe"`), []byte(`"mode": "observe", "maximum_seconds": 1`), 1)
		writeTestFile(t, path, string(contents))
		assertMaintainabilityError(t, root, "unknown field")
	})
}

func validateRepositoryManifest(root string) ([]measurement, error) {
	m, err := readManifest(filepath.Join(root, manifestPath))
	if err != nil {
		return nil, err
	}
	return validateManifest(root, m)
}

func writeMaintainabilityFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"internal/app/a.go":           "package app\n\nfunc appHotspot() {}\n",
		"internal/cli/c.go":           "package cli\n\nfunc cliHotspot() {}\n",
		"internal/contentpolicy/p.go": "package contentpolicy\n\nfunc policyHotspot() {}\n",
		"internal/agenteval/e.go":     "package agenteval\n\nfunc evalHotspot() {}\n",
		"internal/mcpserver/m.go":     "package mcpserver\n\nfunc mcpHotspot() {}\n",
		"Makefile":                    "agent-eval-race:\n\t@true\n\ncheck-core-race-coverage:\n\t@true\n",
	}
	for path, contents := range files {
		writeTestFile(t, filepath.Join(root, filepath.FromSlash(path)), contents)
	}
	m := manifest{
		SchemaVersion: 1,
		Owners:        append([]owner(nil), reviewedOwners...),
		Hotspots: []hotspot{
			{Owner: "app", Path: "internal/app/a.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "app", Path: "internal/app/a.go", Function: "appHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "cli", Path: "internal/cli/c.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "cli", Path: "internal/cli/c.go", Function: "cliHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "contentpolicy", Path: "internal/contentpolicy/p.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "contentpolicy", Path: "internal/contentpolicy/p.go", Function: "policyHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "evaluator", Path: "internal/agenteval/e.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "evaluator", Path: "internal/agenteval/e.go", Function: "evalHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "mcp", Path: "internal/mcpserver/m.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "mcp", Path: "internal/mcpserver/m.go", Function: "mcpHotspot", MaxLines: 5, Rationale: "fixture function"},
		},
		PackageTotals: []packageTotal{
			{Owner: "app", Path: "internal/app/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "cli", Path: "internal/cli/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "contentpolicy", Path: "internal/contentpolicy/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "evaluator", Path: "internal/agenteval/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "mcp", Path: "internal/mcpserver/", MaxLines: 10, Rationale: "fixture package"},
		},
		Timing: timingObservations{Mode: "observe", Observations: []timingObservation{
			{MakeTarget: "agent-eval-race", Source: "github_actions_ubuntu_step", WorkflowRunID: 1, Revision: strings.Repeat("a", 40), ObservedAt: "2026-08-03T00:00:00Z", DurationSeconds: 2, Rationale: "fixture observation"},
			{MakeTarget: "check-core-race-coverage", Source: "github_actions_ubuntu_step", WorkflowRunID: 1, Revision: strings.Repeat("a", 40), ObservedAt: "2026-08-03T00:00:01Z", DurationSeconds: 1, Rationale: "fixture observation"},
		}},
	}
	writeFixtureManifest(t, root, m)
	return root
}

func readFixtureManifest(t *testing.T, root string) manifest {
	t.Helper()
	m, err := readManifest(filepath.Join(root, manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func writeFixtureManifest(t *testing.T, root string, m manifest) {
	t.Helper()
	contents, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	path := filepath.Join(root, manifestPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertMaintainabilityError(t *testing.T, root, want string) {
	t.Helper()
	var output bytes.Buffer
	err := run(root, &output)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error=%v want substring %q", err, want)
	}
	if output.Len() != 0 {
		t.Fatalf("failure emitted success output: %q", output.String())
	}
}
