package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestConfPageTitleSetDryRunThenApply(t *testing.T) {
	titlePath := filepath.Join(t.TempDir(), "title.txt")
	if err := os.WriteFile(titlePath, []byte("  New title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentTitle, currentVersion := "Old title", 7
	putCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"id":"42","title":`+strconv.Quote(currentTitle)+`,"version":{"number":`+strconv.Itoa(currentVersion)+`},"body":{"storage":{"value":"<p>body</p>"}}}`)
		case http.MethodPut:
			putCalls++
			var payload struct {
				Title   string `json:"title"`
				Version struct {
					Number int `json:"number"`
				} `json:"version"`
				Body struct {
					Storage struct {
						Value          string `json:"value"`
						Representation string `json:"representation"`
					} `json:"storage"`
				} `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Title != "New title" || payload.Version.Number != 8 || payload.Body.Storage.Value != "<p>body</p>" || payload.Body.Storage.Representation != "storage" {
				t.Fatalf("unsafe title PUT: %+v", payload)
			}
			currentTitle, currentVersion = payload.Title, payload.Version.Number
			_, _ = io.WriteString(w, `{"version":{"number":8}}`)
		}
	}))
	t.Cleanup(srv.Close)

	out, code := runCLI(t, confEnv(srv), "conf", "page", "title", "set", "42", "--from-file", titlePath)
	if code != exitOK || putCalls != 0 {
		t.Fatalf("dry-run exit=%d puts=%d out=%s", code, putCalls, out)
	}
	assertGolden(t, "conf_page_title_preview.json", []byte(out))
	var preview struct {
		Status         string `json:"status"`
		Title          string `json:"title"`
		CurrentVersion int    `json:"current_version"`
		ProposalHash   string `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "would_apply" || preview.Title != "New title" || preview.CurrentVersion != 7 || preview.ProposalHash == "" {
		t.Fatalf("preview = %+v", preview)
	}

	out, code = runCLI(t, confEnv(srv), "conf", "page", "title", "set", "42", "--from-file", titlePath,
		"--apply", "--expected-version", "7", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || putCalls != 1 {
		t.Fatalf("apply exit=%d puts=%d out=%s", code, putCalls, out)
	}
	var applied struct {
		Status       string `json:"status"`
		FinalVersion int    `json:"final_version"`
	}
	if err := json.Unmarshal([]byte(out), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Status != "applied" || applied.FinalVersion != 8 {
		t.Fatalf("applied = %+v", applied)
	}
}

func TestConfPageTitleSetRequiresFileAndApplyGates(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, `{"id":"42","title":"Old","version":{"number":7},"body":{"storage":{"value":"x"}}}`)
	if out, code := runCLI(t, confEnv(srv), "conf", "page", "title", "set", "42"); code != exitUsage || out != "" {
		t.Fatalf("missing file exit=%d out=%q", code, out)
	}
	path := filepath.Join(t.TempDir(), "title.txt")
	if err := os.WriteFile(path, []byte("New"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := runCLI(t, confEnv(srv), "conf", "page", "title", "set", "42", "--from-file", path, "--apply"); code != exitUsage {
		t.Fatalf("missing apply gates exit=%d", code)
	}
}

func TestConfPageTitleSetDistinguishesMissingAndExplicitEmptyBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "title.txt")
	if err := os.WriteFile(path, []byte("New"), 0o600); err != nil {
		t.Fatal(err)
	}
	putCalls := 0
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			putCalls++
		}
		_, _ = io.WriteString(w, `{"id":"42","title":"Old","version":{"number":7}}`)
	}))
	t.Cleanup(missing.Close)
	_, code := runCLI(t, confEnv(missing), "conf", "page", "title", "set", "42", "--from-file", path,
		"--apply", "--expected-version", "7", "--expected-proposal-hash", "reviewed")
	if code != exitCheckFailed || putCalls != 0 {
		t.Fatalf("omitted body exit=%d puts=%d", code, putCalls)
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			putCalls++
		}
		_, _ = io.WriteString(w, `{"id":"42","title":"Old","version":{"number":7},"body":{"storage":{}}}`)
	}))
	t.Cleanup(malformed.Close)
	_, code = runCLI(t, confEnv(malformed), "conf", "page", "title", "set", "42", "--from-file", path,
		"--apply", "--expected-version", "7", "--expected-proposal-hash", "reviewed")
	if code != exitCheckFailed || putCalls != 0 {
		t.Fatalf("storage without value exit=%d puts=%d", code, putCalls)
	}

	explicitEmpty := jsonServer(t, http.StatusOK, `{"id":"42","title":"Old","version":{"number":7},"body":{"storage":{"value":""}}}`)
	out, code := runCLI(t, confEnv(explicitEmpty), "conf", "page", "title", "set", "42", "--from-file", path)
	if code != exitOK || !strings.Contains(out, `"status": "would_apply"`) {
		t.Fatalf("explicit empty body exit=%d out=%s", code, out)
	}
}

func TestConfPageTitleUnknownEmitsJSONAndNeverReplaysPUT(t *testing.T) {
	path := filepath.Join(t.TempDir(), "title.txt")
	if err := os.WriteFile(path, []byte("New"), 0o600); err != nil {
		t.Fatal(err)
	}
	gets, puts := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			puts++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"ambiguous"}`)
			return
		}
		gets++
		_, _ = io.WriteString(w, `{"id":"42","title":"Old","version":{"number":7},"body":{"storage":{"value":"body"}}}`)
	}))
	t.Cleanup(srv.Close)
	out, _, code := runCLIFull(t, confEnv(srv), "conf", "page", "title", "set", "42", "--from-file", path,
		"--apply", "--expected-version", "7", "--expected-proposal-hash", "682a43495d3a2dad4ee0e7b9622e4f5141fcdf362113a7d63af08350a375492f")
	if code != exitGeneric || gets != 2 || puts != 1 || !strings.Contains(out, `"status": "unknown"`) {
		t.Fatalf("unknown contract exit=%d gets=%d puts=%d out=%s", code, gets, puts, out)
	}
}
