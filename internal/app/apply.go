package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdmerge"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

// ApplyOpts tunes Apply.
type ApplyOpts struct {
	DryRun            bool
	AllowFragmentLoss bool
	Into              string // mirror root override (defaults to nearest .atl)
}

// ApplyResult is the JSON contract of `conf apply`.
type ApplyResult struct {
	Path    string          `json:"path"`     // the .md that was applied
	CSFPath string          `json:"csf_path"` // the .csf that was (or would be) written
	DryRun  bool            `json:"dry_run"`
	Report  *mdmerge.Report `json:"report"`
	CSFOK   bool            `json:"csf_ok"`
	Wrote   bool            `json:"wrote"`
	Warning string          `json:"warning,omitempty"` // post-write degradation (e.g. .md view not refreshed)
}

// Apply merges edits from a page's .md view into its .csf (block-level,
// non-lossy: untouched blocks keep their exact base bytes). It is a local
// operation — no backend access; `conf push` stays the write path to the
// server. Preconditions: the page was pulled (meta + pristine base exist) and
// the local .csf still matches the base (direct CSF edits win over the md
// surface; push or re-pull first).
func Apply(mdPath string, o ApplyOpts) (*ApplyResult, error) {
	if !strings.HasSuffix(mdPath, ".md") {
		return nil, fmt.Errorf("%w: conf apply takes the page's .md view (got %q)", domain.ErrUsage, mdPath)
	}
	// Root discovery walks parent directories; a relative path (the common
	// case when running from inside the mirror) must be absolutized first or
	// the walk terminates immediately at ".".
	if abs, err := filepath.Abs(mdPath); err == nil {
		mdPath = abs
	}
	csfPath := strings.TrimSuffix(mdPath, ".md") + ".csf"
	root := o.Into
	if root == "" {
		root = mirrorRootOf(csfPath)
	}
	m := mirror.New(root)
	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()

	lc, cur, err := m.LoadCSF(csfPath)
	if err != nil {
		// A corrupt sidecar is its own actionable failure (exit 8) — wrapping it
		// as "not a mirrored page" would misdirect toward a pointless re-pull.
		if errors.Is(err, domain.ErrCheckFailed) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: no .csf next to %s — is this a mirrored page? (%v)", domain.ErrNotFound, mdPath, err)
	}
	if lc.Meta.ID == "" {
		return nil, fmt.Errorf("%w: %s has no .meta.json — pull the page first", domain.ErrNotFound, csfPath)
	}
	base, ok := m.BaseBody(lc.Meta.ID)
	if !ok {
		return nil, fmt.Errorf("%w: no pristine base for page %s — re-pull it (older mirrors lack .atl/base)", domain.ErrNotFound, lc.Meta.ID)
	}
	if mirror.Hash(cur) != mirror.Hash(base) {
		return nil, fmt.Errorf("%w: %s has diverged from the last-synced base (the .csf was edited directly) — push or re-pull before applying .md edits",
			domain.ErrCheckFailed, csfPath)
	}
	rawEdited, err := safepath.ReadFileWithin(m.Root, mdPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrNotFound, err)
	}
	edited := normalizeMD(string(rawEdited))
	if strings.HasPrefix(edited, "---\n") {
		return nil, fmt.Errorf("%w: this is a legacy YAML-headed Confluence view; save edits outside the derived view, run `conf render` (or pull again), then reapply them", domain.ErrCheckFailed)
	}
	if err := validateConfluenceDocumentMarker(edited); err != nil {
		return nil, err
	}

	dir := filepath.Dir(csfPath)
	slug := strings.TrimSuffix(filepath.Base(csfPath), ".csf")

	// Resolve the recorded view state and reproduce the exact generated prefix /
	// suffix around the editable body. A corrupt sidecar is already
	// ErrCheckFailed — propagate it. A pre-version mirror must first be rendered
	// again because its document marker was rejected above.
	vs, hasView, verr := m.ViewStateOf(lc.Meta.ID)
	if verr != nil {
		return nil, verr
	}
	rsView := settingsFromViewState(vs)
	decorated := hasView

	node, perr := csf.Parse(base)
	if perr != nil {
		return nil, fmt.Errorf("%w: pristine CSF for page %s no longer parses; re-pull before applying Markdown edits: %v", domain.ErrCheckFailed, lc.Meta.ID, perr)
	}
	page := confPageFromMeta(lc.Meta)
	mdOpts, sidecarErr := confMDViewOptsFromSidecars(rsView, page, readCommentsSidecar(m.Root, dir, slug), m.Root, dir, slug, lc.Meta.ID, node)
	if sidecarErr != nil {
		return nil, fmt.Errorf("%w: Jira macro enrichment sidecar cannot reproduce the generated view: %v; remove only the generated .jira-macros.json sidecar, then run `conf pull`", domain.ErrCheckFailed, sidecarErr)
	}
	prefix, pristineBody, suffix := mirror.RenderMarkdownViewParts(node, lc.Meta.Refs, mdOpts)
	mergeInput, err := extractConfBody(edited, prefix, pristineBody, suffix)
	if err != nil {
		return nil, err
	}

	out, rep, err := mdmerge.Merge(base, lc.Meta.Refs, mergeInput, mdmerge.Options{
		AllowFragmentLoss: o.AllowFragmentLoss,
	})
	res := &ApplyResult{Path: mdPath, CSFPath: csfPath, DryRun: o.DryRun, Report: rep}
	if err != nil {
		// Every merge refusal — unconvertible block, fragment loss, internal
		// invariant breach — is a pre-write check failure (exit 8).
		return res, fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
	}
	res.CSFOK = !csf.HasErrors(rep.Problems)

	if o.DryRun {
		return res, nil
	}
	if err := safepath.WriteFileWithin(m.Root, csfPath, out, 0o644); err != nil {
		return res, err
	}
	res.Wrote = true
	// Renormalize the md view from the merged body so the two surfaces agree —
	// best-effort, same contract as pull: the read-view must never silently
	// contradict the .csf, so an unparseable merge result gets the explicit
	// stub, and a failed write is a warning, not an error (the .csf write
	// already succeeded; erroring here would tell the user the apply failed
	// when it did not, and a retry would refuse on base divergence). On the
	// decorated path the refresh re-emits the same generated metadata/comments so the
	// view stays full; otherwise it is the body-only render.
	md := []byte(mirror.MDUnavailableStub)
	stub := true
	if root2, perr := csf.Parse(out); perr == nil {
		stub = false
		if decorated {
			if jiraMacroDescriptorHash(mirror.JiraMacroDescriptors(node)) != jiraMacroDescriptorHash(mirror.JiraMacroDescriptors(root2)) {
				if removeErr := writeConfluenceJiraMacroSidecar(m.Root, dir, slug, nil); removeErr != nil {
					res.Warning = "applied, but the obsolete Jira macro sidecar could not be removed: " + removeErr.Error()
				} else {
					res.Warning = "applied; Jira query results were retired because the native macro set changed — re-pull to resolve remaining macros"
				}
			}
			opts, sidecarErr := confMDViewOptsFromSidecars(rsView, confPageFromMeta(lc.Meta), readCommentsSidecar(m.Root, dir, slug), m.Root, dir, slug, lc.Meta.ID, root2)
			if sidecarErr == nil {
				md = mirror.RenderMarkdownOpts(root2, lc.Meta.Refs, opts)
			} else {
				md = mirror.RenderMarkdownOpts(root2, lc.Meta.Refs, confMDViewOpts(rsView, confPageFromMeta(lc.Meta), readCommentsSidecar(m.Root, dir, slug)))
				res.Warning = "applied, but Jira macro enrichment could not be refreshed; re-pull the page"
			}
		} else {
			md = mirror.RenderMarkdownOpts(root2, lc.Meta.Refs, mirror.MDViewOpts{})
		}
	}
	if werr := safepath.WriteFileWithin(m.Root, mdPath, md, 0o644); werr != nil {
		res.Warning = "applied, but the .md view could not be refreshed and may be stale: " + werr.Error()
	} else if !stub {
		// Record the settings the refreshed view was written with: the recorded
		// (decorated) settings, or body-only when no decorations applied. Skipped
		// when the stub was written (there is no faithful view to reproduce).
		used := RenderSettings{Sections: map[string]bool{}}
		if decorated {
			used = rsView
		}
		if rerr := m.SaveViewStates(map[string]mirror.ViewState{lc.Meta.ID: viewStateOf(used)}); rerr != nil {
			res.Warning = "applied, but the view state could not be recorded: " + rerr.Error()
		}
	}
	return res, nil
}

