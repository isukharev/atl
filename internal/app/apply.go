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
	decorated := hasView && (rsView.On(SecFrontmatter) || rsView.On(SecComments))

	node, perr := csf.Parse(base)
	if perr != nil {
		return nil, fmt.Errorf("%w: pristine CSF for page %s no longer parses; re-pull before applying Markdown edits: %v", domain.ErrCheckFailed, lc.Meta.ID, perr)
	}
	page := confPageFromMeta(lc.Meta)
	mdOpts := confMDViewOpts(rsView, page, readCommentsSidecar(m.Root, dir, slug))
	prefix, _, suffix := mirror.RenderMarkdownViewParts(node, lc.Meta.Refs, mdOpts)
	mergeInput, err := extractConfBody(edited, prefix, suffix)
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
	// decorated path the refresh re-emits the same frontmatter/comments so the
	// view stays full; otherwise it is the body-only render.
	md := []byte(mirror.MDUnavailableStub)
	stub := true
	if root2, perr := csf.Parse(out); perr == nil {
		stub = false
		if decorated {
			md = mirror.RenderMarkdownOpts(root2, lc.Meta.Refs, confMDViewOpts(rsView, confPageFromMeta(lc.Meta), readCommentsSidecar(m.Root, dir, slug)))
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
// need (frontmatter fields) from a page's mirror meta.
func confPageFromMeta(meta mirror.Meta) *domain.Resource {
	return &domain.Resource{
		Title:    meta.Title,
		SpaceKey: meta.Space,
		Version:  meta.Version,
		Labels:   meta.Labels,
	}
}

// extractConfBody isolates the editable page body from a decorated `.md` view
// (a YAML frontmatter above, a "## Comments" section below) and enforces that
// both decorations are read-only. The edited document must start with the
// byte-exact pristine prefix (frontmatter) and end with the pristine suffix (the
// Comments section) — trailing newlines compared loosely, everything else
// byte-exact. Whatever sits between the anchors is the edited body, returned with
// a single trailing newline so it feeds mdmerge exactly like a body-only view
// would (an untouched full view then yields zero converted blocks and a
// byte-identical .csf). Anchoring — not re-parsing `## ` headings — is what keeps
// a body heading (which also renders as a top-level `## ` line) as body content.
func extractConfBody(edited, prefix, suffix string) (string, error) {
	if !strings.HasPrefix(edited, prefix) {
		return "", fmt.Errorf("%w: generated frontmatter/metadata or the body boundary above editable content changed; run `conf render` only after preserving any edits externally", domain.ErrCheckFailed)
	}
	rest := strings.TrimRight(edited[len(prefix):], "\n")
	tail := strings.TrimRight(suffix, "\n")
	if tail != "" {
		if !strings.HasSuffix(rest, tail) {
			return "", fmt.Errorf("%w: the \"## Comments\" section is read-only in the md view — use `conf comment add`", domain.ErrCheckFailed)
		}
		rest = rest[:len(rest)-len(tail)]
	}
	body := strings.Trim(rest, "\n") + "\n"
	if strings.Contains(body, mirror.ConfluenceReservedPrefix) {
		return "", fmt.Errorf("%w: editable Confluence body contains reserved atl document/section marker text", domain.ErrCheckFailed)
	}
	return body, nil
}

func validateConfluenceDocumentMarker(edited string) error {
	first, _, _ := strings.Cut(edited, "\n")
	if first == mirror.ConfluenceDocumentMarker {
		return nil
	}
	if strings.HasPrefix(first, "<!-- atl:document confluence-page") {
		return fmt.Errorf("%w: unsupported Confluence view format marker %q; preserve edits and update atl before opening this view — do not render or downgrade it with this binary", domain.ErrCheckFailed, first)
	}
	return fmt.Errorf("%w: missing Confluence view format marker %q; save edits outside the derived view, run `conf render` (or pull again), then reapply them", domain.ErrCheckFailed, mirror.ConfluenceDocumentMarker)
}
