package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
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

	snaps, err := jiraSnapshotFiles(target)
	if err != nil {
		return nil, err
	}
	views := map[string]mirror.ViewState{}
	missingEpicSidecars := 0
	for _, jsonPath := range snaps {
		is, ok := loadIssueSnapshot(root, jsonPath)
		if !ok {
			continue // unreadable/oddly-shaped snapshot: skip, never fail the batch
		}
		dir := filepath.Dir(jsonPath)
		keySeg := strings.TrimSuffix(filepath.Base(jsonPath), ".json")
		mdPath := filepath.Join(dir, keySeg+".md")
		related := loadEpicChildrenSidecar(root, epicChildrenPath(dir, keySeg))
		if rs.On(SecEpicChildren) && strings.EqualFold(is.Type, "epic") && related == nil {
			missingEpicSidecars++
		}
		used := rs
		if related != nil && used.EpicField == "" {
			used.EpicField = related.EpicField
		}
		md := renderIssueMarkdownWithRelated(is, assetsOnDisk(dir, keySeg), related, used)
		if err := safepath.WriteFileWithin(root, mdPath, md, 0o644); err != nil {
			return res, err
		}
		// Keyed by the .wiki basename, same key as the pull/apply paths.
		views[keySeg] = viewStateOf(used)
		rel, _ := filepath.Rel(root, mdPath)
		res.Rendered = append(res.Rendered, JiraRendered{Key: is.Key, Path: rel})
	}
	if missingEpicSidecars > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("render: epic_children is enabled but %d epic snapshot(s) have no sidecar; re-run jira pull with the section enabled", missingEpicSidecars))
	}
	// Persist the recorded views in one load-modify-save. This writes only the
	// `views` map, never a `pages` sync entry, so `jira status` stays clean.
	if err := mirror.New(root).SaveViewStates(views); err != nil {
		return res, err
	}
	return res, nil
}

// jiraSnapshotFiles returns the `<KEY>.json` snapshot paths a render should
// rewrite. A file target is mapped to its sibling snapshot; a directory target is
// walked for every `.json` that is not itself a sidecar (`.comments.json`).
func jiraSnapshotFiles(target string) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("%w: render target %q: %v", domain.ErrUsage, target, err)
	}
	if !info.IsDir() {
		base := target
		switch {
		case strings.HasSuffix(target, ".json"):
			// already a snapshot
		case strings.HasSuffix(target, ".md"):
			base = strings.TrimSuffix(target, ".md") + ".json"
		case strings.HasSuffix(target, wikiExt):
			base = strings.TrimSuffix(target, wikiExt) + ".json"
		default:
			return nil, fmt.Errorf("%w: render target %q must be a directory, a .md, or a .wiki file", domain.ErrUsage, target)
		}
		if _, err := os.Stat(base); err != nil {
			return nil, fmt.Errorf("%w: no snapshot for %q (%v)", domain.ErrUsage, target, err)
		}
		return []string{base}, nil
	}
	var out []string
	err = filepath.WalkDir(target, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".comments.json") && !strings.HasSuffix(name, ".epic-children.json") {
			out = append(out, path)
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
	return jiraadapter.MapIssueFields(snap.ID, snap.Key, snap.Fields), true
}
