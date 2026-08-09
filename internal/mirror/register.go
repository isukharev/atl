package mirror

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// RegistrationArtifact is one already-rendered, mirror-relative file in a new
// resource registration. Path is a constructed public artifact destination;
// the registration owner constructs its private pristine base separately.
// Data is published verbatim at Mode.
type RegistrationArtifact struct {
	Path ArtifactPath
	Data []byte
	Mode os.FileMode
}

type preparedRegistrationArtifact struct {
	rel  ArtifactPath
	path string
	data []byte
	mode os.FileMode
}

type registrationOps struct {
	writeExclusive func(root, target string, data []byte, mode os.FileMode) error
	saveSidecar    func(sidecarFile) error
	syncDirectory  func(root, target string) error
}

// RegisterNew exclusively publishes a new resource's complete artifact set,
// pristine base, view settings, and sync state. It never adopts or overwrites
// an existing artifact or sidecar identity. The sync state is saved last, so a
// successful call implies that every supplied artifact and the exact base were
// present and verified before the resource became tracked.
func (m *Mirror) RegisterNew(state SyncState, view ViewState, baseExt string, baseBody []byte, artifacts []RegistrationArtifact) error {
	return m.registerNewWith(state, view, baseExt, baseBody, artifacts, registrationOps{
		writeExclusive: safepath.WriteFileExclusiveWithin,
		saveSidecar: func(sc sidecarFile) error {
			return m.saveSidecar(sc)
		},
		syncDirectory: safepath.SyncDirectoryWithin,
	})
}

func (m *Mirror) registerNewWith(state SyncState, view ViewState, baseExt string, baseBody []byte, artifacts []RegistrationArtifact, ops registrationOps) error {
	prepared, base, err := m.prepareRegistration(state, baseExt, baseBody, artifacts)
	if err != nil {
		return err
	}
	if ops.writeExclusive == nil || ops.saveSidecar == nil {
		return fmt.Errorf("%w: incomplete mirror registration writer", domain.ErrCheckFailed)
	}
	if ops.syncDirectory == nil {
		ops.syncDirectory = safepath.SyncDirectoryWithin
	}

	lock, err := m.lockSidecar()
	if err != nil {
		return fmt.Errorf("%w: lock mirror registration state: %v", domain.ErrCheckFailed, err)
	}
	defer func() { _ = lock.Unlock() }()

	sc, err := m.loadSidecar()
	if err != nil {
		return err
	}
	if err := validateRegistrationVacancy(sc, state, prepared, base.rel); err != nil {
		return err
	}
	all := append(append(make([]preparedRegistrationArtifact, 0, len(prepared)+1), prepared...), base)
	for _, artifact := range all {
		if _, err := safepath.StatWithin(m.Root, artifact.path); err == nil {
			return fmt.Errorf("%w: mirror registration target already exists; preserve it and choose another target: %s", domain.ErrCheckFailed, artifact.path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("%w: inspect mirror registration target %s: %v", domain.ErrCheckFailed, artifact.path, err)
		}
	}

	created := make([]preparedRegistrationArtifact, 0, len(all))
	for _, artifact := range all {
		if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(artifact.path), 0o755); err != nil {
			return m.registrationPublishFailure(fmt.Errorf("create parent for %s: %w", artifact.path, err), created, ops.syncDirectory)
		}
		if err := ops.writeExclusive(m.Root, artifact.path, artifact.data, artifact.mode); err != nil {
			return m.registrationPublishFailure(fmt.Errorf("publish %s: %w", artifact.path, err), created, ops.syncDirectory)
		}
		created = append(created, artifact)
	}
	if err := verifyRegistrationFiles(m.Root, all); err != nil {
		return m.registrationPublishFailure(err, created, ops.syncDirectory)
	}
	if err := syncRegistrationTargetDirectories(m.Root, all, ops.syncDirectory); err != nil {
		return m.registrationPublishFailure(fmt.Errorf("durably publish mirror registration artifacts: %w", err), created, ops.syncDirectory)
	}

	next := sc
	next.Pages[state.ID] = state
	next.Views[state.ID] = view
	delete(next.Staged, state.ID)
	var committedSaveErr error
	if err := ops.saveSidecar(next); err != nil {
		committed, absent, inspectErr := m.registrationCommitOutcome(state, view, all)
		switch {
		case committed:
			committedSaveErr = err
		case absent && inspectErr == nil:
			return m.registrationPublishFailure(fmt.Errorf("save mirror registration state: %w", err), created, ops.syncDirectory)
		default:
			if inspectErr != nil {
				err = errors.Join(err, inspectErr)
			}
			return fmt.Errorf("%w: mirror registration state is ambiguous after a sidecar save failure; all published artifacts were preserved for recovery: %v", domain.ErrCheckFailed, err)
		}
	}
	stateTarget := preparedRegistrationArtifact{path: m.sidecarPath()}
	if err := syncRegistrationTargetDirectories(m.Root, []preparedRegistrationArtifact{stateTarget}, ops.syncDirectory); err != nil {
		if committedSaveErr != nil {
			err = errors.Join(committedSaveErr, err)
		}
		return fmt.Errorf("%w: mirror registration state and artifacts were published but their directory durability is ambiguous; all state and bytes were preserved for recovery: %v", domain.ErrCheckFailed, err)
	}
	if err := verifyRegistrationFiles(m.Root, all); err != nil {
		return fmt.Errorf("%w: mirror registration state was saved but its artifact set changed during commit; all state and remaining bytes were preserved for recovery: %v", domain.ErrCheckFailed, err)
	}
	return nil
}

