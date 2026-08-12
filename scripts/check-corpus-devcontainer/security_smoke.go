package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runHermeticSmoke(repositoryRoot string) error {
	return runHermeticSmokeWithATL(repositoryRoot, "")
}

func validateLockedDevcontainersCLI(root string) error {
	manifestPath := filepath.Join(root, "scripts", "check-corpus-devcontainer", "package.json")
	lockPath := filepath.Join(root, "scripts", "check-corpus-devcontainer", "package-lock.json")
	for _, path := range []string{manifestPath, lockPath} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
			return fmt.Errorf("%s must be a regular 0644 file", filepath.Base(path))
		}
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest struct {
		Name         string            `json:"name"`
		Private      bool              `json:"private"`
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := strictJSON(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("Dev Containers CLI manifest: %w", err)
	}
	const cliVersion = "0.88.0"
	if manifest.Name != "atl-corpus-devcontainer-ci" || !manifest.Private || manifest.Version != "1.0.0" ||
		len(manifest.Dependencies) != 1 || manifest.Dependencies["@devcontainers/cli"] != cliVersion {
		return errors.New("Dev Containers CLI manifest is not exact-pinned")
	}
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		return err
	}
	var lock struct {
		Name            string `json:"name"`
		Version         string `json:"version"`
		LockfileVersion int    `json:"lockfileVersion"`
		Requires        bool   `json:"requires"`
		Packages        map[string]struct {
			Name         string            `json:"name,omitempty"`
			Version      string            `json:"version,omitempty"`
			Dependencies map[string]string `json:"dependencies,omitempty"`
			Resolved     string            `json:"resolved,omitempty"`
			Integrity    string            `json:"integrity,omitempty"`
			License      string            `json:"license,omitempty"`
			Bin          map[string]string `json:"bin,omitempty"`
			Engines      map[string]string `json:"engines,omitempty"`
		} `json:"packages"`
	}
	if err := strictJSON(lockBytes, &lock); err != nil {
		return fmt.Errorf("Dev Containers CLI lock: %w", err)
	}
	rootPackage, rootOK := lock.Packages[""]
	cliPackage, cliOK := lock.Packages["node_modules/@devcontainers/cli"]
	if lock.Name != manifest.Name || lock.Version != manifest.Version || lock.LockfileVersion != 3 || !lock.Requires ||
		len(lock.Packages) != 2 || !rootOK || !cliOK || len(rootPackage.Dependencies) != 1 ||
		rootPackage.Dependencies["@devcontainers/cli"] != cliVersion || cliPackage.Version != cliVersion ||
		cliPackage.Resolved != "https://registry.npmjs.org/@devcontainers/cli/-/cli-0.88.0.tgz" ||
		cliPackage.Integrity != "sha512-sMkruPy/icfov20mdQh2EjFYZogxvMEZptDEvg5/eMBIUOr2xr+8wlsI7nvDR6EJxoBjqoasXqgRGbiMqbaJ1w==" ||
		cliPackage.Bin["devcontainer"] != "devcontainer.js" {
		return errors.New("Dev Containers CLI lock is not exact-pinned")
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		return err
	}
	if bytes.Contains(workflow, []byte("devcontainers/ci@")) ||
		!bytes.Contains(workflow, []byte("npm ci --prefix \"$RUNNER_TEMP/atl-devcontainers-cli\"")) ||
		!bytes.Contains(workflow, []byte("scripts/check-corpus-devcontainer/package-lock.json")) ||
		!bytes.Contains(workflow, []byte("node_modules/.bin/devcontainer")) ||
		!bytes.Contains(workflow, []byte("--frozen-lockfile")) {
		return errors.New("corpus devcontainer CI does not use the locked CLI")
	}
	return nil
}

