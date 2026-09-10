package brokerserver

import (
	"encoding/pem"
	"net/http/httptest"
	"testing"

	"github.com/isukharev/atl/internal/httpx"
)

// Keep synthetic peer trust independent of the Unix process-test helpers.
func projectPageProcessTLSOptions(t *testing.T, server *httptest.Server) httpx.TLSOptions {
	t.Helper()
	options, _, err := httpx.QualifiedTLSOptionsBytes(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	return options
}