func syncRegistrationTargetDirectories(root string, artifacts []preparedRegistrationArtifact, syncDirectory func(root, target string) error) error {
	dirs, err := registrationTargetDirectories(root, artifacts)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if err := syncDirectory(root, dir); err != nil {
			return fmt.Errorf("sync directory %s: %w", dir, err)
		}
	}
	return nil
}

// registrationTargetDirectories returns every containing directory and its
// ancestors through root, deepest first. Fsyncing a child before its parent
// makes both the already-fsynced file link and any newly-created directory
// entry durable before state.json can claim the registration.
func registrationTargetDirectories(root string, artifacts []preparedRegistrationArtifact) ([]string, error) {
	root = filepath.Clean(root)
	dirs := map[string]int{}
	for _, artifact := range artifacts {
		dir := filepath.Clean(filepath.Dir(artifact.path))
		if !safepath.Within(root, dir) {
			return nil, fmt.Errorf("%w: mirror registration durability path escapes root: %s", domain.ErrCheckFailed, dir)
		}
		for {
			rel, err := filepath.Rel(root, dir)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("%w: resolve mirror registration durability path %s", domain.ErrCheckFailed, dir)
			}
			depth := 0
			if rel != "." {
				depth = strings.Count(filepath.Clean(rel), string(filepath.Separator)) + 1
			}
			dirs[dir] = depth
			if dir == root {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return nil, fmt.Errorf("%w: mirror registration durability ancestry escaped root", domain.ErrCheckFailed)
			}
			dir = parent
		}
	}
	out := make([]string, 0, len(dirs))
	for dir := range dirs {
		out = append(out, dir)
	}
	sort.Slice(out, func(i, j int) bool {
		if dirs[out[i]] != dirs[out[j]] {
			return dirs[out[i]] > dirs[out[j]]
		}
		return filepath.ToSlash(out[i]) < filepath.ToSlash(out[j])
	})
	return out, nil
}

