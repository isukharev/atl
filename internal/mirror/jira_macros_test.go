package mirror

import (
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

func TestJiraMacroDescriptorsPreserveOccurrenceColumnsAndLimit(t *testing.T) {
	body := `<p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">PROJ-1</ac:parameter></ac:structured-macro></p>` +
		`<p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="jqlQuery">project = PROJ</ac:parameter><ac:parameter ac:name="columns">key, summary, status</ac:parameter><ac:parameter ac:name="maximumIssues">25</ac:parameter></ac:structured-macro></p>`
	root, err := csf.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	descriptors := JiraMacroDescriptors(root)
	if len(descriptors) != 1 || descriptors[0].Index != 1 || descriptors[0].JQL != "project = PROJ" || strings.Join(descriptors[0].Columns, ",") != "key,summary,status" || descriptors[0].Limit != 25 {
		t.Fatalf("descriptors=%+v", descriptors)
	}
}

func TestJiraMacroGeneratedSectionPrecedesComments(t *testing.T) {
	root, err := csf.Parse([]byte(`<p>Body</p>`))
	if err != nil {
		t.Fatal(err)
	}
	_, _, suffix := RenderMarkdownViewParts(root, nil, MDViewOpts{
		JiraMacros: []JiraMacroView{{Index: 0, Markdown: "| Key |\n| --- |", Complete: true}},
		Comments:   []domain.Comment{{Author: "Ada", Body: "Reviewed"}},
	})
	queries := strings.Index(suffix, "# Jira Queries")
	comments := strings.Index(suffix, "# Comments")
	if queries < 0 || comments < 0 || queries >= comments {
		t.Fatalf("generated section order:\n%s", suffix)
	}
}

func TestJiraMacroViewsRenderOnlyInGeneratedSuffix(t *testing.T) {
	root, err := csf.Parse([]byte(`<p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="jqlQuery">project = PROJ</ac:parameter></ac:structured-macro></p>`))
	if err != nil {
		t.Fatal(err)
	}
	prefix, body, suffix := RenderMarkdownViewParts(root, nil, MDViewOpts{JiraMacros: []JiraMacroView{{Index: 0, Markdown: "| Key |\n| --- |\n| PROJ-1 |", Complete: true}}})
	if !strings.Contains(body, "jira query: project = PROJ") || strings.Contains(body, "| PROJ-1 |") {
		t.Fatalf("editable body contains generated table: %q", body)
	}
	if !strings.Contains(suffix, ConfluenceJiraMacrosMarker) || !strings.Contains(suffix, "# Jira Queries") || !strings.Contains(suffix, "| PROJ-1 |") {
		t.Fatalf("generated suffix missing table: %q", suffix)
	}
	if !strings.HasPrefix(prefix, ConfluenceDocumentMarker+"\n") {
		t.Fatalf("prefix=%q", prefix)
	}
}
