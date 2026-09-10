package jira

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func newBrokerAttachmentTLSJira(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Jira) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	tlsOptions, _, err := httpx.QualifiedTLSOptionsBytes(bundle)
	if err != nil {
		t.Fatal(err)
	}
	jira, err := NewWithSchedulerTLS(server.URL+"/jira", "attachment-token", "test", nil, tlsOptions)
	if err != nil {
		t.Fatal(err)
	}
	return server, jira
}

func brokerAttachmentContext(t *testing.T, attempts int, responseBytes int64) (context.Context, context.CancelFunc, *domain.ReadBudget) {
	t.Helper()
	budget, err := domain.NewReadBudget(attempts, responseBytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	return domain.WithReadBudget(ctx, budget), cancel, budget
}

func assertBrokerAttachmentTextSafe(t *testing.T, value string, forbidden ...string) {
	t.Helper()
	for _, current := range forbidden {
		if current != "" && strings.Contains(value, current) {
			t.Fatalf("formatted value exposed private content: %q", value)
		}
	}
}

func brokerAttachmentFormats(value any) []string {
	return []string{
		fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value),
		fmt.Sprintf("%s", value), fmt.Sprintf("%q", value), fmt.Sprintf("%x", value), fmt.Sprintf("%X", value), fmt.Sprintf("%p", value),
	}
}
