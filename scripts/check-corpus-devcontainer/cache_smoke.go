package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runCacheBootstrapSmoke(repositoryRoot, temporary string) error {
	root := filepath.Join(temporary, "cache-bootstrap")
	if err := os.Mkdir(root, 0o700); err != nil {
		return err
	}
	sourceRoot := filepath.Join(root, "source")
	aggregateCacheRoot := filepath.Join(root, "aggregate-cache")
	jiraCacheRoot := filepath.Join(root, "jira-cache")
	confluenceCacheRoot := filepath.Join(root, "confluence-cache")
	inputsRoot := filepath.Join(root, "inputs")
	for _, directory := range []string{sourceRoot, aggregateCacheRoot, jiraCacheRoot, confluenceCacheRoot, inputsRoot} {
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

	const jiraSecret = "synthetic-cache-jira-secret-canary"
	const confluenceSecret = "synthetic-cache-confluence-secret-canary"
	privateFiles := map[string]string{
		"jira-url":         "https://jira.example.test\n",
		"jira-pat":         jiraSecret + "\n",
		"jira-project":     "EXAMPLE\n",
		"confluence-url":   "https://confluence.example.test\n",
		"confluence-pat":   confluenceSecret + "\n",
		"confluence-space": "EXAMPLE\n",
		"private-ca":       "synthetic cache certificate fixture\n",
	}
	for name, content := range privateFiles {
		if err := os.WriteFile(filepath.Join(inputsRoot, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	fakeATL := filepath.Join(root, "fake-atl")
	if err := installFakeATL(repositoryRoot, fakeATL); err != nil {
		return err
	}
	runs := []struct {
		name       string
		initialize string
		cacheRoot  string
		jira       bool
		confluence bool
		withCA     bool
	}{
		{name: "aggregate-initialize", initialize: "1", cacheRoot: aggregateCacheRoot, jira: true, confluence: true, withCA: true},
		{name: "aggregate-reuse", initialize: "0", cacheRoot: aggregateCacheRoot, jira: true, confluence: true, withCA: true},
		{name: "jira-initialize", initialize: "1", cacheRoot: jiraCacheRoot, jira: true, withCA: true},
		{name: "confluence-initialize", initialize: "1", cacheRoot: confluenceCacheRoot, confluence: true},
	}
	for _, run := range runs {
		indexRoot := filepath.Join(root, run.name+"-index")
		contextParent := filepath.Join(root, run.name+"-contexts")
		for _, directory := range []string{indexRoot, contextParent} {
			if err := os.Mkdir(directory, 0o700); err != nil {
				return err
			}
		}
		indexer := filepath.Join(root, run.name+"-isolated-indexer")
		indexerScript := "#!/bin/sh\nset -eu\n" +
			"case \"${ATL_CORPUS_DOCUMENT:-}\" in\n\t" + shellSingleQuote(run.cacheRoot) + "|" + shellSingleQuote(run.cacheRoot) + "/*) exit 2 ;;\nesac\n" +
			"if env | grep -F -- " + shellSingleQuote(run.cacheRoot) + " >/dev/null 2>&1; then exit 2; fi\n" +
			"exec " + shellSingleQuote(filepath.Join(repositoryRoot, exampleRelative, "local-indexer-stub.sh")) + "\n"
		if err := writeExecutable(indexer, indexerScript); err != nil {
			return err
		}
		environment := []string{
			"PATH=" + os.Getenv("PATH"),
			"ATL_SOURCE_ROOT=" + sourceRoot,
			"ATL_INDEX_ROOT=" + indexRoot,
			"ATL_INDEXER=" + indexer,
			"ATL_BIN=" + fakeATL,
			"ATL_CONTEXT_PARENT=" + contextParent,
			"ATL_CACHE_ROOT=" + run.cacheRoot,
			"ATL_INITIALIZE_CACHE=" + run.initialize,
			"ATL_CACHE_MAX_REQUESTS=25",
			"ATL_CACHE_MAX_RESPONSE_BYTES=2097152",
			"ATL_CACHE_DEADLINE=30s",
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
			"ATL_JIRA_CA_BUNDLE=/synthetic/ambient-jira-ca.pem",
			"ATL_CONFLUENCE_CA_BUNDLE=/synthetic/ambient-confluence-ca.pem",
			"SSL_CERT_DIR=/synthetic/ambient-certificates",
			"HTTPS_PROXY=http://ambient-proxy.example.test:8080",
			"UNRELATED_SECRET=synthetic-cache-ambient-secret-canary",
		}
		if run.withCA {
			environment = append(environment, "ATL_CA_FILE="+filepath.Join(inputsRoot, "private-ca"))
		}
		if run.jira {
			environment = append(environment,
				"ATL_JIRA_URL_FILE="+filepath.Join(inputsRoot, "jira-url"),
				"ATL_JIRA_PAT_FILE="+filepath.Join(inputsRoot, "jira-pat"),
				"ATL_JIRA_PROJECT_FILE="+filepath.Join(inputsRoot, "jira-project"),
				"ATL_MAX_JIRA_ISSUES=10",
			)
		}
		if run.confluence {
			environment = append(environment,
				"ATL_CONFLUENCE_URL_FILE="+filepath.Join(inputsRoot, "confluence-url"),
				"ATL_CONFLUENCE_PAT_FILE="+filepath.Join(inputsRoot, "confluence-pat"),
				"ATL_CONFLUENCE_SPACE_FILE="+filepath.Join(inputsRoot, "confluence-space"),
				"ATL_MAX_CONFLUENCE_PAGES=20",
			)
		}
		stdout, stderr, runErr := runCommand(filepath.Join(repositoryRoot, exampleRelative, "run-corpus.sh"), environment)
		if runErr != nil {
			return fmt.Errorf("cache %s smoke: %w: %s", run.name, runErr, stderr)
		}
		if strings.TrimSpace(stdout) != `{"schema_version":1,"status":"complete","handoff":"sealed","indexer":"completed"}` || stderr != "" {
			return fmt.Errorf("unexpected cache %s output stdout=%q stderr=%q", run.name, stdout, stderr)
		}
		runtimeRoot, err := onlyRuntimeRoot(contextParent)
		if err != nil {
			return err
		}
		if err := verifyCacheFakeATLCommands(runtimeRoot, run.cacheRoot, run.initialize == "1", run.jira, run.confluence); err != nil {
			return err
		}
		corpusEntries, err := os.ReadDir(filepath.Join(runtimeRoot, "corpus"))
		if err != nil || len(corpusEntries) != 0 {
			return errors.New("cache build populated the ephemeral corpus store")
		}
		if _, err := os.Stat(filepath.Join(indexRoot, "index-receipt.v1.json")); err != nil {
			return errors.New("cache handoff did not reach the isolated indexer")
		}
		for _, privateRoot := range []string{contextParent, indexRoot, run.cacheRoot} {
			if err := verifyPrivateTree(privateRoot); err != nil {
				return err
			}
		}
		for _, marker := range []string{jiraSecret, confluenceSecret} {
			for _, scanRoot := range []string{contextParent, indexRoot, run.cacheRoot, sourceRoot} {
				found, scanErr := containsMarker(scanRoot, marker)
				if scanErr != nil {
					return scanErr
				}
				if found {
					return errors.New("cache runtime secret entered generated state")
				}
			}
		}
	}
	after, err := treeDigest(sourceRoot)
	if err != nil || before != after {
		return errors.New("cache bootstrap changed the source checkout")
	}
	return nil
}

func verifyNoCacheFakeATLCommands(contextParent string) error {
	runtimeRoot, err := onlyRuntimeRoot(contextParent)
	if err != nil {
		return err
	}
	expected := []string{
		"corpus build --root " + filepath.Join(runtimeRoot, "corpus") +
			" --initialize --max-requests 100 --max-response-bytes 1048576 --max-members 1000" +
			" --max-generation-bytes 10485760 --deadline 5m --max-in-flight 2 --requests-per-second 10" +
			" --jira-project EXAMPLE --max-jira-issues 10",
		"corpus handoff --store " + filepath.Join(runtimeRoot, "corpus") +
			" --handoff-artifact " + filepath.Join(runtimeRoot, "handoff", "current.indexer-handoff.v1.json"),
	}
	return verifyFakeATLCommands(runtimeRoot, expected)
}

func verifyCacheFakeATLCommands(runtimeRoot, cacheRoot string, initialize, jira, confluence bool) error {
	cacheArguments := " --cache-root " + cacheRoot
	if initialize {
		cacheArguments += " --initialize-cache"
	}
	cacheArguments += " --cache-max-requests 25 --cache-max-response-bytes 2097152 --cache-deadline 30s"
	build := "corpus build --root " + filepath.Join(runtimeRoot, "corpus") +
		" --initialize --max-requests 100 --max-response-bytes 1048576 --max-members 1000" +
		" --max-generation-bytes 10485760 --deadline 5m --max-in-flight 2 --requests-per-second 10" + cacheArguments
	if jira {
		build += " --jira-project EXAMPLE --max-jira-issues 10"
	}
	if confluence {
		build += " --confluence-space EXAMPLE --max-confluence-pages 20"
	}
	expected := []string{
		build,
		"corpus handoff --store " + cacheRoot +
			" --handoff-artifact " + filepath.Join(runtimeRoot, "handoff", "current.indexer-handoff.v1.json"),
	}
	return verifyFakeATLCommands(runtimeRoot, expected)
}

func onlyRuntimeRoot(contextParent string) (string, error) {
	entries, err := os.ReadDir(contextParent)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return "", errors.New("fake ATL runtime root count drifted")
	}
	return filepath.Join(contextParent, entries[0].Name()), nil
}

func verifyFakeATLCommands(runtimeRoot string, expected []string) error {
	logBytes, err := os.ReadFile(filepath.Join(runtimeRoot, "atl-argv.log"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(logBytes), "\n"), "\n")
	if len(lines) != len(expected) {
		return fmt.Errorf("fake ATL command count drifted: got=%d want=%d", len(lines), len(expected))
	}
	for index, want := range expected {
		if lines[index] != want {
			return fmt.Errorf("fake ATL argv drifted at command %d", index+1)
		}
		if strings.HasPrefix(lines[index], "doctor ") || strings.HasPrefix(lines[index], "jira issue ") || strings.HasPrefix(lines[index], "conf search ") {
			return errors.New("wrapper performed a remote preflight before corpus validation")
		}
	}
	return nil
}

type cacheSecurityFixture struct {
	root           string
	sourceRoot     string
	indexRoot      string
	contextParent  string
	inputsRoot     string
	cacheRoot      string
	indexer        string
	fakeATL        string
	fakeATLInvoked string
}

type cacheSecurityEnvironment struct {
	cacheRoot              string
	initialize             string
	omitInitialize         bool
	cacheMaxRequests       string
	omitCacheMaxRequests   bool
	cacheMaxResponseBytes  string
	omitCacheResponseBytes bool
	cacheDeadline          string
	omitCacheDeadline      bool
	caFile                 string
}

func runCachePathSecuritySmoke(repositoryRoot, temporary string) error {
	securityRoot := filepath.Join(temporary, "cache-path-security")
	if err := os.Mkdir(securityRoot, 0o700); err != nil {
		return err
	}
	tests := []struct {
		name  string
		setup func(*cacheSecurityFixture, *cacheSecurityEnvironment) error
		want  string
	}{
		{name: "relative", setup: func(_ *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = "relative-cache"
			return nil
		}, want: "ATL_CACHE_ROOT must be absolute"},
		{name: "missing", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = filepath.Join(fixture.root, "missing-cache")
			return nil
		}, want: "ATL_CACHE_ROOT must be an existing non-symlink directory"},
		{name: "regular-file", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = filepath.Join(fixture.root, "cache-file")
			return os.WriteFile(environment.cacheRoot, []byte("not a directory\n"), 0o600)
		}, want: "ATL_CACHE_ROOT must be an existing non-symlink directory"},
		{name: "special-file", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = filepath.Join(fixture.root, "cache-fifo")
			return exec.Command("mkfifo", environment.cacheRoot).Run()
		}, want: "ATL_CACHE_ROOT must be an existing non-symlink directory"},
		{name: "symlink", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = filepath.Join(fixture.root, "cache-link")
			return os.Symlink(fixture.cacheRoot, environment.cacheRoot)
		}, want: "ATL_CACHE_ROOT must be an existing non-symlink directory"},
		{name: "symlink-ancestor", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			actual := filepath.Join(fixture.sourceRoot, "aliased-cache")
			if err := os.Mkdir(actual, 0o700); err != nil {
				return err
			}
			alias := filepath.Join(fixture.root, "source-alias")
			if err := os.Symlink(fixture.sourceRoot, alias); err != nil {
				return err
			}
			environment.cacheRoot = filepath.Join(alias, "aliased-cache")
			return nil
		}, want: "ATL_CACHE_ROOT must not traverse symlinks"},
		{name: "mode", setup: func(fixture *cacheSecurityFixture, _ *cacheSecurityEnvironment) error {
			return os.Chmod(fixture.cacheRoot, 0o750)
		}, want: "ATL_CACHE_ROOT must have exact mode 0700"},
		{name: "source-same", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = fixture.sourceRoot
			return nil
		}, want: "cache root overlaps a protected boundary"},
		{name: "source-ancestor", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = fixture.root
			return nil
		}, want: "cache root overlaps a protected boundary"},
		{name: "source-descendant", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = filepath.Join(fixture.sourceRoot, "cache")
			return os.Mkdir(environment.cacheRoot, 0o700)
		}, want: "cache root overlaps a protected boundary"},
		{name: "index-same", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = fixture.indexRoot
			return nil
		}, want: "cache root overlaps a protected boundary"},
		{name: "runtime-parent", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = fixture.contextParent
			return nil
		}, want: "cache root overlaps a protected boundary"},
		{name: "runtime-descendant", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = filepath.Join(fixture.contextParent, "cache")
			return os.Mkdir(environment.cacheRoot, 0o700)
		}, want: "cache root overlaps a protected boundary"},
		{name: "mounted-input-ancestor", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheRoot = fixture.inputsRoot
			return nil
		}, want: "cache root overlaps a protected boundary"},
		{name: "ca-descendant", setup: func(fixture *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.caFile = filepath.Join(fixture.cacheRoot, "private-ca")
			return os.WriteFile(environment.caFile, []byte("synthetic certificate fixture\n"), 0o600)
		}, want: "cache root overlaps a protected boundary"},
		{name: "initialize-missing", setup: func(_ *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.omitInitialize = true
			return nil
		}, want: "ATL_INITIALIZE_CACHE must be 0 or 1"},
		{name: "initialize-invalid", setup: func(_ *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.initialize = "2"
			return nil
		}, want: "ATL_INITIALIZE_CACHE must be 0 or 1"},
		{name: "request-budget-missing", setup: func(_ *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.omitCacheMaxRequests = true
			return nil
		}, want: "ATL_CACHE_MAX_REQUESTS must be a positive integer"},
		{name: "response-budget-zero", setup: func(_ *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.cacheMaxResponseBytes = "0"
			return nil
		}, want: "ATL_CACHE_MAX_RESPONSE_BYTES must be a positive integer"},
		{name: "deadline-missing", setup: func(_ *cacheSecurityFixture, environment *cacheSecurityEnvironment) error {
			environment.omitCacheDeadline = true
			return nil
		}, want: "ATL_CACHE_DEADLINE is required"},
	}
	for _, test := range tests {
		fixture, err := newCacheSecurityFixture(repositoryRoot, filepath.Join(securityRoot, test.name))
		if err != nil {
			return err
		}
		environment := cacheSecurityEnvironment{
			cacheRoot:             fixture.cacheRoot,
			initialize:            "0",
			cacheMaxRequests:      "10",
			cacheMaxResponseBytes: "1048576",
			cacheDeadline:         "30s",
		}
		if err := test.setup(fixture, &environment); err != nil {
			return err
		}
		stdout, stderr, runErr := runCommand(filepath.Join(repositoryRoot, exampleRelative, "run-corpus.sh"), fixture.environment(environment))
		if runErr == nil || stdout != "" || !strings.Contains(stderr, test.want) {
			return fmt.Errorf("cache security case %s did not fail closed: error=%v stdout=%q stderr=%q", test.name, runErr, stdout, stderr)
		}
		if strings.Contains(stderr, "synthetic-cache-path-secret-canary") ||
			(environment.cacheRoot != "" && strings.Contains(stderr, environment.cacheRoot)) {
			return fmt.Errorf("cache security case %s exposed private input", test.name)
		}
		if _, err := os.Lstat(fixture.fakeATLInvoked); !os.IsNotExist(err) {
			return fmt.Errorf("cache security case %s invoked ATL before refusal", test.name)
		}
		entries, err := os.ReadDir(fixture.indexRoot)
		if err != nil || len(entries) != 0 {
			return fmt.Errorf("cache security case %s populated the index", test.name)
		}
	}
	return nil
}

