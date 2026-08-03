package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/testbackend"
)

const onboardingDemoTimeout = 20 * time.Second

var canonicalCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

type binaryIdentity struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	BuildState string `json:"build_state"`
}

type demoCommandResult struct {
	stdout string
	stderr string
}

type demoRunner struct {
	binary string
	root   string
	env    []string
}

func validateDemos(atlBinary string) (int, error) {
	binary, err := filepath.Abs(atlBinary)
	if err != nil {
		return 0, fmt.Errorf("resolve demo atl binary: %w", err)
	}
	demos := []struct {
		name string
		run  func(string) error
	}{
		{name: "lossless Confluence edit", run: validateLosslessConfluenceDemo},
		{name: "Confluence conflict refusal", run: validateConfluenceConflictDemo},
		{name: "bounded Jira artifact graph", run: validateJiraGraphDemo},
	}
	for index, demo := range demos {
		if err := demo.run(binary); err != nil {
			return index, fmt.Errorf("onboarding demo %q failed: %w", demo.name, err)
		}
	}
	return len(demos), nil
}

func newDemoRunner(binary string, overlay map[string]string) (*demoRunner, error) {
	binary, err := filepath.Abs(binary)
	if err != nil {
		return nil, fmt.Errorf("resolve demo atl binary: %w", err)
	}
	root, err := os.MkdirTemp("", "atl-onboarding-demo-")
	if err != nil {
		return nil, fmt.Errorf("create demo workspace: %w", err)
	}
	configDir := filepath.Join(root, "config")
	tempDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("create demo config: %w", err)
	}
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("create demo temp directory: %w", err)
	}
	environment := map[string]string{
		"ATL_CONFIG_DIR":  configDir,
		"ATL_NO_UPDATE":   "1",
		"HOME":            filepath.Join(root, "home"),
		"HTTP_PROXY":      "http://127.0.0.1:1",
		"HTTPS_PROXY":     "http://127.0.0.1:1",
		"NO_PROXY":        "127.0.0.1,localhost",
		"TEMP":            tempDir,
		"TMP":             tempDir,
		"TMPDIR":          tempDir,
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg"),
	}
	for name, value := range overlay {
		environment[name] = value
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+environment[name])
	}
	return &demoRunner{binary: binary, root: root, env: env}, nil
}

func validateBinaryIdentity(repositoryRoot, binary string) (identity binaryIdentity, retErr error) {
	expectedBytes, err := os.ReadFile(filepath.Join(repositoryRoot, "VERSION"))
	if err != nil {
		return identity, fmt.Errorf("read repository VERSION: %w", err)
	}
	expected := strings.TrimSpace(string(expectedBytes))
	if expected == "" {
		return identity, errors.New("repository VERSION is empty")
	}
	runner, err := newDemoRunner(binary, nil)
	if err != nil {
		return identity, err
	}
	defer func() { retErr = errors.Join(retErr, runner.close()) }()

	result, err := runner.run(0, "version")
	if err != nil {
		return identity, err
	}
	if err := json.Unmarshal([]byte(result.stdout), &identity); err != nil {
		return identity, fmt.Errorf("decode atl version: %w", err)
	}
	if identity.Version == "dev" || identity.Version != expected {
		return identity, fmt.Errorf("atl version identity %q does not match repository VERSION %q", identity.Version, expected)
	}
	if !canonicalCommitRE.MatchString(identity.Commit) {
		return identity, fmt.Errorf("atl version commit %q is not a canonical full lowercase git SHA", identity.Commit)
	}
	if identity.BuildState != "clean" && identity.BuildState != "dirty" {
		return identity, fmt.Errorf("atl version build_state %q is not one of clean or dirty", identity.BuildState)
	}
	return identity, nil
}

