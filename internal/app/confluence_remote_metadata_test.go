package app

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type confluenceMetadataBatchStore struct {
	domain.DocStore
	batchSize     int
	requests      [][]string
	exactCalls    []string
	singleAttempt []bool
	redactedTrace []bool
	plan          func([]string) ([][]string, error)
	read          func([]string, int) (domain.ConfluencePageMetadataBatch, error)
	exact         map[string]*domain.PageMeta
}

func (s *confluenceMetadataBatchStore) PlanPageMetadataBatches(ids []string) ([][]string, error) {
	if s.plan != nil {
		return s.plan(ids)
	}
	size := s.batchSize
	if size <= 0 {
		size = 100
	}
	var batches [][]string
	for start := 0; start < len(ids); start += size {
		end := min(start+size, len(ids))
		batches = append(batches, append([]string(nil), ids[start:end]...))
	}
	return batches, nil
}

func (s *confluenceMetadataBatchStore) ReadPageMetadataBatch(ctx context.Context, ids []string) (domain.ConfluencePageMetadataBatch, error) {
	s.requests = append(s.requests, append([]string(nil), ids...))
	s.singleAttempt = append(s.singleAttempt, domain.SingleAttempt(ctx))
	s.redactedTrace = append(s.redactedTrace, domain.RedactedHTTPTrace(ctx))
	if s.read != nil {
		return s.read(ids, len(s.requests)-1)
	}
	results := make([]domain.PageRef, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		results = append(results, domain.PageRef{ID: ids[i], Version: 3})
	}
	return domain.ConfluencePageMetadataBatch{Results: results, Complete: true}, nil
}

func (s *confluenceMetadataBatchStore) GetMeta(_ context.Context, id string) (*domain.PageMeta, error) {
	s.exactCalls = append(s.exactCalls, id)
	return s.exact[id], nil
}

func TestReadConfluenceRemoteMetadataBatchesContinuesAfterFailedBatch(t *testing.T) {
	for name, batchErr := range map[string]error{"forbidden": domain.ErrForbidden, "not found": domain.ErrNotFound} {
		t.Run(name, func(t *testing.T) {
			store := &confluenceMetadataBatchStore{batchSize: 2}
			store.read = func(ids []string, call int) (domain.ConfluencePageMetadataBatch, error) {
				if call == 0 {
					return domain.ConfluencePageMetadataBatch{}, batchErr
				}
				return domain.ConfluencePageMetadataBatch{Complete: true, Results: []domain.PageRef{
					{ID: ids[1], Version: 4}, {ID: ids[0], Version: 3},
				}}, nil
			}
			got := readConfluenceRemoteMetadataBatches(context.Background(), store, []string{"1", "2", "3", "4"})
			if got["1"].reason != confluenceRemoteEvidenceIncomplete || got["2"].reason != confluenceRemoteEvidenceIncomplete ||
				!got["3"].available || got["3"].version != 3 || !got["4"].available || got["4"].version != 4 || len(store.requests) != 2 {
				t.Fatalf("evidence=%+v requests=%v", got, store.requests)
			}
		})
	}
}

func TestReadConfluenceRemoteMetadataBatchesInvalidatesWholeMalformedBatch(t *testing.T) {
	tests := map[string]domain.ConfluencePageMetadataBatch{
		"omitted":                     {Complete: true, Results: []domain.PageRef{{ID: "1", Version: 1}}},
		"duplicate":                   {Complete: true, Results: []domain.PageRef{{ID: "1", Version: 1}, {ID: "1", Version: 2}}},
		"unexpected":                  {Complete: true, Results: []domain.PageRef{{ID: "1", Version: 1}, {ID: "9", Version: 2}}},
		"zero version":                {Complete: true, Results: []domain.PageRef{{ID: "1", Version: 1}, {ID: "2"}}},
		"partial":                     {Results: []domain.PageRef{{ID: "1", Version: 1}, {ID: "2", Version: 2}}, PartialReason: domain.ConfluencePageMetadataPartialPaginationUnqualified},
		"contradictory qualification": {Complete: true, Results: []domain.PageRef{{ID: "1", Version: 1}, {ID: "2", Version: 2}}, PartialReason: domain.ConfluencePageMetadataPartialPaginationUnqualified},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			store := &confluenceMetadataBatchStore{read: func([]string, int) (domain.ConfluencePageMetadataBatch, error) { return response, nil }}
			got := readConfluenceRemoteMetadataBatches(context.Background(), store, []string{"1", "2"})
			for _, id := range []string{"1", "2"} {
				if got[id].available || got[id].reason != confluenceRemoteEvidenceIncomplete {
					t.Fatalf("%s evidence=%+v", id, got[id])
				}
			}
		})
	}
}

