package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/wikimerge"
)

// JiraApplyOpts tunes Apply.
type JiraApplyOpts struct {
	DryRun    bool
	AllowLoss bool
	Into      string               // mirror root override (defaults to nearest .atl)
	Render    config.RenderService // per-run markdown-view profile override
}

// JiraApplyResult is the JSON contract of `jira apply` — it mirrors conf apply's
// ApplyResult, swapping the CSF-specific fields for the `.wiki` substrate.
type JiraApplyResult struct {
	Path     string            `json:"path"`      // the .md that was applied
	WikiPath string            `json:"wiki_path"` // the .wiki that was (or would be) written
	DryRun   bool              `json:"dry_run"`
	Report   *wikimerge.Report `json:"report"`
	Wrote    bool              `json:"wrote"`
	Warning  string            `json:"warning,omitempty"` // post-write degradation (e.g. .md view not refreshed)
}

// Apply merges edits made in an issue's `.md` view back into its `.wiki`
// substrate (block-level, non-lossy: untouched Description blocks keep their
// exact base bytes). Only the generated `# Description` section is writable through the
// view — an edit to any other section (generated metadata/title, Comments, Links,
// Image Attachments) is detected and refused with a pointer to the dedicated
// command, so a stray edit never silently vanishes.
//
// It is a local operation — no backend access; `jira push` stays the write path
// to the server. Preconditions mirror conf apply: the issue was pulled through
// the sidecar (base + snapshot exist) and the local `.wiki` still matches the
// base (direct wiki edits win over the md surface; push or re-pull first).
//
// The pristine view the edit is diffed against is reproduced from the render
// settings recorded when the .md was last written (pull/render/apply), not the
// ambient config — so a profile change between render and apply never causes a
// spurious refusal. Explicit --render-* flags (o.Render) override the recorded
// settings; a pre-upgrade mirror with no recorded view falls back to the ambient
// config (today's behavior). The chosen settings are recorded again after the
// post-write refresh.
//
// CRLF: the base `.wiki` holds the server's bytes verbatim (Jira DC descriptions
// are typically CRLF). wikimerge preserves those bytes byte-for-byte — its block
// scanner treats `\n`, `\r\n`, and lone `\r` as one line break whose bytes live
// in the inter-block gaps, so an untouched view round-trips a CRLF base
// unchanged. The *edited* `.md` is normalized CRLF→LF
// (matching wikimd's own line handling) before section-splitting, comparison, and
// merge, since the pristine view it is diffed against is always LF.
func (s *JiraService) Apply(mdPath string, o JiraApplyOpts) (*JiraApplyResult, error) {
	if !strings.HasSuffix(mdPath, ".md") {
		return nil, fmt.Errorf("%w: jira apply takes the issue's .md view (got %q)", domain.ErrUsage, mdPath)
	}
	// Root discovery walks parent directories; a relative path (the common case
	// when running from inside the mirror) must be absolutized first or the walk
	// terminates immediately at ".".
	if abs, err := filepath.Abs(mdPath); err == nil {
		mdPath = abs
	}
	wikiPath := strings.TrimSuffix(mdPath, ".md") + wikiExt
	dir := filepath.Dir(wikiPath)
	keySeg := strings.TrimSuffix(filepath.Base(wikiPath), wikiExt)
	root := o.Into
	if root == "" {
		root = mirrorRootOf(wikiPath)
	}
	m := mirror.New(root)

	// The `.wiki` substrate must exist next to the .md.
	curWiki, err := safepath.ReadFileWithin(root, wikiPath)
	if err != nil {
		return nil, fmt.Errorf("%w: no %s next to %s — is this a mirrored issue? (jira mirror)", domain.ErrNotFound, keySeg+wikiExt, mdPath)
	}
	// A pristine base is required so the merge has a three-way anchor and the
	// divergence check has a baseline.
	baseWiki, ok := m.BaseBodyExt(keySeg, wikiExt)
	if !ok {
		return nil, fmt.Errorf("%w: no pristine base for %s — re-pull it (older mirrors lack .atl/base)", domain.ErrNotFound, keySeg)
	}
	// The local `.wiki` must still match the base: a direct wiki edit wins over
	// the md surface (exit 8), exactly like conf apply and jira push.
	if mirror.Hash(curWiki) != mirror.Hash(baseWiki) {
		return nil, fmt.Errorf("%w: %s has diverged from the last-synced base (the .wiki was edited directly) — push or re-pull before applying .md edits",
			domain.ErrCheckFailed, keySeg+wikiExt)
	}
	// The `<KEY>.json` snapshot supplies the metadata/section fields needed to
	// reproduce the pristine view.
	is, snapOK := loadIssueSnapshot(root, filepath.Join(dir, keySeg+".json"))
	if !snapOK {
		return nil, fmt.Errorf("%w: no %s.json snapshot for %s — re-pull it", domain.ErrNotFound, keySeg, keySeg)
	}

	rawEdited, err := safepath.ReadFileWithin(root, mdPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrNotFound, err)
	}
	edited := normalizeMD(string(rawEdited))

	// Reproduce the pristine view from the base body + snapshot fields under the
	// render settings the .md was actually written with, so an untouched .md
	// compares byte-identical and unchanged-block detection (image embeds
	// included) matches what the reader saw. Resolution order:
	//   1. explicit --render-* flags win (the escape hatch);
	//   2. else the settings recorded when this view was last rendered/pulled,
	//      ignoring the ambient config so a profile change between render and
	//      apply cannot cause a spurious refusal;
	//   3. else (pre-upgrade mirror with no recorded view) the ambient config,
	//      exactly today's behavior.
	is.Body = string(baseWiki)
	var rs RenderSettings
	switch {
	case renderOverrideSet(o.Render):
		rs, _ = ResolveRender(s.cfg, root, o.Render, "jira")
	default:
		vs, ok, verr := m.ViewStateOf(keySeg)
		if verr != nil {
			// Corrupt sidecar is already ErrCheckFailed — propagate it.
			return nil, verr
		}
		if ok {
			rs = settingsFromViewState(vs)
		} else {
			rs, _ = ResolveRender(s.cfg, root, config.RenderService{}, "jira")
		}
	}
	assets := assetsOnDisk(root, dir, keySeg)
	related := loadEpicChildrenSidecar(root, epicChildrenPath(dir, keySeg))
	if related != nil && !compatibleEpicSidecar(related, is.Key, rs.EpicField) {
		related = nil
	}
	if related != nil && (rs.EpicField == "" || !isDirectEpicFieldID(rs.EpicField)) {
		rs.EpicField = related.EpicField
	}
	prefix, _, suffix := renderIssueMarkdownPartsWithRelated(is, assets, related, rs)

	// Locate the edited description by the pristine view's structural anchors
	// (everything before/after it must be byte-identical) and refuse any edit
	// outside it. Stable generated markers and exact anchors keep remote headings
	// inside the Description regardless of their visible text.
	editedDesc, err := extractEditedDescription(edited, prefix, suffix)
	if err != nil {
		return nil, err
	}

	// Merge the Description bodies. baseWiki stays raw (its own bytes, CRLF or not);
	// editedDesc is LF-normalized. An untouched view yields the base body verbatim.
	merged, rep, err := wikimerge.Merge(baseWiki, editedDesc, wikimerge.Options{
		AllowLoss:     o.AllowLoss,
		Images:        assetImageMap(assets),
		HeadingOffset: 1,
	})
	res := &JiraApplyResult{Path: mdPath, WikiPath: wikiPath, DryRun: o.DryRun, Report: rep}
	if err != nil {
		// Every merge refusal — unconvertible block or removed-construct loss — is a
		// pre-write check failure (exit 8). A LossError carries the report so the
		// caller can show what would be dropped (conf apply parity).
		return res, fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
	}

	if o.DryRun {
		return res, nil
	}
	// Write only the `.wiki`; do NOT touch the sidecar or pristine base, so the
	// issue reads locally_edited (and still synced) afterwards and `jira push`
	// remains the transport under its own drift gate.
	if err := safepath.WriteFileWithin(root, wikiPath, merged, 0o644); err != nil {
		return res, err
	}
	res.Wrote = true
	// Refresh the .md view from the merged body so the two surfaces agree —
	// best-effort, same contract as pull: renderIssueMarkdown is total, so this
	// only fails on a write error, which is a warning (the .wiki write already
	// succeeded; erroring here would misreport the apply as failed and a retry
	// would refuse on base divergence).
	is.Body = string(merged)
	if werr := safepath.WriteFileWithin(root, mdPath, renderIssueMarkdownWithRelated(is, assets, related, rs), 0o644); werr != nil {
		res.Warning = "applied, but the .md view could not be refreshed and may be stale: " + werr.Error()
	} else if verr := m.SaveViewStates(map[string]mirror.ViewState{keySeg: viewStateOf(rs)}); verr != nil {
		// Record the settings the refreshed view was written with (best-effort,
		// same contract as the refresh itself: the .wiki write already succeeded).
		res.Warning = "applied, but the view state could not be recorded: " + verr.Error()
	}
	return res, nil
}

