package brokerserver

import (
	"bytes"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestCredentialGuardClonesClearsAndChecksDecodedValues(t *testing.T) {
	original := []byte("synthetic-configured-secret")
	guard, err := NewCredentialGuard(original)
	if err != nil {
		t.Fatal(err)
	}
	retained := guard.credentials[0]
	clear(original)
	result := app.BrokerExactReadResult{JiraIssue: &domain.BrokerJiraIssueReadResult{Fields: []domain.BrokerJiraIssueReadField{{Value: "prefix synthetic-configured-secret suffix"}}}}
	if err := guard.Check(result, []byte(`{"value":"unrelated"}`), nil); err == nil {
		t.Fatal("decoded credential was not detected")
	}
	guard.Close()
	if len(guard.credentials) != 0 {
		t.Fatal("guard retained credential references after close")
	}
	if !bytes.Equal(original, make([]byte, len(original))) || !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("credential bytes were not cleared")
	}
}

func TestCredentialGuardChecksConfluenceMetadataAndWireEscaping(t *testing.T) {
	credential := []byte("synthetic-secret\\\"<")
	guard, err := NewCredentialGuard(credential)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	page := domain.BrokerConfluencePageReadResult{
		SchemaVersion:   1,
		ArgumentsSHA256: strings.Repeat("a", 64),
		PageID:          "42",
		Type:            "page",
		Space:           string(credential),
		Version:         1,
		Title:           "Synthetic page",
		Updated:         "2026-09-08T10:00:00Z",
		Projection:      domain.BrokerConfluenceProjectionMetadata,
		Complete:        true,
	}
	wire, err := brokercontract.EncodeConfluencePageReadResultV1(page)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Check(app.BrokerExactReadResult{ConfluencePage: &page}, wire, nil); err == nil {
		t.Fatal("credential in Confluence metadata was not detected")
	}
	if err := guard.Check(app.BrokerExactReadResult{}, wire, nil); err == nil {
		t.Fatal("credential escaped by the result encoder was not detected")
	}
}
