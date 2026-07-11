package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type moveStore struct {
	domain.DocStore
	reads       []*domain.Resource
	readErrs    []error
	readIDs     []string
	getCalls    int
	moveCalls   int
	movedID     string
	movedParent string
	movedExpect int
	movedTitle  string
	movedBody   []byte
	moveErr     error
}

func (s *moveStore) GetPage(_ context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	i := s.getCalls
	s.getCalls++
	s.readIDs = append(s.readIDs, id)
	if i < len(s.readErrs) && s.readErrs[i] != nil {
		return nil, s.readErrs[i]
	}
	if i >= len(s.reads) {
		return nil, errors.New("unexpected read")
	}
	return s.reads[i], nil
}

func (s *moveStore) MovePage(_ context.Context, id, parent string, expect int, title string, body []byte) (int, error) {
	s.moveCalls++
	s.movedID, s.movedParent, s.movedExpect, s.movedTitle = id, parent, expect, title
	s.movedBody = append([]byte(nil), body...)
	if s.moveErr != nil {
		return 0, s.moveErr
	}
	return expect + 1, nil
}

func movePage(id, parent string, version int, body string) *domain.Resource {
	p := &domain.Resource{ID: id, Type: "page", Title: "Page " + id, SpaceKey: "S", Version: version, Body: []byte(body), BodyPresent: true, AncestorsPresent: true}
	if parent != "" {
		p.Parent = parent
		p.Ancestors = []string{"Parent " + parent}
		p.AncestorIDs = []string{parent}
	}
	return p
}

func moveTarget(id string, version int, ancestorIDs ...string) *domain.Resource {
	p := &domain.Resource{ID: id, Type: "page", Title: "Target", SpaceKey: "S", Version: version, AncestorsPresent: true}
	for _, ancestorID := range ancestorIDs {
		p.Ancestors = append(p.Ancestors, "Ancestor "+ancestorID)
		p.AncestorIDs = append(p.AncestorIDs, ancestorID)
	}
	if len(ancestorIDs) > 0 {
		p.Parent = ancestorIDs[len(ancestorIDs)-1]
	}
	return p
}

func movedPage(id, parent string, version int, body string, ancestors ...string) *domain.Resource {
	p := movePage(id, "", version, body)
	p.Parent = parent
	for _, ancestorID := range ancestors {
		p.Ancestors = append(p.Ancestors, "Ancestor "+ancestorID)
		p.AncestorIDs = append(p.AncestorIDs, ancestorID)
	}
	return p
}

