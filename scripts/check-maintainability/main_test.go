package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	if got.Status != "ok" || got.Timing.Mode != "observe" || got.Timing.Observations != 2 {
		t.Fatalf("unexpected report: %+v", got)
	}
	if len(got.Hotspots) != 22 || len(got.PackageTotals) != 14 {
		t.Fatalf("hotspots=%d package_totals=%d want 22 and 14", len(got.Hotspots), len(got.PackageTotals))
	}
	appFile := got.Hotspots[hotspotIndex(t, readFixtureManifest(t, root), "app", "")]
	appFunction := got.Hotspots[hotspotIndex(t, readFixtureManifest(t, root), "app", "appHotspot")]
	if appFile.Lines != reviewedLargeFileThreshold || appFunction.Lines != 1 {
		t.Fatalf("app measurements=%+v %+v want file=%d function=1", appFile, appFunction, reviewedLargeFileThreshold)
	}
	if got.ChangeSurface.ProductionFiles != 14 || len(got.ChangeSurface.LargeFiles) != 1 || got.ChangeSurface.LargeFiles[0].Path != "internal/app/a.go" {
		t.Fatalf("unexpected change-surface report: %+v", got.ChangeSurface)
	}
}

func TestRunRetainsLegacyJSONProjection(t *testing.T) {
	root := writeMaintainabilityFixture(t)
	var output bytes.Buffer
	if err := run(root, &output); err != nil {
		t.Fatal(err)
	}
	var repeated bytes.Buffer
	if err := run(root, &repeated); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), repeated.Bytes()) {
		t.Fatal("success JSON is not deterministic")
	}

	var wire map[string]any
	if err := json.Unmarshal(output.Bytes(), &wire); err != nil {
		t.Fatalf("decode generic report: %v\n%s", err, output.String())
	}
	legacyMeasurements, ok := wire["measurements"].([]any)
	if !ok {
		t.Fatalf("legacy measurements missing or invalid: %#v", wire["measurements"])
	}
	hotspots, ok := wire["hotspots"].([]any)
	if !ok {
		t.Fatalf("categorized hotspots missing or invalid: %#v", wire["hotspots"])
	}
	packageTotals, ok := wire["package_totals"].([]any)
	if !ok {
		t.Fatalf("categorized package_totals missing or invalid: %#v", wire["package_totals"])
	}
	wantMeasurements := append(append([]any(nil), hotspots...), packageTotals...)
	if !reflect.DeepEqual(legacyMeasurements, wantMeasurements) {
		t.Fatalf("legacy measurements differ from ordered hotspot and package measurements")
	}
	timing, ok := wire["timing"].(map[string]any)
	if !ok {
		t.Fatalf("categorized timing missing or invalid: %#v", wire["timing"])
	}
	if wire["timing_mode"] != timing["mode"] || wire["timing_observations"] != timing["observations"] {
		t.Fatalf("legacy timing aliases differ: mode=%#v/%#v observations=%#v/%#v", wire["timing_mode"], timing["mode"], wire["timing_observations"], timing["observations"])
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
		m.Hotspots[hotspotIndex(t, m, "app", "appHotspot")].MaxLines = 3
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

func TestChangeSurfaceRejectsUnreviewedLargeFileAndExclusionGrowth(t *testing.T) {
	t.Run("new large file requires review", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		writeTestFile(t, filepath.Join(root, "internal", "other", "large.go"), largeProductionFile("other", reviewedLargeFileThreshold))
		assertMaintainabilityError(t, root, `large production file "internal/other/large.go" is 750 lines and has no hotspot or exclusion`)
	})

	t.Run("bounded exclusion passes then rejects growth", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		path := filepath.Join(root, "internal", "other", "large.go")
		writeTestFile(t, path, largeProductionFile("other", reviewedLargeFileThreshold))
		m := readFixtureManifest(t, root)
		m.ChangeSurface.Exclusions = []changeSurfaceExclusion{{
			Path:      "internal/other/large.go",
			MaxLines:  reviewedLargeFileThreshold,
			Rationale: "focused fixture exclusion",
		}}
		writeFixtureManifest(t, root, m)
		var output bytes.Buffer
		if err := run(root, &output); err != nil {
			t.Fatalf("bounded exclusion should pass: %v", err)
		}
		writeTestFile(t, path, largeProductionFile("other", reviewedLargeFileThreshold+1))
		assertMaintainabilityError(t, root, `change_surface exclusion "internal/other/large.go" is 751 lines, exceeds reviewed maximum 750`)
	})

	t.Run("split makes exclusion stale", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		path := filepath.Join(root, "internal", "other", "large.go")
		writeTestFile(t, path, largeProductionFile("other", reviewedLargeFileThreshold-1))
		m := readFixtureManifest(t, root)
		m.ChangeSurface.Exclusions = []changeSurfaceExclusion{{
			Path:      "internal/other/large.go",
			MaxLines:  reviewedLargeFileThreshold,
			Rationale: "focused fixture exclusion",
		}}
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, `change_surface exclusion "internal/other/large.go" is stale`)
	})
}

