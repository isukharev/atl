package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryCorpusDevcontainerTemplate(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTemplate(root); err != nil {
		t.Fatal(err)
	}
	if err := runHermeticSmoke(root); err != nil {
		t.Fatal(err)
	}
}

func TestStrictJSONRejectsTrailingValue(t *testing.T) {
	var target struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := strictJSON([]byte("{\"schema_version\":1}\n"), &target); err != nil || target.SchemaVersion != 1 {
		t.Fatalf("canonical error=%v target=%#v", err, target)
	}
	for _, input := range []string{
		"{\"schema_version\":1} {}",
		"{\"schema_version\":1} false",
		"{\"schema_version\":1} trailing",
	} {
		if err := strictJSON([]byte(input), &target); err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("trailing input %q error=%v", input, err)
		}
	}
}

func TestCorpusDevcontainerWorkflowBindsContractsToExactJob(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCorpusDevcontainerWorkflow(workflow); err != nil {
		t.Fatalf("repository workflow: %v", err)
	}

	const header = "  corpus-devcontainer:\n"
	const relocated = `  corpus-devcontainer:
    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'
    runs-on: ubuntu-latest
    steps:
      - run: true

  relocated-corpus-runtime:
`
	workflowWithRelocatedContracts := bytes.Replace(workflow, []byte(header), []byte(relocated), 1)
	if bytes.Equal(workflowWithRelocatedContracts, workflow) {
		t.Fatal("corpus devcontainer job header fixture was not replaced")
	}
	if err := validateCorpusDevcontainerWorkflow(workflowWithRelocatedContracts); err == nil {
		t.Fatal("contracts relocated to another job were accepted")
	}

	var commented strings.Builder
	commented.WriteString("jobs:\n")
	commented.WriteString(relocated[:strings.Index(relocated, "\n  relocated-corpus-runtime:")])
	commented.WriteByte('\n')
	for _, line := range corpusDevcontainerJobContract {
		commented.WriteString("      # ")
		commented.WriteString(line)
		commented.WriteByte('\n')
	}
	if err := validateCorpusDevcontainerWorkflow([]byte(commented.String())); err == nil {
		t.Fatal("contracts present only in comments were accepted")
	}
}

func TestBootstrapRefusesSourceOverlapBeforeATL(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	if err := os.Chmod(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(temporary, "source", "index")
	if err := os.MkdirAll(index, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeATL := filepath.Join(temporary, "fake-atl")
	if err := writeExecutable(fakeATL, "#!/bin/sh\nexit 99\n"); err != nil {
		t.Fatal(err)
	}
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"ATL_SOURCE_ROOT=" + filepath.Join(temporary, "source"),
		"ATL_INDEX_ROOT=" + index,
		"ATL_INDEXER=" + filepath.Join(root, exampleRelative, "local-indexer-stub.sh"),
		"ATL_BIN=" + fakeATL,
	}
	_, _, runErr := runCommand(filepath.Join(root, exampleRelative, "run-corpus.sh"), environment)
	if runErr == nil {
		t.Fatal("source-contained index root was accepted")
	}
}

func TestBootstrapRefusesSourceContainedPrivateInputBeforeATL(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	if err := os.Chmod(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(temporary, "source")
	index := filepath.Join(temporary, "index")
	contexts := filepath.Join(temporary, "contexts")
	for _, directory := range []string{source, index, contexts} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	privateFiles := map[string]string{
		filepath.Join(temporary, "jira-url"):     "https://backend.example.test\n",
		filepath.Join(source, "jira-pat"):        "synthetic-source-secret\n",
		filepath.Join(temporary, "jira-project"): "EXAMPLE\n",
	}
	for path, content := range privateFiles {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(temporary, "atl-invoked")
	fakeATL := filepath.Join(temporary, "fake-atl")
	if err := writeExecutable(fakeATL, "#!/bin/sh\n: >\"$ATL_FAKE_MARKER\"\nexit 99\n"); err != nil {
		t.Fatal(err)
	}
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"ATL_SOURCE_ROOT=" + source,
		"ATL_INDEX_ROOT=" + index,
		"ATL_INDEXER=" + filepath.Join(root, exampleRelative, "local-indexer-stub.sh"),
		"ATL_BIN=" + fakeATL,
		"ATL_CONTEXT_PARENT=" + contexts,
		"ATL_FAKE_MARKER=" + marker,
		"ATL_JIRA_URL_FILE=" + filepath.Join(temporary, "jira-url"),
		"ATL_JIRA_PAT_FILE=" + filepath.Join(source, "jira-pat"),
		"ATL_JIRA_PROJECT_FILE=" + filepath.Join(temporary, "jira-project"),
	}
	stdout, stderr, runErr := runCommand(filepath.Join(root, exampleRelative, "run-corpus.sh"), environment)
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "overlaps a protected boundary") || strings.Contains(stderr, source) {
		t.Fatalf("source-contained private input error=%v stdout=%q stderr=%q", runErr, stdout, stderr)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("ATL was invoked before private-input refusal: %v", err)
	}
}
