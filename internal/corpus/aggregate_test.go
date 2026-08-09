//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package corpus

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestStorePublishesAggregateMembersWithBackendQualifications(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	spec := MemberSpec{
		Service: ServiceAggregate, StableID: "indexer-v1-documents", Role: RoleDocument,
		Path: "indexer-v1/documents.jsonl",
	}
	if err := stage.Add(context.Background(), spec, strings.NewReader("{}\n")); err != nil {
		t.Fatal(err)
	}
	generation, err := stage.Seal(context.Background(), sealOptions("", ServiceConfluence, ServiceJira))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = generation.Close() }()
	summary, err := store.Publish(context.Background(), stage.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Services) != 2 || summary.Services[0] != ServiceConfluence || summary.Services[1] != ServiceJira {
		t.Fatalf("qualified services = %v", summary.Services)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	var copied bytes.Buffer
	if _, err := selected.CopyMember(context.Background(), ServiceAggregate, spec.StableID, spec.Role, &copied); err != nil {
		t.Fatal(err)
	}
	if copied.String() != "{}\n" {
		t.Fatalf("aggregate bytes = %q", copied.String())
	}
}