func TestReadConfluenceRemoteMetadataBatchesPreservesCanonicalPlan(t *testing.T) {
	store := &confluenceMetadataBatchStore{}
	store.batchSize = 2
	// Pin the planner contract independently of response ordering.
	batches, _ := store.PlanPageMetadataBatches([]string{"3", "1", "2"})
	if !reflect.DeepEqual(batches, [][]string{{"3", "1"}, {"2"}}) {
		t.Fatalf("batches=%v", batches)
	}
	store.read = func(ids []string, _ int) (domain.ConfluencePageMetadataBatch, error) {
		results := make([]domain.PageRef, 0, len(ids))
		for i := len(ids) - 1; i >= 0; i-- {
			results = append(results, domain.PageRef{ID: ids[i], Version: 1})
		}
		return domain.ConfluencePageMetadataBatch{Complete: true, Results: results}, nil
	}
	got := readConfluenceRemoteMetadataBatches(context.Background(), store, []string{"3", "1", "2"})
	if !got["3"].available || !got["1"].available || !got["2"].available {
		t.Fatalf("evidence=%+v", got)
	}
}

func TestReadConfluenceRemoteMetadataBatchesRejectsNonCanonicalPlan(t *testing.T) {
	store := &confluenceMetadataBatchStore{plan: func([]string) ([][]string, error) {
		return [][]string{{"2", "1"}}, nil
	}}
	got := readConfluenceRemoteMetadataBatches(context.Background(), store, []string{"1", "2"})
	if len(store.requests) != 0 || got["1"].reason != confluenceRemoteEvidenceIncomplete || got["2"].reason != confluenceRemoteEvidenceIncomplete {
		t.Fatalf("requests=%v evidence=%+v", store.requests, got)
	}
}

func TestConfluenceRemoteMetadataBatchStoreLegacyExactPathForSingleIdentity(t *testing.T) {
	root := t.TempDir()
	writeDiffPage(t, root, "1", "only", `<p>body</p>`)
	bindConfluenceTestMirror(t, root)
	store := &confluenceMetadataBatchStore{exact: map[string]*domain.PageMeta{"1": {ID: "1", Version: 3}}}
	got, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).SnapshotMirror(context.Background(), root, true)
	if err != nil || !got.Complete || len(store.exactCalls) != 1 || len(store.requests) != 0 {
		t.Fatalf("snapshot=%+v exact=%v bulk=%v err=%v", got, store.exactCalls, store.requests, err)
	}
	entries, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Status(context.Background(), root, true)
	if err != nil || len(entries) != 1 || entries[0].RemoteVersion != 3 || len(store.exactCalls) != 2 || len(store.requests) != 0 {
		t.Fatalf("status=%+v exact=%v bulk=%v err=%v", entries, store.exactCalls, store.requests, err)
	}
}

func TestConfluenceStatusProjectsBulkMetadataInCanonicalLocalOrder(t *testing.T) {
	root := t.TempDir()
	for _, page := range []struct{ id, slug string }{{"3", "a"}, {"1", "b"}, {"2", "c"}} {
		writeDiffPage(t, root, page.id, page.slug, `<p>body</p>`)
	}
	bindConfluenceTestMirror(t, root)
	store := &confluenceMetadataBatchStore{read: func(ids []string, _ int) (domain.ConfluencePageMetadataBatch, error) {
		results := make([]domain.PageRef, 0, len(ids))
		for i := len(ids) - 1; i >= 0; i-- {
			version := 3
			if ids[i] == "1" {
				version = 4
			}
			results = append(results, domain.PageRef{ID: ids[i], Version: version})
		}
		return domain.ConfluencePageMetadataBatch{Results: results, Complete: true}, nil
	}}
	entries, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Status(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].ID != "3" || entries[1].ID != "1" || entries[2].ID != "2" ||
		len(store.requests) != 1 || !reflect.DeepEqual(store.requests[0], []string{"3", "1", "2"}) || len(store.exactCalls) != 0 {
		t.Fatalf("entries=%+v requests=%v exact=%v", entries, store.requests, store.exactCalls)
	}
	for i, entry := range entries {
		wantVersion := 3
		wantDrift := false
		if entry.ID == "1" {
			wantVersion = 4
			wantDrift = true
		}
		if entry.RemoteVersion != wantVersion || entry.Drifted != wantDrift || entry.RemoteError != "" {
			t.Fatalf("entry[%d]=%+v", i, entry)
		}
	}
}

func TestConfluenceSnapshotSplitsMoreThanOneHundredRemoteIdentities(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("%03d", i+1)
		writeDiffPage(t, root, id, id, `<p>body</p>`)
	}
	bindConfluenceTestMirror(t, root)
	store := &confluenceMetadataBatchStore{}
	got, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).SnapshotMirror(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	requestSizes := make([]int, len(store.requests))
	for i := range store.requests {
		requestSizes[i] = len(store.requests[i])
	}
	if len(store.requests) != 2 || len(store.requests[0]) != 100 || len(store.requests[1]) != 1 || len(store.exactCalls) != 0 ||
		got.Remote.Attempted != 101 || got.Remote.Checked != 101 || got.Remote.InSync != 101 || got.Remote.Unavailable != 0 || !got.Complete {
		t.Fatalf("requests=%d sizes=%v snapshot=%+v", len(store.requests), requestSizes, got)
	}
	for i, single := range store.singleAttempt {
		if !single {
			t.Fatalf("batch request %d lacked single-attempt policy", i)
		}
		if !store.redactedTrace[i] {
			t.Fatalf("batch request %d lacked redacted-trace policy", i)
		}
	}
}

var _ domain.QualifiedConfluencePageMetadataBatchReader = (*confluenceMetadataBatchStore)(nil)
