package mirror

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	backendBindingsSchemaVersion = 1
	backendBindingsMaxBytes      = 64 << 10
	backendBindingsLockAttempts  = 20
	backendBindingsLockDelay     = 10 * time.Millisecond
)

// BackendBinding is the only durable backend identity shape. OriginSHA256 is
// a tagged digest; raw URLs and hostnames never cross into mirror storage.
type BackendBinding struct {
	Service      string `json:"service"`
	OriginSHA256 string `json:"origin_sha256,omitempty"`
}

type backendBindingsFile struct {
	SchemaVersion int               `json:"schema_version"`
	Services      map[string]string `json:"services"`
}

// BackendBindingPopulationGuard holds the single backend-binding CAS owner
// across a remote population workflow. Explicit binds use the same owner, so
// no binding can be committed between qualification and local publication.
type BackendBindingPopulationGuard struct {
	m       *Mirror
	lock    *safepath.FileLock
	want    BackendBinding
	state   backendBindingsFile
	present bool
}

func (m *Mirror) backendBindingsPath() string {
	return filepath.Join(m.Root, ".atl", "backend-bindings.json")
}

func (m *Mirror) backendBindingsLockPath() string {
	return filepath.Join(m.Root, ".atl", "backend-bindings.lock")
}

// BackendBindings returns a deterministic content-minimized snapshot. A
// missing file is a valid legacy/unbound mirror.
func (m *Mirror) BackendBindings() ([]BackendBinding, error) {
	state, _, err := m.loadBackendBindings()
	if err != nil {
		return nil, err
	}
	services := make([]string, 0, len(state.Services))
	for service := range state.Services {
		services = append(services, service)
	}
	sort.Strings(services)
	out := make([]BackendBinding, 0, len(services))
	for _, service := range services {
		out = append(out, BackendBinding{Service: service, OriginSHA256: state.Services[service]})
	}
	return out, nil
}

// BackendBinding returns one binding without disclosing any configured URL.
func (m *Mirror) BackendBinding(service string) (BackendBinding, bool, error) {
	service, err := validateBackendService(service)
	if err != nil {
		return BackendBinding{}, false, err
	}
	state, _, err := m.loadBackendBindings()
	if err != nil {
		return BackendBinding{}, false, err
	}
	digest, ok := state.Services[service]
	return BackendBinding{Service: service, OriginSHA256: digest}, ok, nil
}

// RequireBackendBinding fails closed when a mirror is unbound or bound to a
// different configured origin. Errors intentionally omit both identities.
func (m *Mirror) RequireBackendBinding(want BackendBinding) error {
	want, err := validateBackendBinding(want)
	if err != nil {
		return err
	}
	got, ok, err := m.BackendBinding(want.Service)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: mirror backend is not bound for service %s; review and bind it before remote mirror operations", domain.ErrCheckFailed, want.Service)
	}
	if got.OriginSHA256 != want.OriginSHA256 {
		return fmt.Errorf("%w: mirror backend binding does not match the configured service %s; use the original backend or a new mirror", domain.ErrCheckFailed, want.Service)
	}
	return nil
}

// BindBackend is an explicit compare-and-set migration. It never replaces a
// different binding and performs no network access.
func (m *Mirror) BindBackend(want BackendBinding) (bool, error) {
	return m.bindBackend(want, "", false)
}

// BindBackendIfFresh establishes a missing binding only when no durable or
// native evidence for this service exists. Population paths call it before the
// first remote request. A matching binding is an idempotent no-op.
func (m *Mirror) BindBackendIfFresh(want BackendBinding, nativeExt string) (bool, error) {
	if nativeExt != ".csf" && nativeExt != ".wiki" {
		return false, fmt.Errorf("%w: unsupported native substrate for backend binding", domain.ErrCheckFailed)
	}
	return m.bindBackend(want, nativeExt, true)
}

