package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

func TestQualifyJiraPullIssuePathsContainsServerSegments(t *testing.T) {
	root := t.TempDir()
	issue := &domain.Issue{Key: "../../PROJ-1", Project: "../PROJ"}

	paths, err := qualifyJiraPullIssuePaths(root, issue)
	if err != nil {
		t.Fatalf("qualify paths: %v", err)
	}
	if paths.keySeg != safepath.Segment(issue.Key) {
		t.Fatalf("key segment = %q, want sanitized %q", paths.keySeg, safepath.Segment(issue.Key))
	}
	for name, path := range map[string]string{
		"issue directory": paths.dir,
		"markdown":        paths.markdown,
		"wiki":            paths.wiki,
		"snapshot":        paths.snapshot,
		"epic children":   paths.epicChildren,
	} {
		if !safepath.Within(root, path) {
			t.Errorf("%s escaped pull root: %s", name, path)
		}
	}
	if got, want := filepath.Base(paths.wiki), paths.keySeg+wikiExt; got != want {
		t.Errorf("wiki basename = %q, want %q", got, want)
	}
}

func TestJiraPullSnapshotBytesKeepsContract(t *testing.T) {
	issue := &domain.Issue{
		Key: "PROJ-1",
		ID:  "10001",
		Fields: map[string]any{
			"customfield_2": "{code}\r\nnative wiki bytes\r\n{code}",
			"customfield_1": true,
		},
	}

	got, err := jiraPullSnapshotBytes(issue)
	if err != nil {
		t.Fatalf("snapshot bytes: %v", err)
	}
	want := "{\n" +
		"  \"key\": \"PROJ-1\",\n" +
		"  \"id\": \"10001\",\n" +
		"  \"fields\": {\n" +
		"    \"customfield_1\": true,\n" +
		"    \"customfield_2\": \"{code}\\r\\nnative wiki bytes\\r\\n{code}\"\n" +
		"  }\n" +
		"}\n"
	if string(got) != want {
		t.Fatalf("snapshot bytes changed\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestJiraPullSnapshotBytesUsesEmptyFieldsObject(t *testing.T) {
	got, err := jiraPullSnapshotBytes(&domain.Issue{Key: "PROJ-1", ID: "10001"})
	if err != nil {
		t.Fatalf("snapshot bytes: %v", err)
	}
	want := "{\n  \"key\": \"PROJ-1\",\n  \"id\": \"10001\",\n  \"fields\": {}\n}\n"
	if string(got) != want {
		t.Fatalf("nil fields changed snapshot shape\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestJiraSyncIdentityPreservesAndBindsStableNumericID(t *testing.T) {
	previous := mirror.SyncState{ID: "PROJ-1", Identity: "10001"}
	for _, observed := range []string{"", "opaque"} {
		got, err := jiraSyncIdentity(observed, &previous)
		if err != nil || got != "10001" {
			t.Fatalf("observed=%q identity=%q err=%v", observed, got, err)
		}
	}
	if _, err := jiraSyncIdentity("10002", &previous); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("mismatch err=%v", err)
	}
}
