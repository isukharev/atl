package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if strings.HasPrefix(filepath.Base(os.Args[0]), "fake-atl-") {
		fakeATLMain()
		return
	}
	os.Exit(m.Run())
}

func fakeATLMain() {
	arguments := os.Args[1:]
	fail := func(message string) {
		_, _ = fmt.Fprintln(os.Stderr, message)
		os.Exit(90)
	}
	if os.Getenv("ATL_NO_UPDATE") != "1" || os.Getenv("ATL_CONFIG_DIR") == "" {
		fail("fake atl did not receive the offline safety environment")
	}
	for _, forbidden := range []string{
		"ATL_JIRA_PAT", "ATL_CONFLUENCE_PAT", "JIRA_PAT", "CONFLUENCE_PAT",
		"ATL_JIRA_URL", "ATL_CONFLUENCE_URL",
	} {
		if os.Getenv(forbidden) != "" {
			fail("fake atl received credential or backend environment: " + forbidden)
		}
	}

	base := os.Args[0]
	if len(arguments) == 1 && arguments[0] == "version" {
		identity := binaryIdentity{Version: "1.2.3", Commit: strings.Repeat("a", 40), BuildState: "dirty"}
		if content, err := os.ReadFile(base + ".identity"); err == nil {
			if err := json.Unmarshal(content, &identity); err != nil {
				fail(err.Error())
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(identity)
		os.Exit(0)
	}
	if len(arguments) == 4 && arguments[0] == "config" && arguments[1] == "set" && arguments[2] == "--confluence-url" {
		configPath := filepath.Join(os.Getenv("ATL_CONFIG_DIR"), "config.json")
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			fail(err.Error())
		}
		contents, _ := json.Marshal(map[string]string{"confluence_url": arguments[3]})
		if err := os.WriteFile(configPath, contents, 0o600); err != nil {
			fail(err.Error())
		}
		_, _ = os.Stdout.Write(contents)
		os.Exit(0)
	}
	if len(arguments) == 2 && arguments[0] == "config" && arguments[1] == "show" {
		contents, err := os.ReadFile(filepath.Join(os.Getenv("ATL_CONFIG_DIR"), "config.json"))
		if err != nil {
			fail(err.Error())
		}
		_, _ = os.Stdout.Write(contents)
		os.Exit(0)
	}
	if len(arguments) == 0 || arguments[len(arguments)-1] != "--help" {
		fail("fake atl received an unexpected invocation")
	}
	if os.Getenv("ATL_READ_ONLY") != "1" {
		fail("fake atl help did not receive read-only policy")
	}
	path := strings.Join(arguments[:len(arguments)-1], " ")
	if content, err := os.ReadFile(base + ".fail"); err == nil && strings.TrimSpace(string(content)) == path {
		_, _ = fmt.Fprintln(os.Stderr, "unknown command path: "+path)
		os.Exit(9)
	}
	omitted := ""
	if content, err := os.ReadFile(base + ".omit"); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(content)), "\t", 2)
		if len(parts) == 2 && parts[0] == path {
			omitted = parts[1]
		}
	}
	log, err := os.OpenFile(base+".log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err.Error())
	}
	_, _ = fmt.Fprintln(log, strings.Join(arguments, " "))
	_ = log.Close()
	_, _ = fmt.Fprintln(os.Stdout, "offline fake help")
	for _, required := range commandHelpRequirements[path] {
		if required != omitted {
			_, _ = fmt.Fprintln(os.Stdout, required)
		}
	}
	os.Exit(0)
}

func TestValidateRepositoryAcceptsLocalLinksAndOfflineHelp(t *testing.T) {
	root := validRepository(t)
	binary := fakeATL(t, "", "", "")
	t.Setenv("ATL_JIRA_PAT", "must-not-leak")
	t.Setenv("ATL_CONFLUENCE_URL", "https://must-not-leak.invalid")

	report, err := validateRepository(root, binary)
	if err != nil {
		t.Fatal(err)
	}
	if report.Documents != len(requiredDocuments) || report.Links != 18 || report.Commands != len(commandPaths) ||
		report.Identity.Version != "1.2.3" || report.Identity.BuildState != "dirty" {
		t.Fatalf("unexpected report: %+v", report)
	}
	logBytes, err := os.ReadFile(binary + ".log")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.FieldsFunc(strings.TrimSpace(string(logBytes)), func(character rune) bool { return character == '\n' })
	if len(lines) != len(commandPaths) {
		t.Fatalf("fake binary received %d calls, want %d:\n%s", len(lines), len(commandPaths), logBytes)
	}
	for _, line := range lines {
		if line != "--help" && !strings.HasSuffix(line, " --help") {
			t.Fatalf("non-help invocation recorded: %q", line)
		}
	}
}

