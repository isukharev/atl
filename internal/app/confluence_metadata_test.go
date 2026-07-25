package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestConfluencePageMetadataProjectsClosedTriStateResult(t *testing.T) {
	for _, test := range []struct {
		name       string
		restricted *bool
		wantState  string
	}{
		{name: "unknown", wantState: ConfluenceRestrictionUnknown},
		{name: "unrestricted", restricted: boolPointer(false), wantState: ConfluenceRestrictionUnrestricted},
		{name: "restricted", restricted: boolPointer(true), wantState: ConfluenceRestrictionRestricted},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{meta: &domain.PageMeta{
				ID: "42", Title: "Design", Space: "DOCS", Version: 7,
				Updated: "2026-07-25T12:00:00.000Z", Restrictions: test.restricted,
				URL: "https://backend.invalid/private", Labels: []string{"private-label"},
				Ancestors: []string{"private-ancestor"},
			}}
			result, err := (&ConfluenceService{store: store}).PageMetadata(context.Background(), "42")
			if err != nil {
				t.Fatal(err)
			}
			if store.metaID != "42" || store.getID != "" ||
				result.SchemaVersion != ConfluencePageMetadataSchemaVersion ||
				result.ID != "42" || result.Title != "Design" || result.Space != "DOCS" ||
				result.Version != 7 || result.Updated != "2026-07-25T12:00:00.000Z" ||
				result.RestrictionState != test.wantState {
				t.Fatalf("result=%+v meta id=%q", result, store.metaID)
			}
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, forbidden := range []string{"backend.invalid", "private-label", "private-ancestor", "labels", "ancestors", "url"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("closed projection exposed %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestConfluencePageMetadataRejectsUnreconciledStoreResult(t *testing.T) {
	for _, meta := range []*domain.PageMeta{
		nil,
		{ID: "other", Title: "Design", Space: "DOCS", Version: 7},
		{ID: "42", Space: "DOCS", Version: 7},
		{ID: "42", Title: "Design", Version: 7},
		{ID: "42", Title: "Design", Space: "DOCS"},
	} {
		_, err := (&ConfluenceService{store: &recordingStore{meta: meta}}).PageMetadata(context.Background(), "42")
		if !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("meta=%+v error=%v", meta, err)
		}
	}
}

func boolPointer(value bool) *bool { return &value }