func runGraphifyPolicySmoke(repositoryRoot, temporary string) error {
	root := filepath.Join(temporary, "graphify-policy")
	fakeBin := filepath.Join(root, "bin")
	inputRoot := filepath.Join(root, "input")
	indexRoot := filepath.Join(root, "index")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		return err
	}
	for _, directory := range []string{inputRoot, indexRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return err
		}
	}
	document := filepath.Join(inputRoot, "documents.indexer-v1.txt")
	if err := os.WriteFile(document, []byte("{\"synthetic\":\"document\"}\n"), 0o600); err != nil {
		return err
	}
	logPath := filepath.Join(root, "graphify.argv")
	graphifyBinary := filepath.Join(fakeBin, "graphify")
	if err := writeExecutable(graphifyBinary, "#!/bin/sh\nprintf '%s\\n' \"$*\" >\"$HOME/graphify.argv\"\n"); err != nil {
		return err
	}
	wrapper := filepath.Join(repositoryRoot, exampleRelative, "graphify-indexer.sh")
	baseEnvironment := []string{
		"HOME=" + root,
		"ATL_CORPUS_DOCUMENT=" + document,
		"ATL_INDEX_ROOT=" + indexRoot,
		"GRAPHIFY_BIN=" + graphifyBinary,
	}
	stdout, stderr, runErr := runCommand(wrapper, append(append([]string(nil), baseEnvironment...), "GRAPHIFY_BACKEND=remote-provider"))
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "semantic provider egress is not approved") {
		return errors.New("Graphify wrapper accepted unapproved semantic egress")
	}
	if _, err := os.Lstat(logPath); !os.IsNotExist(err) {
		return errors.New("Graphify executable ran before semantic egress approval")
	}
	for _, endpoint := range []string{
		"http://127.0.0.1:11434@semantic.example.test",
		"http://localhost:11434@semantic.example.test",
		"http://[::1]:11434@semantic.example.test",
		"http://127.0.0.1:11434/path",
	} {
		attempt := append(append([]string(nil), baseEnvironment...), "GRAPHIFY_BACKEND=ollama", "OLLAMA_HOST="+endpoint)
		stdout, stderr, runErr = runCommand(wrapper, attempt)
		if runErr == nil || stdout != "" || !strings.Contains(stderr, "non-loopback semantic egress is not approved") {
			return fmt.Errorf("Graphify wrapper accepted a forged loopback endpoint %q", endpoint)
		}
		if _, err := os.Lstat(logPath); !os.IsNotExist(err) {
			return errors.New("Graphify executable ran for a forged loopback endpoint")
		}
	}
	approvedLocal := append(append([]string(nil), baseEnvironment...), "GRAPHIFY_BACKEND=ollama", "OLLAMA_HOST=http://127.0.0.1:11434")
	extra := filepath.Join(inputRoot, "native.csf")
	if err := os.WriteFile(extra, []byte("synthetic excluded native bytes"), 0o600); err != nil {
		return err
	}
	stdout, stderr, runErr = runCommand(wrapper, approvedLocal)
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "exactly one document") {
		return errors.New("Graphify wrapper accepted an extra native input")
	}
	if _, err := os.Lstat(logPath); !os.IsNotExist(err) {
		return errors.New("Graphify executable ran with an extra native input")
	}
	if err := os.Remove(extra); err != nil {
		return err
	}
	for _, endpoint := range []string{
		"http://127.0.0.1:11434",
		"http://localhost:11434",
		"http://[::1]:11434",
	} {
		local := append(append([]string(nil), baseEnvironment...), "GRAPHIFY_BACKEND=ollama", "OLLAMA_HOST="+endpoint)
		stdout, stderr, runErr = runCommand(wrapper, local)
		if runErr != nil || stdout != "" || stderr != "" {
			return fmt.Errorf("loopback Graphify wrapper failed for %q", endpoint)
		}
	}
	arguments, err := os.ReadFile(logPath)
	if err != nil {
		return err
	}
	want := "extract " + inputRoot + " --out " + indexRoot + " --backend=ollama --no-cluster\n"
	if string(arguments) != want || bytes.Contains(arguments, []byte("--code-only")) {
		return errors.New("Graphify wrapper document-only argument boundary drifted")
	}
	return nil
}

func runInvalidSelectorReadScopeSmoke(repositoryRoot, atlBinary, backendURL, secret string) error {
	root, err := os.MkdirTemp("", "atl-corpus-invalid-selector-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	sourceRoot := filepath.Join(root, "source")
	indexRoot := filepath.Join(root, "index")
	contextParent := filepath.Join(root, "contexts")
	for _, directory := range []string{sourceRoot, indexRoot, contextParent} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return err
		}
	}
	privateFiles := map[string]string{
		"jira-url":     backendURL + "\n",
		"jira-pat":     secret + "\n",
		"jira-project": "EXAMPLE OR project IS NOT EMPTY\n",
	}
	for name, content := range privateFiles {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	marker := filepath.Join(root, "indexer-invoked")
	indexer := filepath.Join(root, "must-not-run-indexer")
	if err := writeExecutable(indexer, "#!/bin/sh\n: >"+shellSingleQuote(marker)+"\n"); err != nil {
		return err
	}
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"ATL_SOURCE_ROOT=" + sourceRoot,
		"ATL_INDEX_ROOT=" + indexRoot,
		"ATL_INDEXER=" + indexer,
		"ATL_BIN=" + atlBinary,
		"ATL_CONTEXT_PARENT=" + contextParent,
		"ATL_JIRA_URL_FILE=" + filepath.Join(root, "jira-url"),
		"ATL_JIRA_PAT_FILE=" + filepath.Join(root, "jira-pat"),
		"ATL_JIRA_PROJECT_FILE=" + filepath.Join(root, "jira-project"),
		"ATL_MAX_JIRA_ISSUES=10",
		"ATL_MAX_REQUESTS=100",
		"ATL_MAX_RESPONSE_BYTES=1048576",
		"ATL_MAX_MEMBERS=1000",
		"ATL_MAX_GENERATION_BYTES=10485760",
		"ATL_DEADLINE=1m",
		"ATL_MAX_IN_FLIGHT=2",
		"ATL_REQUESTS_PER_SECOND=1000",
		"ATL_CAPTURE_COMMENTS=0",
		"ATL_CAPTURE_ATTACHMENTS=0",
		"ATL_CAPTURE_ATTACHMENT_BODIES=0",
	}
	stdout, stderr, runErr := runCommand(filepath.Join(repositoryRoot, exampleRelative, "run-corpus.sh"), environment)
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "corpus build failed; indexer was not invoked") {
		return fmt.Errorf("invalid selector did not fail closed: error=%v stdout=%q stderr=%q", runErr, stdout, stderr)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		return errors.New("invalid selector invoked the indexer")
	}
	return nil
}

func verifyFakeATLCommands(contextParent string, expected int) error {
	entries, err := os.ReadDir(contextParent)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return errors.New("fake ATL runtime root count drifted")
	}
	logBytes, err := os.ReadFile(filepath.Join(contextParent, entries[0].Name(), "atl-argv.log"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(logBytes), "\n"), "\n")
	if len(lines) != expected || !strings.HasPrefix(lines[0], "corpus build ") ||
		!strings.HasPrefix(lines[len(lines)-1], "corpus handoff ") {
		return errors.New("fake ATL command boundary drifted")
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "doctor ") || strings.HasPrefix(line, "jira issue ") || strings.HasPrefix(line, "conf search ") {
			return errors.New("wrapper performed a remote preflight before corpus validation")
		}
	}
	return nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