func validateCleanConfig(binary string) (retErr error) {
	const syntheticURL = "http://127.0.0.1:1/wiki"
	runner, err := newDemoRunner(binary, nil)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, runner.close()) }()

	configPath := filepath.Join(runner.root, "config", "config.json")
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fresh config path exists before config set: %v", err)
	}
	set, err := runner.run(0, "config", "set", "--confluence-url", syntheticURL)
	if err != nil {
		return err
	}
	var persisted struct {
		ConfluenceURL string `json:"confluence_url"`
	}
	if err := json.Unmarshal([]byte(set.stdout), &persisted); err != nil || persisted.ConfluenceURL != syntheticURL {
		return fmt.Errorf("config set did not select the synthetic Confluence URL: url=%q err=%v", persisted.ConfluenceURL, err)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read persisted clean config: %w", err)
	}
	if bytes.Contains(bytes.ToLower(contents), []byte("token")) || bytes.Contains(bytes.ToLower(contents), []byte("pat")) {
		return errors.New("config set persisted credential-like material")
	}
	shown, err := runner.run(0, "config", "show")
	if err != nil {
		return err
	}
	var selected struct {
		ConfluenceURL string `json:"confluence_url"`
	}
	if err := json.Unmarshal([]byte(shown.stdout), &selected); err != nil || selected.ConfluenceURL != syntheticURL {
		return fmt.Errorf("config show did not reload the synthetic Confluence URL: url=%q err=%v", selected.ConfluenceURL, err)
	}
	return nil
}

func (r *demoRunner) close() error {
	if r == nil {
		return nil
	}
	if err := os.RemoveAll(r.root); err != nil {
		return fmt.Errorf("remove demo workspace: %w", err)
	}
	return nil
}

func (r *demoRunner) run(expectedExit int, arguments ...string) (demoCommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), onboardingDemoTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, r.binary, arguments...)
	command.Dir = r.root
	command.Env = r.env
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return demoCommandResult{}, fmt.Errorf("atl %s exceeded %s", strings.Join(arguments, " "), onboardingDemoTimeout)
	}
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return demoCommandResult{}, fmt.Errorf("run atl %s: %w", strings.Join(arguments, " "), err)
		}
		exitCode = exitErr.ExitCode()
	}
	result := demoCommandResult{stdout: stdout.String(), stderr: stderr.String()}
	if exitCode != expectedExit {
		return result, fmt.Errorf("atl %s exited %d, want %d (stdout=%q stderr=%q)",
			strings.Join(arguments, " "), exitCode, expectedExit,
			boundedDetail(result.stdout), boundedDetail(result.stderr))
	}
	return result, nil
}

func boundedDetail(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 2048 {
		return value[:2048] + "..."
	}
	return value
}

