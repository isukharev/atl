package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const exampleRelative = "examples/corpus-devcontainer"

func main() {
	root := flag.String("root", ".", "repository root")
	atlBinary := flag.String("atl", "", "optional current ATL binary for the real smoke")
	flag.Parse()
	if err := validateTemplate(*root); err != nil {
		fmt.Fprintf(os.Stderr, "corpus devcontainer check failed: %v\n", err)
		os.Exit(1)
	}
	if err := runHermeticSmokeWithATL(*root, *atlBinary); err != nil {
		fmt.Fprintf(os.Stderr, "corpus devcontainer smoke failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("corpus devcontainer: pinned template and hermetic sealed-handoff smoke verified")
}

func validateTemplate(repositoryRoot string) error {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return err
	}
	example := filepath.Join(root, exampleRelative)
	wantModes := map[string]fs.FileMode{
		".devcontainer/devcontainer.json":      0o644,
		".devcontainer/devcontainer-lock.json": 0o644,
		"install-atl.sh":                       0o755,
		"run-corpus.sh":                        0o755,
		"local-indexer-stub.sh":                0o755,
		"graphify-indexer.sh":                  0o755,
	}
	contents := make(map[string][]byte, len(wantModes))
	for relative, mode := range wantModes {
		path := filepath.Join(example, filepath.FromSlash(relative))
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("%s: %w", relative, statErr)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != mode {
			return fmt.Errorf("%s must be a regular %04o file", relative, mode)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		contents[relative] = data
	}

	var devcontainer struct {
		Name              string            `json:"name"`
		Image             string            `json:"image"`
		Features          map[string]any    `json:"features"`
		ContainerEnv      map[string]string `json:"containerEnv"`
		PostCreateCommand string            `json:"postCreateCommand"`
		RemoteUser        string            `json:"remoteUser"`
	}
	if err := strictJSON(contents[".devcontainer/devcontainer.json"], &devcontainer); err != nil {
		return fmt.Errorf("devcontainer: %w", err)
	}
	if devcontainer.Name != "atl private corpus runtime" || !strings.Contains(devcontainer.Image, "@sha256:") || len(devcontainer.Features) != 1 ||
		devcontainer.Features["ghcr.io/devcontainers/features/github-cli:1"] == nil ||
		devcontainer.ContainerEnv["ATL_READ_ONLY"] != "1" || devcontainer.ContainerEnv["ATL_NO_UPDATE"] != "1" ||
		devcontainer.ContainerEnv["ATL_VERSION"] != "${localEnv:ATL_VERSION}" ||
		devcontainer.ContainerEnv["ATL_ASSET_SHA256"] != "${localEnv:ATL_ASSET_SHA256}" ||
		len(devcontainer.ContainerEnv) != 4 || devcontainer.RemoteUser != "vscode" ||
		devcontainer.PostCreateCommand != "sh ${containerWorkspaceFolder}/examples/corpus-devcontainer/install-atl.sh" {
		return errors.New("devcontainer pinning or environment contract drifted")
	}
	var lock struct {
		Features map[string]struct {
			Version   string `json:"version"`
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
		} `json:"features"`
	}
	if err := strictJSON(contents[".devcontainer/devcontainer-lock.json"], &lock); err != nil {
		return fmt.Errorf("devcontainer lock: %w", err)
	}
	feature, ok := lock.Features["ghcr.io/devcontainers/features/github-cli:1"]
	if !ok || !strings.HasPrefix(feature.Resolved, "ghcr.io/devcontainers/features/github-cli@sha256:") ||
		feature.Integrity != strings.TrimPrefix(feature.Resolved, "ghcr.io/devcontainers/features/github-cli@") {
		return errors.New("GitHub CLI feature is not digest-locked")
	}

	required := map[string][]string{
		"install-atl.sh": {
			"ATL_ASSET_SHA256", "gh attestation verify", "--signer-workflow", "--source-ref \"refs/tags/${version}\"",
			".version == $expected", "releases/download/${version}",
		},
		"run-corpus.sh": {
			"umask 077", "mktemp -d \"$context_parent/atl-context.XXXXXX\"", "ATL_READ_ONLY=1", "ATL_NO_UPDATE=1",
			"env -i", "corpus build", "corpus handoff", "resolve_private_file", "ATL_JIRA_URL=\"$jira_url\"",
			"documents.indexer-v1.txt",
		},
		"graphify-indexer.sh": {"GRAPHIFY_BIN", "is_loopback_ollama_endpoint", "--out \"$ATL_INDEX_ROOT\"", "--no-cluster", "ATL_APPROVE_SEMANTIC_EGRESS"},
	}
	for file, markers := range required {
		text := string(contents[file])
		for _, marker := range markers {
			if !strings.Contains(text, marker) {
				return fmt.Errorf("%s is missing required contract marker", file)
			}
		}
	}
	if bytes.Contains(contents["graphify-indexer.sh"], []byte("--code-only")) ||
		bytes.Contains(contents["run-corpus.sh"], []byte("doctor --remote")) ||
		bytes.Contains(contents["run-corpus.sh"], []byte("jira issue search")) ||
		bytes.Contains(contents["run-corpus.sh"], []byte("conf search")) ||
		bytes.Contains(contents["run-corpus.sh"], []byte("export ATL_JIRA_URL")) ||
		bytes.Contains(contents["run-corpus.sh"], []byte("export ATL_JIRA_PAT")) ||
		bytes.Contains(contents["run-corpus.sh"], []byte("export ATL_CONFLUENCE_URL")) ||
		bytes.Contains(contents["run-corpus.sh"], []byte("export ATL_CONFLUENCE_PAT")) ||
		bytes.Contains(contents[".devcontainer/devcontainer.json"], []byte("PAT")) ||
		bytes.Contains(contents[".devcontainer/devcontainer.json"], []byte("URL")) {
		return errors.New("template exposes a forbidden Graphify or secret/image contract")
	}
	for _, script := range []string{"install-atl.sh", "run-corpus.sh", "local-indexer-stub.sh", "graphify-indexer.sh"} {
		command := exec.Command("sh", "-n", filepath.Join(example, script))
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("%s syntax: %w: %s", script, err, strings.TrimSpace(string(output)))
		}
	}
	for _, relative := range []string{
		"scripts/check-corpus-devcontainer/container-smoke.sh",
		"scripts/check-corpus-devcontainer/testdata/fake-atl.sh",
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
			return fmt.Errorf("%s must be a regular 0755 file", relative)
		}
		command := exec.Command("sh", "-n", path)
		if output, syntaxErr := command.CombinedOutput(); syntaxErr != nil {
			return fmt.Errorf("%s syntax: %w: %s", relative, syntaxErr, strings.TrimSpace(string(output)))
		}
	}
	if err := validateLockedDevcontainersCLI(root); err != nil {
		return err
	}
	return nil
}