// normalizeMD strips a leading BOM and normalizes CRLF / lone CR to LF, matching
// wikimd's own line handling so the edited .md compares against the LF-only
// pristine view on equal terms.
func normalizeMD(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// extractEditedDescription locates the edited description between the pristine
// view's structural anchors and enforces the "only # Description is editable"
// contract: the edited document must start with the exact pristine prefix
// (title + generated metadata + the `# Description` boundary) and
// end with the exact pristine suffix (every generated section after the
// description). Whatever sits between the anchors is the edited description
// body. Anchoring — rather than re-parsing `## ` headings — is what keeps a
// body-internal heading as body content instead of a phantom read-only section.
//
// Trailing newlines are compared loosely (an editor may add or strip a final
// newline); everything else is byte-exact. Empty descriptions still carry the
// generated boundary, so adding text requires no special structural edit.
func extractEditedDescription(edited, prefix, suffix string) (string, error) {
	if !strings.HasPrefix(edited, prefix) {
		if strings.HasPrefix(edited, "---\n") && !strings.HasPrefix(prefix, "---\n") {
			return "", fmt.Errorf("%w: this is a legacy YAML-headed Jira view; run `jira render` (or pull again) before applying Markdown edits", domain.ErrCheckFailed)
		}
		return "", fmt.Errorf("%w: the generated metadata, title, section markers, or `# Description` heading changed, but they are read-only — edit summary/fields with `jira issue update` and keep the Description boundary", domain.ErrCheckFailed)
	}
	rest := strings.TrimRight(edited[len(prefix):], "\n")
	tail := strings.TrimRight(suffix, "\n")
	if tail != "" {
		if !strings.HasSuffix(rest, tail) {
			return "", fmt.Errorf("%w: the sections after the description (configured fields, Image Attachments, Attachments, Links, Subtasks, Epic Children, Sprint, Comments) are read-only in the md view — use the matching Jira field/comment/link/attachment command instead",
				domain.ErrCheckFailed)
		}
		rest = rest[:len(rest)-len(tail)]
	}
	return strings.Trim(rest, "\n"), nil
}