// confPageFromMeta builds the minimal domain.Resource the markdown-view helpers
// need from a page's mirror meta.
func confPageFromMeta(meta mirror.Meta) *domain.Resource {
	return &domain.Resource{
		Title:      meta.Title,
		SpaceKey:   meta.Space,
		Version:    meta.Version,
		Parent:     meta.Parent,
		Ancestors:  meta.Ancestors,
		Labels:     meta.Labels,
		Updated:    meta.Updated,
		Restricted: meta.Restricted,
	}
}

// extractConfBody isolates the editable page body from a decorated `.md` view
// (generated metadata above, a "# Comments" section below) and enforces that
// both decorations are read-only. The edited document must start with the
// byte-exact pristine prefix (generated metadata) and end with the pristine suffix (the
// Comments section) — trailing newlines compared loosely, everything else
// byte-exact. Whatever sits between the anchors is the edited body, returned with
// a single trailing newline so it feeds mdmerge exactly like a body-only view
// would (an untouched full view then yields zero converted blocks and a
// byte-identical .csf). Anchoring — not re-parsing `## ` headings — is what keeps
// a body heading (which also renders as a top-level `## ` line) as body content.
func extractConfBody(edited, prefix, pristineBody, suffix string) (string, error) {
	if !strings.HasPrefix(edited, prefix) {
		return "", fmt.Errorf("%w: generated page metadata or the body boundary above editable content changed; run `conf render` only after preserving any edits externally", domain.ErrCheckFailed)
	}
	rest := strings.TrimRight(edited[len(prefix):], "\n")
	tail := strings.TrimRight(suffix, "\n")
	if tail != "" {
		var suffixErr error
		rest, suffixErr = stripConfluenceGeneratedSuffix(rest, tail)
		if suffixErr != nil {
			return "", suffixErr
		}
	}
	body := strings.Trim(rest, "\n") + "\n"
	if !sameReservedMarkerText(body, pristineBody) {
		return "", fmt.Errorf("%w: editable Confluence body added, removed, renamed, or reordered reserved atl document/section marker text; edit the native .csf for intentional marker prose changes", domain.ErrCheckFailed)
	}
	return body, nil
}