func runHermeticSmokeWithATL(repositoryRoot, currentATL string) error {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "atl-corpus-container-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return err
	}
	if err := runInstallSmoke(root, temporary); err != nil {
		return err
	}
	if err := runBootstrapSmoke(root, temporary); err != nil {
		return err
	}
	if err := runFailedBootstrapSmoke(root, temporary); err != nil {
		return err
	}
	if err := runGraphifyPolicySmoke(root, temporary); err != nil {
		return err
	}
	return runRealBootstrapSmoke(root, temporary, currentATL)
}

func runInstallSmoke(repositoryRoot, temporary string) error {
	fakeBin := filepath.Join(temporary, "install-tools")
	installRoot := filepath.Join(temporary, "installed")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		return err
	}
	release := filepath.Join(temporary, "release-atl")
	if err := writeExecutable(release, "#!/bin/sh\n[ \"$1\" = version ]\nprintf '%s\\n' '{\"version\":\"1.2.3\",\"commit\":\"synthetic\",\"build_state\":\"clean\"}'\n"); err != nil {
		return err
	}
	if err := writeExecutable(filepath.Join(fakeBin, "curl"), fakeCurlScript); err != nil {
		return err
	}
	if err := writeExecutable(filepath.Join(fakeBin, "gh"), fakeGHScript); err != nil {
		return err
	}
	data, err := os.ReadFile(release)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	ghLog := filepath.Join(temporary, "gh.log")
	environment := []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + temporary,
		"ATL_VERSION=v1.2.3",
		"ATL_ASSET_SHA256=" + hex.EncodeToString(sum[:]),
		"ATL_INSTALL_DIR=" + installRoot,
		"ATL_FAKE_RELEASE_BINARY=" + release,
		"ATL_FAKE_GH_LOG=" + ghLog,
	}
	stdout, stderr, runErr := runCommand(filepath.Join(repositoryRoot, exampleRelative, "install-atl.sh"), environment)
	if runErr != nil {
		return fmt.Errorf("install smoke: %w: %s", runErr, stderr)
	}
	if strings.Contains(stdout+stderr, temporary) || !strings.Contains(stdout, "verified pinned ATL release") {
		return errors.New("installer output is not content-free")
	}
	installed, err := os.ReadFile(filepath.Join(installRoot, "atl"))
	if err != nil || !bytes.Equal(installed, data) {
		return errors.New("installer did not preserve the attested binary")
	}
	logBytes, err := os.ReadFile(ghLog)
	if err != nil || !bytes.Contains(logBytes, []byte("attestation verify")) ||
		!bytes.Contains(logBytes, []byte(".github/workflows/release.yml")) ||
		!bytes.Contains(logBytes, []byte("--source-ref refs/tags/v1.2.3")) {
		return errors.New("installer did not invoke the pinned provenance check")
	}
	rejectedRoot := filepath.Join(temporary, "rejected-install")
	rejectedEnvironment := []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + temporary,
		"ATL_VERSION=v1.2.3",
		"ATL_ASSET_SHA256=" + hex.EncodeToString(sum[:]),
		"ATL_INSTALL_DIR=" + rejectedRoot,
		"ATL_FAKE_RELEASE_BINARY=" + release,
		"ATL_FAKE_GH_LOG=" + ghLog,
		"ATL_FAKE_GH_FAIL=1",
	}
	stdout, stderr, runErr = runCommand(filepath.Join(repositoryRoot, exampleRelative, "install-atl.sh"), rejectedEnvironment)
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "release provenance verification failed") {
		return errors.New("installer accepted a failed provenance verification")
	}
	if _, err := os.Lstat(filepath.Join(rejectedRoot, "atl")); !os.IsNotExist(err) {
		return errors.New("installer published a binary after failed provenance verification")
	}
	mismatchedRelease := filepath.Join(temporary, "mismatched-release-atl")
	if err := writeExecutable(mismatchedRelease, "#!/bin/sh\n[ \"$1\" = version ]\nprintf '%s\\n' '{\"version\":\"9.9.9\",\"commit\":\"synthetic\",\"build_state\":\"clean\"}'\n"); err != nil {
		return err
	}
	mismatchedBytes, err := os.ReadFile(mismatchedRelease)
	if err != nil {
		return err
	}
	mismatchedSum := sha256.Sum256(mismatchedBytes)
	mismatchedRoot := filepath.Join(temporary, "mismatched-install")
	mismatchedEnvironment := []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + temporary,
		"ATL_VERSION=v1.2.3",
		"ATL_ASSET_SHA256=" + hex.EncodeToString(mismatchedSum[:]),
		"ATL_INSTALL_DIR=" + mismatchedRoot,
		"ATL_FAKE_RELEASE_BINARY=" + mismatchedRelease,
		"ATL_FAKE_GH_LOG=" + ghLog,
	}
	stdout, stderr, runErr = runCommand(filepath.Join(repositoryRoot, exampleRelative, "install-atl.sh"), mismatchedEnvironment)
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "verified binary version does not match ATL_VERSION") {
		return errors.New("installer accepted an attested binary from a different release version")
	}
	if _, err := os.Lstat(filepath.Join(mismatchedRoot, "atl")); !os.IsNotExist(err) {
		return errors.New("installer published a version-mismatched binary")
	}
	return nil
}

