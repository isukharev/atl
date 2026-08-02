package mirror

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// StageReconcileArtifacts preserves exact base and remote bytes for an
// external three-way reconcile without modifying the canonical native file.
// Artifacts live in the ignored mirror state directory, preserve the native
// file's mirror-relative layout, and are returned as slash-separated paths
// relative to the mirror root.
func (m *Mirror) StageReconcileArtifacts(service, nativePath string, base, theirs []byte) (basePath, theirsPath string, err error) {
	ext, err := reconcileNativeExtension(service)
	if err != nil {
		return "", "", err
	}
	if filepath.Ext(nativePath) != ext {
		return "", "", fmt.Errorf("%w: reconcile service %q requires a %s native path", domain.ErrCheckFailed, service, ext)
	}

	info, err := safepath.StatWithin(m.Root, nativePath)
	if err != nil {
		return "", "", fmt.Errorf("%w: inspect reconcile native path %s: %v", domain.ErrCheckFailed, nativePath, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%w: reconcile native path is not a regular file: %s", domain.ErrCheckFailed, nativePath)
	}

	nativeRelative, err := filepath.Rel(m.Root, nativePath)
	if err != nil || nativeRelative == "." || nativeRelative == ".." || strings.HasPrefix(nativeRelative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w: resolve reconcile native path inside mirror root", domain.ErrCheckFailed)
	}

	reconcileRoot := filepath.Join(m.Root, ".atl", "reconcile", service)
	baseTarget := filepath.Join(reconcileRoot, nativeRelative) + ".base"
	theirsTarget := filepath.Join(reconcileRoot, nativeRelative) + ".theirs"
	if !safepath.Within(reconcileRoot, baseTarget) || !safepath.Within(reconcileRoot, theirsTarget) {
		return "", "", fmt.Errorf("%w: refusing unsafe reconcile artifact path", domain.ErrCheckFailed)
	}
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(baseTarget), 0o700); err != nil {
		return "", "", fmt.Errorf("%w: create reconcile artifact directory: %v", domain.ErrCheckFailed, err)
	}

	if err := stageReconcileArtifact(m.Root, baseTarget, ext, ".base", base); err != nil {
		return "", "", err
	}
	if err := stageReconcileArtifact(m.Root, theirsTarget, ext, ".theirs", theirs); err != nil {
		return "", "", err
	}

	basePath, err = reconcileRelativePath(m.Root, baseTarget)
	if err != nil {
		return "", "", err
	}
	theirsPath, err = reconcileRelativePath(m.Root, theirsTarget)
	if err != nil {
		return "", "", err
	}
	return basePath, theirsPath, nil
}

func reconcileNativeExtension(service string) (string, error) {
	switch service {
	case "confluence":
		return ".csf", nil
	case "jira":
		return ".wiki", nil
	default:
		return "", fmt.Errorf("%w: reconcile service must be confluence or jira", domain.ErrCheckFailed)
	}
}

func stageReconcileArtifact(root, target, nativeExt, suffix string, body []byte) error {
	if suffix != ".base" && suffix != ".theirs" {
		return fmt.Errorf("%w: reconcile artifact suffix must be .base or .theirs", domain.ErrCheckFailed)
	}
	if !strings.HasSuffix(target, nativeExt+suffix) {
		return fmt.Errorf("%w: reconcile artifact suffix does not match native extension", domain.ErrCheckFailed)
	}
	if err := safepath.WriteFileExclusiveWithin(root, target, body, 0o600); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: stage reconcile artifact %s: %v", domain.ErrCheckFailed, target, err)
		}
		existing, readErr := safepath.ReadFileWithin(root, target)
		if readErr != nil {
			return fmt.Errorf("%w: inspect existing reconcile artifact %s: %v", domain.ErrCheckFailed, target, readErr)
		}
		if !bytes.Equal(existing, body) {
			return fmt.Errorf("%w: existing reconcile artifact differs; preserve and resolve it manually: %s", domain.ErrCheckFailed, target)
		}
		info, statErr := safepath.StatWithin(root, target)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%w: existing reconcile artifact has unsafe type or permissions; preserve and repair it manually: %s", domain.ErrCheckFailed, target)
		}
	}
	return nil
}

func reconcileRelativePath(root, target string) (string, error) {
	relativePath, err := filepath.Rel(root, target)
	if err != nil || relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || !safepath.Within(root, target) {
		return "", fmt.Errorf("%w: resolve reconcile artifact path", domain.ErrCheckFailed)
	}
	return filepath.ToSlash(relativePath), nil
}
