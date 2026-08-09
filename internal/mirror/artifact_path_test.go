package mirror

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func mustArtifactPath(value string) ArtifactPath {
	qualified, err := parseDurableArtifactPath(value)
	if err != nil {
		panic(err)
	}
	return qualified
}

func TestArtifactPathQualification(t *testing.T) {
	t.Parallel()
	public, err := NewPublicArtifactPath("SPACE/page/page.csf")
	if err != nil || public.String() != "SPACE/page/page.csf" {
		t.Fatalf("public=%q err=%v", public.String(), err)
	}
	private, err := NewPrivateBaseArtifactPath(".atl/base/42.csf")
	if err != nil || private.String() != ".atl/base/42.csf" {
		t.Fatalf("private=%q err=%v", private.String(), err)
	}

	invalidPublic := []string{
		"", ".", "..", "../escape", "/absolute", `C:/drive`, `a\b`,
		"a//b", "a/./b", "a/../b", ".atl", ".atl/state.json", ".ATL/base/42.csf",
		"control\x00byte", "control\nbyte", strings.Repeat("a", maxArtifactPathBytes+1),
	}
	for _, value := range invalidPublic {
		value := value
		t.Run("public", func(t *testing.T) {
			t.Parallel()
			if _, err := NewPublicArtifactPath(value); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("NewPublicArtifactPath(%q) error=%v", value, err)
			}
		})
	}
	invalidPrivate := []string{
		".atl", ".atl/base", ".atl/base/", ".ATL/base/42.csf", ".atl/BASE/42.csf",
		".atl/other/42.csf", ".atl/base/../state.json", "SPACE/page.csf",
	}
	for _, value := range invalidPrivate {
		value := value
		t.Run("private", func(t *testing.T) {
			t.Parallel()
			if _, err := NewPrivateBaseArtifactPath(value); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("NewPrivateBaseArtifactPath(%q) error=%v", value, err)
			}
		})
	}
}

func TestPublicArtifactPathWithinRejectsEscapeAndReservedAlias(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "tmp", "mirror")
	for _, target := range []string{
		filepath.Join(root, "..", "escape"),
		filepath.Join(root, ".ATL", "state.json"),
	} {
		if _, err := PublicArtifactPathWithin(root, target); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("target=%q error=%v", target, err)
		}
	}
}

func TestDurablePublicStatePathNormalizesOnlyLegacyWindowsSeparators(t *testing.T) {
	t.Parallel()
	qualified, err := parseDurablePublicStatePath(`PROJ\PROJ-1.wiki`)
	if err != nil || qualified.String() != "PROJ/PROJ-1.wiki" {
		t.Fatalf("legacy path=%q err=%v", qualified.String(), err)
	}
	for _, value := range []string{`.ATL\base\10.csf`, `..\escape`, `mixed/path\file`} {
		if _, err := parseDurablePublicStatePath(value); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("legacy path %q error=%v", value, err)
		}
	}
}

func FuzzArtifactPathQualification(f *testing.F) {
	for _, seed := range []string{
		"SPACE/page.csf", ".atl/base/42.csf", "../escape", `a\b`, ".ATL/state.json", "a//b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if public, err := NewPublicArtifactPath(value); err == nil {
			if public.String() != value || public.class != artifactPathClassPublic {
				t.Fatalf("public qualification changed value or class")
			}
			if _, reparsedErr := NewPublicArtifactPath(public.String()); reparsedErr != nil {
				t.Fatalf("qualified public path did not round-trip: %v", reparsedErr)
			}
		}
		if private, err := NewPrivateBaseArtifactPath(value); err == nil {
			if private.String() != value || private.class != artifactPathClassPrivateBase {
				t.Fatalf("private qualification changed value or class")
			}
			if _, reparsedErr := NewPrivateBaseArtifactPath(private.String()); reparsedErr != nil {
				t.Fatalf("qualified private path did not round-trip: %v", reparsedErr)
			}
		}
	})
}
