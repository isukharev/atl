package main

import (
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
	if len(arguments) == 0 || arguments[len(arguments)-1] != "--help" {
		fail("fake atl received a non-help invocation")
	}
	if os.Getenv("ATL_NO_UPDATE") != "1" || os.Getenv("ATL_READ_ONLY") != "1" || os.Getenv("ATL_CONFIG_DIR") == "" {
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
	path := strings.Join(arguments[:len(arguments)-1], " ")
	if content, err := os.ReadFile(base + ".fail"); err == nil && strings.TrimSpace(string(content)) == path {
		_, _ = fmt.Fprintln(os.Stderr, "unknown command path: "+path)
		os.Exit(9)
	}
	log, err := os.OpenFile(base+".log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err.Error())
	}
	_, _ = fmt.Fprintln(log, strings.Join(arguments, " "))
	_ = log.Close()
	_, _ = fmt.Fprintln(os.Stdout, "offline fake help")
	os.Exit(0)
}

func TestValidateRepositoryAcceptsLocalLinksAndOfflineHelp(t *testing.T) {
	root := validRepository(t)
	binary := fakeATL(t, "")
	t.Setenv("ATL_JIRA_PAT", "must-not-leak")
	t.Setenv("ATL_CONFLUENCE_URL", "https://must-not-leak.invalid")

	report, err := validateRepository(root, binary)
	if err != nil {
		t.Fatal(err)
	}
	if report.Documents != len(requiredDocuments) || report.Links != 5 || report.Commands != len(commandPaths) {
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

func TestValidateCommandsRejectsStalePath(t *testing.T) {
	root := validRepository(t)
	binary := fakeATL(t, "jira apply")

	checked, err := validateCommands(root, binary)
	if err == nil || !strings.Contains(err.Error(), `path "atl jira apply" failed offline help validation`) {
		t.Fatalf("checked=%d validation error=%v", checked, err)
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
	for _, relative := range requiredDocuments {
		writeFile(t, root, relative, "# Guide\n")
	}
	writeFile(t, root, "README.md", "# Project\n\n[Russian](README.ru.md)\n[Start](docs/getting-started.md#install)\n[website](https://example.com)\n[email](mailto:docs@example.com)\n")
	writeFile(t, root, "README.ru.md", "# Project\n\n[Docs](/docs/README.md)\n")
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

func fakeATL(t *testing.T, stalePath string) string {
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
	return target
}