func validateLosslessConfluenceDemo(binary string) (retErr error) {
	const (
		pageID = "7001"
		title  = "Synthetic complex page"
	)
	csfBody := `<h1>Demo</h1><p>Editable paragraph.</p>` +
		`<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter>` +
		"<ac:plain-text-body><![CDATA[fmt.Println(\"```\")]]></ac:plain-text-body></ac:structured-macro>" +
		`<p>Line one<br/>Line two</p>` +
		`<table><tbody><tr><th>Name</th><th>State</th></tr>` +
		`<tr><td><div class="content-wrapper"><p>alpha</p></div></td>` +
		`<td style="text-align: center;">?</td></tr></tbody></table>`
	fixture := testbackend.MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []testbackend.MockRoute{
			{
				Name: "lossless-page", Method: http.MethodGet, Path: "/wiki/rest/api/content/" + pageID,
				QueryEquals: map[string]string{"expand": "body.storage,version,space,ancestors,metadata.labels"},
				Status:      http.StatusOK, Body: demoPageJSON(pageID, title, 3, csfBody),
			},
			{
				Name: "lossless-preview-meta", Method: http.MethodGet, Path: "/wiki/rest/api/content/" + pageID,
				QueryEquals: map[string]string{"expand": "version,space,ancestors,metadata.labels,restrictions.read.restrictions.user,restrictions.read.restrictions.group"},
				Status:      http.StatusOK, Body: demoPageJSON(pageID, title, 3, csfBody),
			},
		},
		RequestSequence: []string{"lossless-page", "lossless-preview-meta"},
	}
	backend, err := testbackend.StartMockBackend(fixture)
	if err != nil {
		return err
	}
	defer backend.Close()
	runner, err := newDemoRunner(binary, backend.Environment())
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, runner.close()) }()

	mirrorRoot := filepath.Join(runner.root, "mirror")
	pulled, err := runner.run(0, "conf", "pull", "--id", pageID, "--into", mirrorRoot)
	if err != nil {
		return err
	}
	var pull app.PullResult
	if err := json.Unmarshal([]byte(pulled.stdout), &pull); err != nil || len(pull.Pages) != 1 {
		return fmt.Errorf("decode one-page pull: pages=%d err=%v", len(pull.Pages), err)
	}
	csfPath := filepath.Join(mirrorRoot, filepath.FromSlash(pull.Pages[0].Path))
	initial, err := os.ReadFile(csfPath)
	if err != nil {
		return fmt.Errorf("read pulled CSF: %w", err)
	}
	if string(initial) != csfBody {
		return errors.New("pull changed the synthetic native CSF bytes")
	}
	mdPath := strings.TrimSuffix(csfPath, ".csf") + ".md"
	markdown, err := os.ReadFile(mdPath)
	if err != nil {
		return fmt.Errorf("read derived Markdown: %w", err)
	}
	const beforeCell = "| alpha | ? |"
	const afterCell = "| alpha | ready |"
	if strings.Count(string(markdown), beforeCell) != 1 {
		return errors.New("derived Markdown did not expose exactly one editable synthetic table row")
	}
	editedMarkdown := strings.Replace(string(markdown), beforeCell, afterCell, 1)
	if err := os.WriteFile(mdPath, []byte(editedMarkdown), 0o644); err != nil {
		return fmt.Errorf("stage Markdown edit: %w", err)
	}

	dryRun, err := runner.run(0, "conf", "apply", mdPath, "--dry-run", "--into", mirrorRoot)
	if err != nil {
		return err
	}
	var dryResult app.ApplyResult
	if err := json.Unmarshal([]byte(dryRun.stdout), &dryResult); err != nil {
		return fmt.Errorf("decode apply dry-run: %w", err)
	}
	if !dryResult.DryRun || dryResult.Wrote || !dryResult.CSFOK || dryResult.Report == nil ||
		dryResult.Report.MergedTables != 1 || len(dryResult.Report.RemovedFragments) != 0 {
		return fmt.Errorf("unexpected apply dry-run qualification: %+v", dryResult)
	}
	afterDryRun, err := os.ReadFile(csfPath)
	if err != nil || !bytes.Equal(afterDryRun, initial) {
		return errors.New("apply dry-run changed the native CSF")
	}

	applied, err := runner.run(0, "conf", "apply", mdPath, "--into", mirrorRoot)
	if err != nil {
		return err
	}
	var applyResult app.ApplyResult
	if err := json.Unmarshal([]byte(applied.stdout), &applyResult); err != nil {
		return fmt.Errorf("decode apply result: %w", err)
	}
	if applyResult.DryRun || !applyResult.Wrote || !applyResult.CSFOK || applyResult.Report == nil ||
		applyResult.Report.MergedTables != 1 || len(applyResult.Report.RemovedFragments) != 0 {
		return fmt.Errorf("unexpected apply qualification: %+v", applyResult)
	}
	finalCSF, err := os.ReadFile(csfPath)
	if err != nil {
		return fmt.Errorf("read applied CSF: %w", err)
	}
	want := strings.Replace(csfBody, `>?<`, `>ready<`, 1)
	if string(finalCSF) != want {
		return errors.New("apply changed bytes outside the reviewed table cell")
	}
	previewed, err := runner.run(0, "conf", "push", csfPath, "--dry-run", "--into", mirrorRoot)
	if err != nil {
		return err
	}
	var preview app.PushResult
	if err := json.Unmarshal([]byte(previewed.stdout), &preview); err != nil || len(preview.Items) != 1 {
		return fmt.Errorf("decode push dry-run: items=%d err=%v", len(preview.Items), err)
	}
	item := preview.Items[0]
	if !item.DryRun || item.Pushed || item.ID != pageID || item.Path != csfPath || item.Drifted || item.Failed != "" {
		return fmt.Errorf("unexpected guarded push preview: %+v", item)
	}
	return requireDemoBackend(backend, map[string]int{http.MethodGet: 2})
}

