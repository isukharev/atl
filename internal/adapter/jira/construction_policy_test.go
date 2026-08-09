package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestConstructionOwnsGenericConflictAndWriteClearancePolicy(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errorMessages":["locked"]}`))
			return
		}
		writes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	adapter := New(server.URL, "token", "test")
	_, conflictErr := adapter.c.Do(context.Background(), http.MethodGet, "/conflict", nil, nil)
	if conflictErr == nil || errors.Is(conflictErr, domain.ErrVersionConflict) {
		t.Fatalf("409 error = %v, want generic API conflict", conflictErr)
	}
	if _, err := adapter.c.Do(context.Background(), http.MethodPost, "/write", []byte(`{}`), nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("uncleared write error = %v", err)
	}
	if got := writes.Load(); got != 0 {
		t.Fatalf("uncleared transport writes = %d", got)
	}
}
