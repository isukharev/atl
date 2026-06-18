package fragment

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

type fakeResolver struct{ data []byte }

func (f fakeResolver) Resolve(_ context.Context, _ *domain.Resource, ref domain.Ref) ([]byte, string, error) {
	if ref.Kind == domain.RefDrawio {
		return f.data, ref.Key + ".png", nil
	}
	return nil, "", domain.ErrNotFound
}

type fakeSink struct{ put map[string][]byte }

func (s *fakeSink) Put(name string, data []byte) (string, error) {
	if s.put == nil {
		s.put = map[string][]byte{}
	}
	s.put[name] = data
	return "assets/" + name, nil
}

func TestExtractDedup(t *testing.T) {
	body := `<p><ri:user ri:userkey="K1"/> and <ri:user ri:userkey="K1"/> and <ri:user ri:userkey="K2"/></p>`
	root, _ := csf.Parse([]byte(body))
	refs := Extract(root)
	users := 0
	for _, r := range refs {
		if r.Kind == domain.RefUser {
			users++
		}
	}
	if users != 2 {
		t.Errorf("expected 2 distinct users, got %d", users)
	}
}

func TestResolveAssetAndUser(t *testing.T) {
	body := `<ac:structured-macro ac:name="drawio"><ac:parameter ac:name="diagramName">d</ac:parameter><ac:parameter ac:name="revision">4</ac:parameter></ac:structured-macro><p><ri:user ri:userkey="K1"/></p>`
	root, _ := csf.Parse([]byte(body))
	refs := Extract(root)
	sink := &fakeSink{}
	deps := Deps{
		Assets:   sink,
		Resolver: fakeResolver{data: []byte("PNGBYTES")},
		Users: func(_ context.Context, key string) (string, error) {
			if key == "K1" {
				return "Alice", nil
			}
			return "", errors.New("unknown")
		},
	}
	refs = Resolve(context.Background(), &domain.Resource{ID: "1"}, refs, deps)
	var drawio, user *domain.Ref
	for i := range refs {
		switch refs[i].Kind {
		case domain.RefDrawio:
			drawio = &refs[i]
		case domain.RefUser:
			user = &refs[i]
		}
	}
	if drawio == nil || drawio.Asset != "assets/d.png" {
		t.Errorf("drawio asset not set: %+v", drawio)
	}
	if string(sink.put["d.png"]) != "PNGBYTES" {
		t.Error("asset bytes not stored")
	}
	if user == nil || user.Display != "Alice" {
		t.Errorf("user not resolved: %+v", user)
	}
}

func TestResolveDegradesGracefully(t *testing.T) {
	body := `<ac:structured-macro ac:name="drawio"><ac:parameter ac:name="diagramName">d</ac:parameter></ac:structured-macro>`
	root, _ := csf.Parse([]byte(body))
	refs := Extract(root)
	// No resolver/sink/users → must not panic, just leave fields empty.
	refs = Resolve(context.Background(), &domain.Resource{ID: "1"}, refs, Deps{})
	if refs[0].Asset != "" {
		t.Error("expected no asset without resolver")
	}
}