func validateConfluenceConflictDemo(binary string) (retErr error) {
	const (
		pageID = "7002"
		title  = "Synthetic conflict page"
		base   = "<p>Draft state.</p>"
		local  = "<p>Local state.</p>"
	)
	fixture := testbackend.MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []testbackend.MockRoute{
			{Name: "conflict-page", Method: http.MethodGet, Path: "/wiki/rest/api/content/" + pageID,
				Status: http.StatusOK, Body: demoPageJSON(pageID, title, 7, base)},
			{Name: "conflict-put", Method: http.MethodPut, Path: "/wiki/rest/api/content/" + pageID,
				RequestBody: demoUpdateJSON(title, 8, local), Status: http.StatusConflict,
				Body: demoJSON(map[string]any{"message": "synthetic version conflict"})},
		},
		RequestSequence: []string{"conflict-page", "conflict-put"},
	}
	backend, err := testbackend.StartMockBackend(fixture)
	if err != nil {
		return err
	}
	defer backend.Close()
	runner, err := newDemoRunner(binary, backend.Environment())
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, runner.close()) }()

	mirrorRoot := filepath.Join(runner.root, "mirror")
	pulled, err := runner.run(0, "conf", "pull", "--id", pageID, "--into", mirrorRoot)
	if err != nil {
		return err
	}
	var pull app.PullResult
	if err := json.Unmarshal([]byte(pulled.stdout), &pull); err != nil || len(pull.Pages) != 1 {
		return fmt.Errorf("decode conflict pull: pages=%d err=%v", len(pull.Pages), err)
	}
	csfPath := filepath.Join(mirrorRoot, filepath.FromSlash(pull.Pages[0].Path))
	if _, err := runner.run(0, "conf", "edit", csfPath, "--old", "Draft", "--new", "Local"); err != nil {
		return err
	}
	before, err := os.ReadFile(csfPath)
	if err != nil || string(before) != local {
		return errors.New("local CSF did not contain the exact staged conflict edit")
	}
	beforeHash := sha256.Sum256(before)
	beforeTree, err := demoTreeDigest(mirrorRoot)
	if err != nil {
		return fmt.Errorf("snapshot local conflict artifacts: %w", err)
	}

	failedPush, err := runner.run(5, "conf", "push", csfPath, "--into", mirrorRoot)
	if err != nil {
		return err
	}
	var push app.PushResult
	if err := json.Unmarshal([]byte(failedPush.stdout), &push); err != nil || len(push.Items) != 1 {
		return fmt.Errorf("decode conflict push: items=%d err=%v", len(push.Items), err)
	}
	if push.Items[0].Pushed || push.Items[0].Skipped != "version-conflict" {
		return fmt.Errorf("conflict result claimed an unexpected outcome: %+v", push.Items[0])
	}
	var envelope struct {
		Kind string `json:"kind"`
		Code int    `json:"code"`
	}
	if err := json.Unmarshal([]byte(failedPush.stderr), &envelope); err != nil ||
		envelope.Code != 5 || envelope.Kind != "version_conflict" {
		return fmt.Errorf("unexpected conflict error envelope: kind=%q code=%d err=%v", envelope.Kind, envelope.Code, err)
	}
	after, err := os.ReadFile(csfPath)
	if err != nil || !bytes.Equal(after, before) || sha256.Sum256(after) != beforeHash {
		return errors.New("version-conflict handling changed the local CSF")
	}
	afterTree, err := demoTreeDigest(mirrorRoot)
	if err != nil {
		return fmt.Errorf("resnapshot local conflict artifacts: %w", err)
	}
	if beforeTree != afterTree {
		return errors.New("version-conflict handling changed the local mirror artifact set")
	}
	return requireDemoBackend(backend, map[string]int{http.MethodGet: 1, http.MethodPut: 1})
}