func runBootstrapSmoke(repositoryRoot, temporary string) error {
	sourceRoot := filepath.Join(temporary, "source")
	indexRoot := filepath.Join(temporary, "index")
	contextParent := filepath.Join(temporary, "contexts")
	for _, directory := range []string{sourceRoot, indexRoot, contextParent} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "sentinel.txt"), []byte("source stays unchanged\n"), 0o600); err != nil {
		return err
	}
	before, err := treeDigest(sourceRoot)
	if err != nil {
		return err
	}

	secret := "synthetic-runtime-secret-canary"
	privateFiles := map[string]string{
		"jira-url":     "https://backend.example.test\n",
		"jira-pat":     secret + "\n",
		"jira-project": "EXAMPLE\n",
		"private-ca":   "synthetic certificate fixture\n",
	}
	for name, content := range privateFiles {
		path := filepath.Join(temporary, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
	}
	fakeATL := filepath.Join(temporary, "fake-atl")
	if err := installFakeATL(repositoryRoot, fakeATL); err != nil {
		return err
	}
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"ATL_SOURCE_ROOT=" + sourceRoot,
		"ATL_INDEX_ROOT=" + indexRoot,
		"ATL_INDEXER=" + filepath.Join(repositoryRoot, exampleRelative, "local-indexer-stub.sh"),
		"ATL_BIN=" + fakeATL,
		"ATL_CONTEXT_PARENT=" + contextParent,
		"ATL_JIRA_URL_FILE=" + filepath.Join(temporary, "jira-url"),
		"ATL_JIRA_PAT_FILE=" + filepath.Join(temporary, "jira-pat"),
		"ATL_JIRA_PROJECT_FILE=" + filepath.Join(temporary, "jira-project"),
		"ATL_CA_FILE=" + filepath.Join(temporary, "private-ca"),
		"ATL_MAX_JIRA_ISSUES=10",
		"ATL_MAX_REQUESTS=100",
		"ATL_MAX_RESPONSE_BYTES=1048576",
		"ATL_MAX_MEMBERS=1000",
		"ATL_MAX_GENERATION_BYTES=10485760",
		"ATL_DEADLINE=5m",
		"ATL_MAX_IN_FLIGHT=2",
		"ATL_REQUESTS_PER_SECOND=10",
		"ATL_CAPTURE_COMMENTS=0",
		"ATL_CAPTURE_ATTACHMENTS=0",
		"ATL_CAPTURE_ATTACHMENT_BODIES=0",
		"CONFLUENCE_URL=https://ambient-backend.example.test",
		"CONFLUENCE_PAT=synthetic-ambient-secret-canary",
		"TEST_CONFLUENCE_PAT=synthetic-integration-secret-canary",
		"ATL_INTEGRATION=1",
		"ATL_JIRA_CA_BUNDLE=/synthetic/ambient-ca.pem",
		"ATL_ALLOW_INSECURE=1",
		"ATL_UPDATE_URL=https://ambient-update.example.test",
		"ATL_POLICY=deny all synthetic ambient policy",
		"ATL_MIRROR_ROOT=/synthetic/ambient-mirror",
		"SSL_CERT_DIR=/synthetic/ambient-certificates",
		"HTTPS_PROXY=http://ambient-proxy.example.test:8080",
		"HTTP_PROXY=http://ambient-proxy.example.test:8080",
		"ALL_PROXY=socks5://ambient-proxy.example.test:1080",
		"NO_PROXY=backend.example.test",
		"UNRELATED_SECRET=synthetic-unrelated-secret-canary",
	}
	stdout, stderr, runErr := runCommand(filepath.Join(repositoryRoot, exampleRelative, "run-corpus.sh"), environment)
	if runErr != nil {
		return fmt.Errorf("bootstrap smoke: %w: %s", runErr, stderr)
	}
	if strings.TrimSpace(stdout) != `{"schema_version":1,"status":"complete","handoff":"sealed","indexer":"completed"}` || stderr != "" {
		return fmt.Errorf("unexpected bootstrap output stdout=%q stderr=%q", stdout, stderr)
	}
	after, err := treeDigest(sourceRoot)
	if err != nil || before != after {
		return errors.New("bootstrap changed the source checkout")
	}
	if _, err := os.Stat(filepath.Join(indexRoot, "index-receipt.v1.json")); err != nil {
		return errors.New("sealed handoff did not reach the stub indexer")
	}
	if err := verifyPrivateTree(contextParent); err != nil {
		return err
	}
	if err := verifyPrivateTree(indexRoot); err != nil {
		return err
	}
	if err := verifyFakeATLCommands(contextParent, 2); err != nil {
		return err
	}
	for _, scanRoot := range []string{contextParent, indexRoot, sourceRoot} {
		found, scanErr := containsMarker(scanRoot, secret)
		if scanErr != nil {
			return scanErr
		}
		if found {
			return errors.New("runtime secret entered argv, output, receipt, or workspace state")
		}
	}
	return nil
}