// BeginBackendBindingPopulation qualifies a matching or service-fresh binding
// under the same CAS lock used by BindBackend. The caller must hold the guard
// until its remote operation and local publication are fully closed out.
func (m *Mirror) BeginBackendBindingPopulation(want BackendBinding, nativeExt string) (*BackendBindingPopulationGuard, error) {
	if nativeExt != ".csf" && nativeExt != ".wiki" {
		return nil, fmt.Errorf("%w: unsupported native substrate for backend binding", domain.ErrCheckFailed)
	}
	return m.beginBackendBinding(want, nativeExt, true)
}

// Commit publishes a missing, already-qualified binding while retaining the
// guard. A matching existing binding is an idempotent no-op.
func (g *BackendBindingPopulationGuard) Commit() (bool, error) {
	if g == nil || g.m == nil || g.lock == nil {
		return false, fmt.Errorf("%w: mirror backend-binding coordination is not active", domain.ErrCheckFailed)
	}
	if g.present {
		return false, nil
	}
	g.state.Services[g.want.Service] = g.want.OriginSHA256
	if err := g.m.saveBackendBindings(g.state); err != nil {
		delete(g.state.Services, g.want.Service)
		return false, err
	}
	g.present = true
	return true, nil
}

// Unlock releases backend-binding coordination. It deliberately matches the
// common lock interface used by higher-level mutation closeout.
func (g *BackendBindingPopulationGuard) Unlock() error {
	if g == nil || g.lock == nil {
		return nil
	}
	err := g.lock.Unlock()
	g.lock = nil
	return err
}

// CheckBackendBindingForPopulation is the write-free counterpart used by
// pull dry-runs. A missing binding is accepted only for a service-fresh root.
func (m *Mirror) CheckBackendBindingForPopulation(want BackendBinding, nativeExt string) error {
	if nativeExt != ".csf" && nativeExt != ".wiki" {
		return fmt.Errorf("%w: unsupported native substrate for backend binding", domain.ErrCheckFailed)
	}
	want, err := validateBackendBinding(want)
	if err != nil {
		return err
	}
	got, ok, err := m.BackendBinding(want.Service)
	if err != nil {
		return err
	}
	if ok {
		if got.OriginSHA256 != want.OriginSHA256 {
			return fmt.Errorf("%w: mirror backend binding does not match the configured service %s; use the original backend or a new mirror", domain.ErrCheckFailed, want.Service)
		}
		return nil
	}
	evidence, err := m.hasBackendServiceEvidence(want.Service, nativeExt)
	if err != nil {
		return err
	}
	if evidence {
		return fmt.Errorf("%w: existing %s mirror evidence has no backend binding; use the explicit reviewed bind workflow before remote access", domain.ErrCheckFailed, want.Service)
	}
	return nil
}

func (m *Mirror) bindBackend(want BackendBinding, nativeExt string, requireFresh bool) (bool, error) {
	guard, err := m.beginBackendBinding(want, nativeExt, requireFresh)
	if err != nil {
		return false, err
	}
	defer func() { _ = guard.Unlock() }()
	return guard.Commit()
}

func (m *Mirror) beginBackendBinding(want BackendBinding, nativeExt string, requireFresh bool) (*BackendBindingPopulationGuard, error) {
	want, err := validateBackendBinding(want)
	if err != nil {
		return nil, err
	}
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(m.backendBindingsPath()), 0o700); err != nil {
		return nil, err
	}
	lock, err := m.lockBackendBindings()
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*BackendBindingPopulationGuard, error) {
		_ = lock.Unlock()
		return nil, cause
	}
	state, _, err := m.loadBackendBindings()
	if err != nil {
		return fail(err)
	}
	if current, ok := state.Services[want.Service]; ok {
		if current != want.OriginSHA256 {
			return fail(fmt.Errorf("%w: mirror backend binding does not match the configured service %s; bindings cannot be replaced", domain.ErrCheckFailed, want.Service))
		}
		return &BackendBindingPopulationGuard{m: m, lock: lock, want: want, state: state, present: true}, nil
	}
	if requireFresh {
		evidence, err := m.hasBackendServiceEvidence(want.Service, nativeExt)
		if err != nil {
			return fail(err)
		}
		if evidence {
			return fail(fmt.Errorf("%w: existing %s mirror evidence has no backend binding; use the explicit reviewed bind workflow before remote access", domain.ErrCheckFailed, want.Service))
		}
	}
	return &BackendBindingPopulationGuard{m: m, lock: lock, want: want, state: state}, nil
}