func validateJiraGraphDemo(binary string) (retErr error) {
	const issuePath = "/jira/rest/api/2/issue/DEMO-1"
	fixture := testbackend.MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []testbackend.MockRoute{
			{Name: "graph-snapshot", Method: http.MethodGet, Path: issuePath,
				QueryEquals: map[string]string{"fields": "*all", "properties": "*all", "expand": "names,schema"},
				Status:      http.StatusOK, Body: json.RawMessage(`{
					"id":"10001","key":"DEMO-1",
					"fields":{"summary":"Graph seed","description":"See DEMO-2 and pageId=7","issuelinks":[{"id":"9","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"outwardIssue":{"id":"10003","key":"DEMO-3"}}],"parent":null,"subtasks":[],"attachment":[{"id":"4","filename":"design.txt","content":"https://unreachable.invalid/download"}]},
					"names":{"summary":"Summary","description":"Description"},
					"schema":{"summary":{"type":"string","system":"summary"},"description":{"type":"string","system":"description"}},"properties":{}
				}`)},
			{Name: "graph-comments", Method: http.MethodGet, Path: issuePath + "/comment",
				Status: http.StatusOK, Body: json.RawMessage(`{"startAt":0,"total":0,"comments":[]}`)},
			{Name: "graph-worklogs", Method: http.MethodGet, Path: issuePath + "/worklog",
				Status: http.StatusOK, Body: json.RawMessage(`{"startAt":0,"total":0,"worklogs":[]}`)},
			{Name: "graph-remote-links", Method: http.MethodGet, Path: issuePath + "/remotelink",
				Status: http.StatusOK, Body: json.RawMessage(`[]`)},
		},
		RequestSequence: []string{"graph-snapshot", "graph-comments", "graph-worklogs", "graph-remote-links"},
	}
	backend, err := testbackend.StartMockBackend(fixture)
	if err != nil {
		return err
	}
	defer backend.Close()
	runner, err := newDemoRunner(binary, backend.Environment())
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, runner.close()) }()

	result, err := runner.run(0,
		"--read-only", "jira", "issue", "graph", "DEMO-1",
		"--depth", "0", "--max-nodes", "10", "--max-edges", "10",
		"--max-evidence", "10", "--max-requests", "8", "--max-bytes", "65536", "--strict",
	)
	if err != nil {
		return err
	}
	if strings.Contains(result.stdout, "unreachable.invalid") {
		return errors.New("graph exposed or followed the synthetic attachment URL")
	}
	var graph app.JiraIssueGraphResult
	if err := json.Unmarshal([]byte(result.stdout), &graph); err != nil {
		return fmt.Errorf("decode Jira graph: %w", err)
	}
	if graph.SchemaVersion != 2 || !graph.Complete || graph.Truncated || len(graph.Frontier) != 0 ||
		graph.Bounds.RequestedDepth != 0 || graph.Bounds.MaxNodes != 10 || graph.Bounds.MaxEdges != 10 ||
		graph.Bounds.MaxEvidence != 10 || graph.Bounds.MaxRequests != 8 || graph.Bounds.RequestsUsed != 4 ||
		graph.Bounds.MaxResponseBytes != 65536 || graph.Bounds.ResponseBytesUsed <= 0 {
		return fmt.Errorf("unexpected bounded graph envelope: %+v", graph.Bounds)
	}
	summary := graph.Summary
	if summary.NodeCount != len(graph.Nodes) || summary.EdgeCount != len(graph.Edges) ||
		summary.SourceCount != len(graph.Sources) || !summary.NodeCountMatchesNodes ||
		!summary.EdgeCountMatchesEdges || !summary.EvidenceCountMatchesEdges ||
		!summary.SourceCountMatchesSources || !summary.SourceStatusCountsMatch ||
		!summary.IncompleteCountMatches || !summary.ExpandedCountMatchesNodes || !summary.CompleteMatchesSources {
		return fmt.Errorf("graph reconciliation failed: %+v", summary)
	}
	wantNodes := map[string]domain.ArtifactGraphNodeState{
		"jira:issue:DEMO-1": domain.ArtifactNodeResolved,
		"jira:issue:DEMO-2": domain.ArtifactNodeUnresolved,
		"jira:issue:DEMO-3": domain.ArtifactNodeStub,
		"confluence:page:7": domain.ArtifactNodeStub,
		"jira:attachment:4": domain.ArtifactNodeResolved,
	}
	for _, node := range graph.Nodes {
		if state, ok := wantNodes[node.ID]; ok && state == node.State {
			delete(wantNodes, node.ID)
		}
	}
	if len(wantNodes) != 0 {
		return fmt.Errorf("graph omitted qualified synthetic nodes: %v", wantNodes)
	}
	return requireDemoBackend(backend, map[string]int{http.MethodGet: 4})
}