func newCacheSecurityFixture(repositoryRoot, root string) (*cacheSecurityFixture, error) {
	fixture := &cacheSecurityFixture{
		root:           root,
		sourceRoot:     filepath.Join(root, "source"),
		indexRoot:      filepath.Join(root, "index"),
		contextParent:  filepath.Join(root, "contexts"),
		inputsRoot:     filepath.Join(root, "inputs"),
		cacheRoot:      filepath.Join(root, "cache"),
		indexer:        filepath.Join(repositoryRoot, exampleRelative, "local-indexer-stub.sh"),
		fakeATL:        filepath.Join(root, "fake-atl"),
		fakeATLInvoked: filepath.Join(root, "atl-invoked"),
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	for _, directory := range []string{fixture.sourceRoot, fixture.indexRoot, fixture.contextParent, fixture.inputsRoot, fixture.cacheRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return nil, err
		}
	}
	privateFiles := map[string]string{
		"jira-url":     "https://backend.example.test\n",
		"jira-pat":     "synthetic-cache-path-secret-canary\n",
		"jira-project": "EXAMPLE\n",
	}
	for name, content := range privateFiles {
		if err := os.WriteFile(filepath.Join(fixture.inputsRoot, name), []byte(content), 0o600); err != nil {
			return nil, err
		}
	}
	fakeScript := "#!/bin/sh\n: >" + shellSingleQuote(fixture.fakeATLInvoked) + "\nexit 99\n"
	if err := writeExecutable(fixture.fakeATL, fakeScript); err != nil {
		return nil, err
	}
	return fixture, nil
}

