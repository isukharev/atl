package mirror

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const maxArtifactPathBytes = 4096

type artifactPathClass uint8

const (
	artifactPathPublic artifactPathClass = iota + 1
	artifactPathPrivateBase
)

// ArtifactPath is a constructed, canonical path to one mirror publication
// artifact. Its representation and public/private class are deliberately
// opaque: callers can construct values, but cannot recover or mutate the raw
// relative string.
type ArtifactPath struct {
	value string
	class artifactPathClass
}

// NewPublicArtifactPath qualifies a canonical public mirror-relative path.
// Public artifacts can never target the reserved .atl state tree.
func NewPublicArtifactPath(value string) (ArtifactPath, error) {
	return newArtifactPath(value, artifactPathPublic)
}

// newPrivateArtifactPath qualifies a canonical private pristine-base path.
// Private construction remains package-owned so callers can only publish
// public artifacts through the transient API.
func newPrivateArtifactPath(value string) (ArtifactPath, error) {
	return newArtifactPath(value, artifactPathPrivateBase)
}

func newArtifactPath(value string, class artifactPathClass) (ArtifactPath, error) {
	if value == "" || len(value) > maxArtifactPathBytes || path.IsAbs(value) || strings.ContainsAny(value, "\\:\x00") {
		return ArtifactPath{}, fmt.Errorf("%w: invalid mirror artifact path", domain.ErrCheckFailed)
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ArtifactPath{}, fmt.Errorf("%w: non-canonical mirror artifact path", domain.ErrCheckFailed)
	}
	switch class {
	case artifactPathPublic:
		first := clean
		if slash := strings.IndexByte(first, '/'); slash >= 0 {
			first = first[:slash]
		}
		if asciiEqualFold(first, ".atl") {
			return ArtifactPath{}, fmt.Errorf("%w: public mirror artifact path targets reserved private state", domain.ErrCheckFailed)
		}
	case artifactPathPrivateBase:
		if !strings.HasPrefix(clean, ".atl/base/") {
			return ArtifactPath{}, fmt.Errorf("%w: private mirror artifact path is outside the pristine-base subtree", domain.ErrCheckFailed)
		}
	default:
		return ArtifactPath{}, fmt.Errorf("%w: invalid mirror artifact path class", domain.ErrCheckFailed)
	}
	return ArtifactPath{value: clean, class: class}, nil
}

func asciiEqualFold(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range len(left) {
		a, b := left[i], right[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

// artifactPathFromDurable reparses a persisted string as untrusted input. The
// path itself determines its closed public/private class; no durable class bit
// is added to the byte-stable publication schema.
func artifactPathFromDurable(value string) (ArtifactPath, error) {
	if strings.HasPrefix(value, ".atl/") {
		return newPrivateArtifactPath(value)
	}
	return NewPublicArtifactPath(value)
}

func validateArtifactPath(path ArtifactPath) error {
	qualified, err := newArtifactPath(path.value, path.class)
	if err != nil || qualified != path {
		return fmt.Errorf("%w: invalid constructed mirror artifact path", domain.ErrCheckFailed)
	}
	return nil
}

// artifactPathTarget revalidates containment at the concrete mirror root.
// Filesystem operations must still use safepath so a symlink swap between
// qualification and publication fails closed.
func artifactPathTarget(root string, path ArtifactPath) (string, error) {
	if err := validateArtifactPath(path); err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(path.value))
	if !safepath.Within(root, target) {
		return "", fmt.Errorf("%w: mirror artifact path escapes its publication root", domain.ErrCheckFailed)
	}
	return target, nil
}

func artifactPathCollisionKey(path ArtifactPath) (string, error) {
	if err := validateArtifactPath(path); err != nil {
		return "", err
	}
	return registrationPathKey(path.value), nil
}

func artifactPathMatchesDurable(path ArtifactPath, durable string) bool {
	return validateArtifactPath(path) == nil && registrationPathsEqual(path.value, durable)
}

func artifactPathIsPublic(path ArtifactPath) bool {
	return validateArtifactPath(path) == nil && path.class == artifactPathPublic
}

func artifactPathIsPrivateBase(path ArtifactPath) bool {
	return validateArtifactPath(path) == nil && path.class == artifactPathPrivateBase
}

// artifactPathDurableString is the sole transient-to-durable publication
// bridge. Keep it at intent construction; recovery must use
// artifactPathFromDurable instead of trusting the persisted string.
func artifactPathDurableString(path ArtifactPath) (string, error) {
	if err := validateArtifactPath(path); err != nil {
		return "", err
	}
	return path.value, nil
}
