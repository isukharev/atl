package mirror

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const maxArtifactPathBytes = 4096

type artifactPathClass uint8

const (
	artifactPathClassInvalid artifactPathClass = iota
	artifactPathClassPublic
	artifactPathClassPrivateBase
)

// ArtifactPath is a constructed, canonical path relative to one mirror root.
// Its fields are deliberately private: callers can inspect a qualified value,
// but cannot turn an arbitrary string into a publication capability.
type ArtifactPath struct {
	value string
	class artifactPathClass
}

// NewPublicArtifactPath qualifies one public mirror-relative artifact path.
// Reserved .atl state is never public, including ASCII case aliases that may
// collide on a different supported filesystem.
func NewPublicArtifactPath(value string) (ArtifactPath, error) {
	return newArtifactPath(value, artifactPathClassPublic)
}

// NewPrivateBaseArtifactPath qualifies one private pristine-base artifact.
// The only admitted private namespace is a non-empty descendant of the exact
// .atl/base subtree.
func NewPrivateBaseArtifactPath(value string) (ArtifactPath, error) {
	return newArtifactPath(value, artifactPathClassPrivateBase)
}

// PublicArtifactPathWithin derives and qualifies target relative to root.
// filepath.Rel failures are part of the construction boundary rather than a
// late publication concern.
func PublicArtifactPathWithin(root, target string) (ArtifactPath, error) {
	return artifactPathWithin(root, target, artifactPathClassPublic)
}

func artifactPathWithin(root, target string, class artifactPathClass) (ArtifactPath, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return ArtifactPath{}, fmt.Errorf("%w: resolve mirror artifact path", domain.ErrCheckFailed)
	}
	return newArtifactPath(filepath.ToSlash(rel), class)
}

func newArtifactPath(value string, class artifactPathClass) (ArtifactPath, error) {
	if err := validateArtifactPath(value, class); err != nil {
		return ArtifactPath{}, fmt.Errorf("%w: invalid mirror artifact path: %v", domain.ErrCheckFailed, err)
	}
	return ArtifactPath{value: value, class: class}, nil
}

func validateArtifactPath(value string, class artifactPathClass) error {
	if value == "" || len(value) > maxArtifactPathBytes || !utf8.ValidString(value) {
		return fmt.Errorf("path is empty, overlong, or invalid UTF-8")
	}
	if strings.ContainsAny(value, "\\:\x00") || strings.IndexFunc(value, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	}) >= 0 {
		return fmt.Errorf("path contains an alternate separator, drive marker, or control byte")
	}
	clean := path.Clean(value)
	if clean != value || path.IsAbs(value) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path is not a canonical contained relative path")
	}
	first, remainder, _ := strings.Cut(value, "/")
	switch class {
	case artifactPathClassPublic:
		if strings.EqualFold(first, ".atl") {
			return fmt.Errorf("public path targets reserved private state")
		}
	case artifactPathClassPrivateBase:
		if first != ".atl" || remainder == "" {
			return fmt.Errorf("private path is outside the pristine-base subtree")
		}
		base, child, _ := strings.Cut(remainder, "/")
		if base != "base" || child == "" {
			return fmt.Errorf("private path is not a descendant of .atl/base")
		}
	default:
		return fmt.Errorf("path class is invalid")
	}
	return nil
}

func parseDurableArtifactPath(value string) (ArtifactPath, error) {
	first, _, _ := strings.Cut(value, "/")
	if strings.EqualFold(first, ".atl") {
		return NewPrivateBaseArtifactPath(value)
	}
	return NewPublicArtifactPath(value)
}

// parseDurablePublicStatePath accepts the one historical Windows spelling
// written by Jira pull before public paths were canonicalized at construction.
// Mixed separators and every normalized traversal/reserved alias still fail.
func parseDurablePublicStatePath(state SyncState) (ArtifactPath, error) {
	value := state.Path
	qualified, err := NewPublicArtifactPath(value)
	if err == nil || !strings.Contains(value, `\`) || strings.Contains(value, "/") {
		return qualified, err
	}
	qualified, err = NewPublicArtifactPath(strings.ReplaceAll(value, `\`, "/"))
	if err != nil {
		return ArtifactPath{}, err
	}
	switch {
	case state.Version == 0 && path.Ext(qualified.String()) == ".wiki" && path.Base(qualified.String()) == state.ID+".wiki":
		return qualified, nil
	case state.Version > 0 && positiveDecimalIdentity(state.ID) && path.Ext(qualified.String()) == ".csf":
		return qualified, nil
	default:
		return ArtifactPath{}, fmt.Errorf("%w: legacy Windows state path does not match its resource identity", domain.ErrCheckFailed)
	}
}

func (p ArtifactPath) relative(expected artifactPathClass) (string, error) {
	if p.class != expected {
		return "", fmt.Errorf("%w: mirror artifact path has the wrong ownership class", domain.ErrCheckFailed)
	}
	if err := validateArtifactPath(p.value, expected); err != nil {
		return "", fmt.Errorf("%w: invalid mirror artifact path: %v", domain.ErrCheckFailed, err)
	}
	return p.value, nil
}

func (p ArtifactPath) relativeAny() (string, error) {
	switch p.class {
	case artifactPathClassPublic, artifactPathClassPrivateBase:
		return p.relative(p.class)
	default:
		return "", fmt.Errorf("%w: mirror artifact path is unqualified", domain.ErrCheckFailed)
	}
}

// String returns the already-qualified canonical relative spelling. It does
// not provide a constructor or permit reclassification.
func (p ArtifactPath) String() string { return p.value }

func artifactPathKey(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}
