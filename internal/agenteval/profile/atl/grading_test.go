package atl_test

import (
	"slices"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/grading"
	profileatl "github.com/isukharev/atl/internal/agenteval/profile/atl"
)

func TestLegacyATLCheckCatalogIsClosedDeterministicAndImmutable(t *testing.T) {
	catalog := profileatl.LegacyGradingCatalog()
	if len(catalog) != 28 {
		t.Fatalf("catalog entries=%d", len(catalog))
	}
	kinds := grading.CheckKinds()
	for index, descriptor := range catalog {
		if index > 0 && catalog[index-1].Kind >= descriptor.Kind || !slices.Contains(kinds, descriptor.EvidenceFamily) {
			t.Fatalf("descriptor[%d]=%+v", index, descriptor)
		}
		if family, ok := profileatl.LegacyGradingFamily(descriptor.Kind); !ok || family != descriptor.EvidenceFamily {
			t.Fatalf("lookup %q=%q,%t", descriptor.Kind, family, ok)
		}
	}
	catalog[0].Kind = "mutated"
	if again := profileatl.LegacyGradingCatalog(); again[0].Kind == "mutated" {
		t.Fatal("catalog returned shared mutable storage")
	}
	if _, ok := profileatl.LegacyGradingFamily("opaque_model_judge"); ok {
		t.Fatal("unknown ATL check acquired implicit grading authority")
	}
}