func TestValidateBinaryIdentityRejectsVersionMismatchAndUnstampedIdentity(t *testing.T) {
	root := validRepository(t)
	tests := []struct {
		name     string
		identity binaryIdentity
		want     string
	}{
		{
			name: "version mismatch",
			identity: binaryIdentity{
				Version: "1.2.4", Commit: strings.Repeat("a", 40), BuildState: "clean",
			},
			want: `does not match repository VERSION "1.2.3"`,
		},
		{
			name: "development version",
			identity: binaryIdentity{
				Version: "dev", Commit: "unknown", BuildState: "unknown",
			},
			want: `atl version identity "dev"`,
		},
		{
			name: "unstamped commit",
			identity: binaryIdentity{
				Version: "1.2.3", Commit: "unknown", BuildState: "dirty",
			},
			want: `is not a canonical full lowercase git SHA`,
		},
		{
			name: "unknown build state",
			identity: binaryIdentity{
				Version: "1.2.3", Commit: strings.Repeat("a", 40), BuildState: "unknown",
			},
			want: `build_state "unknown" is not one of clean or dirty`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := fakeATL(t, "", "", "")
			contents, err := json.Marshal(test.identity)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binary+".identity", contents, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = validateBinaryIdentity(root, binary)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("identity validation error=%v", err)
			}
		})
	}
}

func TestValidateCleanConfigIgnoresAmbientConfigAndCredentials(t *testing.T) {
	binary := fakeATL(t, "", "", "")
	ambient := t.TempDir()
	writeFile(t, ambient, "config.json", `{"confluence_url":"https://ambient.invalid"}`)
	t.Setenv("ATL_CONFIG_DIR", ambient)
	t.Setenv("ATL_CONFLUENCE_URL", "https://ambient.invalid")
	t.Setenv("ATL_CONFLUENCE_PAT", "ambient-must-not-leak")

	if err := validateCleanConfig(binary); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(ambient, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "ambient.invalid") {
		t.Fatalf("ambient config was changed: %s", contents)
	}
}

func TestValidateDocumentationRejectsBrokenLink(t *testing.T) {
	root := validRepository(t)
	writeFile(t, root, "docs/getting-started.md", "# Start\n\n[missing](missing.md)\n")

	_, err := validateDocumentation(root)
	if err == nil || !strings.Contains(err.Error(), `docs/getting-started.md:3: link "missing.md": target does not exist`) {
		t.Fatalf("validation error=%v", err)
	}
}

func TestValidateDocumentationRejectsPathEscape(t *testing.T) {
	root := validRepository(t)
	writeFile(t, root, "docs/safe-writes.md", "# Safe writes\n\n[escape](../../outside.md)\n")

	_, err := validateDocumentation(root)
	if err == nil || !strings.Contains(err.Error(), "target escapes repository root") {
		t.Fatalf("validation error=%v", err)
	}
}

func TestValidateDocumentationRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires extra privileges on Windows")
	}
	root := validRepository(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs", "outside.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "docs/safe-writes.md", "# Safe writes\n\n[escape](outside.md)\n")

	_, err := validateDocumentation(root)
	if err == nil || !strings.Contains(err.Error(), "target resolves outside repository root") {
		t.Fatalf("validation error=%v", err)
	}
}

func TestValidateDocumentationReportsMissingRequiredGuide(t *testing.T) {
	root := validRepository(t)
	if err := os.Remove(filepath.Join(root, "docs", "compatibility.md")); err != nil {
		t.Fatal(err)
	}

	_, err := validateDocumentation(root)
	if err == nil || !strings.Contains(err.Error(), "required onboarding document docs/compatibility.md is missing") {
		t.Fatalf("validation error=%v", err)
	}
}

func TestValidateDocumentationRejectsBloatedOrDisconnectedEntryPoint(t *testing.T) {
	root := validRepository(t)
	writeFile(t, root, "README.md", "# Project\n\n"+strings.Repeat("word ", 1201)+"\n")

	_, err := validateDocumentation(root)
	if err == nil || !strings.Contains(err.Error(), "README.md has 1203 words; entry point limit is 1200") ||
		!strings.Contains(err.Error(), "README.md does not link directly to task guide docs/safe-writes.md") {
		t.Fatalf("validation error=%v", err)
	}
}

