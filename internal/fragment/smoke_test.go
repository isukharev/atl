package fragment

import (
	"os"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

func TestExtractSample(t *testing.T) {
	raw, err := os.ReadFile("../csf/testdata/sample.csf")
	if err != nil {
		t.Fatal(err)
	}
	root, err := csf.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	refs := Extract(root)
	kinds := map[domain.RefKind]int{}
	var drawioRev string
	for _, r := range refs {
		kinds[r.Kind]++
		if r.Kind == domain.RefDrawio {
			drawioRev = r.Params["revision"]
		}
	}
	for _, want := range []domain.RefKind{domain.RefDrawio, domain.RefUser, domain.RefImage, domain.RefPageLink, domain.RefAttachment} {
		if kinds[want] == 0 {
			t.Errorf("expected at least one %q fragment, kinds=%v", want, kinds)
		}
	}
	if drawioRev != "7" {
		t.Errorf("drawio revision = %q, want 7", drawioRev)
	}
}
