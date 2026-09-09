package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseOptionsIsClosed(t *testing.T) {
	want := options{shard: 2, source: strings.Repeat("a", 40)}
	got, err := parseOptions([]string{"-shard", "2", "-source", want.source})
	if err != nil || got != want {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	for _, args := range [][]string{
		nil,
		{"-shard", "-1", "-source", want.source},
		{"-shard", "4", "-source", want.source},
		{"-shard", "0", "-source", "main"},
		{"-shard", "0", "-source", want.source, "extra"},
		{"-shard", "0", "-source", want.source, "-run", "TestOne"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestRunRejectsInheritedBuildAndSelectionOverrides(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("hosted runner is Linux/amd64-only")
	}
	setBootstrapEnvironment(t)
	for key, value := range map[string]string{
		"GOENV": "", "GOFLAGS": "-run=TestOne", "GOOS": "darwin", "GOARCH": "arm64",
		"GOAMD64": "v4", "GOEXPERIMENT": "arenas", "CGO_ENABLED": "0",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			err := run(context.Background(), []string{"-shard", "0", "-source", strings.Repeat("a", 40)}, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "bootstrap environment") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestControlledEnvironmentDropsBackendAndProviderAuthority(t *testing.T) {
	canaries := []string{
		"ATL_JIRA_PAT", "ATL_CONFLUENCE_PAT", "ATL_JIRA_URL", "ATL_CONFLUENCE_URL",
		"ATL_CONFIG_DIR", "ATL_AGENT_EVAL_LIVE_CONFIG_DIR", "ATL_AGENT_EVAL_SYNTHETIC_CONFIG",
		"ATL_AGENT_EVAL_EXTERNAL_MCP_PROFILE", "ATL_BROKER_URL", "ATL_TEST_PAGE_ID",
		"JIRA_URL", "CONFLUENCE_URL", "TEST_JIRA_PAT", "OPENAI_API_KEY", "GH_TOKEN",
	}
	for _, key := range canaries {
		t.Setenv(key, "fixture-secret")
	}
	wantControls := map[string]string{
		"GOTOOLCHAIN": "auto", "GOWORK": "off", "GOENV": "off", "GOFLAGS": "",
		"GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1", "GOEXPERIMENT": "", "CGO_ENABLED": "1",
	}
	seen := map[string]string{}
	for _, item := range controlledEnvironment() {
		key, value, _ := strings.Cut(item, "=")
		if value == "fixture-secret" || strings.HasPrefix(key, "ATL_") {
			t.Fatalf("controlled environment retained authority key %s", key)
		}
		seen[key] = value
	}
	for key, value := range wantControls {
		if seen[key] != value {
			t.Fatalf("controlled %s=%q, want %q", key, seen[key], value)
		}
	}
	for _, key := range canaries {
		if _, ok := seen[key]; ok {
			t.Fatalf("controlled environment retained %s", key)
		}
	}
}

func TestControlledEnvironmentOverridesAmbientAndPersistedGoSettings(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("hosted runner is Linux/amd64-only")
	}
	envFile := filepath.Join(t.TempDir(), "go-env")
	writeFile(t, envFile, "GOAMD64=v4\nGOEXPERIMENT=not-a-real-experiment\nGOFLAGS=-tags=private\n")
	t.Setenv("GOENV", envFile)
	t.Setenv("GOAMD64", "v4")
	t.Setenv("GOEXPERIMENT", "not-a-real-experiment")
	t.Setenv("GOFLAGS", "-tags=private")
	body, stderr, err := captureCommand(context.Background(), commandSpec{
		dir: ".", name: "go",
		args: []string{"env", "-json", "GOENV", "GOAMD64", "GOEXPERIMENT", "GOFLAGS"}, env: controlledEnvironment(),
	}, 64<<10)
	if err != nil || len(stderr) != 0 {
		t.Fatalf("go env: %v: %s", err, stderr)
	}
	var got goEnvironment
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.GOENV != "" || got.GOAMD64 != "v1" || got.GOExperiment != "" || got.GOFlags != "" {
		t.Fatalf("effective Go environment=%+v", got)
	}
	if persisted, err := os.ReadFile(envFile); err != nil || !bytes.Contains(persisted, []byte("GOAMD64=v4")) {
		t.Fatalf("owned persistent environment changed: %v", err)
	}
}

func TestPartitionInventoryIsCompleteDisjointAndAutomatic(t *testing.T) {
	root := "example.test/evaluator"
	names := []string{"ExampleOne", "FuzzOne", "TestA", "TestB", "TestC", "TestD", "TestE", "TestNew", "TestZ"}
	items := make([]testItem, 0, len(names))
	for _, name := range names {
		items = append(items, testItem{Name: name, Kind: itemKind(name)})
	}
	inventory := testInventory{Packages: []string{root}, Items: map[string][]testItem{root: items}}
	partitions, err := partitionInventory(inventory, root)
	if err != nil {
		t.Fatal(err)
	}
	wantSizes := []int{3, 2, 2, 2}
	seen := map[string]bool{}
	for index, partition := range partitions {
		if len(partition) != wantSizes[index] {
			t.Fatalf("shard %d has %d names", index, len(partition))
		}
		for _, name := range partition {
			if seen[name] {
				t.Fatalf("duplicate %s", name)
			}
			seen[name] = true
		}
	}
	if len(seen) != len(names) || !seen["TestNew"] || !seen["ExampleOne"] || !seen["FuzzOne"] {
		t.Fatalf("union=%v", seen)
	}
	if _, err := partitionInventory(testInventory{Packages: []string{root}, Items: map[string][]testItem{root: items[:3]}}, root); err == nil {
		t.Fatal("accepted inventory smaller than shard count")
	}
	duplicate := append([]testItem(nil), items[:4]...)
	duplicate[3] = duplicate[2]
	if _, err := partitionInventory(testInventory{Packages: []string{root}, Items: map[string][]testItem{root: duplicate}}, root); err == nil {
		t.Fatal("accepted duplicate inventory")
	}
	unsorted := append([]testItem(nil), items[:4]...)
	unsorted[0], unsorted[1] = unsorted[1], unsorted[0]
	if _, err := partitionInventory(testInventory{Packages: []string{root}, Items: map[string][]testItem{root: unsorted}}, root); err == nil {
		t.Fatal("accepted unsorted inventory")
	}
}

func TestExactSelectorIsAnchoredEscapedAndBounded(t *testing.T) {
	got, err := exactSelector([]string{"TestA", "Test[Meta]", "FuzzOne", "ExampleOne"})
	if err != nil || got != `^(TestA|Test\[Meta\]|FuzzOne|ExampleOne)$` {
		t.Fatalf("selector=%q err=%v", got, err)
	}
	if _, err := exactSelector(nil); err == nil {
		t.Fatal("accepted empty selector")
	}
	if _, err := exactSelector([]string{"Test" + strings.Repeat("A", maxSelectorBytes)}); err == nil {
		t.Fatal("accepted oversized selector")
	}
}

func TestInventoryDigestBindsPackagesWithoutTests(t *testing.T) {
	first := testInventory{Packages: []string{"example.test/one"}, Items: map[string][]testItem{"example.test/one": nil}}
	second := testInventory{Packages: []string{"example.test/one", "example.test/two"}, Items: map[string][]testItem{"example.test/one": nil, "example.test/two": nil}}
	if inventoryDigest(first) == inventoryDigest(second) {
		t.Fatal("empty package did not affect inventory identity")
	}
}

func TestSplitRootPackageDerivesCompleteComplement(t *testing.T) {
	packages := []string{"example.test/eval/z", "example.test/eval", "example.test/eval/a"}
	root, others, err := splitRootPackage(packages)
	if err != nil || root != "example.test/eval" || !reflect.DeepEqual(others, []string{"example.test/eval/z", "example.test/eval/a"}) {
		t.Fatalf("root=%q others=%v err=%v", root, others, err)
	}
	if _, _, err := splitRootPackage([]string{"one", "two"}); err == nil {
		t.Fatal("accepted inventory without a common package root")
	}
}

func TestRaceCommandArgumentsAreExact(t *testing.T) {
	if got, want := raceCommandArgs([]string{"example.test/eval/a", "example.test/eval/b"}, ""),
		[]string{"test", "-json", "-race", "example.test/eval/a", "example.test/eval/b", "-count=1", "-timeout=45m"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("non-root args=%v", got)
	}
	if got, want := raceCommandArgs([]string{"."}, "^(TestA)$"),
		[]string{"test", "-json", "-race", ".", "-run", "^(TestA)$", "-count=1", "-timeout=45m"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root args=%v", got)
	}
}

func TestFailureTailIsBounded(t *testing.T) {
	buffer := newTailBuffer(8)
	if _, err := buffer.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if !buffer.overflow || string(buffer.body) != "23456789" {
		t.Fatalf("overflow=%v body=%q", buffer.overflow, buffer.body)
	}
}

func TestParseListEventsOwnsRunnableInventory(t *testing.T) {
	pkg := "example.test/eval"
	body := encodeEvents(t,
		goEvent{Action: "start", Package: pkg},
		goEvent{Action: "output", Package: pkg, Output: "TestB\nFuzzOne\nExampleOne\nTestA\n"},
		goEvent{Action: "output", Package: pkg, Output: "ok  \t" + pkg + "\t0.01s\n"},
		goEvent{Action: "pass", Package: pkg, Elapsed: .01},
	)
	inventory, err := parseListEvents(body, []string{pkg})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range inventory.Items[pkg] {
		got = append(got, item.Name+":"+item.Kind)
	}
	want := []string{"ExampleOne:example", "FuzzOne:fuzz", "TestA:test", "TestB:test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items=%v", got)
	}
	for name, mutate := range map[string]func([]goEvent) []goEvent{
		"duplicate":         func(events []goEvent) []goEvent { events[1].Output = "TestA\nTestA\n"; return events },
		"unexpected output": func(events []goEvent) []goEvent { events[1].Output = "private value\n"; return events },
		"executed":          func(events []goEvent) []goEvent { events[1].Test = "TestA"; return events },
		"missing terminal":  func(events []goEvent) []goEvent { return events[:3] },
		"wrong package":     func(events []goEvent) []goEvent { events[1].Package = "other"; return events },
	} {
		t.Run(name, func(t *testing.T) {
			events := []goEvent{{Action: "start", Package: pkg}, {Action: "output", Package: pkg, Output: "TestA\n"}, {Action: "output", Package: pkg, Output: "ok  \t" + pkg + "\t0.01s\n"}, {Action: "pass", Package: pkg}}
			if _, err := parseListEvents(encodeEvents(t, mutate(events)...), []string{pkg}); err == nil {
				t.Fatal("accepted invalid list events")
			}
		})
	}
}

func TestExecutionEventsRequireEverySelectedTerminalAndObserveSeeds(t *testing.T) {
	pkg := "example.test/eval"
	inventory := testInventory{Packages: []string{pkg}, Items: map[string][]testItem{pkg: {
		{Name: "TestOne", Kind: "test"}, {Name: "FuzzOne", Kind: "fuzz"}, {Name: "ExampleOne", Kind: "example"},
	}}}
	state := newExecutionState(inventory)
	for _, event := range []goEvent{
		{Action: "start", Package: pkg},
		{Action: "run", Package: pkg, Test: "TestOne"}, {Action: "pass", Package: pkg, Test: "TestOne", Elapsed: .2},
		{Action: "run", Package: pkg, Test: "FuzzOne"},
		{Action: "run", Package: pkg, Test: "FuzzOne/seed#0"}, {Action: "pass", Package: pkg, Test: "FuzzOne/seed#0"},
		{Action: "pass", Package: pkg, Test: "FuzzOne", Elapsed: .3},
		{Action: "run", Package: pkg, Test: "ExampleOne"}, {Action: "skip", Package: pkg, Test: "ExampleOne", Elapsed: .1},
		{Action: "pass", Package: pkg, Elapsed: .6},
	} {
		if err := state.consume(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.verify(); err != nil {
		t.Fatal(err)
	}
	summary := state.summary(.7)
	if summary.Tests != 1 || summary.FuzzTargets != 1 || summary.Examples != 1 || summary.ObservedSeeds != 1 || summary.Skipped != 1 || summary.SelectedCount != 3 {
		t.Fatalf("summary=%+v", summary)
	}
	missing := newExecutionState(inventory)
	if err := missing.verify(); err == nil {
		t.Fatal("accepted missing execution events")
	}
	unexpected := newExecutionState(inventory)
	if err := unexpected.consume(goEvent{Action: "run", Package: pkg, Test: "TestOther"}); err == nil {
		t.Fatal("accepted unselected runnable")
	}
	unknown := newExecutionState(inventory)
	if err := unknown.consume(goEvent{Action: "future", Package: pkg}); err == nil {
		t.Fatal("accepted unknown JSON action")
	}
	duplicate := newExecutionState(inventory)
	for _, event := range []goEvent{
		{Action: "start", Package: pkg},
		{Action: "run", Package: pkg, Test: "TestOne"},
		{Action: "pass", Package: pkg, Test: "TestOne"},
		{Action: "pass", Package: pkg, Test: "TestOne"},
		{Action: "pass", Package: pkg},
	} {
		if err := duplicate.consume(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := duplicate.verify(); err == nil {
		t.Fatal("accepted duplicate and incomplete terminal evidence")
	}
}

func TestFuzzSeedRunAndTerminalEvidenceAreDistinct(t *testing.T) {
	pkg := "example.test/eval"
	inventory := testInventory{Packages: []string{pkg}, Items: map[string][]testItem{
		pkg: {{Name: "FuzzOne", Kind: "fuzz"}},
	}}
	for name, seedEvents := range map[string][]goEvent{
		"two runs": {
			{Action: "run", Package: pkg, Test: "FuzzOne/seed#0"},
			{Action: "run", Package: pkg, Test: "FuzzOne/seed#0"},
		},
		"two terminals": {
			{Action: "pass", Package: pkg, Test: "FuzzOne/seed#0"},
			{Action: "skip", Package: pkg, Test: "FuzzOne/seed#0"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := newExecutionState(inventory)
			events := []goEvent{{Action: "start", Package: pkg}, {Action: "run", Package: pkg, Test: "FuzzOne"}}
			events = append(events, seedEvents...)
			events = append(events, goEvent{Action: "pass", Package: pkg, Test: "FuzzOne"}, goEvent{Action: "pass", Package: pkg})
			for _, event := range events {
				if err := state.consume(event); err != nil {
					t.Fatal(err)
				}
			}
			if err := state.verify(); err == nil {
				t.Fatal("accepted non-causal fuzz seed evidence")
			}
		})
	}
}

func TestModuleVersionAndActiveSourceIdentity(t *testing.T) {
	root := t.TempDir()
	goMod := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goMod, []byte("module example.test/x\n\ngo 1.26.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := moduleGoVersion(goMod); err != nil || got != "1.26.6" {
		t.Fatalf("version=%q err=%v", got, err)
	}
	body := []byte("package x\n")
	path := filepath.Join(root, "x.go")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := gitBlobSHA1(path, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	active := map[string]bool{path: true}
	if err := verifyActiveFiles(context.Background(), root, map[string]string{"x.go": digest}, active); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, []byte("// drift\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyActiveFiles(context.Background(), root, map[string]string{"x.go": digest}, active); err == nil {
		t.Fatal("accepted changed active source")
	}
	if err := verifyActiveFiles(context.Background(), root, map[string]string{}, active); err == nil {
		t.Fatal("accepted untracked active source")
	}
}

func TestCollectActiveFilesIncludesCompiledTestsAndEmbeds(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pkg")
	packages := []listedPackage{{
		Dir: dir, ImportPath: "example.test/pkg",
		GoFiles: []string{"pkg.go"}, TestGoFiles: []string{"pkg_test.go"},
		EmbedFiles: []string{"schema.json"}, TestEmbedFiles: []string{"testdata/fixture.json"},
	}}
	product := map[string]bool{}
	if err := collectActiveFiles(root, packages, product, productBuildSources); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"pkg.go", "schema.json"} {
		if !product[filepath.Join(dir, relative)] {
			t.Fatalf("product source omitted %s", relative)
		}
	}
	for _, relative := range []string{"pkg_test.go", "testdata/fixture.json"} {
		if product[filepath.Join(dir, relative)] {
			t.Fatalf("product source included unexecuted test input %s", relative)
		}
	}
	active := map[string]bool{}
	if err := collectActiveFiles(root, packages, active, evaluatorRaceSources); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"pkg.go", "pkg_test.go", "schema.json", "testdata/fixture.json"} {
		if !active[filepath.Join(dir, relative)] {
			t.Fatalf("evaluator race source omitted %s", relative)
		}
	}
	escape := packages
	escape[0].EmbedFiles = []string{"../../outside"}
	if err := collectActiveFiles(root, escape, map[string]bool{}, productBuildSources); err == nil {
		t.Fatal("accepted escaping embedded source")
	}
	if err := collectActiveFiles(root, packages, map[string]bool{}, sourceInventoryRole(99)); err == nil {
		t.Fatal("accepted unknown source inventory role")
	}
}

func TestCombinedActiveSourceInventoryRetainsWholeFileBound(t *testing.T) {
	product, evaluator := map[string]bool{}, map[string]bool{}
	for index := range maxSourceFiles {
		path := fmt.Sprintf("source-%d.go", index)
		product[path], evaluator[path] = true, true
	}
	if err := mergeActiveFiles(product, evaluator); err != nil || len(product) != maxSourceFiles {
		t.Fatalf("shared inputs must count once: %v", err)
	}
	if err := mergeActiveFiles(product, map[string]bool{"one-new-evaluator-file.go": true}); err == nil {
		t.Fatal("combined source inventory exceeded the whole-file bound")
	}
}

func TestVerboseDependentTestsFailClosed(t *testing.T) {
	for name, source := range map[string]string{
		"default": "package x\nimport \"testing\"\nfunc TestX(t *testing.T) { if testing.Verbose() {} }\n",
		"alias":   "package x\nimport testpkg \"testing\"\nfunc TestX(t *testpkg.T) { verbose := testpkg.Verbose; _ = verbose }\n",
		"dot":     "package x\nimport . \"testing\"\nfunc TestX(t *T) { _ = Verbose() }\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x_test.go")
			writeFile(t, path, source)
			if err := rejectVerboseTests(context.Background(), map[string]bool{path: true}); err == nil {
				t.Fatal("accepted output-mode-dependent test")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "x_test.go")
	writeFile(t, path, "package x\nimport \"testing\"\nfunc TestX(t *testing.T) {}\n")
	if err := rejectVerboseTests(context.Background(), map[string]bool{path: true}); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeResidueAllowsOnlyOwnedProductArtifact(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hosted runner is Linux-only")
	}
	root := t.TempDir()
	gitCommand(t, root, "init", "-q")
	gitCommand(t, root, "config", "user.email", "fixture@example.test")
	gitCommand(t, root, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/atl\n/hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, root, "add", ".gitignore")
	gitCommand(t, root, "commit", "-qm", "fixture")
	env := controlledEnvironment()
	if err := verifyWorktreeResidue(context.Background(), root, false, env); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "atl"), []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyWorktreeResidue(context.Background(), root, true, env); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hidden"), []byte("injected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyWorktreeResidue(context.Background(), root, true, env); err == nil {
		t.Fatal("accepted additional ignored residue")
	} else if strings.Contains(err.Error(), "hidden") {
		t.Fatal("residue diagnostic disclosed unexpected path metadata")
	}
}

func TestSourceCertificationRejectsInjectedActiveSource(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("hosted runner is Linux/amd64-only")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/product\n\ngo 1.26.6\n")
	writeFile(t, filepath.Join(root, "go.sum"), "")
	writeFile(t, filepath.Join(root, ".gitignore"), "/atl\n/injected_test.go\n")
	writeFile(t, filepath.Join(root, "cmd", "atl", "main.go"), "package main\nfunc main() {}\n")
	evaluator := filepath.Join(root, "internal", "agenteval")
	writeFile(t, filepath.Join(evaluator, "go.mod"), "module example.test/product/internal/agenteval\n\ngo 1.26.6\n")
	writeFile(t, filepath.Join(evaluator, "go.sum"), "")
	writeFile(t, filepath.Join(evaluator, "eval.go"), "package agenteval\n")
	writeFile(t, filepath.Join(evaluator, "eval_test.go"), "package agenteval\nimport \"testing\"\nfunc TestOne(t *testing.T) {}\n")
	gitCommand(t, root, "init", "-q")
	gitCommand(t, root, "config", "user.email", "fixture@example.test")
	gitCommand(t, root, "config", "user.name", "Fixture")
	gitCommand(t, root, "add", ".")
	gitCommand(t, root, "commit", "-qm", "fixture")
	source := gitCommand(t, root, "rev-parse", "HEAD")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if _, _, err := certifySource(context.Background(), root, evaluator, source, false, controlledEnvironment()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "injected_test.go"), "package product\n")
	if _, _, err := certifySource(context.Background(), root, evaluator, source, false, controlledEnvironment()); err == nil {
		t.Fatal("accepted ignored injected Go source")
	}
}

func TestSourceCertificationScopesVerboseGuardToEvaluatorTests(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("hosted runner is Linux/amd64-only")
	}
	setBootstrapEnvironment(t)
	root := syntheticRaceRepository(t)
	evaluator := filepath.Join(root, "internal", "agenteval")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	source := gitCommand(t, root, "rev-parse", "HEAD")
	if _, _, err := certifySource(context.Background(), root, evaluator, source, false, controlledEnvironment()); err != nil {
		t.Fatalf("product dependency test incorrectly entered evaluator guard: %v", err)
	}
	writeFile(t, filepath.Join(evaluator, "eval_test.go"), "package agenteval\nimport \"testing\"\nfunc TestOne(t *testing.T) { _ = testing.Verbose() }\n")
	gitCommand(t, root, "add", "internal/agenteval/eval_test.go")
	gitCommand(t, root, "commit", "-qm", "evaluator verbose fixture")
	source = gitCommand(t, root, "rev-parse", "HEAD")
	if _, _, err := certifySource(context.Background(), root, evaluator, source, false, controlledEnvironment()); err == nil || !strings.Contains(err.Error(), "must not branch on verbose") {
		t.Fatalf("evaluator verbose dependency error=%v", err)
	}
}

func TestProductArtifactMustBeRegularExecutableAndProvenanceBound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hosted runner is Linux-only")
	}
	root := t.TempDir()
	source := strings.Repeat("a", 40)
	path := filepath.Join(root, "atl")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"fixture\",\"commit\":\"" + source + "\",\"build_state\":\"clean\"}'\n"
	writeFile(t, path, script)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyProduct(context.Background(), root, source, controlledEnvironment()); err != nil {
		t.Fatal(err)
	}
	if err := verifyProduct(context.Background(), root, strings.Repeat("b", 40), controlledEnvironment()); err == nil {
		t.Fatal("accepted wrong product provenance")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", path); err != nil {
		t.Fatal(err)
	}
	if err := verifyProduct(context.Background(), root, source, controlledEnvironment()); err == nil {
		t.Fatal("accepted symlink product artifact")
	}
}

func TestSyntheticHostedShardsExecuteDerivedInventory(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("hosted runner is Linux/amd64-only")
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	setBootstrapEnvironment(t)
	for shard := range shardCount {
		t.Run(fmt.Sprintf("shard-%d", shard), func(t *testing.T) {
			root := syntheticRaceRepository(t)
			source := gitCommand(t, root, "rev-parse", "HEAD")
			if err := os.Chdir(root); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chdir(old) }()
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), []string{"-shard", fmt.Sprint(shard), "-source", source}, &stdout, &stderr); err != nil {
				t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
			}
			if bytes.Contains(stdout.Bytes(), []byte("success-output-marker")) || bytes.Contains(stderr.Bytes(), []byte("success-output-marker")) {
				t.Fatal("successful test output escaped the content-minimized runner")
			}
			var got evidence
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Source != source || got.Shard != shard || got.ShardCount != shardCount || got.Root.InventoryCount != 6 || got.Root.SelectedCount == 0 ||
				got.GOAMD64 != "v1" || got.GOEXPERIMENT != "" || got.GOENV != "off" || got.GOFLAGS != "" || got.CGOEnabled != "1" {
				t.Fatalf("evidence=%+v", got)
			}
			if shard == 0 {
				if got.NonRoot == nil || got.NonRoot.Packages != 1 || got.NonRoot.InventoryCount != 1 {
					t.Fatalf("non-root evidence=%+v", got.NonRoot)
				}
			} else if got.NonRoot != nil {
				t.Fatalf("unexpected non-root evidence=%+v", got.NonRoot)
			}
			if shard == 1 && got.Root.ObservedSeeds != 1 {
				t.Fatalf("observed fuzz seeds=%d, want 1", got.Root.ObservedSeeds)
			}
		})
	}
}

func setBootstrapEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"GOTOOLCHAIN": "auto", "GOWORK": "off", "GOENV": "off", "GOFLAGS": "",
		"GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1", "GOEXPERIMENT": "", "CGO_ENABLED": "1",
	} {
		t.Setenv(key, value)
	}
}

func syntheticRaceRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "/atl\n")
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/product\n\ngo 1.26.6\n")
	writeFile(t, filepath.Join(root, "go.sum"), "")
	writeFile(t, filepath.Join(root, "Makefile"), "build:\n\tenv -u GOROOT GOTOOLCHAIN=auto GOWORK=off CGO_ENABLED=0 go build -ldflags \"-X main.commit=$$(git rev-parse HEAD)\" -o atl ./cmd/atl\n")
	writeFile(t, filepath.Join(root, "cmd", "atl", "main.go"), `package main

import (
	"encoding/json"
	"os"

	"example.test/product/internal/productdep"
)

var commit = "unknown"

func main() {
	_ = productdep.Value
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
		"version": "fixture", "commit": commit, "build_state": "clean",
	})
}
`)
	writeFile(t, filepath.Join(root, "internal", "productdep", "dep.go"), "package productdep\nconst Value = 1\n")
	writeFile(t, filepath.Join(root, "internal", "productdep", "dep_cgo.go"), "//go:build cgo\n\npackage wrongcgo\n")
	writeFile(t, filepath.Join(root, "internal", "productdep", "dep_test.go"), "package productdep\nimport \"testing\"\nfunc TestVerboseProductDependency(t *testing.T) { _ = testing.Verbose() }\n")
	evaluator := filepath.Join(root, "internal", "agenteval")
	writeFile(t, filepath.Join(evaluator, "go.mod"), "module example.test/product/internal/agenteval\n\ngo 1.26.6\n")
	writeFile(t, filepath.Join(evaluator, "go.sum"), "")
	writeFile(t, filepath.Join(evaluator, "eval.go"), "package agenteval\n")
	writeFile(t, filepath.Join(evaluator, "eval_test.go"), `package agenteval

import (
	"fmt"
	"testing"
)

func TestA(t *testing.T) { t.Log("success-output-marker") }
func TestB(t *testing.T) {}
func TestC(t *testing.T) {}
func TestD(t *testing.T) {}

func FuzzOne(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, value string) {})
}

func Example() {
	fmt.Println("ok")
	// Output: ok
}
`)
	writeFile(t, filepath.Join(evaluator, "sub", "sub.go"), "package sub\n")
	writeFile(t, filepath.Join(evaluator, "sub", "sub_test.go"), "package sub\nimport \"testing\"\nfunc TestSub(t *testing.T) {}\n")
	gitCommand(t, root, "init", "-q")
	gitCommand(t, root, "config", "user.email", "fixture@example.test")
	gitCommand(t, root, "config", "user.name", "Fixture")
	gitCommand(t, root, "add", ".")
	gitCommand(t, root, "commit", "-qm", "fixture")
	return root
}

func encodeEvents(t *testing.T, events ...goEvent) []byte {
	t.Helper()
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	return body.Bytes()
}

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, body)
	}
	return strings.TrimSpace(string(body))
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
