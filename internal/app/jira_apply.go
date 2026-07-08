package app

import (
	"fmt"
	"os"
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
// exact base bytes). Only the `## Description` section is writable through the
// view — an edit to any other section (frontmatter/title, Comments, Links,
// Image Attachments) is detected and refused with a pointer to the dedicated
// command, so a stray edit never silently vanishes.
//
// It is a local operation — no backend access; `jira push` stays the write path
// to the server. Preconditions mirror conf apply: the issue was pulled through
// the sidecar (base + snapshot exist) and the local `.wiki` still matches the
// base (direct wiki edits win over the md surface; push or re-pull first).
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
	curWiki, err := os.ReadFile(wikiPath)
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
	// The `<KEY>.json` snapshot supplies the frontmatter/section fields needed to
	// reproduce the pristine view.
	is, snapOK := loadIssueSnapshot(filepath.Join(dir, keySeg+".json"))
	if !snapOK {
		return nil, fmt.Errorf("%w: no %s.json snapshot for %s — re-pull it", domain.ErrNotFound, keySeg, keySeg)
	}

	rawEdited, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrNotFound, err)
	}
	edited := normalizeMD(string(rawEdited))

	// Reproduce the pristine view from the base body + snapshot fields under the
	// effective render settings, so an untouched .md compares byte-identical and
	// unchanged-block detection (image embeds included) matches what the reader saw.
	is.Body = string(baseWiki)
	rs, _ := ResolveRender(s.cfg, root, o.Render, "jira")
	assets := assetsOnDisk(dir, keySeg)
	pristine := string(renderIssueMarkdown(is, assets, rs))

	// Split both views into a preamble + `## `-delimited sections and refuse any
	// edit outside `## Description` with a pointer to the dedicated command.
	editedDesc, err := reconcileSections(pristine, edited)
	if err != nil {
		return nil, err
	}

	// Merge the Description bodies. baseWiki stays raw (its own bytes, CRLF or not);
	// editedDesc is LF-normalized. An untouched view yields the base body verbatim.
	merged, rep, err := wikimerge.Merge(baseWiki, editedDesc, wikimerge.Options{
		AllowLoss: o.AllowLoss,
		Images:    assetImageMap(assets),
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
	if err := safepath.WriteFile(wikiPath, merged, 0o644); err != nil {
		return res, err
	}
	res.Wrote = true
	// Refresh the .md view from the merged body so the two surfaces agree —
	// best-effort, same contract as pull: renderIssueMarkdown is total, so this
	// only fails on a write error, which is a warning (the .wiki write already
	// succeeded; erroring here would misreport the apply as failed and a retry
	// would refuse on base divergence).
	is.Body = string(merged)
	if werr := safepath.WriteFile(mdPath, renderIssueMarkdown(is, assets, rs), 0o644); werr != nil {
		res.Warning = "applied, but the .md view could not be refreshed and may be stale: " + werr.Error()
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

// mdSection is one `## `-delimited section of a rendered issue view: its heading
// name and its raw lines (the `## name` line first, then the body lines).
type mdSection struct {
	name  string
	lines []string
}

// full is the section's exact bytes (heading line + body), as they appear in the
// document.
func (sec mdSection) full() string { return strings.Join(sec.lines, "\n") }

// body is the section content below the heading, trimmed of the blank lines that
// frame it — for `## Description` this is exactly wikimd's render of the body.
func (sec mdSection) body() string {
	if len(sec.lines) <= 1 {
		return ""
	}
	return strings.Trim(strings.Join(sec.lines[1:], "\n"), "\n")
}

// splitMDSections splits a rendered issue markdown view into the preamble (bytes
// before the first top-level `## ` heading — the frontmatter and title) and the
// ordered list of sections. A `## ` line inside a fenced code block does NOT
// start a section: the Description body can contain ``` fences (from a rendered
// `{code}`/`{noformat}` macro), and splitting inside one would corrupt the merge.
func splitMDSections(md string) (preamble string, sections []mdSection) {
	lines := strings.Split(md, "\n")
	inFence := false
	var pre []string
	inPreamble := true
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(line, "## ") {
			inPreamble = false
			sections = append(sections, mdSection{name: strings.TrimSpace(line[len("## "):]), lines: []string{line}})
			continue
		}
		if inPreamble {
			pre = append(pre, line)
			continue
		}
		last := &sections[len(sections)-1]
		last.lines = append(last.lines, line)
	}
	return strings.Join(pre, "\n"), sections
}

// reconcileSections enforces the "only ## Description is editable" contract: it
// refuses (ErrCheckFailed) any change to the preamble (frontmatter/title) or to a
// non-Description section — added, removed, or altered — and returns the edited
// Description body to merge ("" when the edit empties or omits it). Description
// may legitimately appear on one side only (an empty base body has no Description
// section in the pristine view; emptying it removes it from the edit).
func reconcileSections(pristine, edited string) (editedDesc string, err error) {
	preP, secsP := splitMDSections(pristine)
	preE, secsE := splitMDSections(edited)
	if preE != preP {
		return "", fmt.Errorf("%w: the frontmatter/title changed, but they are read-only in the md view — edit the summary and fields with `jira issue update`",
			domain.ErrCheckFailed)
	}
	byName := func(secs []mdSection) (map[string]mdSection, error) {
		out := make(map[string]mdSection, len(secs))
		for _, sec := range secs {
			if _, dup := out[sec.name]; dup {
				return nil, fmt.Errorf("%w: the md view has a duplicate '## %s' section — only ## Description is editable through the view",
					domain.ErrCheckFailed, sec.name)
			}
			out[sec.name] = sec
		}
		return out, nil
	}
	mapP, err := byName(secsP)
	if err != nil {
		return "", err
	}
	mapE, err := byName(secsE)
	if err != nil {
		return "", err
	}
	// Every non-Description section must be byte-identical and present on both
	// sides. Scanning both maps covers alterations, additions, and removals.
	seen := map[string]bool{}
	for _, name := range append(sectionNames(secsP), sectionNames(secsE)...) {
		if name == "Description" || seen[name] {
			continue
		}
		seen[name] = true
		p, okP := mapP[name]
		e, okE := mapE[name]
		switch {
		case okP && !okE:
			return "", fmt.Errorf("%w: the '## %s' section was removed, but it is read-only in the md view — %s",
				domain.ErrCheckFailed, name, sectionHint(name))
		case okE && !okP:
			return "", fmt.Errorf("%w: a new '## %s' section was added, but it is read-only in the md view — %s",
				domain.ErrCheckFailed, name, sectionHint(name))
		case p.full() != e.full():
			return "", fmt.Errorf("%w: the '## %s' section was edited, but it is read-only in the md view — %s",
				domain.ErrCheckFailed, name, sectionHint(name))
		}
	}
	if e, ok := mapE["Description"]; ok {
		return e.body(), nil
	}
	return "", nil
}

// sectionNames lists the section names in document order.
func sectionNames(secs []mdSection) []string {
	out := make([]string, len(secs))
	for i, sec := range secs {
		out[i] = sec.name
	}
	return out
}

// sectionHint points at the dedicated command that owns a read-only section, so a
// refusal tells the user where the edit belongs instead of silently dropping it.
func sectionHint(name string) string {
	switch name {
	case "Comments":
		return "add comments with `jira issue comment add`"
	case "Links":
		return "manage links with `jira issue link add`"
	case "Image Attachments", "Attachments":
		return "upload attachments with `jira issue attachment upload`"
	default:
		return "only ## Description is editable through the view"
	}
}
