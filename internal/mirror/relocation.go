package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// PageRelocation is an immutable, hash-bound plan for retiring one page's old
// primary artifacts after its replacement path and sidecar state are durable.
// Descendant directories, assets, comment caches, and unrelated files are
// deliberately outside the plan and are never recursively removed.
type PageRelocation struct {
	id, newRel      string
	oldCSF, oldMD   string
	oldMeta         string
	csfHash, mdHash string
	metaHash        string
	tombstonePath   string
	tombstoneHash   string
	tombstoneExists bool
}

type relocationTombstone struct {
	ID            string `json:"id"`
	CanonicalPath string `json:"canonical_path"`
}

// PlanPageRelocation proves that the currently tracked page is clean and its
// derived view is pristine before a pull writes the same id at newRel. The
// caller supplies the exact reconstructed pristine Markdown bytes because the
// app layer owns typed render-setting interpretation.
func (m *Mirror) PlanPageRelocation(id, newRel string, pristineMD []byte) (*PageRelocation, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, err
	}
	st, ok := sc.Pages[id]
	if !ok || filepath.Clean(st.Path) == filepath.Clean(newRel) {
		return nil, nil
	}
	oldCSF := filepath.Join(m.Root, filepath.FromSlash(st.Path))
	oldMD := strings.TrimSuffix(oldCSF, ".csf") + ".md"
	oldMeta := strings.TrimSuffix(oldCSF, ".csf") + ".meta.json"
	oldTombstone := strings.TrimSuffix(oldCSF, ".csf") + ".relocated.json"
	oldTombstoneHash := ""
	oldTombstoneExists := false
	if markerBytes, err := safepath.ReadFileWithin(m.Root, oldTombstone); err == nil {
		var marker relocationTombstone
		if json.Unmarshal(markerBytes, &marker) != nil || marker.ID != id {
			return nil, fmt.Errorf("%w: relocation ownership marker %s is invalid or belongs to another page; preserve it and resolve manually", domain.ErrCheckFailed, oldTombstone)
		}
		oldTombstoneExists = true
		oldTombstoneHash = Hash(markerBytes)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: inspect relocation ownership marker %s: %v", domain.ErrCheckFailed, oldTombstone, err)
	}
	newCSF := filepath.Join(m.Root, filepath.FromSlash(newRel))
	for _, target := range []string{newCSF, strings.TrimSuffix(newCSF, ".csf") + ".md", strings.TrimSuffix(newCSF, ".csf") + ".meta.json"} {
		if _, err := safepath.ReadFileWithin(m.Root, target); err == nil {
			return nil, fmt.Errorf("%w: relocation target %s already contains a page artifact; preserve both copies and resolve the collision manually", domain.ErrCheckFailed, target)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: inspect relocation target %s: %v", domain.ErrCheckFailed, target, err)
		}
	}
	newTombstone := strings.TrimSuffix(newCSF, ".csf") + ".relocated.json"
	if markerBytes, err := safepath.ReadFileWithin(m.Root, newTombstone); err == nil {
		var marker relocationTombstone
		if json.Unmarshal(markerBytes, &marker) != nil || marker.ID != id {
			return nil, fmt.Errorf("%w: relocation target %s is retained for another or unknown page owner", domain.ErrCheckFailed, newTombstone)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: inspect relocation target ownership %s: %v", domain.ErrCheckFailed, newTombstone, err)
	}
	csfBytes, err := safepath.ReadFileWithin(m.Root, oldCSF)
	if err != nil {
		return nil, fmt.Errorf("%w: tracked relocation source %s is unreadable: %v", domain.ErrCheckFailed, oldCSF, err)
	}
	if Hash(csfBytes) != st.Hash {
		return nil, fmt.Errorf("%w: tracked relocation source %s has local CSF edits; apply/push or preserve them before re-pulling", domain.ErrCheckFailed, oldCSF)
	}
	metaBytes, err := safepath.ReadFileWithin(m.Root, oldMeta)
	if err != nil {
		return nil, fmt.Errorf("%w: tracked relocation metadata %s is unreadable: %v", domain.ErrCheckFailed, oldMeta, err)
	}
	var meta Meta
	if json.Unmarshal(metaBytes, &meta) != nil || meta.ID != id {
		return nil, fmt.Errorf("%w: relocation source metadata %s does not prove ownership by page %s", domain.ErrCheckFailed, oldMeta, id)
	}
	if meta.Hash != st.Hash || meta.Version != st.Version {
		return nil, fmt.Errorf("%w: relocation source metadata %s diverges from tracked version/hash; preserve and reconcile it before re-pulling", domain.ErrCheckFailed, oldMeta)
	}
	mdBytes, err := safepath.ReadFileWithin(m.Root, oldMD)
	if err != nil {
		return nil, fmt.Errorf("%w: tracked relocation view %s is unreadable: %v", domain.ErrCheckFailed, oldMD, err)
	}
	if Hash(mdBytes) != Hash(pristineMD) {
		return nil, fmt.Errorf("%w: tracked relocation view %s has unapplied Markdown edits; preserve/apply them before re-pulling", domain.ErrCheckFailed, oldMD)
	}
	return &PageRelocation{
		id: id, newRel: newRel, oldCSF: oldCSF, oldMD: oldMD, oldMeta: oldMeta,
		csfHash: Hash(csfBytes), mdHash: Hash(mdBytes), metaHash: Hash(metaBytes),
		tombstonePath: oldTombstone, tombstoneHash: oldTombstoneHash, tombstoneExists: oldTombstoneExists,
	}, nil
}

