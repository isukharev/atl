package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/jiramap"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

// JiraRendered is one re-rendered issue view.
type JiraRendered struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

// JiraRenderResult summarizes an offline `jira render`.
type JiraRenderResult struct {
	Root     string         `json:"root"`
	Rendered []JiraRendered `json:"rendered"`
	Warnings []string       `json:"-"`
}

// Render regenerates the `.md` read views of a Jira mirror offline — no network,
// no PAT. target is a mirror directory, a `<KEY>.md`, or a `<KEY>.wiki`; the
// mirror root is resolved by walking up to the `.atl` marker. For each issue it
// decodes the `<KEY>.json` snapshot into a domain.Issue via the pure adapter
// mapper, re-indexes any already-downloaded `<KEY>.assets/` images from disk, and
// rewrites `<KEY>.md` under the effective render settings (config + override).
// It records each issue's view state in `.atl/state.json` (so a later `jira
// apply` reproduces the exact pristine view) but never touches the
// `.wiki`/`.json` substrate or the `pages` sync entries, so `jira status` stays
// clean across a re-render.
func (s *JiraService) Render(target string, override config.RenderService) (*JiraRenderResult, error) {
	if target == "" {
		target = "mirror-jira"
	}
	root := target
	if r, ok := MirrorRootOf(target); ok {
		root = r
	}
	rs, warns := ResolveRender(s.cfg, root, override, "jira")
	res := &JiraRenderResult{Root: root, Rendered: []JiraRendered{}, Warnings: warns}

	snaps, err := jiraSnapshotFiles(root, target)
	if err != nil {
		return nil, err
	}
	missingEpicSidecars := 0
	for _, jsonPath := range snaps {
		keySeg := strings.TrimSuffix(filepath.Base(jsonPath), ".json")
		issueLock, lockErr := lockJiraPendingFields(root, keySeg)
		if lockErr != nil {
			return res, lockErr
		}
		is, ok := loadIssueSnapshot(root, jsonPath)
		if !ok {
			_ = issueLock.Unlock()
			continue // unreadable/oddly-shaped snapshot: skip, never fail the batch
		}
		dir := filepath.Dir(jsonPath)
		mdPath := filepath.Join(dir, keySeg+".md")
		related := loadEpicChildrenSidecar(root, epicChildrenPath(dir, keySeg))
		used := rs
		if related != nil && !compatibleEpicSidecar(related, is.Key, used.EpicField) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("render: ignoring stale or mismatched epic sidecar for %s; re-run jira pull", is.Key))
			related = nil
		}
		if related != nil {
			if used.EpicField == "" || !isDirectEpicFieldID(used.EpicField) {
				used.EpicField = related.EpicField
			}
		} else if rs.On(SecEpicChildren) && isEpicIssue(*is) {
			missingEpicSidecars++
		}
		pending, _, pendingErr := loadJiraPendingFieldsLocked(root, keySeg)
		if pendingErr != nil {
			_ = issueLock.Unlock()
			return res, pendingErr
		}
		if pendingErr := validatePendingFieldsEditable(pending, used); pendingErr != nil {
			_ = issueLock.Unlock()
			return res, pendingErr
		}
		displayIssue := issueWithPendingFields(is, pending)
		if pending != nil {
			wikiPath := strings.TrimSuffix(jsonPath, ".json") + wikiExt
			lw, wiki, loadErr := mirror.New(root).LoadWiki(wikiPath)
			if loadErr != nil {
				_ = issueLock.Unlock()
				return res, loadErr
			}
			if bindErr := validatePendingMirrorBinding(root, pending, lw, wiki); bindErr != nil {
				_ = issueLock.Unlock()
				return res, bindErr
			}
			if displayIssue == is {
				copyIssue := *is
				displayIssue = &copyIssue
			}
			displayIssue.Body = string(wiki)
		}
		md := renderIssueMarkdownWithRelated(displayIssue, assetsOnDisk(root, dir, keySeg), related, used)
		if err := safepath.WriteFileWithin(root, mdPath, md, 0o644); err != nil {
			_ = issueLock.Unlock()
			return res, err
		}
		if err := mirror.New(root).SaveViewStates(map[string]mirror.ViewState{keySeg: viewStateOf(used)}); err != nil {
			_ = issueLock.Unlock()
			return res, err
		}
		if err := issueLock.Unlock(); err != nil {
			return res, err
		}
		rel, _ := filepath.Rel(root, mdPath)
		res.Rendered = append(res.Rendered, JiraRendered{Key: is.Key, Path: rel})
	}
	if missingEpicSidecars > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("render: epic_children is enabled but %d epic snapshot(s) have no sidecar; re-run jira pull with the section enabled", missingEpicSidecars))
	}
	return res, nil
}

// jiraSnapshotFiles returns the `<KEY>.json` snapshot paths a render should
// rewrite. A file target is mapped to its sibling snapshot; a directory target is
// walked for every `.json` that is not itself a sidecar (`.comments.json`).
func jiraSnapshotFiles(root, target string) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("%w: render target %q: %v", domain.ErrUsage, target, err)
	}
	if !info.IsDir() {
		base := target
		switch {
		case strings.HasSuffix(target, ".json"):
			if strings.HasSuffix(target, ".comments.json") || strings.HasSuffix(target, ".epic-children.json") {
				return nil, fmt.Errorf("%w: render target %q is a sidecar, not an issue snapshot", domain.ErrUsage, target)
			}
		case strings.HasSuffix(target, ".md"):
			base = strings.TrimSuffix(target, ".md") + ".json"
		case strings.HasSuffix(target, wikiExt):
			base = strings.TrimSuffix(target, wikiExt) + ".json"
		default:
			return nil, fmt.Errorf("%w: render target %q must be a directory, a .md, or a .wiki file", domain.ErrUsage, target)
		}
		if _, err := safepath.ReadFileWithin(root, base); err != nil {
			return nil, fmt.Errorf("%w: no snapshot for %q (%v)", domain.ErrUsage, target, err)
		}
		return []string{base}, nil
	}
	if _, err := safepath.ReadDirWithin(root, target); err != nil {
		return nil, fmt.Errorf("%w: render target %q: %v", domain.ErrUsage, target, err)
	}
	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	relTarget, err := filepath.Rel(root, target)
	if err != nil || relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: render target %q is outside mirror root %q", domain.ErrUsage, target, root)
	}
	physicalTarget := filepath.Join(walkRoot, relTarget)
	var out []string
	err = filepath.WalkDir(physicalTarget, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing descendant symlink in mirror: %s", path)
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".comments.json") && !strings.HasSuffix(name, ".epic-children.json") {
			rel, relErr := filepath.Rel(walkRoot, path)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("map Jira snapshot %s to mirror root", path)
			}
			out = append(out, filepath.Join(root, rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// loadIssueSnapshot decodes a `<KEY>.json` mirror snapshot into a domain.Issue
// via the pure adapter mapper. Returns ok=false on any read/parse failure so the
// caller skips the file rather than aborting the whole render.
func loadIssueSnapshot(root, path string) (*domain.Issue, bool) {
	b, err := safepath.ReadFileWithin(root, path)
	if err != nil {
		return nil, false
	}
	var snap JiraIssueSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, false
	}
	if snap.Key == "" {
		return nil, false
	}
	return jiramap.Issue(snap.ID, snap.Key, snap.Fields), true
}