func (m *Mirror) prepareRegistration(state SyncState, baseExt string, baseBody []byte, artifacts []RegistrationArtifact) ([]preparedRegistrationArtifact, preparedRegistrationArtifact, error) {
	if state.ID == "" {
		return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: mirror registration identity is empty", domain.ErrCheckFailed)
	}
	if _, err := NewPublicArtifactPath(state.Path); err != nil {
		return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: invalid mirror registration native path %q", domain.ErrCheckFailed, state.Path)
	}
	if baseExt == "" || baseExt == "." || !strings.HasPrefix(baseExt, ".") || strings.ContainsAny(baseExt, `/\\:`) || filepath.Ext(filepath.FromSlash(state.Path)) != baseExt {
		return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: mirror registration base extension does not match the native path", domain.ErrCheckFailed)
	}
	if Hash(baseBody) != state.Hash {
		return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: mirror registration base bytes do not match sync state hash", domain.ErrCheckFailed)
	}
	if len(artifacts) == 0 {
		return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: mirror registration has no artifacts", domain.ErrCheckFailed)
	}

	prepared := make([]preparedRegistrationArtifact, 0, len(artifacts))
	seen := make(map[string]struct{}, len(artifacts))
	var native []byte
	nativeFound := false
	for _, artifact := range artifacts {
		if err := validateArtifactPath(artifact.Path); err != nil || !artifactPathIsPublic(artifact.Path) {
			return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: invalid mirror registration artifact path", domain.ErrCheckFailed)
		}
		if artifact.Mode != artifact.Mode.Perm() || artifact.Mode.Perm() == 0 {
			return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: invalid mirror registration mode", domain.ErrCheckFailed)
		}
		key, err := artifactPathCollisionKey(artifact.Path)
		if err != nil {
			return nil, preparedRegistrationArtifact{}, err
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: duplicate mirror registration artifact path", domain.ErrCheckFailed)
		}
		seen[key] = struct{}{}
		target, err := artifactPathTarget(m.Root, artifact.Path)
		if err != nil {
			return nil, preparedRegistrationArtifact{}, err
		}
		data := append([]byte(nil), artifact.Data...)
		prepared = append(prepared, preparedRegistrationArtifact{rel: artifact.Path, path: target, data: data, mode: artifact.Mode})
		if artifactPathMatchesDurable(artifact.Path, state.Path) {
			native = data
			nativeFound = true
		}
	}
	if !nativeFound || Hash(native) != state.Hash || !bytes.Equal(native, baseBody) {
		return nil, preparedRegistrationArtifact{}, fmt.Errorf("%w: mirror registration native artifact and pristine base must be the exact sync-state bytes", domain.ErrCheckFailed)
	}

	baseRel := filepath.ToSlash(filepath.Join(".atl", "base", safepath.Segment(state.ID)+baseExt))
	baseArtifactPath, err := newPrivateArtifactPath(baseRel)
	if err != nil {
		return nil, preparedRegistrationArtifact{}, err
	}
	basePath, err := artifactPathTarget(m.Root, baseArtifactPath)
	if err != nil {
		return nil, preparedRegistrationArtifact{}, err
	}
	base := preparedRegistrationArtifact{rel: baseArtifactPath, path: basePath, data: append([]byte(nil), baseBody...), mode: 0o600}
	return prepared, base, nil
}

func validateRegistrationVacancy(sc sidecarFile, state SyncState, artifacts []preparedRegistrationArtifact, baseRel ArtifactPath) error {
	if _, exists := sc.Pages[state.ID]; exists {
		return fmt.Errorf("%w: mirror registration identity %q is already tracked", domain.ErrCheckFailed, state.ID)
	}
	if _, exists := sc.Views[state.ID]; exists {
		return fmt.Errorf("%w: mirror registration identity %q already has view state", domain.ErrCheckFailed, state.ID)
	}
	if _, exists := sc.Staged[state.ID]; exists {
		return fmt.Errorf("%w: mirror registration identity %q already has staged lineage", domain.ErrCheckFailed, state.ID)
	}
	claims := make(map[string]struct{}, len(artifacts)+1)
	for _, artifact := range artifacts {
		key, err := artifactPathCollisionKey(artifact.rel)
		if err != nil {
			return err
		}
		claims[key] = struct{}{}
	}
	baseKey, err := artifactPathCollisionKey(baseRel)
	if err != nil {
		return err
	}
	claims[baseKey] = struct{}{}
	for id, existing := range sc.Pages {
		if _, collision := claims[registrationPathKey(existing.Path)]; collision {
			return fmt.Errorf("%w: mirror registration path collides with tracked identity %q", domain.ErrCheckFailed, id)
		}
		existingBase := filepath.ToSlash(filepath.Join(".atl", "base", safepath.Segment(id)+filepath.Ext(filepath.FromSlash(existing.Path))))
		if artifactPathMatchesDurable(baseRel, existingBase) {
			return fmt.Errorf("%w: mirror registration base path collides with tracked identity %q", domain.ErrCheckFailed, id)
		}
	}
	for id, existing := range sc.Staged {
		if _, collision := claims[registrationPathKey(existing.Path)]; collision {
			return fmt.Errorf("%w: mirror registration path collides with staged identity %q", domain.ErrCheckFailed, id)
		}
	}
	return nil
}