func TestValidateCommandsRejectsStalePath(t *testing.T) {
	root := validRepository(t)
	binary := fakeATL(t, "jira apply", "", "")

	checked, err := validateCommands(root, binary)
	if err == nil || !strings.Contains(err.Error(), `path "atl jira apply" failed offline help validation`) {
		t.Fatalf("checked=%d validation error=%v", checked, err)
	}
}

func TestValidateCommandsRejectsMissingDocumentedFlag(t *testing.T) {
	root := validRepository(t)
	binary := fakeATL(t, "", "jira fields", "--summary-only")

	checked, err := validateCommands(root, binary)
	if err == nil || !strings.Contains(err.Error(), `path "atl jira fields" help is missing documented flag "--summary-only"`) {
		t.Fatalf("checked=%d validation error=%v", checked, err)
	}
}

func TestDemoTreeDigestCoversTheWholeArtifactSet(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "page/page.csf", "<p>local</p>")
	writeFile(t, root, "page/page.md", "local\n")
	before, err := demoTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, ".atl/state.json", "{}\n")
	after, err := demoTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("adding a sidecar did not change the demo tree digest")
	}
}

func TestDemoRunnerUsesClosedConfigAndTempEnvironment(t *testing.T) {
	t.Setenv("ATL_JIRA_PAT", "ambient-must-not-leak")
	runner, err := newDemoRunner("atl", map[string]string{"ATL_JIRA_PAT": "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	root := runner.root
	seen := map[string]bool{}
	for _, entry := range runner.env {
		if entry == "ATL_JIRA_PAT=ambient-must-not-leak" {
			t.Fatalf("runner inherited ambient credential: %q", entry)
		}
		for _, name := range []string{"ATL_CONFIG_DIR", "HOME", "TMPDIR", "TMP", "TEMP", "XDG_CONFIG_HOME"} {
			prefix := name + "="
			if strings.HasPrefix(entry, prefix) {
				seen[name] = true
				if !strings.HasPrefix(strings.TrimPrefix(entry, prefix), root+string(filepath.Separator)) {
					t.Fatalf("%s escaped demo root: %q", name, entry)
				}
			}
		}
	}
	for _, name := range []string{"ATL_CONFIG_DIR", "HOME", "TMPDIR", "TMP", "TEMP", "XDG_CONFIG_HOME"} {
		if !seen[name] {
			t.Fatalf("runner environment is missing %s", name)
		}
	}
	if err := runner.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("demo root remains after cleanup: %v", err)
	}
}

func TestMarkdownLinksIgnoreFencedAndInlineCode(t *testing.T) {
	content := "[real](docs/README.md)\n\n`[inline](ignored.md)`\n\n```md\n[fenced](ignored.md)\n```\n[ref]: <README.ru.md> \"title\"\n"
	got := markdownLinks(content)
	if len(got) != 2 || got[0].destination != "docs/README.md" || got[1].destination != "README.ru.md" {
		t.Fatalf("links=%+v", got)
	}
}

func validRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "VERSION", "1.2.3\n")
	for _, relative := range requiredDocuments {
		writeFile(t, root, relative, "# Guide\n")
	}
	entryLinks := "[Start](docs/getting-started.md#install)\n[Agents](docs/agent-setup.md)\n[Writes](docs/safe-writes.md)\n[Graph](docs/jira-artifact-graph.md)\n[Comments](docs/confluence-comments.md)\n[Demos](docs/demos/README.md)\n[Troubleshooting](docs/troubleshooting.md)\n"
	writeFile(t, root, "README.md", "# Project\n\n[Russian](README.ru.md)\n"+entryLinks+"[website](https://example.com)\n[email](mailto:docs@example.com)\n")
	writeFile(t, root, "README.ru.md", "# Project\n\n[Docs](/docs/README.md)\n"+entryLinks)
	writeFile(t, root, "docs/README.md", "# Docs\n\n[Setup](agent-setup.md)\n")
	writeFile(t, root, "docs/agent-setup.md", "# Agent setup\n\n[Project](../README.md?from=setup#install)\n")
	return root
}

func writeFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeATL(t *testing.T, stalePath, omitPath, omitFlag string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	target := filepath.Join(t.TempDir(), "fake-atl-fixture"+suffix)
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	if stalePath != "" {
		if err := os.WriteFile(target+".fail", []byte(stalePath+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if omitPath != "" || omitFlag != "" {
		if omitPath == "" || omitFlag == "" {
			t.Fatal("omit path and flag must be provided together")
		}
		if err := os.WriteFile(target+".omit", []byte(omitPath+"\t"+omitFlag+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}
