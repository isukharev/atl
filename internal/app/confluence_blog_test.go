package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type blogPostStoreStub struct {
	domain.DocStore
	created *domain.Resource
	err     error
	calls   int
}

func (s *blogPostStoreStub) CreateBlogPost(context.Context, string, string, []byte) (*domain.Resource, error) {
	s.calls++
	return s.created, s.err
}

func TestCreateBlogPostRequiresVerifiedNativeResponse(t *testing.T) {
	valid := &domain.Resource{
		ID: "900", Type: "blogpost", Title: "Release notes", SpaceKey: "DOC",
		Version: 1, Body: []byte("<p>body</p>"), BodyPresent: true,
	}
	store := &blogPostStoreStub{created: valid}
	created, err := (&ConfluenceService{store: store}).CreateBlogPost(context.Background(), " DOC ", " Release notes ", []byte("<p>body</p>"))
	if err != nil || created != valid || store.calls != 1 {
		t.Fatalf("created=%+v calls=%d err=%v", created, store.calls, err)
	}

	invalid := []*domain.Resource{
		nil,
		{Type: "blogpost", Title: "Release notes", SpaceKey: "DOC", Version: 1, BodyPresent: true},
		{ID: "900", Type: "page", Title: "Release notes", SpaceKey: "DOC", Version: 1, BodyPresent: true},
		{ID: "900", Type: "blogpost", Title: "release notes", SpaceKey: "DOC", Version: 1, BodyPresent: true},
		{ID: "900", Type: "blogpost", Title: "Other", SpaceKey: "DOC", Version: 1, BodyPresent: true},
		{ID: "900", Type: "blogpost", Title: "Release notes", SpaceKey: "doc", Version: 1, BodyPresent: true},
		{ID: "900", Type: "blogpost", Title: "Release notes", SpaceKey: "OTHER", Version: 1, BodyPresent: true},
		{ID: "900", Type: "blogpost", Title: "Release notes", SpaceKey: "DOC", BodyPresent: true},
		{ID: "900", Type: "blogpost", Title: "Release notes", SpaceKey: "DOC", Version: 1},
	}
	for i, response := range invalid {
		store := &blogPostStoreStub{created: response}
		_, err := (&ConfluenceService{store: store}).CreateBlogPost(context.Background(), "DOC", "Release notes", []byte("<p>body</p>"))
		if !errors.Is(err, domain.ErrCheckFailed) || store.calls != 1 {
			t.Errorf("case %d response=%+v calls=%d err=%v", i, response, store.calls, err)
		}
	}
	normalized := &blogPostStoreStub{created: &domain.Resource{
		ID: "900", Type: "blogpost", Title: "Cafe\u0301", SpaceKey: "DOC", Version: 1, BodyPresent: true,
	}}
	if _, err := (&ConfluenceService{store: normalized}).CreateBlogPost(context.Background(), "DOC", "Café", []byte("<p>body</p>")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("Unicode-normalized response err=%v", err)
	}
}

func TestCreateBlogPostRejectsInputAndUnsupportedBackendBeforeWrite(t *testing.T) {
	store := &blogPostStoreStub{created: &domain.Resource{}}
	service := &ConfluenceService{store: store}
	for _, input := range []struct {
		space string
		title string
		body  []byte
	}{
		{"", "T", []byte("<p>x</p>")},
		{"DOC", "", []byte("<p>x</p>")},
		{"DOC", "T", nil},
		{"DOC", "T", []byte(" \n\t")},
		{"DOC", "T", []byte("<p>broken")},
	} {
		if _, err := service.CreateBlogPost(context.Background(), input.space, input.title, input.body); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("input=%+v err=%v", input, err)
		}
	}
	if store.calls != 0 {
		t.Fatalf("invalid input reached creator %d times", store.calls)
	}

	unsupported := &ConfluenceService{store: &recordingStore{}}
	if _, err := unsupported.CreateBlogPost(context.Background(), "DOC", "T", []byte("<p>x</p>")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unsupported error=%v", err)
	}
}

type blogStatusError int

func (e blogStatusError) Error() string   { return "blog create rejected" }
func (e blogStatusError) HTTPStatus() int { return int(e) }

func TestCreateBlogPostClassifiesAmbiguousWriteWithoutReplay(t *testing.T) {
	store := &blogPostStoreStub{err: errors.New("connection lost")}
	_, err := (&ConfluenceService{store: store}).CreateBlogPost(context.Background(), "DOC", "T", []byte("<p>x</p>"))
	if !errors.Is(err, domain.ErrCheckFailed) || store.calls != 1 {
		t.Fatalf("ambiguous calls=%d err=%v", store.calls, err)
	}

	store = &blogPostStoreStub{err: blogStatusError(403)}
	_, err = (&ConfluenceService{store: store}).CreateBlogPost(context.Background(), "DOC", "T", []byte("<p>x</p>"))
	if errors.Is(err, domain.ErrCheckFailed) || err == nil || store.calls != 1 {
		t.Fatalf("definitive calls=%d err=%v", store.calls, err)
	}
}