func demoTreeDigest(root string) ([sha256.Size]byte, error) {
	type record struct {
		Path   string `json:"path"`
		Mode   uint32 `json:"mode"`
		Size   int    `json:"size"`
		SHA256 string `json:"sha256"`
	}
	const maxBytes = 16 << 20
	records := make([]record, 0)
	total := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("local demo artifact is not a regular file: %s", path)
		}
		body, err := safepath.ReadFileWithin(root, path)
		if err != nil {
			return err
		}
		total += len(body)
		if total > maxBytes {
			return fmt.Errorf("local demo artifacts exceed %d bytes", maxBytes)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		records = append(records, record{
			Path: filepath.ToSlash(relative), Mode: uint32(info.Mode().Perm()), Size: len(body),
			SHA256: fmt.Sprintf("%x", digest),
		})
		return nil
	})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func requireDemoBackend(backend *testbackend.MockBackend, wantMethods map[string]int) error {
	methods, unexpected, duplicates := backend.Summary()
	if unexpected != 0 || duplicates != 0 || !backend.RequestSequenceComplete() || len(methods) != len(wantMethods) {
		return fmt.Errorf("synthetic backend summary methods=%v unexpected=%d duplicates=%d sequence_complete=%t",
			methods, unexpected, duplicates, backend.RequestSequenceComplete())
	}
	for method, count := range wantMethods {
		if methods[method] != count {
			return fmt.Errorf("synthetic backend method %s count=%d want=%d", method, methods[method], count)
		}
	}
	return nil
}

func demoPageJSON(id, title string, version int, body string) json.RawMessage {
	return demoJSON(map[string]any{
		"id": id, "type": "page", "title": title,
		"space":   map[string]any{"key": "DEMO"},
		"version": map[string]any{"number": version},
		"body": map[string]any{"storage": map[string]any{
			"representation": "storage", "value": body,
		}},
	})
}

func demoUpdateJSON(title string, version int, body string) json.RawMessage {
	return demoJSON(map[string]any{
		"type": "page", "title": title,
		"version": map[string]any{"number": version},
		"body": map[string]any{"storage": map[string]any{
			"representation": "storage", "value": body,
		}},
	})
}

func demoJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
