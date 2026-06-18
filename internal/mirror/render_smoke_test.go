package mirror

import (
	"os"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
)

func TestRenderSample(t *testing.T) {
	raw, err := os.ReadFile("../csf/testdata/sample.csf")
	if err != nil {
		t.Fatal(err)
	}
	root, err := csf.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	refs := fragment.Extract(root)
	// Pretend the drawio asset was resolved so we can assert the image link.
	for i := range refs {
		if refs[i].Kind == domain.RefDrawio {
			refs[i].Asset = "sample.assets/my-diagram.png"
		}
	}
	md := string(RenderMarkdown(root, refs))
	for _, want := range []string{
		"## Sample page",          // heading
		"| Key | Value |",         // table
		"```python",               // code fence with language
		"![diagram: my-diagram](", // resolved drawio image
		"DONE",                    // status macro title
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered md missing %q\n---\n%s", want, md)
		}
	}
}
