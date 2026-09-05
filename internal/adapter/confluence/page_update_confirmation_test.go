package confluence

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestUpdatePageQualifiesAcknowledgement(t *testing.T) {
	for _, response := range []string{"", "{}", "null", "{", `{"version":{"number":0}}`, `{"version":{"number":-1}}`, `{"version":{"number":5}}`, `{"version":{"number":"4"}}`, `{"id":"999","version":{"number":4}}`, `{"version":{"number":1,"number":4}}`, `{"version":{"number":3,"Number":4}}`, `{"version":{"Number":4}}`, `{"version":{"number":3},"Version":{"number":4}}`} {
		t.Run(response, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPut {
					t.Errorf("unexpected method %s", r.Method)
				}
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			adapter := New(server.URL, "synthetic", "test")
			version, err := adapter.UpdatePage(t.Context(), "123", 3, "T", []byte("<p>candidate</p>"), false)
			var unconfirmed *domain.PageUpdateUnconfirmedError
			if version != 0 || calls != 1 || !errors.As(err, &unconfirmed) || unconfirmed.ExpectedVersion != 4 || !unconfirmed.DiagnosticAmbiguousWrite() {
				t.Fatalf("version=%d calls=%d err=%v", version, calls, err)
			}
		})
	}
}

func TestUpdatePageUnconfirmedForceBindsActualSentVersion(t *testing.T) {
	reads, writes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reads++
			_, _ = io.WriteString(w, `{"title":"T","version":{"number":7}}`)
			return
		}
		writes++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	_, err := New(server.URL, "synthetic", "test").UpdatePage(t.Context(), "123", 3, "T", []byte("<p>candidate</p>"), true)
	var unconfirmed *domain.PageUpdateUnconfirmedError
	if !errors.As(err, &unconfirmed) || unconfirmed.ExpectedVersion != 8 || reads != 1 || writes != 1 {
		t.Fatalf("reads=%d writes=%d err=%v", reads, writes, err)
	}
}