func TestConfluenceMoveDryRunAndApply(t *testing.T) {
	previewStore := &moveStore{reads: []*domain.Resource{movePage("42", "10", 7, "body"), moveTarget("20", 3, "10")}}
	preview, err := (&ConfluenceService{store: previewStore}).MoveGuarded(context.Background(), "42", ConfluenceMoveOpts{Parent: "20"})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != "would_apply" || preview.CurrentParent != "10" || preview.Parent != "20" || preview.CurrentVersion != 7 || preview.TargetVersion != 3 || preview.ProposalHash == "" || previewStore.moveCalls != 0 {
		t.Fatalf("preview=%+v store=%+v", preview, previewStore)
	}

	applyStore := &moveStore{reads: []*domain.Resource{
		movePage("42", "10", 7, "body"), moveTarget("20", 3, "10"), movedPage("42", "20", 8, "body", "10", "20"),
	}}
	result, err := (&ConfluenceService{store: applyStore}).MoveGuarded(context.Background(), "42", ConfluenceMoveOpts{
		Parent: "20", ExpectedVersion: 7, ExpectedParent: "10", ExpectedParentSet: true,
		ExpectedProposalHash: preview.ProposalHash, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.FinalVersion != 8 || applyStore.moveCalls != 1 || applyStore.movedExpect != 7 || applyStore.movedTitle != "Page 42" || string(applyStore.movedBody) != "body" {
		t.Fatalf("result=%+v store=%+v", result, applyStore)
	}
}

func TestConfluenceMoveFailsClosedBeforeWrite(t *testing.T) {
	tests := []struct {
		name   string
		reads  []*domain.Resource
		opts   ConfluenceMoveOpts
		status string
	}{
		{name: "missing source body", reads: []*domain.Resource{{ID: "42", Type: "page", Title: "Page", SpaceKey: "S", Version: 7, AncestorsPresent: true}}, opts: ConfluenceMoveOpts{Parent: "20"}},
		{name: "missing source identity", reads: []*domain.Resource{{Type: "page", Title: "Page", SpaceKey: "S", Version: 7, BodyPresent: true, AncestorsPresent: true}}, opts: ConfluenceMoveOpts{Parent: "20"}},
		{name: "missing source title", reads: []*domain.Resource{{ID: "42", Type: "page", SpaceKey: "S", Version: 7, BodyPresent: true, AncestorsPresent: true}}, opts: ConfluenceMoveOpts{Parent: "20"}},
		{name: "missing source type", reads: []*domain.Resource{{ID: "42", Title: "Page", SpaceKey: "S", Version: 7, BodyPresent: true, AncestorsPresent: true}}, opts: ConfluenceMoveOpts{Parent: "20"}},
		{name: "omitted source hierarchy", reads: []*domain.Resource{{ID: "42", Type: "page", Title: "Page", SpaceKey: "S", Version: 7, BodyPresent: true}}, opts: ConfluenceMoveOpts{Parent: "20"}},
		{name: "incomplete source hierarchy", reads: []*domain.Resource{{ID: "42", Type: "page", Title: "Page", SpaceKey: "S", Version: 7, BodyPresent: true, AncestorsPresent: true, Ancestors: []string{"x"}}}, opts: ConfluenceMoveOpts{Parent: "20"}},
		{name: "omitted target hierarchy", reads: []*domain.Resource{movePage("42", "10", 7, "body"), {ID: "20", Type: "page", Title: "Target", SpaceKey: "S", Version: 3}}, opts: ConfluenceMoveOpts{Parent: "20"}},
		{name: "descendant cycle", reads: []*domain.Resource{movePage("42", "10", 7, "body"), moveTarget("20", 3, "10", "42")}, opts: ConfluenceMoveOpts{Parent: "20"}},
		{name: "cross space", reads: []*domain.Resource{movePage("42", "10", 7, "body"), func() *domain.Resource { p := moveTarget("20", 3); p.SpaceKey = "OTHER"; return p }()}, opts: ConfluenceMoveOpts{Parent: "20"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &moveStore{reads: tt.reads}
			_, err := (&ConfluenceService{store: store}).MoveGuarded(context.Background(), "42", tt.opts)
			if !errors.Is(err, domain.ErrCheckFailed) || store.moveCalls != 0 {
				t.Fatalf("err=%v moves=%d", err, store.moveCalls)
			}
		})
	}
}

func TestConfluenceMoveExplicitTopLevelParentGate(t *testing.T) {
	hash := confluenceMoveProposalHash("42", 7, "", "20")
	store := &moveStore{reads: []*domain.Resource{
		movePage("42", "", 7, "body"), moveTarget("20", 3), movedPage("42", "20", 8, "body", "20"),
	}}
	res, err := (&ConfluenceService{store: store}).MoveGuarded(context.Background(), "42", ConfluenceMoveOpts{
		Parent: "20", ExpectedVersion: 7, ExpectedParentSet: true, ExpectedProposalHash: hash, Apply: true,
	})
	if err != nil || res.Status != "applied" || store.moveCalls != 1 {
		t.Fatalf("res=%+v err=%v store=%+v", res, err, store)
	}
}

func TestConfluenceMoveApplyGates(t *testing.T) {
	baseReads := func() []*domain.Resource {
		return []*domain.Resource{movePage("42", "10", 7, "body"), moveTarget("20", 3, "10")}
	}
	hash := confluenceMoveProposalHash("42", 7, "10", "20")
	for _, tt := range []struct {
		name string
		opts ConfluenceMoveOpts
	}{
		{name: "stale version", opts: ConfluenceMoveOpts{Parent: "20", ExpectedVersion: 6, ExpectedParent: "10", ExpectedParentSet: true, ExpectedProposalHash: hash, Apply: true}},
		{name: "stale parent", opts: ConfluenceMoveOpts{Parent: "20", ExpectedVersion: 7, ExpectedParent: "11", ExpectedParentSet: true, ExpectedProposalHash: hash, Apply: true}},
		{name: "changed proposal", opts: ConfluenceMoveOpts{Parent: "20", ExpectedVersion: 7, ExpectedParent: "10", ExpectedParentSet: true, ExpectedProposalHash: "other", Apply: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &moveStore{reads: baseReads()}
			res, err := (&ConfluenceService{store: store}).MoveGuarded(context.Background(), "42", tt.opts)
			if !errors.Is(err, domain.ErrCheckFailed) || res.Status != "blocked" || store.moveCalls != 0 {
				t.Fatalf("res=%+v err=%v moves=%d", res, err, store.moveCalls)
			}
		})
	}
}

func TestConfluenceMoveAmbiguousWriteReconcilesWithoutReplay(t *testing.T) {
	hash := confluenceMoveProposalHash("42", 7, "10", "20")
	store := &moveStore{
		reads:   []*domain.Resource{movePage("42", "10", 7, "body"), moveTarget("20", 3, "10"), movedPage("42", "20", 8, "body", "10", "20")},
		moveErr: errors.New("connection reset after write"),
	}
	res, err := (&ConfluenceService{store: store}).MoveGuarded(context.Background(), "42", ConfluenceMoveOpts{
		Parent: "20", ExpectedVersion: 7, ExpectedParent: "10", ExpectedParentSet: true, ExpectedProposalHash: hash, Apply: true,
	})
	if err != nil || res.Status != "applied" || !res.Reconciled || store.moveCalls != 1 || store.getCalls != 3 {
		t.Fatalf("res=%+v err=%v store=%+v", res, err, store)
	}
}

func TestConfluenceMoveUnknownAndDefinitiveRejection(t *testing.T) {
	hash := confluenceMoveProposalHash("42", 7, "10", "20")
	opts := ConfluenceMoveOpts{Parent: "20", ExpectedVersion: 7, ExpectedParent: "10", ExpectedParentSet: true, ExpectedProposalHash: hash, Apply: true}
	t.Run("verification mismatch", func(t *testing.T) {
		store := &moveStore{reads: []*domain.Resource{movePage("42", "10", 7, "body"), moveTarget("20", 3, "10"), movedPage("42", "20", 8, "changed", "10", "20")}}
		res, err := (&ConfluenceService{store: store}).MoveGuarded(context.Background(), "42", opts)
		if err == nil || res.Status != "unknown" || store.moveCalls != 1 {
			t.Fatalf("res=%+v err=%v store=%+v", res, err, store)
		}
	})
	t.Run("definitive rejection", func(t *testing.T) {
		store := &moveStore{reads: []*domain.Resource{movePage("42", "10", 7, "body"), moveTarget("20", 3, "10")}, moveErr: titleHTTPError{status: 409}}
		res, err := (&ConfluenceService{store: store}).MoveGuarded(context.Background(), "42", opts)
		if err == nil || res.Status != "failed" || store.moveCalls != 1 || store.getCalls != 2 {
			t.Fatalf("res=%+v err=%v store=%+v", res, err, store)
		}
	})
}
