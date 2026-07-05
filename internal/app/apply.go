package app

import (
	"errors"
	"fmt"
	"os"
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
	edited, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrNotFound, err)
	}

	out, rep, err := mdmerge.Merge(base, lc.Meta.Refs, string(edited), mdmerge.Options{
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
	if err := safepath.WriteFile(csfPath, out, 0o644); err != nil {
		return res, err
	}
	res.Wrote = true
	// Renormalize the md view from the merged body so the two surfaces agree
	// (best-effort, same as pull).
	if root2, perr := csf.Parse(out); perr == nil {
		md := mirror.RenderMarkdown(root2, lc.Meta.Refs)
		if werr := safepath.WriteFile(mdPath, md, 0o644); werr != nil {
			return res, werr
		}
	}
	return res, nil
}
