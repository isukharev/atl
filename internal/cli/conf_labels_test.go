package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestConfPageLabelsListPreviewAndApply(t *testing.T) {
	labels := []domain.ContentLabel{{ID: "1", Prefix: "global", Name: "existing", Label: "global:existing"}}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path != "/rest/api/content/42/label" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		switch request.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"results": labels, "_links": map[string]string{}})
		case http.MethodPost:
			writes++
			var added []domain.ContentLabel
			if err := json.NewDecoder(request.Body).Decode(&added); err != nil {
				t.Fatal(err)
			}
			labels = append(labels, added...)
			_, _ = io.WriteString(w, `{"results":[]}`)
		default:
			t.Fatalf("method=%s", request.Method)
		}
	}))
	t.Cleanup(server.Close)

	out, code := runCLI(t, confEnv(server), "conf", "page", "labels", "list", "42")
	if code != exitOK || !strings.Contains(out, `"complete": true`) || !strings.Contains(out, `"name": "existing"`) {
		t.Fatalf("list exit=%d out=%s", code, out)
	}
	out, code = runCLI(t, confEnv(server), "conf", "page", "labels", "add", "42", " release ", "release")
	if code != exitOK || writes != 0 {
		t.Fatalf("preview exit=%d writes=%d out=%s", code, writes, out)
	}
	assertGolden(t, "conf_page_labels_add_preview.json", []byte(out))
	var preview struct {
		Status       string   `json:"status"`
		Requested    []string `json:"requested"`
		ProposalHash string   `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(out), &preview); err != nil || preview.Status != "would_apply" || len(preview.Requested) != 1 || preview.ProposalHash == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	out, code = runCLI(t, confEnv(server), "conf", "page", "labels", "add", "42", "release", "--apply", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || writes != 1 || !strings.Contains(out, `"status": "applied"`) {
		t.Fatalf("apply exit=%d writes=%d out=%s", code, writes, out)
	}
}

func TestConfPageLabelsRemoveUsesEncodedQueryAndReadOnlyBlocksWrite(t *testing.T) {
	labels := []string{"team/blue", "keep"}
	requests, deletes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case http.MethodGet:
			records := make([]domain.ContentLabel, 0, len(labels))
			for index, name := range labels {
				records = append(records, domain.ContentLabel{ID: fmt.Sprint(index + 1), Prefix: "global", Name: name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": records, "_links": map[string]string{}})
		case http.MethodDelete:
			deletes++
			name, _ := url.QueryUnescape(request.URL.Query().Get("name"))
			filtered := labels[:0]
			for _, label := range labels {
				if label != name {
					filtered = append(filtered, label)
				}
			}
			labels = filtered
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(server.Close)
	previewOut, code := runCLI(t, confEnv(server), "conf", "page", "labels", "remove", "42", "team/blue")
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, previewOut)
	}
	var preview struct {
		ProposalHash string `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, confEnv(server), "conf", "page", "labels", "remove", "42", "team/blue", "--apply", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || deletes != 1 || !strings.Contains(out, `"status": "applied"`) || !sort.StringsAreSorted(labels) {
		t.Fatalf("remove exit=%d deletes=%d labels=%v out=%s", code, deletes, labels, out)
	}
	before := requests
	if _, code := runCLI(t, confEnv(server), "--read-only", "conf", "page", "labels", "add", "42", "blocked", "--apply", "--expected-proposal-hash", "x"); code != exitCheckFailed || requests != before {
		t.Fatalf("read-only add exit=%d requests=%d->%d", code, before, requests)
	}
}
