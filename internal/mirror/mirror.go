// Package mirror owns the on-disk git-style mirror: layout, the markdown
// read-view, content hashing, the last-synced sidecar, and dirty/drift
// detection. It is backend-agnostic — it stores domain.Resource bytes and does
// not know whether they are Confluence pages or Jira issues.
package mirror

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// Mirror is rooted at a directory holding one or more spaces.
type Mirror struct {
	Root string
}

func New(root string) *Mirror { return &Mirror{Root: root} }

// Meta is the per-page sidecar visible to the agent.
type Meta struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Space   string       `json:"space"`
	Version int          `json:"version"`
	Hash    string       `json:"content_hash"`
	Parent  string       `json:"parent,omitempty"`
	Labels  []string     `json:"labels,omitempty"`
	URL     string       `json:"url,omitempty"`
	Refs    []domain.Ref `json:"fragments,omitempty"`
}

// Hash returns the canonical content hash of a body (sha256 hex of raw bytes).
func Hash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// PageDir computes the directory for a page: root/SPACE/<anc…>/<ownSlug>.
// ancestors are ancestor titles top→down (may be empty).
func (m *Mirror) PageDir(space string, ancestors []string, title string) (dir, slug string) {
	parts := []string{m.Root, safeSeg(space)}
	for _, a := range ancestors {
		parts = append(parts, slugify(a))
	}
	slug = slugify(title)
	parts = append(parts, slug)
	return filepath.Join(parts...), slug
}

// pageSink writes assets under <dir>/<slug>.assets/ and returns paths relative
// to the page dir (so .md links resolve).
type pageSink struct {
	dir  string
	slug string
}

func (s pageSink) Put(name string, data []byte) (string, error) {
	// name is a backend-supplied attachment filename: reduce it to a single safe
	// base name and refuse anything with no usable basename ("." / "..").
	safe, ok := safepath.Base(name)
	if !ok {
		return "", fmt.Errorf("refusing unsafe asset name %q", name)
	}
	adir := filepath.Join(s.dir, s.slug+".assets")
	if err := os.MkdirAll(adir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(adir, safe)
	if !safepath.Within(adir, target) {
		return "", fmt.Errorf("refusing unsafe asset path %q", name)
	}
	if err := safepath.WriteFile(target, data, 0o644); err != nil {
		return "", err
	}
	return s.slug + ".assets/" + safe, nil
}

// AssetSink returns the asset sink for a page directory.
func (m *Mirror) AssetSink(dir, slug string) domain.AssetSink { return pageSink{dir: dir, slug: slug} }

// Write persists a page: .csf (source of truth), .md (read view), .meta.json,
// and updates the sidecar. refs must already be resolved (display/asset filled).
func (m *Mirror) Write(dir, slug string, page *domain.Resource, refs []domain.Ref) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	csfPath := filepath.Join(dir, slug+".csf")
	if err := safepath.WriteFile(csfPath, page.Body, 0o644); err != nil {
		return err
	}
	// Markdown view (best-effort: never fail a pull because rendering choked).
	if root, err := csf.Parse(page.Body); err == nil {
		md := RenderMarkdown(root, refs)
		if err := safepath.WriteFile(filepath.Join(dir, slug+".md"), md, 0o644); err != nil {
			return err
		}
	}
	meta := Meta{
		ID: page.ID, Title: page.Title, Space: page.SpaceKey, Version: page.Version,
		Hash: Hash(page.Body), Parent: page.Parent, Labels: page.Labels, Refs: refs,
	}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := safepath.WriteFile(filepath.Join(dir, slug+".meta.json"), append(mb, '\n'), 0o644); err != nil {
		return err
	}
	rel, _ := filepath.Rel(m.Root, csfPath)
	if err := m.saveBase(page.ID, page.Body); err != nil {
		return err
	}
	return m.recordSync(page.ID, page.Version, meta.Hash, rel)
}

// EnsureScaffold writes a .gitignore guarding secrets in the mirror root.
func (m *Mirror) EnsureScaffold() error {
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return err
	}
	gi := filepath.Join(m.Root, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		_ = safepath.WriteFile(gi, []byte("# atl mirror — never commit secrets\n.atl/\ncredentials.json\n*.pat\n"), 0o644)
	}
	return nil
}

// LocalCSF describes a tracked .csf file and its expected (last-synced) state.
type LocalCSF struct {
	Path    string // absolute path to the .csf
	Meta    Meta
	Synced  *SyncState // last-synced state from the sidecar (nil if untracked)
	Current string     // current on-disk content hash
	Dirty   bool       // current != synced
}

// LoadCSF reads a .csf path and its neighboring meta + sidecar state.
func (m *Mirror) LoadCSF(csfPath string) (*LocalCSF, []byte, error) {
	body, err := os.ReadFile(csfPath)
	if err != nil {
		return nil, nil, err
	}
	lc := &LocalCSF{Path: csfPath, Current: Hash(body)}
	metaPath := strings.TrimSuffix(csfPath, ".csf") + ".meta.json"
	if mb, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(mb, &lc.Meta)
	}
	sc, _ := m.loadSidecar()
	if st, ok := sc.Pages[lc.Meta.ID]; ok {
		s := st
		lc.Synced = &s
		lc.Dirty = s.Hash != lc.Current
	} else {
		lc.Dirty = true // untracked / never synced
	}
	return lc, body, nil
}

// ListCSF walks the mirror returning every tracked .csf with dirty status.
func (m *Mirror) ListCSF() ([]*LocalCSF, error) {
	var out []*LocalCSF
	err := filepath.Walk(m.Root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".atl" {
				return filepath.SkipDir // sidecar (pristine base copies) is not user content
			}
			return nil
		}
		if strings.HasSuffix(p, ".csf") {
			lc, _, err := m.LoadCSF(p)
			if err == nil {
				out = append(out, lc)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// slugify keeps unicode letters/digits (Cyrillic included), lowercases, and
// turns everything else into single hyphens.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "untitled"
	}
	// Truncate by runes, not bytes, so multibyte (e.g. Cyrillic) titles are
	// never split mid-character.
	if r := []rune(out); len(r) > 80 {
		out = strings.Trim(string(r[:80]), "-")
	}
	return out
}

// safeSeg sanitizes a single path segment (space key) without lowercasing. It
// neutralizes separators and "." / ".." and escapes dot-prefixed names so a
// hostile server space key (including the reserved ".atl") can neither traverse
// upward nor collide with the mirror's internal state directory.
func safeSeg(s string) string {
	return safepath.Segment(s)
}