// RetirePageRelocation revalidates the reviewed source hashes, then removes
// only the old page's three primary artifacts. It runs after state.json points
// at the replacement, so an interrupted cleanup cannot make the retired path
// look current. Non-empty directories are intentionally retained.
func (m *Mirror) RetirePageRelocation(plan *PageRelocation) error {
	if plan == nil {
		return nil
	}
	sc, err := m.loadSidecar()
	if err != nil {
		return err
	}
	newState, ok := sc.Pages[plan.id]
	if !ok || filepath.Clean(newState.Path) != filepath.Clean(plan.newRel) {
		return fmt.Errorf("%w: relocation replacement for page %s is not yet canonical in mirror state; preserving old artifacts", domain.ErrCheckFailed, plan.id)
	}
	newCSF := filepath.Join(m.Root, filepath.FromSlash(plan.newRel))
	newBytes, err := safepath.ReadFileWithin(m.Root, newCSF)
	if err != nil || Hash(newBytes) != newState.Hash {
		return fmt.Errorf("%w: relocation replacement %s is missing or differs from canonical state; preserving old artifacts", domain.ErrCheckFailed, newCSF)
	}
	newMetaPath := strings.TrimSuffix(newCSF, ".csf") + ".meta.json"
	newMetaBytes, err := safepath.ReadFileWithin(m.Root, newMetaPath)
	var newMeta Meta
	if err != nil || json.Unmarshal(newMetaBytes, &newMeta) != nil || newMeta.ID != plan.id || newMeta.Version != newState.Version || newMeta.Hash != newState.Hash {
		return fmt.Errorf("%w: relocation replacement metadata %s does not prove the canonical page state; preserving old artifacts", domain.ErrCheckFailed, newMetaPath)
	}
	artifacts := []struct{ path, hash string }{
		{plan.oldCSF, plan.csfHash}, {plan.oldMD, plan.mdHash}, {plan.oldMeta, plan.metaHash},
	}
	// Preserve directory ownership before removing the primary meta. Otherwise
	// a later page with the same slug could inherit this page's retained
	// comments/assets/unrelated files. The marker is intentionally local-only.
	if existing, err := safepath.ReadFileWithin(m.Root, plan.tombstonePath); plan.tombstoneExists {
		if err != nil || Hash(existing) != plan.tombstoneHash {
			return fmt.Errorf("%w: relocation ownership marker changed before retirement: %s", domain.ErrCheckFailed, plan.tombstonePath)
		}
	} else if err == nil || !os.IsNotExist(err) {
		return fmt.Errorf("%w: relocation ownership marker appeared before retirement: %s", domain.ErrCheckFailed, plan.tombstonePath)
	}
	tombstone, _ := json.MarshalIndent(relocationTombstone{ID: plan.id, CanonicalPath: plan.newRel}, "", "  ")
	if err := safepath.WriteFileWithin(m.Root, plan.tombstonePath, append(tombstone, '\n'), 0o600); err != nil {
		return fmt.Errorf("%w: record ownership of retired page directory: %v", domain.ErrCheckFailed, err)
	}
	// Validate the complete set before removing anything, so an edit already
	// present at retirement time preserves all three reconciliation inputs.
	for _, artifact := range artifacts {
		got, err := safepath.ReadFileWithin(m.Root, artifact.path)
		if err != nil || Hash(got) != artifact.hash {
			return fmt.Errorf("%w: relocation source changed before retirement: %s; preserve and reconcile the old artifact manually", domain.ErrCheckFailed, artifact.path)
		}
	}
	// Remove the native substrate first: once state.json points at the new path,
	// this immediately prevents the old directory from being mistaken for a
	// second current page. All bytes were hash-revalidated above.
	for _, artifact := range artifacts {
		// Re-read immediately before each removal. The mirror lock serializes atl
		// operations, while this narrow check also refuses a late external edit.
		got, err := safepath.ReadFileWithin(m.Root, artifact.path)
		if err != nil || Hash(got) != artifact.hash {
			return fmt.Errorf("%w: relocation source changed before retirement: %s; preserve and reconcile the old artifact manually", domain.ErrCheckFailed, artifact.path)
		}
		if err := safepath.RemoveWithin(m.Root, artifact.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: retire old page artifact %s: %v", domain.ErrCheckFailed, artifact.path, err)
		}
	}
	// os.Remove semantics through RemoveWithin are non-recursive. Ignore a
	// non-empty directory: it may contain descendants, assets, comment caches,
	// or unrelated local files that this page relocation must preserve.
	_ = safepath.RemoveWithin(m.Root, filepath.Dir(plan.oldCSF))
	return nil
}