func runFailedBootstrapSmoke(repositoryRoot, temporary string) error {
	root := filepath.Join(temporary, "failed-bootstrap")
	if err := os.Mkdir(root, 0o700); err != nil {
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
		"jira-url":     "https://backend.example.test\n",
		"jira-pat":     "synthetic-failed-runtime-secret-canary\n",
		"jira-project": "EXAMPLE\n",
	}
	for name, content := range privateFiles {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	fakeATL := filepath.Join(root, "fake-atl")
	if err := installFakeATL(repositoryRoot, fakeATL); err != nil {
		return err
	}
	if err := os.WriteFile(fakeATL+".fail-build", []byte("synthetic failure marker\n"), 0o600); err != nil {
		return err
	}
	indexer := filepath.Join(root, "must-not-run-indexer")
	marker := filepath.Join(root, "indexer-invoked")
	if err := writeExecutable(indexer, "#!/bin/sh\n: >"+shellSingleQuote(marker)+"\n"); err != nil {
		return err
	}
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"ATL_SOURCE_ROOT=" + sourceRoot,
		"ATL_INDEX_ROOT=" + indexRoot,
		"ATL_INDEXER=" + indexer,
		"ATL_BIN=" + fakeATL,
		"ATL_CONTEXT_PARENT=" + contextParent,
		"ATL_JIRA_URL_FILE=" + filepath.Join(root, "jira-url"),
		"ATL_JIRA_PAT_FILE=" + filepath.Join(root, "jira-pat"),
		"ATL_JIRA_PROJECT_FILE=" + filepath.Join(root, "jira-project"),
		"ATL_MAX_JIRA_ISSUES=10",
		"ATL_MAX_REQUESTS=100",
		"ATL_MAX_RESPONSE_BYTES=1048576",
		"ATL_MAX_MEMBERS=1000",
		"ATL_MAX_GENERATION_BYTES=10485760",
		"ATL_DEADLINE=5m",
		"ATL_MAX_IN_FLIGHT=2",
		"ATL_REQUESTS_PER_SECOND=10",
		"ATL_CAPTURE_COMMENTS=0",
		"ATL_CAPTURE_ATTACHMENTS=0",
		"ATL_CAPTURE_ATTACHMENT_BODIES=0",
	}
	stdout, stderr, runErr := runCommand(filepath.Join(repositoryRoot, exampleRelative, "run-corpus.sh"), environment)
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "corpus build failed; indexer was not invoked") {
		return fmt.Errorf("failed bootstrap did not fail closed: error=%v stdout=%q stderr=%q", runErr, stdout, stderr)
	}
	if strings.Contains(stderr, privateFiles["jira-pat"][:len(privateFiles["jira-pat"])-1]) {
		return errors.New("failed bootstrap leaked a credential")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		return errors.New("failed bootstrap invoked the indexer")
	}
	if entry, err := os.ReadDir(indexRoot); err != nil || len(entry) != 0 {
		return errors.New("failed bootstrap populated the index root")
	}
	if err := verifyPrivateTree(contextParent); err != nil {
		return err
	}
	if err := verifyPrivateTree(indexRoot); err != nil {
		return err
	}
	return nil
}