func stripConfluenceGeneratedSuffix(edited, pristine string) (string, error) {
	type section struct {
		marker string
		label  string
		remedy string
	}
	// Generated order is Jira Queries followed by Comments, so validate and
	// strip in reverse. This lets an unchanged trailing Comments section prove
	// that a preceding Jira table—not Comments—was the region that changed.
	sections := []section{
		{marker: mirror.ConfluenceCommentsMarker, label: "# Comments", remedy: "use `conf comment add`"},
		{marker: mirror.ConfluenceJiraMacrosMarker, label: "# Jira Queries", remedy: "re-pull to refresh query results and use Jira commands to change issues"},
	}
	remaining := pristine
	for _, generated := range sections {
		needle := "\n" + generated.marker + "\n"
		start := strings.Index(remaining, needle)
		if start < 0 {
			if strings.HasPrefix(remaining, generated.marker+"\n") {
				start = 0
			} else {
				continue
			}
		}
		sectionBytes := remaining[start:]
		if !strings.HasSuffix(edited, sectionBytes) {
			return "", fmt.Errorf("%w: the %q generated section is read-only in the md view — %s", domain.ErrCheckFailed, generated.label, generated.remedy)
		}
		edited = strings.TrimRight(edited[:len(edited)-len(sectionBytes)], "\n")
		remaining = strings.TrimRight(remaining[:start], "\n")
	}
	if remaining != "" {
		if !strings.HasSuffix(edited, remaining) {
			return "", fmt.Errorf("%w: a generated read-only suffix changed; restore it from `conf render` after preserving body edits", domain.ErrCheckFailed)
		}
		edited = edited[:len(edited)-len(remaining)]
	}
	return edited, nil
}

func sameReservedMarkerText(edited, pristine string) bool {
	tokens := func(s string) []string {
		var out []string
		for {
			start := strings.Index(s, mirror.ConfluenceReservedPrefix)
			if start < 0 {
				return out
			}
			s = s[start:]
			end := strings.Index(s, "-->")
			if end < 0 {
				out = append(out, s)
				return out
			}
			token := s[:end+3]
			out = append(out, token)
			s = s[end+3:]
		}
	}
	a, b := tokens(edited), tokens(pristine)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateConfluenceDocumentMarker(edited string) error {
	first, _, _ := strings.Cut(edited, "\n")
	if first == mirror.ConfluenceDocumentMarker {
		return nil
	}
	if first == "<!-- atl:document confluence-page v2 -->" || first == "<!-- atl:document confluence-page v1 -->" || first == "<!-- atl:document confluence-page -->" {
		return fmt.Errorf("%w: this Confluence view uses a legacy document format; preserve edits outside the derived view, run `conf render` (or pull again) with this binary, then reapply them", domain.ErrCheckFailed)
	}
	if strings.HasPrefix(first, "<!-- atl:document confluence-page") {
		return fmt.Errorf("%w: unsupported Confluence view format marker %q; preserve edits and update atl before opening this view — do not render or downgrade it with this binary", domain.ErrCheckFailed, first)
	}
	return fmt.Errorf("%w: missing Confluence view format marker %q; save edits outside the derived view, run `conf render` (or pull again), then reapply them", domain.ErrCheckFailed, mirror.ConfluenceDocumentMarker)
}
