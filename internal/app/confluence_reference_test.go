package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type referenceStore struct {
	domain.DocStore
	refs       []domain.PageRef
	next       string
	finalURL   string
	searches   []string
	shortPaths []string
}

func (s *referenceStore) Search(_ context.Context, query string, _ int, _ string) ([]domain.PageRef, string, error) {
	s.searches = append(s.searches, query)
	return s.refs, s.next, nil
}

func (s *referenceStore) ResolveShortPageLink(_ context.Context, path string) (string, error) {
	s.shortPaths = append(s.shortPaths, path)
	return s.finalURL, nil
}

func TestResolveConfluenceDirectPageReferences(t *testing.T) {
	store := &referenceStore{}
	service := &ConfluenceService{store: store, baseURL: "https://docs.example.test/wiki"}
	for _, tc := range []struct {
		ref  string
		id   string
		kind string
	}{
		{"42", "42", "id"},
		{"opaque-id", "opaque-id", "id"},
		{"https://docs.example.test/wiki/spaces/ENG/pages/42/Page+Title", "42", "canonical"},
		{"https://docs.example.test/wiki/pages/viewpage.action?pageId=42#section", "42", "viewpage"},
		{"/pages/viewpage.action?pageId=42", "42", "viewpage"},
		{"https://docs.example.test/wiki/rest/api/content/42", "42", "rest"},
	} {
		result, err := service.ResolvePageReference(context.Background(), tc.ref)
		if err != nil || result.ID != tc.id || result.Kind != tc.kind || result.NetworkRequests != 0 {
			t.Errorf("ResolvePageReference(%q)=%+v err=%v", tc.ref, result, err)
		}
	}
	if len(store.searches) != 0 || len(store.shortPaths) != 0 {
		t.Fatalf("direct references made network calls: %+v %+v", store.searches, store.shortPaths)
	}
}

func TestResolveConfluenceReferenceRejectsUnsafeForms(t *testing.T) {
	service := &ConfluenceService{store: &referenceStore{}, baseURL: "https://docs.example.test/wiki"}
	for _, ref := range []string{
		"https://foreign.example.test/wiki/pages/viewpage.action?pageId=42",
		"http://docs.example.test/wiki/pages/viewpage.action?pageId=42",
		"https://user@docs.example.test/wiki/pages/viewpage.action?pageId=42",
		"https://docs.example.test/other/pages/viewpage.action?pageId=42",
		"https://docs.example.test/wiki/pages/viewpage.action?pageId=42&pageId=43",
		"//foreign.example.test/x/AwAG",
		"relative/path",
	} {
		if _, err := service.ResolvePageReference(context.Background(), ref); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("reference %q err=%v, want ErrUsage", ref, err)
		}
	}
}

func TestResolveConfluenceDisplayReferenceExactAndAmbiguous(t *testing.T) {
	store := &referenceStore{refs: []domain.PageRef{{ID: "42"}}}
	service := &ConfluenceService{store: store, baseURL: "https://docs.example.test/wiki"}
	result, err := service.ResolvePageReference(context.Background(), "https://docs.example.test/wiki/display/ENG/Delivery+Notes")
	if err != nil || result.ID != "42" || result.Kind != "display" || result.NetworkRequests != 1 || result.Space != "ENG" || result.Title != "Delivery Notes" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(store.searches) != 1 || !strings.Contains(store.searches[0], `space = "ENG"`) || !strings.Contains(store.searches[0], `title = "Delivery Notes"`) {
		t.Fatalf("searches=%v", store.searches)
	}

	store.refs = []domain.PageRef{{ID: "42"}, {ID: "43"}}
	if _, err := service.ResolvePageReference(context.Background(), "/display/ENG/Delivery+Notes"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("ambiguous err=%v", err)
	}
	store.refs = nil
	if _, err := service.ResolvePageReference(context.Background(), "/display/ENG/Missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
}

func TestResolveConfluenceShortReferenceIsBounded(t *testing.T) {
	store := &referenceStore{finalURL: "https://docs.example.test/wiki/spaces/ENG/pages/42/Page"}
	service := &ConfluenceService{store: store, baseURL: "https://docs.example.test/wiki"}
	result, err := service.ResolvePageReference(context.Background(), "/x/AwAG")
	if err != nil || result.ID != "42" || result.Kind != "short" || result.Via != "canonical" || result.NetworkRequests != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(store.shortPaths) != 1 || store.shortPaths[0] != "/x/AwAG" {
		t.Fatalf("short paths=%v", store.shortPaths)
	}

	store.finalURL = "https://docs.example.test/wiki/x/Again"
	if _, err := service.ResolvePageReference(context.Background(), "/x/AwAG"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("nested short err=%v", err)
	}
	store.finalURL = "https://foreign.example.test/wiki/spaces/ENG/pages/42/Page"
	if _, err := service.ResolvePageReference(context.Background(), "/x/AwAG"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("foreign final err=%v", err)
	}
}