func runRealBootstrapSmoke(repositoryRoot, temporary, currentATL string) error {
	root := filepath.Join(temporary, "real-bootstrap")
	if err := os.Mkdir(root, 0o700); err != nil {
		return err
	}
	atlBinary := currentATL
	if atlBinary == "" {
		atlBinary = filepath.Join(root, "atl")
		if err := buildCurrentATL(repositoryRoot, atlBinary); err != nil {
			return err
		}
	} else {
		var err error
		atlBinary, err = filepath.Abs(atlBinary)
		if err != nil {
			return err
		}
		info, statErr := os.Lstat(atlBinary)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return errors.New("provided ATL binary must be a regular executable")
		}
	}

	const secret = "synthetic-real-runtime-secret-canary"
	var mu sync.Mutex
	requests := make([]string, 0, 6)
	authenticated := true
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		if request.Header.Get("Authorization") != "Bearer "+secret {
			authenticated = false
		}
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if request.Method != http.MethodGet {
			http.Error(writer, "synthetic read-only backend", http.StatusMethodNotAllowed)
			return
		}
		switch request.URL.Path {
		case "/rest/api/2/serverInfo":
			_, _ = io.WriteString(writer, `{"version":"9.12.7","deploymentType":"Data Center"}`)
		case "/rest/api/2/myself":
			_, _ = io.WriteString(writer, `{"name":"synthetic-principal","key":"synthetic-key","displayName":"Synthetic Person","active":true}`)
		case "/rest/api/2/search":
			_, _ = io.WriteString(writer, `{"issues":[{"id":"100","key":"EXAMPLE-1","fields":{"project":{"key":"EXAMPLE"}}}],"startAt":0,"maxResults":100,"total":1}`)
		case "/rest/api/2/issue/100":
			_, _ = io.WriteString(writer, `{"id":"100","key":"EXAMPLE-1","fields":{"summary":"Synthetic issue","description":"synthetic private document marker","updated":"2026-08-12T12:34:56.000+0000","status":{"name":"Open"},"issuetype":{"name":"Task"},"project":{"key":"EXAMPLE"},"issuelinks":[]}}`)
		default:
			http.Error(writer, "synthetic unexpected backend path", http.StatusTeapot)
		}
	}))
	defer backend.Close()
	if err := runInvalidSelectorReadScopeSmoke(repositoryRoot, atlBinary, backend.URL, secret); err != nil {
		return err
	}
	mu.Lock()
	invalidSelectorRequests := len(requests)
	mu.Unlock()
	if invalidSelectorRequests != 0 {
		return fmt.Errorf("invalid Jira selector caused %d backend requests before corpus validation", invalidSelectorRequests)
	}

	sourceRoot := filepath.Join(root, "source")
	indexRoot := filepath.Join(root, "index")
	contextParent := filepath.Join(root, "contexts")
	for _, directory := range []string{sourceRoot, indexRoot, contextParent} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "sentinel.txt"), []byte("source stays unchanged\n"), 0o600); err != nil {
		return err
	}
	before, err := treeDigest(sourceRoot)
	if err != nil {
		return err
	}
	privateFiles := map[string]string{
		"jira-url":     backend.URL + "\n",
		"jira-pat":     secret + "\n",
		"jira-project": "EXAMPLE\n",
		"private-ca":   "synthetic CA mount marker\n",
	}
	for name, content := range privateFiles {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"ATL_SOURCE_ROOT=" + sourceRoot,
		"ATL_INDEX_ROOT=" + indexRoot,
		"ATL_INDEXER=" + filepath.Join(repositoryRoot, exampleRelative, "local-indexer-stub.sh"),
		"ATL_BIN=" + atlBinary,
		"ATL_CONTEXT_PARENT=" + contextParent,
		"ATL_JIRA_URL_FILE=" + filepath.Join(root, "jira-url"),
		"ATL_JIRA_PAT_FILE=" + filepath.Join(root, "jira-pat"),
		"ATL_JIRA_PROJECT_FILE=" + filepath.Join(root, "jira-project"),
		"ATL_CA_FILE=" + filepath.Join(root, "private-ca"),
		"ATL_UPDATE_URL=" + backend.URL + "/synthetic-update-must-not-run",
		"CONFLUENCE_URL=https://ambient-backend.example.test",
		"CONFLUENCE_PAT=synthetic-ambient-secret-canary",
		"TEST_CONFLUENCE_PAT=synthetic-integration-secret-canary",
		"ATL_INTEGRATION=1",
		"ATL_JIRA_CA_BUNDLE=/synthetic/ambient-ca.pem",
		"ATL_ALLOW_INSECURE=1",
		"ATL_POLICY=deny all synthetic ambient policy",
		"ATL_MIRROR_ROOT=/synthetic/ambient-mirror",
		"SSL_CERT_DIR=/synthetic/ambient-certificates",
		"HTTPS_PROXY=http://ambient-proxy.example.test:8080",
		"HTTP_PROXY=http://ambient-proxy.example.test:8080",
		"ALL_PROXY=socks5://ambient-proxy.example.test:1080",
		"NO_PROXY=backend.example.test",
		"UNRELATED_SECRET=synthetic-unrelated-secret-canary",
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
	if runErr != nil {
		return fmt.Errorf("real bootstrap smoke: %w: %s", runErr, stderr)
	}
	if strings.TrimSpace(stdout) != `{"schema_version":1,"status":"complete","handoff":"sealed","indexer":"completed"}` || stderr != "" {
		return fmt.Errorf("unexpected real bootstrap output stdout=%q stderr=%q", stdout, stderr)
	}
	after, err := treeDigest(sourceRoot)
	if err != nil || before != after {
		return errors.New("real bootstrap changed the source checkout")
	}
	if _, err := os.Stat(filepath.Join(indexRoot, "index-receipt.v1.json")); err != nil {
		return errors.New("real sealed handoff did not reach the stub indexer")
	}
	if err := verifyPrivateTree(contextParent); err != nil {
		return err
	}
	if err := verifyPrivateTree(indexRoot); err != nil {
		return err
	}
	for _, scanRoot := range []string{contextParent, indexRoot, sourceRoot} {
		found, scanErr := containsMarker(scanRoot, secret)
		if scanErr != nil {
			return scanErr
		}
		if found {
			return errors.New("real runtime secret entered output, receipt, index, or workspace state")
		}
	}

	mu.Lock()
	observed := append([]string(nil), requests...)
	validAuth := authenticated
	mu.Unlock()
	counts := map[string]int{}
	for _, request := range observed {
		parts := strings.SplitN(request, " ", 2)
		if len(parts) != 2 || parts[0] != http.MethodGet {
			return fmt.Errorf("real bootstrap made a non-read request: %q", request)
		}
		path := strings.SplitN(parts[1], "?", 2)[0]
		counts[path]++
	}
	want := map[string]int{
		"/rest/api/2/search":    2,
		"/rest/api/2/myself":    1,
		"/rest/api/2/issue/100": 1,
	}
	if !validAuth || len(observed) != 4 || len(counts) != len(want) {
		return fmt.Errorf("real bootstrap request boundary drifted: requests=%d authenticated=%t path_counts=%v", len(observed), validAuth, counts)
	}
	for path, expected := range want {
		if counts[path] != expected {
			return fmt.Errorf("real bootstrap request boundary drifted for %s: got=%d want=%d", path, counts[path], expected)
		}
	}
	return nil
}

