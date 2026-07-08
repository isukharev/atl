package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/wikimerge"
)

// applyBody is a Description with a plain paragraph, a {panel} (a wiki-only
// construct that trips the loss gate), and a trailing paragraph.
const applyBody = "Intro paragraph.\n\n{panel:title=Note}\nheads up\n{panel}\n\nOutro paragraph."

// seedApplyMirror writes a pulled single-issue Jira mirror (.wiki + .json snapshot
// + pristine base + sidecar) and renders its .md view via the offline `jira
// render` command, so the .md matches exactly what `jira apply` regenerates. It
// returns the mirror root and the absolute .md path.
func seedApplyMirror(t *testing.T) (root, mdPath string) {
	t.Helper()
	root = t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"summary":     "Fix the thing",
		"description": applyBody,
		"status":      map[string]any{"name": "Open"},
		"issuetype":   map[string]any{"name": "Bug"},
		"project":     map[string]any{"key": "PROJ"},
		"comment": map[string]any{"comments": []any{
			map[string]any{"id": "c1", "author": map[string]any{"displayName": "carol"}, "created": "2026-01-02", "body": "a comment"},
		}},
	}
	snap := map[string]any{"key": "PROJ-1", "id": "1001", "fields": fields}
	b, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "PROJ-1.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PROJ-1.wiki"), []byte(applyBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveBaseExt("PROJ-1", []byte(applyBody), ".wiki"); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(mirror.SyncState{ID: "PROJ-1", Version: 0, Hash: mirror.Hash([]byte(applyBody)), Path: "PROJ/PROJ-1.wiki"})
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
	// Produce the .md view exactly as a pull would.
	if _, _, code := runCLIFull(t, nil, "jira", "render", root); code != exitOK {
		t.Fatalf("jira render: exit %d", code)
	}
	return root, filepath.Join(dir, "PROJ-1.md")
}

// TestJiraApplyDryRunGolden locks the JSON shape of `jira apply --dry-run` on a
// single-paragraph Description edit: it reports the merge (1 converted) and writes
// nothing.
func TestJiraApplyDryRunGolden(t *testing.T) {
	root, mdPath := seedApplyMirror(t)
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte(strings.Replace(string(md), "Intro paragraph.", "Intro paragraph, edited.", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, nil, "jira", "apply", mdPath, "--into", root, "--dry-run")
	if code != exitOK {
		t.Fatalf("jira apply --dry-run: exit %d (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_apply_dryrun.json", []byte(normalizeRoot(out, root)))
	if wiki, _ := os.ReadFile(filepath.Join(root, "PROJ", "PROJ-1.wiki")); string(wiki) != applyBody {
		t.Error("dry-run modified the .wiki")
	}
}

// TestJiraApplyLossGolden locks the exit-8 loss refusal: dropping the {panel}
// block reports it under removed_constructs and writes nothing.
func TestJiraApplyLossGolden(t *testing.T) {
	root, mdPath := seedApplyMirror(t)
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	// The panel renders to a blockquote; remove that block from the view.
	edited := strings.Replace(string(md), "> **Note**\n>\n> heads up\n\n", "", 1)
	if edited == string(md) {
		t.Fatalf("panel block not found to remove in view:\n%s", md)
	}
	if err := os.WriteFile(mdPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, nil, "jira", "apply", mdPath, "--into", root)
	if code != 8 {
		t.Fatalf("jira apply (loss): exit %d, want 8 (stdout=%q)", code, out)
	}
	if !strings.Contains(out, `"removed_constructs"`) {
		t.Errorf("loss not reported in output: %s", out)
	}
	assertGolden(t, "jira_apply_loss.json", []byte(normalizeRoot(out, root)))
	if wiki, _ := os.ReadFile(filepath.Join(root, "PROJ", "PROJ-1.wiki")); string(wiki) != applyBody {
		t.Error(".wiki modified on a loss refusal")
	}
}

// TestJiraApplyText locks the `-o text` loss-review contract of `jira apply` on
// an applied result that dropped a wiki construct under --allow-loss, with a
// relative (non-volatile) path.
func TestJiraApplyText(t *testing.T) {
	res := &app.JiraApplyResult{
		Path:     "PROJ/PROJ-1.md",
		WikiPath: "PROJ/PROJ-1.wiki",
		Wrote:    true,
		Report: &wikimerge.Report{
			Unchanged:         2,
			Converted:         1,
			RemovedConstructs: []wikimerge.Construct{{Kind: "panel", Text: "{panel:title=Note}…"}},
		},
	}
	want := strings.Join([]string{
		"applied: PROJ/PROJ-1.wiki",
		"blocks: 2 unchanged, 1 converted",
		"removed constructs:",
		`  - panel "{panel:title=Note}…"`,
		"next: run `jira push PROJ-1.wiki` to publish",
	}, "\n")
	if got := jiraApplyText(res); got != want {
		t.Errorf("jiraApplyText:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestJiraApply_TextUnchangedDryRun drives the CLI: `-o text` on an unedited
// dry-run reports the all-unchanged merge and the "write it" hint.
func TestJiraApply_TextUnchangedDryRun(t *testing.T) {
	root, mdPath := seedApplyMirror(t)
	out, code := runCLI(t, nil, "jira", "apply", mdPath, "--into", root, "--dry-run", "-o", "text")
	if code != exitOK {
		t.Fatalf("exit %d (stdout=%q)", code, out)
	}
	for _, want := range []string{
		"dry-run: no files written",
		"blocks: ",
		"unchanged",
		"next: apply without --dry-run to write",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "removed constructs:") {
		t.Errorf("unedited apply should report no removed constructs:\n%s", out)
	}
}

// TestJiraApply_TextLoss drives the CLI: `-o text` with --allow-loss on a
// dropped {panel} prints a readable review listing the removed construct.
func TestJiraApply_TextLoss(t *testing.T) {
	root, mdPath := seedApplyMirror(t)
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(md), "> **Note**\n>\n> heads up\n\n", "", 1)
	if edited == string(md) {
		t.Fatalf("panel block not found to remove in view:\n%s", md)
	}
	if err := os.WriteFile(mdPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, nil, "jira", "apply", mdPath, "--into", root, "--allow-loss", "--dry-run", "-o", "text")
	if code != exitOK {
		t.Fatalf("exit %d (stdout=%q)", code, out)
	}
	for _, want := range []string{
		"dry-run: no files written",
		"removed constructs:",
		"next: restore the construct(s) in the .md",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}

// TestJiraApplyNotMd exits 2 (usage) when the target is not a .md.
func TestJiraApplyNotMd(t *testing.T) {
	_, code := runCLI(t, nil, "jira", "apply", "PROJ-1.wiki")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
