package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func movePageJSON(id, title, parent string, version int, body string) string {
	ancestors := "[]"
	if parent != "" {
		ancestors = `[{"id":` + strconv.Quote(parent) + `,"title":"Parent"}]`
	}
	return `{"id":` + strconv.Quote(id) + `,"type":"page","title":` + strconv.Quote(title) +
		`,"space":{"key":"S"},"version":{"number":` + strconv.Itoa(version) + `},"ancestors":` + ancestors +
		`,"body":{"storage":{"value":` + strconv.Quote(body) + `}}}`
}

func TestConfPageMoveDryRunThenApply(t *testing.T) {
	currentParent, currentVersion := "10", 7
	putCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/content/42":
			_, _ = io.WriteString(w, movePageJSON("42", "Movable", currentParent, currentVersion, "<p>body</p>"))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/content/20":
			_, _ = io.WriteString(w, movePageJSON("20", "Target", "10", 3, "target"))
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/content/42":
			putCalls++
			var payload struct {
				Title   string `json:"title"`
				Version struct {
					Number int `json:"number"`
				} `json:"version"`
				Ancestors []struct {
					ID string `json:"id"`
				} `json:"ancestors"`
				Body struct {
					Storage struct {
						Value string `json:"value"`
					} `json:"storage"`
				} `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Title != "Movable" || payload.Version.Number != 8 || len(payload.Ancestors) != 1 || payload.Ancestors[0].ID != "20" || payload.Body.Storage.Value != "<p>body</p>" {
				t.Fatalf("unsafe move payload: %+v", payload)
			}
			currentParent, currentVersion = "20", 8
			_, _ = io.WriteString(w, `{"version":{"number":8}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	out, code := runCLI(t, confEnv(srv), "conf", "page", "move", "42", "--parent", "20")
	if code != exitOK || putCalls != 0 {
		t.Fatalf("preview exit=%d puts=%d out=%s", code, putCalls, out)
	}
	assertGolden(t, "conf_page_move_preview.json", []byte(out))
	var preview struct {
		Status       string `json:"status"`
		ProposalHash string `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "would_apply" || preview.ProposalHash == "" {
		t.Fatalf("preview=%+v", preview)
	}

	out, code = runCLI(t, confEnv(srv), "conf", "page", "move", "42", "--parent", "20", "--apply",
		"--expected-version", "7", "--expected-parent", "10", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || putCalls != 1 {
		t.Fatalf("apply exit=%d puts=%d out=%s", code, putCalls, out)
	}
	if !strings.Contains(out, `"status": "applied"`) || !strings.Contains(out, `"final_version": 8`) {
		t.Fatalf("apply out=%s", out)
	}
}

func TestConfPageMoveRequiresApplyGates(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, movePageJSON("42", "Movable", "10", 7, "body"))
	if out, code := runCLI(t, confEnv(srv), "conf", "page", "move", "--parent", "20"); code != exitUsage || out != "" {
		t.Fatalf("missing id exit=%d out=%q", code, out)
	}
	if _, code := runCLI(t, confEnv(srv), "conf", "page", "move", "42"); code != exitUsage {
		t.Fatalf("missing parent exit=%d", code)
	}
	if _, code := runCLI(t, confEnv(srv), "conf", "page", "move", "42", "--parent", "20", "--apply"); code != exitUsage {
		t.Fatalf("missing apply gates exit=%d", code)
	}
}

func TestConfPageMoveUnknownEmitsJSONAndNeverReplaysPUT(t *testing.T) {
	putCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			putCalls++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"ambiguous"}`)
			return
		}
		if r.URL.Path == "/rest/api/content/20" {
			_, _ = io.WriteString(w, movePageJSON("20", "Target", "10", 3, "target"))
			return
		}
		_, _ = io.WriteString(w, movePageJSON("42", "Movable", "10", 7, "body"))
	}))
	t.Cleanup(srv.Close)

	previewOut, code := runCLI(t, confEnv(srv), "conf", "page", "move", "42", "--parent", "20")
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, previewOut)
	}
	var preview struct {
		ProposalHash string `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIFull(t, confEnv(srv), "conf", "page", "move", "42", "--parent", "20", "--apply",
		"--expected-version", "7", "--expected-parent", "10", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitGeneric || putCalls != 1 || !strings.Contains(out, `"status": "unknown"`) {
		t.Fatalf("unknown exit=%d puts=%d out=%s", code, putCalls, out)
	}
}

func TestConfPageMoveRejectsDescendantWithoutPUT(t *testing.T) {
	putCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			putCalls++
		}
		if r.URL.Path == "/rest/api/content/20" {
			_, _ = io.WriteString(w, `{"id":"20","type":"page","title":"Descendant","space":{"key":"S"},"version":{"number":3},"ancestors":[{"id":"42","title":"Source"}],"body":{"storage":{"value":"target"}}}`)
			return
		}
		_, _ = io.WriteString(w, movePageJSON("42", "Movable", "10", 7, "body"))
	}))
	t.Cleanup(srv.Close)
	_, code := runCLI(t, confEnv(srv), "conf", "page", "move", "42", "--parent", "20")
	if code != exitCheckFailed || putCalls != 0 {
		t.Fatalf("cycle exit=%d puts=%d", code, putCalls)
	}
}

func TestConfPageMoveRejectsOmittedTargetHierarchyWithoutPUT(t *testing.T) {
	putCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			putCalls++
		}
		if r.URL.Path == "/rest/api/content/20" {
			_, _ = io.WriteString(w, `{"id":"20","type":"page","title":"Partial target","space":{"key":"S"},"version":{"number":3},"body":{"storage":{"value":"target"}}}`)
			return
		}
		_, _ = io.WriteString(w, movePageJSON("42", "Movable", "10", 7, "body"))
	}))
	t.Cleanup(srv.Close)
	_, code := runCLI(t, confEnv(srv), "conf", "page", "move", "42", "--parent", "20")
	if code != exitCheckFailed || putCalls != 0 {
		t.Fatalf("partial target exit=%d puts=%d", code, putCalls)
	}
}