func verifyRegistrationFiles(root string, artifacts []preparedRegistrationArtifact) error {
	for _, artifact := range artifacts {
		got, err := safepath.ReadFileWithin(root, artifact.path)
		if err != nil || !bytes.Equal(got, artifact.data) {
			return fmt.Errorf("%w: mirror registration artifact is missing or changed: %s", domain.ErrCheckFailed, artifact.path)
		}
		info, err := safepath.StatWithin(root, artifact.path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != artifact.mode.Perm() {
			return fmt.Errorf("%w: mirror registration artifact has unexpected type or mode: %s", domain.ErrCheckFailed, artifact.path)
		}
	}
	return nil
}

func (m *Mirror) registrationCommitOutcome(state SyncState, view ViewState, artifacts []preparedRegistrationArtifact) (committed, absent bool, err error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return false, false, fmt.Errorf("inspect mirror registration state: %w", err)
	}
	gotState, hasPage := sc.Pages[state.ID]
	gotView, hasView := sc.Views[state.ID]
	_, hasStaged := sc.Staged[state.ID]
	if hasPage && hasView && !hasStaged && gotState == state && reflect.DeepEqual(gotView, view) {
		if err := verifyRegistrationFiles(m.Root, artifacts); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	if !hasPage && !hasView && !hasStaged {
		return false, true, nil
	}
	return false, false, nil
}

func (m *Mirror) registrationPublishFailure(cause error, created []preparedRegistrationArtifact, syncDirectory func(root, target string) error) error {
	removed, rollbackErr := rollbackRegistrationFiles(m.Root, created)
	var durabilityErr error
	if len(removed) > 0 {
		durabilityErr = syncRegistrationTargetDirectories(m.Root, removed, syncDirectory)
	}
	if rollbackErr != nil || durabilityErr != nil {
		return fmt.Errorf("%w: mirror registration failed and rollback is incomplete or its durability is ambiguous; preserve the reported artifacts for recovery: %v", domain.ErrCheckFailed, errors.Join(cause, rollbackErr, durabilityErr))
	}
	return fmt.Errorf("%w: mirror registration failed; files created by this attempt were rolled back: %v", domain.ErrCheckFailed, cause)
}

func rollbackRegistrationFiles(root string, created []preparedRegistrationArtifact) ([]preparedRegistrationArtifact, error) {
	var errs []error
	removed := make([]preparedRegistrationArtifact, 0, len(created))
	for i := len(created) - 1; i >= 0; i-- {
		artifact := created[i]
		got, err := safepath.ReadFileWithin(root, artifact.path)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("inspect %s before rollback: %w", artifact.path, err))
		case !bytes.Equal(got, artifact.data):
			errs = append(errs, fmt.Errorf("preserve changed registration artifact %s", artifact.path))
		default:
			if err := safepath.RemoveWithin(root, artifact.path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove %s during rollback: %w", artifact.path, err))
			} else {
				removed = append(removed, artifact)
			}
		}
	}
	return removed, errors.Join(errs...)
}

func registrationPathKey(path string) string {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	// Windows and the default macOS filesystems are case-insensitive. Treat
	// case-only aliases as collisions even when a particular macOS volume was
	// formatted case-sensitive; the conservative refusal is portable and keeps
	// one registration plan from acquiring two spellings of the same target.
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(clean)
	}
	return clean
}

func registrationPathsEqual(a, b string) bool {
	return registrationPathKey(a) == registrationPathKey(b)
}