func (fixture *cacheSecurityFixture) environment(settings cacheSecurityEnvironment) []string {
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"ATL_SOURCE_ROOT=" + fixture.sourceRoot,
		"ATL_INDEX_ROOT=" + fixture.indexRoot,
		"ATL_INDEXER=" + fixture.indexer,
		"ATL_BIN=" + fixture.fakeATL,
		"ATL_CONTEXT_PARENT=" + fixture.contextParent,
		"ATL_CACHE_ROOT=" + settings.cacheRoot,
		"ATL_JIRA_URL_FILE=" + filepath.Join(fixture.inputsRoot, "jira-url"),
		"ATL_JIRA_PAT_FILE=" + filepath.Join(fixture.inputsRoot, "jira-pat"),
		"ATL_JIRA_PROJECT_FILE=" + filepath.Join(fixture.inputsRoot, "jira-project"),
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
	if !settings.omitInitialize {
		environment = append(environment, "ATL_INITIALIZE_CACHE="+settings.initialize)
	}
	if !settings.omitCacheMaxRequests {
		environment = append(environment, "ATL_CACHE_MAX_REQUESTS="+settings.cacheMaxRequests)
	}
	if !settings.omitCacheResponseBytes {
		environment = append(environment, "ATL_CACHE_MAX_RESPONSE_BYTES="+settings.cacheMaxResponseBytes)
	}
	if !settings.omitCacheDeadline {
		environment = append(environment, "ATL_CACHE_DEADLINE="+settings.cacheDeadline)
	}
	if settings.caFile != "" {
		environment = append(environment, "ATL_CA_FILE="+settings.caFile)
	}
	return environment
}
