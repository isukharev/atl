package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

// wikiExt is the extension of the Jira substrate file and its base copy.
const wikiExt = ".wiki"

// ---- status ----

// JiraStatusEntry reports the sync state of one mirrored issue. Synced is false
// for a `.wiki` with no sidecar entry (never pulled through the sidecar, or
// pulled by a pre-sidecar build) — such a file also reads LocallyEdited, so the
// pair (LocallyEdited && !Synced) distinguishes "never-synced" from a genuine
// edit of a tracked file.
type JiraStatusEntry struct {
	Path          string `json:"path"`
	Key           string `json:"key"`
	LocallyEdited bool   `json:"locally_edited"`
	Synced        bool   `json:"synced"`
	RemoteDrifted bool   `json:"remote_drifted,omitempty"`
	RemoteError   string `json:"remote_error,omitempty"`
}

// Status reports locally-edited and (with checkRemote) remote-drifted issues
// under dir. Drift is measured against the pristine BASE body, so it needs a
// baseline: an issue with no base copy is never reported as drifted (conf
// parity). A remote fetch failure is recorded in RemoteError so an issue that
// could not be checked never reads as in-sync.
func (s *JiraService) Status(ctx context.Context, dir string, checkRemote bool) ([]JiraStatusEntry, error) {
	if dir == "" {
		dir = "mirror-jira"
	}
	m := mirror.New(dir)
	locals, err := m.ListWiki()
	if err != nil {
		return nil, err
	}
	out := make([]JiraStatusEntry, 0, len(locals))
	for _, lw := range locals {
		e := JiraStatusEntry{Path: lw.Path, Key: lw.Key, LocallyEdited: lw.Dirty, Synced: lw.Synced != nil}
		if checkRemote && lw.Key != "" {
			if base, ok := m.BaseBodyExt(lw.Key, wikiExt); ok {
				if is, gerr := s.tr.GetIssue(ctx, lw.Key, []string{"description"}); gerr == nil {
					e.RemoteDrifted = mirror.Hash([]byte(is.Body)) != mirror.Hash(base)
				} else {
					e.RemoteError = failReason(gerr)
				}
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// ---- push ----

// JiraPushOpts controls a push. A push is a dry-run preview UNLESS Apply is set:
// that is the #29 safety default for a backend with no server-side version gate.
// Force overrides the local drift refusal (re-base on current remote and write).
type JiraPushOpts struct {
	Apply bool
	Force bool
	Into  string // mirror root (for target resolution + refresh-after-push)
}

// JiraPushItem is the outcome for one issue.
type JiraPushItem struct {
	Path            string `json:"path"`
	Key             string `json:"key"`
	Pushed          bool   `json:"pushed"`
	DryRun          bool   `json:"dry_run,omitempty"`
	Skipped         string `json:"skipped,omitempty"`
	Drifted         bool   `json:"remote_drifted,omitempty"`
	DriftOverridden bool   `json:"drift_overridden,omitempty"`
	Diff            string `json:"diff,omitempty"`
	Failed          string `json:"failed,omitempty"`
	Warning         string `json:"warning,omitempty"`
}

// JiraPushResult aggregates per-issue outcomes.
type JiraPushResult struct {
	Items []JiraPushItem `json:"items"`
}

// Push previews (default) or applies an edited `.wiki` description back to its
// issue. It writes only the description body (the #29 allowlist): Update is
// called with an empty summary and nil fields, so no other field is touched.
//
// Jira has no server-side version gate, so the staleness guard is an app-layer
// compare-and-swap: the local base is compared to a fresh remote read, and a
// drift is refused (ErrCheckFailed → exit 8) unless Force. This CAS has an
// inherent TOCTOU window — the remote can change between the read and the write
// — which is documented rather than hidden; --force does not close it, it opts
// out of the refusal.
func (s *JiraService) Push(ctx context.Context, target string, o JiraPushOpts) (*JiraPushResult, error) {
	root := o.Into
	if root == "" {
		root = mirrorRootOf(target)
	}
	m := mirror.New(root)
	files, err := s.jiraPushTargets(m, target)
	if err != nil {
		return nil, err
	}
	// Refresh-after-push rewrites the .md view; resolve the mirror's effective
	// render settings (no per-run override on push) so a `full`-profile mirror
	// keeps its rich view after a push instead of silently reverting to default.
	rs, _ := ResolveRender(s.cfg, root, config.RenderService{}, "jira")
	res := &JiraPushResult{}
	var worst error
	for _, f := range files {
		item, ferr := s.jiraPushOne(ctx, m, f, o, rs)
		res.Items = append(res.Items, item)
		// Surface the most actionable failure across a batch (drift/exit-8 wins;
		// see errRank).
		worst = moreSevereErr(worst, ferr)
	}
	return res, worst
}

func (s *JiraService) jiraPushTargets(m *mirror.Mirror, target string) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("%w: push target %q: %v", domain.ErrUsage, target, err)
	}
	if !info.IsDir() {
		if !strings.HasSuffix(target, wikiExt) {
			return nil, fmt.Errorf("%w: push target %q is not a .wiki substrate file", domain.ErrUsage, target)
		}
		return []string{target}, nil
	}
	// A directory push operates on the dirty set only: --force overrides the drift
	// refusal for those files but deliberately does not resurrect locally-clean
	// issues (that would create no-op remote writes). Force a specific clean file
	// by naming it as the target instead (conf parity).
	locals, err := m.ListWiki()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, lw := range locals {
		if lw.Dirty && within(target, lw.Path) {
			files = append(files, lw.Path)
		}
	}
	return files, nil
}

func (s *JiraService) jiraPushOne(ctx context.Context, m *mirror.Mirror, path string, o JiraPushOpts, rs RenderSettings) (JiraPushItem, error) {
	item := JiraPushItem{Path: path, DryRun: !o.Apply}
	lw, body, err := m.LoadWiki(path)
	if err != nil {
		return item, err
	}
	item.Key = lw.Key
	// The issue must have been pulled through the sidecar: without a synced entry
	// and a pristine base there is no baseline to diff or drift-check against, so
	// refuse and tell the user to pull first. This is a usage precondition (exit
	// 2), deliberately distinct from a drift refusal (exit 8).
	base, hasBase := m.BaseBodyExt(lw.Key, wikiExt)
	if lw.Synced == nil || !hasBase {
		return item, fmt.Errorf("%w: %s was not pulled through the mirror (no sidecar/base entry); run `jira pull` first", domain.ErrUsage, path)
	}
	// Nothing to push if unchanged and not forced: writing an identical body would
	// create a no-op remote revision.
	if !lw.Dirty && !o.Force {
		item.Skipped = "unchanged"
		return item, nil
	}
	// Fresh remote read of the description; drift = remote differs from the base.
	is, gerr := s.tr.GetIssue(ctx, lw.Key, []string{"description"})
	if gerr != nil {
		item.Failed = failReason(gerr)
		return item, gerr
	}
	if mirror.Hash([]byte(is.Body)) != mirror.Hash(base) {
		item.Drifted = true
		if !o.Force {
			// No server-side version gate on Jira: refuse the app-layer CAS rather
			// than clobber the remote change. NEVER ErrVersionConflict (issue #66) —
			// this is a local integrity refusal (ErrCheckFailed → exit 8).
			return item, fmt.Errorf("%w: %s: remote description changed since pull (no server-side version gate on Jira); re-pull or push --force", domain.ErrCheckFailed, lw.Key)
		}
		item.DriftOverridden = true
	}
	// Consequence preview: a compact unified diff of what the write changes ON THE
	// SERVER — current remote → local body. When there is no drift the remote
	// equals the base, so this is the base→local diff too; when drift is being
	// overridden by --force, diffing against the base would hide the remote-only
	// changes the write is about to destroy.
	item.Diff = unifiedDiff(is.Body, string(body), 3)
	if !o.Apply {
		// Dry-run (the default): stop before any write. No Update is issued.
		return item, nil
	}
	// Apply: description-only write (empty summary, nil fields — the #29 allowlist).
	if err := s.tr.Update(ctx, lw.Key, "", body, nil); err != nil {
		// A server 409 stays a generic conflict (issue #66) — passed through
		// untouched; it must not become ErrVersionConflict.
		item.Failed = failReason(err)
		return item, err
	}
	item.Pushed = true
	item.DryRun = false
	// Refresh the mirror so base/hash/sidecar track the new remote state; a stale
	// sidecar would make the NEXT push spuriously report drift. A failure here is
	// a warning, not an error (conf parity).
	if werr := s.refreshAfterPush(ctx, m, path, lw.Key, rs); werr != nil {
		item.Warning = "pushed but local refresh failed (re-pull recommended): " + werr.Error()
	}
	return item, nil
}

// refreshAfterPush re-fetches the issue and rewrites its `.wiki` substrate,
// rendered `.md` view, pristine base copy, and sidecar entry so the mirror
// tracks the post-push remote state. The rendered view does not re-download
// image assets; instead the already-downloaded <KEY>.assets/ files (if any) are
// re-indexed from disk so a previously `--assets`-pulled issue keeps its image
// section and inline embeds after a push.
func (s *JiraService) refreshAfterPush(ctx context.Context, m *mirror.Mirror, wikiPath, key string, rs RenderSettings) error {
	is, err := s.tr.GetIssue(ctx, key, jiraPullFields(nil, rs))
	if err != nil {
		return err
	}
	dir := filepath.Dir(wikiPath)
	keySeg := strings.TrimSuffix(filepath.Base(wikiPath), wikiExt)
	if err := safepath.WriteFileWithin(m.Root, wikiPath, []byte(is.Body), 0o644); err != nil {
		return err
	}
	mdPath := filepath.Join(dir, keySeg+".md")
	related := loadEpicChildrenSidecar(m.Root, epicChildrenPath(dir, keySeg))
	if err := safepath.WriteFileWithin(m.Root, mdPath, renderIssueMarkdownWithRelated(is, assetsOnDisk(m.Root, dir, keySeg), related, rs), 0o644); err != nil {
		_ = safepath.RemoveWithin(m.Root, mdPath) // best-effort view: never let it contradict the substrate
	}
	if err := m.SaveBaseExt(key, []byte(is.Body), wikiExt); err != nil {
		return err
	}
	relWiki, _ := filepath.Rel(m.Root, wikiPath)
	batch, err := m.BeginSync()
	if err != nil {
		return err
	}
	batch.Record(mirror.SyncState{ID: key, Version: 0, Hash: mirror.Hash([]byte(is.Body)), Path: relWiki})
	// Record the render settings the refreshed .md view was written with so a
	// later `jira apply` reproduces the exact pristine view.
	batch.RecordView(key, viewStateOf(rs))
	return batch.Flush()
}

// assetsOnDisk re-indexes the images already mirrored under <dir>/<keySeg>.assets/
// (written by a `pull --assets` as `<attachment-id>-<filename>`) so a refreshed
// render keeps linking them without re-downloading. A missing dir or an entry
// that does not match the id-prefix layout is simply skipped.
func assetsOnDisk(root, dir, keySeg string) []JiraIssueAsset {
	assetsSeg := keySeg + ".assets"
	entries, err := safepath.ReadDirWithin(root, filepath.Join(dir, assetsSeg))
	if err != nil {
		return nil
	}
	var out []JiraIssueAsset
	for _, e := range entries {
		info, infoErr := e.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			continue
		}
		id, name, ok := strings.Cut(e.Name(), "-")
		if !ok || id == "" || name == "" {
			continue
		}
		out = append(out, JiraIssueAsset{ID: id, Title: name, Path: assetsSeg + "/" + e.Name()})
	}
	return out
}

// ---- unified diff (self-contained, no external dependency) ----

// diffLineCap bounds how many input lines unifiedDiff will diff. A description
// body is normally small; beyond the cap the preview is summarized rather than
// left to bloat the push result (an O(n*m) LCS over huge inputs is also avoided).
const diffLineCap = 1000

// unifiedDiff returns a compact line-based unified diff from a→b with `context`
// lines of surrounding context, computed via an LCS over lines. It is
// self-contained (no external diff module). Enormous inputs are summarized.
// When a and b are equal the result is empty.
func unifiedDiff(a, b string, context int) string {
	if a == b {
		return ""
	}
	al := splitLines(a)
	bl := splitLines(b)
	if len(al) > diffLineCap || len(bl) > diffLineCap {
		return fmt.Sprintf("(diff omitted: %d vs %d lines exceed the preview cap of %d)\n", len(al), len(bl), diffLineCap)
	}
	ops := lcsDiff(al, bl)
	return formatUnified(ops, context)
}

// splitLines splits s into lines without a trailing empty element for a final
// newline, so "a\n" is one line "a" (the diff is a preview, not applied, so
// exact trailing-newline fidelity does not matter).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

type diffOp struct {
	kind byte // ' ' equal, '-' delete (in a), '+' insert (in b)
	line string
}

// lcsDiff computes a line edit script from a→b using a longest-common-
// subsequence DP, emitting deletes before inserts at each divergence.
func lcsDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// dp[i][j] = LCS length of a[i:] and b[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// formatUnified renders the edit script into unified-diff hunks with `context`
// lines of surrounding equal context and @@ headers carrying 1-based line ranges.
func formatUnified(ops []diffOp, context int) string {
	if context < 0 {
		context = 0
	}
	// Mark which ops are within `context` of a change so runs of unchanged lines
	// between distant hunks are dropped.
	keep := make([]bool, len(ops))
	for idx, op := range ops {
		if op.kind == ' ' {
			continue
		}
		lo := idx - context
		if lo < 0 {
			lo = 0
		}
		hi := idx + context
		if hi >= len(ops) {
			hi = len(ops) - 1
		}
		for k := lo; k <= hi; k++ {
			keep[k] = true
		}
	}
	var b strings.Builder
	aLine, bLine := 1, 1 // 1-based cursors into the a/b files
	i := 0
	for i < len(ops) {
		if !keep[i] {
			if ops[i].kind != '+' {
				aLine++
			}
			if ops[i].kind != '-' {
				bLine++
			}
			i++
			continue
		}
		// Start of a hunk: gather the contiguous kept run.
		j := i
		aStart, bStart := aLine, bLine
		var aCount, bCount int
		var body strings.Builder
		for j < len(ops) && keep[j] {
			op := ops[j]
			body.WriteByte(op.kind)
			body.WriteString(op.line)
			body.WriteByte('\n')
			if op.kind != '+' {
				aCount++
				aLine++
			}
			if op.kind != '-' {
				bCount++
				bLine++
			}
			j++
		}
		fmt.Fprintf(&b, "@@ -%s +%s @@\n", hunkRange(aStart, aCount), hunkRange(bStart, bCount))
		b.WriteString(body.String())
		i = j
	}
	return b.String()
}

// hunkRange formats a unified-diff range: "start,count", collapsing to "start"
// when count is 1 and to "start,0" (with start decremented per the diff spec)
// when the side is empty.
func hunkRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start-1)
	}
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
