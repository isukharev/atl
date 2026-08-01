package compatibility

import (
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestVersionValidation(t *testing.T) {
	valid := []string{"0.0.0", "9.5.2", "01.002.0003"}
	for _, value := range valid {
		if _, err := ParseVersion(value); err != nil {
			t.Errorf("ParseVersion(%q): %v", value, err)
		}
	}
	invalid := []string{"", "9", "9.5", "9.5.2.1", ".5.2", "9..2", "v9.5.2", "9.5.x", "9. 5.2", "９.５.２", strings.Repeat("1", 21) + ".2.3", strings.Repeat("1", 65)}
	for _, value := range invalid {
		if _, err := ParseVersion(value); !errors.Is(err, domain.ErrConfig) {
			t.Errorf("ParseVersion(%q) error = %v, want ErrConfig", value, err)
		}
	}
}

func TestBuildNumberValidation(t *testing.T) {
	valid := []string{"0", "12345", "00012345", "12345678901234567890"}
	for _, value := range valid {
		if _, err := ParseBuildNumber(value); err != nil {
			t.Errorf("ParseBuildNumber(%q): %v", value, err)
		}
	}
	invalid := []string{"", "-1", "+1", "9.1", " 1234", "１２", "123456789012345678901"}
	for _, value := range invalid {
		if _, err := ParseBuildNumber(value); !errors.Is(err, domain.ErrConfig) {
			t.Errorf("ParseBuildNumber(%q) error = %v, want ErrConfig", value, err)
		}
	}
}

func TestSelectUsesExactClosedProviderIDAndCatalogCopyIsolated(t *testing.T) {
	testDescriptor := Descriptor{
		ID: "synthetic-provider", Product: ProductConfluence,
		Family: "synthetic-family",
	}
	descriptor, ok := selectFrom([]Descriptor{testDescriptor}, ProductConfluence, "synthetic-provider")
	if !ok {
		t.Fatal("exact descriptor not selected")
	}
	if descriptor != testDescriptor {
		t.Fatalf("descriptor = %+v", descriptor)
	}

	for _, mismatch := range []struct {
		product    Product
		providerID string
	}{
		{ProductJira, "synthetic-provider"},
		{ProductConfluence, ""},
		{ProductConfluence, "synthetic-provider-nearby"},
	} {
		if got, selected := selectFrom([]Descriptor{testDescriptor}, mismatch.product, mismatch.providerID); selected || got != (Descriptor{}) {
			t.Errorf("Select(%q, %q) = (%+v, %t), want no selection", mismatch.product, mismatch.providerID, got, selected)
		}
	}

	first := Descriptors()
	second := Catalog()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("catalog sizes = %d, %d", len(first), len(second))
	}
	first[0].ID = "mutated"
	if second[0].ID != ConfluenceInlineCommentsDCProfileID || Descriptors()[0].ID != ConfluenceInlineCommentsDCProfileID {
		t.Fatal("caller mutated compile-time catalog")
	}
	if _, selected := Select(ProductConfluence, "synthetic-provider"); selected {
		t.Fatal("synthetic provider unexpectedly selected from the production catalog")
	}
}

func TestProductIsClosedAndParsedExactly(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  Product
	}{
		{"confluence", ProductConfluence},
		{"jira", ProductJira},
	} {
		got, err := ParseProduct(tc.value)
		if err != nil || got != tc.want || got.String() != tc.value || !got.Valid() {
			t.Fatalf("ParseProduct(%q) = (%q, %v), want %q", tc.value, got, err, tc.want)
		}
	}
	for _, value := range []string{"", "Confluence", "jira ", "other"} {
		if got, err := ParseProduct(value); !errors.Is(err, domain.ErrConfig) || got.Valid() {
			t.Errorf("ParseProduct(%q) = (%q, %v)", value, got, err)
		}
	}
}
