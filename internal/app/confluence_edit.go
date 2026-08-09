package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/textedit"
)

// ConfluenceEditOptions describes one precise local edit. File is reported
// verbatim while all filesystem access uses its canonical target.
type ConfluenceEditOptions struct {
	File   string
	Old    string
	New    string
	All    bool
	DryRun bool
}

// ConfluenceEditResult is the JSON contract of `conf edit`. Fields remain in
// lexical key order so encoding/json preserves the command's established
// pretty-printed bytes.
type ConfluenceEditResult struct {
	Count        int              `json:"count"`
	CSFOK        *bool            `json:"csf_ok,omitempty"`
	DryRun       bool             `json:"dry_run"`
	File         string           `json:"file"`
	Offsets      []textedit.Match `json:"offsets"`
	Pass         string           `json:"pass"`
	Problems     []csf.Problem    `json:"problems,omitempty"`
	RegionAfter  string           `json:"region_after"`
	RegionBefore string           `json:"region_before"`
}

// EditConfluenceFile performs the local filesystem operation behind
// `conf edit`. It does not contact a backend. Mirror files join the shared
// Confluence mutation lock and use root-scoped reads, stats, and writes.
func EditConfluenceFile(opts ConfluenceEditOptions) (*ConfluenceEditResult, error) {
	if opts.Old == "" {
		return nil, fmt.Errorf("%w: --old (or --old-file) is required and must be non-empty", domain.ErrUsage)
	}

	path, root, err := canonicalConfluenceEditTarget(opts.File)
	if err != nil {
		return nil, err
	}
	if root != "" {
		lock, lockErr := lockConfluenceMutations(root, false)
		if lockErr != nil {
			return nil, lockErr
		}
		defer func() { _ = lock.Unlock() }()
	}

	var raw []byte
	if root != "" {
		raw, err = safepath.ReadFileWithin(root, path)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		kind := domain.ErrUsage
		if root != "" {
			kind = domain.ErrCheckFailed
		}
		return nil, fmt.Errorf("%w: read edit target: %v", kind, err)
	}

	replacement, replaceErr := textedit.Replace(string(raw), opts.Old, opts.New, opts.All)
	if replaceErr != nil {
		var ambiguous *textedit.AmbiguousError
		var noMatch *textedit.NoMatchError
		switch {
		case errors.As(replaceErr, &ambiguous):
			return nil, fmt.Errorf("%w: %v", domain.ErrUsage, replaceErr)
		case errors.As(replaceErr, &noMatch):
			return nil, fmt.Errorf("%w: %v", domain.ErrNotFound, replaceErr)
		default:
			return nil, fmt.Errorf("%w: %v", domain.ErrUsage, replaceErr)
		}
	}

	first := replacement.Matches[0]
	result := &ConfluenceEditResult{
		Count:        len(replacement.Matches),
		DryRun:       opts.DryRun,
		File:         opts.File,
		Offsets:      replacement.Matches,
		Pass:         string(replacement.Pass),
		RegionAfter:  quoteConfluenceEditRegion(replacement.Text, first.Start, first.Start+len(opts.New)),
		RegionBefore: quoteConfluenceEditRegion(string(raw), first.Start, first.End),
	}
	if strings.HasSuffix(path, ".csf") {
		result.Problems = csf.Validate([]byte(replacement.Text))
		ok := !csf.HasErrors(result.Problems)
		result.CSFOK = &ok
	}

	if opts.DryRun {
		return result, nil
	}

	mode := os.FileMode(0o644)
	var info os.FileInfo
	var statErr error
	if root != "" {
		info, statErr = safepath.StatWithin(root, path)
	} else {
		info, statErr = os.Stat(path)
	}
	if statErr != nil && root != "" {
		return result, fmt.Errorf("%w: inspect edit target: %v", domain.ErrCheckFailed, statErr)
	}
	if statErr == nil {
		mode = info.Mode()
	}
	if root != "" {
		err = safepath.WriteFileWithin(root, path, []byte(replacement.Text), mode)
	} else {
		err = safepath.WriteFileAtomic(path, []byte(replacement.Text), mode)
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// canonicalConfluenceEditTarget makes symlink aliases participate in the lock
// of their real mirror. A path lexically inside one mirror may not resolve
// outside it (or into another mirror), because that would make the visible lock
// scope a lie. The returned path is used for every target read and write.
func canonicalConfluenceEditTarget(target string) (path, root string, err error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("%w: edit target %q does not exist", domain.ErrNotFound, target)
		}
		return "", "", fmt.Errorf("%w: resolve edit target: %v", domain.ErrUsage, err)
	}
	lexicalRoot, lexicalMirror := MirrorRootOf(abs)
	realRoot, realMirror := MirrorRootOf(real)
	if lexicalMirror {
		canonicalRoot, rootErr := filepath.EvalSymlinks(lexicalRoot)
		if rootErr != nil {
			return "", "", fmt.Errorf("%w: resolve mirror root: %v", domain.ErrCheckFailed, rootErr)
		}
		if !realMirror || canonicalRoot != realRoot {
			return "", "", fmt.Errorf("%w: edit target resolves outside its visible mirror", domain.ErrCheckFailed)
		}
	}
	if realMirror {
		root = realRoot
	}
	return real, root, nil
}

// quoteConfluenceEditRegion renders text around [start,end) with hidden bytes
// visible (%q-quoted), clamped to the file bounds.
func quoteConfluenceEditRegion(s string, start, end int) string {
	lo, hi := start-40, end+40
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	return fmt.Sprintf("%q", s[lo:hi])
}
