package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type verifyStub struct {
	name string
	err  error
}

func (s verifyStub) Whoami(context.Context) (string, error) {
	return s.name, s.err
}

func TestVerifyServicesDelegateToPort(t *testing.T) {
	for _, test := range []struct {
		name   string
		verify func(context.Context, domain.Verifier) (string, error)
	}{
		{name: "confluence", verify: VerifyConfluence},
		{name: "jira", verify: VerifyJira},
	} {
		t.Run(test.name, func(t *testing.T) {
			name, err := test.verify(context.Background(), verifyStub{name: "Jane Doe"})
			if err != nil || name != "Jane Doe" {
				t.Fatalf("name=%q err=%v", name, err)
			}
			sentinel := errors.New("verify failed")
			if _, err := test.verify(context.Background(), verifyStub{err: sentinel}); !errors.Is(err, sentinel) {
				t.Fatalf("error=%v, want sentinel", err)
			}
		})
	}
}

func TestVerifyServicesRejectMissingPort(t *testing.T) {
	if _, err := VerifyConfluence(context.Background(), nil); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("Confluence error=%v, want ErrConfig", err)
	}
	if _, err := VerifyJira(context.Background(), nil); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("Jira error=%v, want ErrConfig", err)
	}
}