func buildCurrentATL(repositoryRoot, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", target, "./cmd/atl")
	command.Dir = repositoryRoot
	environment := make([]string, 0, len(os.Environ())+3)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "GOROOT=") || strings.HasPrefix(item, "GOWORK=") || strings.HasPrefix(item, "GOTOOLCHAIN=") {
			continue
		}
		environment = append(environment, item)
	}
	command.Env = append(environment, "GOWORK=off", "GOTOOLCHAIN=auto", "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("build current ATL: %w", ctx.Err())
		}
		return fmt.Errorf("build current ATL: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("trailing JSON value: %w", err)
		}
		return errors.New("trailing JSON value")
	}
	return nil
}

func runCommand(path string, environment []string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path)
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return stdout.String(), stderr.String(), ctx.Err()
	}
	return stdout.String(), stderr.String(), err
}

func writeExecutable(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o700)
}

func installFakeATL(repositoryRoot, target string) error {
	data, err := os.ReadFile(filepath.Join(repositoryRoot, "scripts", "check-corpus-devcontainer", "testdata", "fake-atl.sh"))
	if err != nil {
		return err
	}
	return writeExecutable(target, string(data))
}

const fakeCurlScript = `#!/bin/sh
set -eu
output=
while [ "$#" -gt 0 ]; do
	if [ "$1" = -o ]; then
		shift
		output=$1
	fi
	shift
done
[ -n "$output" ]
cp "$ATL_FAKE_RELEASE_BINARY" "$output"
`

const fakeGHScript = `#!/bin/sh
set -eu
printf '%s\n' "$*" >"$ATL_FAKE_GH_LOG"
[ -z "${ATL_FAKE_GH_FAIL:-}" ] || exit 9
`
