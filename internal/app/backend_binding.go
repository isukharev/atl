package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const mirrorBackendBindingSchemaVersion = 1

type MirrorBackendStatus struct {
	SchemaVersion int                     `json:"schema_version"`
	Root          string                  `json:"root"`
	Bindings      []mirror.BackendBinding `json:"bindings"`
}

type MirrorBackendBindResult struct {
	SchemaVersion int    `json:"schema_version"`
	Root          string `json:"root"`
	Service       string `json:"service"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	BackendSHA256 string `json:"backend_sha256"`
}

func backendBinding(service, rawURL string) (mirror.BackendBinding, error) {
	if service != "confluence" && service != "jira" {
		return mirror.BackendBinding{}, fmt.Errorf("%w: --service must be confluence or jira", domain.ErrUsage)
	}
	digest, err := backendid.OriginSHA256(rawURL)
	if err != nil {
		return mirror.BackendBinding{}, fmt.Errorf("%w: configured %s backend origin is invalid", domain.ErrConfig, service)
	}
	return mirror.BackendBinding{Service: service, OriginSHA256: digest}, nil
}

func requireMirrorBackend(root, service, rawURL string) error {
	want, err := backendBinding(service, rawURL)
	if err != nil {
		return err
	}
	return mirror.New(root).RequireBackendBinding(want)
}

func prepareMirrorBackendPopulation(root, service, rawURL, nativeExt string, dryRun bool) error {
	want, err := backendBinding(service, rawURL)
	if err != nil {
		return err
	}
	m := mirror.New(root)
	if dryRun {
		return m.CheckBackendBindingForPopulation(want, nativeExt)
	}
	_, err = m.BindBackendIfFresh(want, nativeExt)
	return err
}

// prepareConfluenceJiraMacroPopulation qualifies the optional cross-service
// read only when the parsed page actually contains Jira query macros. A missing
// Jira URL means expansion is unavailable (the caller keeps placeholders), not
// that the enclosing Confluence operation is invalid.
func (s *ConfluenceService) prepareConfluenceJiraMacroPopulation(root string, hasMacros, dryRun bool) (bool, error) {
	if !hasMacros || s == nil || !s.ensureConfluenceJiraReader() || s.cfg == nil || strings.TrimSpace(s.cfg.JiraURL) == "" {
		return false, nil
	}
	if err := prepareMirrorBackendPopulation(root, "jira", s.cfg.JiraURL, wikiExt, dryRun); err != nil {
		return false, err
	}
	return true, nil
}

func InspectMirrorBackends(root string) (*MirrorBackendStatus, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: mirror root is required", domain.ErrUsage)
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if err := requireInitializedMirror(root); err != nil {
		return nil, err
	}
	bindings, err := mirror.New(root).BackendBindings()
	if err != nil {
		return nil, err
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Service < bindings[j].Service })
	return &MirrorBackendStatus{SchemaVersion: mirrorBackendBindingSchemaVersion, Root: root, Bindings: bindings}, nil
}

func PreviewMirrorBackendBind(root, service, rawURL string) (*MirrorBackendBindResult, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: mirror root is required", domain.ErrUsage)
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if err := requireInitializedMirror(root); err != nil {
		return nil, err
	}
	want, err := backendBinding(service, rawURL)
	if err != nil {
		return nil, err
	}
	result := &MirrorBackendBindResult{SchemaVersion: mirrorBackendBindingSchemaVersion, Root: root, Service: service, Mode: "preview", Status: "would_bind", BackendSHA256: want.OriginSHA256}
	got, exists, err := mirror.New(root).BackendBinding(service)
	if err != nil {
		return result, err
	}
	if exists {
		if got.OriginSHA256 != want.OriginSHA256 {
			result.Status = "blocked"
			result.BackendSHA256 = ""
			return result, fmt.Errorf("%w: mirror backend binding does not match the configured service %s; bindings cannot be replaced", domain.ErrCheckFailed, service)
		}
		result.Status = "already_bound"
	}
	return result, nil
}

func ApplyMirrorBackendBind(root, service, rawURL, expectedSHA256, confirm string) (*MirrorBackendBindResult, error) {
	if confirm != "BIND" {
		return nil, fmt.Errorf("%w: --confirm must be exactly BIND", domain.ErrUsage)
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: mirror root is required", domain.ErrUsage)
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if err := requireInitializedMirror(root); err != nil {
		return nil, err
	}
	want, err := backendBinding(service, rawURL)
	if err != nil {
		return nil, err
	}
	if expectedSHA256 == "" || expectedSHA256 != want.OriginSHA256 {
		return nil, fmt.Errorf("%w: reviewed backend hash does not match the configured service", domain.ErrCheckFailed)
	}
	created, err := mirror.New(root).BindBackend(want)
	if err != nil {
		return nil, err
	}
	status := "already_bound"
	if created {
		status = "bound"
	}
	return &MirrorBackendBindResult{SchemaVersion: mirrorBackendBindingSchemaVersion, Root: root, Service: service, Mode: "apply", Status: status, BackendSHA256: want.OriginSHA256}, nil
}

func requireInitializedMirror(root string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("%w: mirror root is required", domain.ErrUsage)
	}
	info, err := os.Lstat(filepath.Join(root, ".atl"))
	if os.IsNotExist(err) {
		return fmt.Errorf("%w: mirror is not initialized", domain.ErrNotFound)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: mirror state path is not a directory", domain.ErrCheckFailed)
	}
	return nil
}