func (m *Mirror) lockBackendBindings() (*safepath.FileLock, error) {
	for attempt := 0; attempt < backendBindingsLockAttempts; attempt++ {
		lock, acquired, err := safepath.TryLockFileWithin(m.Root, m.backendBindingsLockPath(), 0o600)
		if err != nil {
			// Darwin can transiently surface ENOENT when concurrent callers
			// first create and open the same lock beneath the just-created
			// state directory. Retry only that condition; never recreate the
			// directory or absorb containment, permission, or I/O failures.
			if shouldRetryBackendBindingLock(err, attempt) {
				time.Sleep(backendBindingsLockDelay)
				continue
			}
			return nil, err
		}
		if acquired {
			return lock, nil
		}
		if attempt < backendBindingsLockAttempts-1 {
			time.Sleep(backendBindingsLockDelay)
		}
	}
	return nil, fmt.Errorf("%w: another mirror backend-binding update is active", domain.ErrCheckFailed)
}

func shouldRetryBackendBindingLock(err error, attempt int) bool {
	return errors.Is(err, fs.ErrNotExist) && attempt < backendBindingsLockAttempts-1
}

func (m *Mirror) loadBackendBindings() (backendBindingsFile, bool, error) {
	empty := backendBindingsFile{SchemaVersion: backendBindingsSchemaVersion, Services: map[string]string{}}
	b, err := safepath.ReadFileWithinLimit(m.Root, m.backendBindingsPath(), backendBindingsMaxBytes)
	if os.IsNotExist(err) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, fmt.Errorf("%w: read mirror backend binding state", domain.ErrCheckFailed)
	}
	info, err := safepath.StatWithin(m.Root, m.backendBindingsPath())
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return empty, false, fmt.Errorf("%w: mirror backend binding state must be a private regular file", domain.ErrCheckFailed)
	}
	if err := rejectDuplicateJSONKeys(b); err != nil {
		return empty, false, fmt.Errorf("%w: invalid mirror backend binding state", domain.ErrCheckFailed)
	}
	var state backendBindingsFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&state); err != nil {
		return empty, false, fmt.Errorf("%w: invalid mirror backend binding state", domain.ErrCheckFailed)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return empty, false, fmt.Errorf("%w: invalid mirror backend binding state", domain.ErrCheckFailed)
	}
	if state.SchemaVersion != backendBindingsSchemaVersion || len(state.Services) == 0 {
		return empty, false, fmt.Errorf("%w: unsupported or empty mirror backend binding state", domain.ErrCheckFailed)
	}
	for service, digest := range state.Services {
		if _, err := validateBackendBinding(BackendBinding{Service: service, OriginSHA256: digest}); err != nil {
			return empty, false, fmt.Errorf("%w: invalid mirror backend binding state", domain.ErrCheckFailed)
		}
	}
	return state, true, nil
}

