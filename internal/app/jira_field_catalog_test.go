package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type legacyFieldCatalogTracker struct {
	domain.Tracker
	fields []domain.FieldDef
}

func (t *legacyFieldCatalogTracker) Fields(context.Context) ([]domain.FieldDef, error) {
	return append([]domain.FieldDef(nil), t.fields...), nil
}

type qualifiedFieldCatalogTracker struct {
	*legacyFieldCatalogTracker
	snapshot domain.FieldCatalogSnapshot
}

func (t *qualifiedFieldCatalogTracker) ReadFieldCatalog(context.Context) (domain.FieldCatalogSnapshot, error) {
	return t.snapshot, nil
}

func TestFieldCatalogQualifiesFilteredAtomicSnapshot(t *testing.T) {
	tracker := &qualifiedFieldCatalogTracker{
		legacyFieldCatalogTracker: &legacyFieldCatalogTracker{},
		snapshot: domain.FieldCatalogSnapshot{Complete: true, Fields: []domain.FieldDef{
			{ID: "summary", Name: "Summary", Schema: "string"},
			{ID: "customfield_2", Name: "Risk score", Custom: true, Schema: "number"},
			{ID: "customfield_1", Name: "Risk note", Custom: true, Schema: "string"},
		}},
	}
	result, err := (&JiraService{tr: tracker}).FieldCatalog(context.Background(), JiraFieldCatalogOpts{NameLike: "risk", Custom: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.Source != "jira-field-catalog" || !result.Complete || result.PartialReason != "" || result.Total != 3 || result.Count != 2 {
		t.Fatalf("result=%+v", result)
	}
	if result.Fields[0].ID != "customfield_2" || result.Fields[1].ID != "customfield_1" {
		t.Fatalf("fields=%+v", result.Fields)
	}
}

func TestFieldCatalogKeepsLegacySourceFailClosed(t *testing.T) {
	tracker := &legacyFieldCatalogTracker{fields: []domain.FieldDef{{ID: "summary", Name: "Summary"}}}
	result, err := (&JiraService{tr: tracker}).FieldCatalog(context.Background(), JiraFieldCatalogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Source != "legacy" || result.PartialReason == "" || result.Total != 1 || result.Count != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestFieldCatalogRejectsContradictoryOrMalformedSnapshots(t *testing.T) {
	tests := []domain.FieldCatalogSnapshot{
		{Complete: false, Fields: []domain.FieldDef{{ID: "summary"}}},
		{Complete: true, Fields: []domain.FieldDef{}},
		{Complete: true, PartialReason: "partial", Fields: []domain.FieldDef{{ID: "summary"}}},
		{Complete: true, Fields: []domain.FieldDef{{ID: ""}}},
		{Complete: true, Fields: []domain.FieldDef{{ID: "summary"}, {ID: "summary"}}},
	}
	for _, snapshot := range tests {
		tracker := &qualifiedFieldCatalogTracker{legacyFieldCatalogTracker: &legacyFieldCatalogTracker{}, snapshot: snapshot}
		if _, err := (&JiraService{tr: tracker}).FieldCatalog(context.Background(), JiraFieldCatalogOpts{}); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("snapshot=%+v err=%v", snapshot, err)
		}
	}
}