func TestChangeSurfaceAndNoHeadroomControlsCannotBeRelaxedSilently(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifest)
		want   string
	}{
		{name: "threshold", mutate: func(m *manifest) { m.ChangeSurface.LargeFileThreshold++ }, want: "want reviewed threshold 750"},
		{name: "roots", mutate: func(m *manifest) { m.ChangeSurface.PathPrefixes = []string{"internal/app/"} }, want: "retain the reviewed production and tooling roots"},
		{name: "no headroom", mutate: func(m *manifest) {
			item := &m.Hotspots[hotspotIndex(t, *m, "app", "appHotspot")]
			item.NoHeadroom = true
			item.MaxLines = 2
		}, want: "marked no_headroom"},
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
		{name: "legacy evaluator owner prefix", mutate: func(m *manifest) {
			for i := range m.Owners {
				if m.Owners[i].ID == "evaluator" {
					m.Owners[i].PathPrefixes = append(m.Owners[i].PathPrefixes, "scripts/agent-eval/")
					return
				}
			}
		}, want: "reviewed sorted owner/path mapping"},
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
		m.Hotspots[hotspotIndex(t, m, "app", "")].Path = "internal/app/missing.go"
		m.Hotspots[hotspotIndex(t, m, "app", "appHotspot")].Path = "internal/app/missing.go"
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "inspect path component")
	})

	t.Run("non Go", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		writeTestFile(t, filepath.Join(root, "internal", "app", "a.txt"), "not Go\n")
		m := readFixtureManifest(t, root)
		m.Hotspots[hotspotIndex(t, m, "app", "")].Path = "internal/app/a.txt"
		m.Hotspots[hotspotIndex(t, m, "app", "appHotspot")].Path = "internal/app/a.txt"
		writeFixtureManifest(t, root, m)
		assertMaintainabilityError(t, root, "production Go file")
	})

	t.Run("test file", func(t *testing.T) {
		root := writeMaintainabilityFixture(t)
		writeTestFile(t, filepath.Join(root, "internal", "app", "a_test.go"), "package app\n\nfunc appHotspot() {}\n")
		m := readFixtureManifest(t, root)
		m.Hotspots[hotspotIndex(t, m, "app", "")].Path = "internal/app/a_test.go"
		m.Hotspots[hotspotIndex(t, m, "app", "appHotspot")].Path = "internal/app/a_test.go"
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
		m.Hotspots[hotspotIndex(t, m, "app", "")].Path = "internal/app/link.go"
		m.Hotspots[hotspotIndex(t, m, "app", "appHotspot")].Path = "internal/app/link.go"
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
	hotspots, packageTotals, _, err := validateManifest(root, m)
	return append(hotspots, packageTotals...), err
}

func writeMaintainabilityFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"internal/adapter/confluence/a.go":       "package confluence\n\nfunc confluenceAdapterHotspot() {}\n",
		"internal/adapter/jira/j.go":             "package jira\n\nfunc jiraAdapterHotspot() {}\n",
		"internal/app/a.go":                      "package app\n\nfunc appHotspot() {}\n" + strings.Repeat("// filler\n", reviewedLargeFileThreshold-3),
		"internal/cli/c.go":                      "package cli\n\nfunc cliHotspot() {}\n",
		"internal/compose/c.go":                  "package compose\n\nfunc composeHotspot() {}\n",
		"internal/contentpolicy/p.go":            "package contentpolicy\n\nfunc policyHotspot() {}\n",
		"internal/agenteval/e.go":                "package agenteval\n\nfunc evalHotspot() {}\n",
		"internal/httpx/h.go":                    "package httpx\n\nfunc httpxHotspot() {}\n",
		"internal/mcpserver/m.go":                "package mcpserver\n\nfunc mcpHotspot() {}\n",
		"internal/mirror/r.go":                   "package mirror\n\nfunc mirrorHotspot() {}\n",
		"scripts/check-maintainability/m.go":     "package main\n\nfunc toolingHotspot() {}\n",
		"scripts/check-maintainer-contract/m.go": "package main\n",
		"scripts/check-docs-freshness/m.go":      "package main\n",
		"scripts/gen-plugins/m.go":               "package main\n",
		"Makefile":                               "agent-eval-race:\n\t@true\n\ncheck-core-race-coverage:\n\t@true\n",
	}
	for path, contents := range files {
		writeTestFile(t, filepath.Join(root, filepath.FromSlash(path)), contents)
	}
	m := manifest{
		SchemaVersion: 1,
		Owners:        append([]owner(nil), reviewedOwners...),
		Hotspots: []hotspot{
			{Owner: "adapter-confluence", Path: "internal/adapter/confluence/a.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "adapter-confluence", Path: "internal/adapter/confluence/a.go", Function: "confluenceAdapterHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "adapter-jira", Path: "internal/adapter/jira/j.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "adapter-jira", Path: "internal/adapter/jira/j.go", Function: "jiraAdapterHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "app", Path: "internal/app/a.go", MaxLines: reviewedLargeFileThreshold, Rationale: "fixture file"},
			{Owner: "app", Path: "internal/app/a.go", Function: "appHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "cli", Path: "internal/cli/c.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "cli", Path: "internal/cli/c.go", Function: "cliHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "compose", Path: "internal/compose/c.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "compose", Path: "internal/compose/c.go", Function: "composeHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "contentpolicy", Path: "internal/contentpolicy/p.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "contentpolicy", Path: "internal/contentpolicy/p.go", Function: "policyHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "evaluator", Path: "internal/agenteval/e.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "evaluator", Path: "internal/agenteval/e.go", Function: "evalHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "httpx", Path: "internal/httpx/h.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "httpx", Path: "internal/httpx/h.go", Function: "httpxHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "mcp", Path: "internal/mcpserver/m.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "mcp", Path: "internal/mcpserver/m.go", Function: "mcpHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "mirror", Path: "internal/mirror/r.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "mirror", Path: "internal/mirror/r.go", Function: "mirrorHotspot", MaxLines: 5, Rationale: "fixture function"},
			{Owner: "tooling", Path: "scripts/check-maintainability/m.go", MaxLines: 10, Rationale: "fixture file"},
			{Owner: "tooling", Path: "scripts/check-maintainability/m.go", Function: "toolingHotspot", MaxLines: 5, Rationale: "fixture function"},
		},
		PackageTotals: []packageTotal{
			{Owner: "adapter-confluence", Path: "internal/adapter/confluence/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "adapter-jira", Path: "internal/adapter/jira/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "app", Path: "internal/app/", MaxLines: reviewedLargeFileThreshold, Rationale: "fixture package"},
			{Owner: "cli", Path: "internal/cli/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "compose", Path: "internal/compose/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "contentpolicy", Path: "internal/contentpolicy/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "evaluator", Path: "internal/agenteval/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "httpx", Path: "internal/httpx/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "mcp", Path: "internal/mcpserver/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "mirror", Path: "internal/mirror/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "tooling", Path: "scripts/check-docs-freshness/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "tooling", Path: "scripts/check-maintainability/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "tooling", Path: "scripts/check-maintainer-contract/", MaxLines: 10, Rationale: "fixture package"},
			{Owner: "tooling", Path: "scripts/gen-plugins/", MaxLines: 10, Rationale: "fixture package"},
		},
		ChangeSurface: changeSurfacePolicy{PathPrefixes: append([]string(nil), reviewedChangeSurfacePrefixes...), LargeFileThreshold: reviewedLargeFileThreshold},
		Timing: timingObservations{Mode: "observe", Observations: []timingObservation{
			{MakeTarget: "agent-eval-race", Source: "github_actions_ubuntu_step", WorkflowRunID: 1, Revision: strings.Repeat("a", 40), ObservedAt: "2026-08-03T00:00:00Z", DurationSeconds: 2, Rationale: "fixture observation"},
			{MakeTarget: "check-core-race-coverage", Source: "github_actions_ubuntu_step", WorkflowRunID: 1, Revision: strings.Repeat("a", 40), ObservedAt: "2026-08-03T00:00:01Z", DurationSeconds: 1, Rationale: "fixture observation"},
		}},
	}
	writeFixtureManifest(t, root, m)
	return root
}

func hotspotIndex(t *testing.T, m manifest, owner, function string) int {
	t.Helper()
	for i, item := range m.Hotspots {
		if item.Owner == owner && item.Function == function {
			return i
		}
	}
	t.Fatalf("missing fixture hotspot owner=%q function=%q", owner, function)
	return -1
}

func largeProductionFile(packageName string, lines int) string {
	return "package " + packageName + "\n" + strings.Repeat("// measured filler\n", lines-1)
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