func (m *Mirror) saveBackendBindings(state backendBindingsFile) error {
	if state.SchemaVersion != backendBindingsSchemaVersion || len(state.Services) == 0 {
		return fmt.Errorf("%w: refuse invalid mirror backend binding state", domain.ErrCheckFailed)
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return safepath.WriteFileWithin(m.Root, m.backendBindingsPath(), append(b, '\n'), 0o600)
}

func validateBackendBinding(binding BackendBinding) (BackendBinding, error) {
	service, err := validateBackendService(binding.Service)
	if err != nil {
		return BackendBinding{}, err
	}
	digest := binding.OriginSHA256
	if !strings.HasPrefix(digest, backendid.Prefix) || len(digest) != len(backendid.Prefix)+64 {
		return BackendBinding{}, fmt.Errorf("%w: invalid content-minimized backend binding", domain.ErrCheckFailed)
	}
	hexPart := strings.TrimPrefix(digest, backendid.Prefix)
	if hexPart != strings.ToLower(hexPart) {
		return BackendBinding{}, fmt.Errorf("%w: invalid content-minimized backend binding", domain.ErrCheckFailed)
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return BackendBinding{}, fmt.Errorf("%w: invalid content-minimized backend binding", domain.ErrCheckFailed)
	}
	return BackendBinding{Service: service, OriginSHA256: digest}, nil
}

func validateBackendService(service string) (string, error) {
	if service == "" || len(service) > 32 || service != strings.ToLower(service) {
		return "", fmt.Errorf("%w: invalid backend service name", domain.ErrCheckFailed)
	}
	for _, r := range service {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "", fmt.Errorf("%w: invalid backend service name", domain.ErrCheckFailed)
		}
	}
	return service, nil
}

func (m *Mirror) hasBackendServiceEvidence(service, nativeExt string) (bool, error) {
	states, err := m.SyncStates()
	if err != nil {
		return false, err
	}
	for _, state := range states {
		if filepath.Ext(filepath.FromSlash(state.Path)) == nativeExt {
			return true, nil
		}
	}
	if _, err := os.Stat(m.Root); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	evidence := false
	err = filepath.WalkDir(m.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != m.Root && entry.Type()&os.ModeSymlink != 0 {
			if backendEvidenceName(service, nativeExt, entry.Name()) {
				evidence = true
				return fs.SkipAll
			}
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if backendEvidenceName(service, nativeExt, entry.Name()) && (!entry.IsDir() || service == "jira") {
			evidence = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if evidence {
		return true, nil
	}
	var servicePaths []string
	switch service {
	case "confluence":
		servicePaths = []string{
			filepath.Join(m.Root, ".atl", "incremental.json"),
			filepath.Join(m.Root, ".atl", "complete-pulls"),
			filepath.Join(m.Root, ".atl", "reconcile", "confluence"),
			filepath.Join(m.Root, ".atl", "stash", "confluence"),
		}
	case "jira":
		servicePaths = []string{
			filepath.Join(m.Root, ".atl", "pending", "jira"),
			filepath.Join(m.Root, ".atl", "reconcile", "jira"),
			filepath.Join(m.Root, ".atl", "stash", "jira"),
		}
	}
	for _, path := range servicePaths {
		has, err := pathContainsBackendEvidence(path)
		if err != nil {
			return false, err
		}
		if has {
			return true, nil
		}
	}
	return false, nil
}

func backendEvidenceName(service, nativeExt, name string) bool {
	if filepath.Ext(name) == nativeExt {
		return true
	}
	switch service {
	case "confluence":
		// These sidecars remain service evidence if an interrupted/manual cleanup
		// removed both the native page and its state entry.
		return strings.HasSuffix(name, ".meta.json") || strings.HasSuffix(name, ".comments.json")
	case "jira":
		// Confluence mirrors persist successful Jira query-macro expansion beside
		// the page, even when the shared root has no native Jira .wiki substrate.
		if strings.HasSuffix(name, ".jira-macros.json") {
			return true
		}
		return jiraMirrorArtifactName(name)
	default:
		return false
	}
}

func jiraMirrorArtifactName(name string) bool {
	stem := ""
	for _, suffix := range []string{".epic-children.json", ".json", ".assets"} {
		if strings.HasSuffix(name, suffix) {
			stem = strings.TrimSuffix(name, suffix)
			break
		}
	}
	separator := strings.LastIndexByte(stem, '-')
	if separator < 1 || separator == len(stem)-1 {
		return false
	}
	for i, r := range stem[:separator] {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && (r != '_' || i == 0) {
			return false
		}
	}
	for _, r := range stem[separator+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pathContainsBackendEvidence(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return true, nil
	}
	found := false
	err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path || entry.IsDir() {
			return nil
		}
		if entry.Name() == ".mirror.lock" {
			return nil
		}
		found = true
		return fs.SkipAll
	})
	return found, err
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
