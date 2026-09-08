package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func brokerPageFixture() map[string]any {
	return map[string]any{
		"id": "123", "type": "page", "status": "current", "title": "Synthetic title",
		"space":     map[string]any{"key": "DOC"},
		"version":   map[string]any{"number": 4, "when": "2026-09-01T10:00:00.000Z"},
		"ancestors": []any{map[string]any{"id": "10", "type": "page"}},
	}
}

func brokerPageFixtureJSON(t testing.TB, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBrokerPageReadsUseOnlyExactCurrentProjection(t *testing.T) {
	const native = "<p>Кириллица &amp; exact</p>\r\n<ac:structured-macro ac:name=\"code\"><ac:plain-text-body><![CDATA[a < b\n]]></ac:plain-text-body></ac:structured-macro>"
	for _, projection := range []domain.BrokerConfluenceProjection{domain.BrokerConfluenceProjectionMetadata, domain.BrokerConfluenceProjectionStorage} {
		t.Run(string(projection), func(t *testing.T) {
			var requests atomic.Int32
			fixture := brokerPageFixture()
			fixture["_links"] = map[string]any{"base": "https://unused.example.invalid", "webui": "/ignored-title"}
			fixture["_expandable"] = map[string]any{"body": "", "restrictions": "/ignored-restrictions"}
			fixture["version"].(map[string]any)["by"] = map[string]any{
				"type": "known", "displayName": "Ignored actor", "username": "ignored-user",
				"profilePicture": map[string]any{"path": "/ignored-profile", "width": 48, "height": 48, "isDefault": true},
			}
			fixture["version"].(map[string]any)["message"] = "Ignored version message"
			fixture["version"].(map[string]any)["minorEdit"] = false
			if projection == domain.BrokerConfluenceProjectionStorage {
				fixture["body"] = map[string]any{
					"storage":     map[string]any{"value": native, "representation": "storage", "_expandable": map[string]any{"content": "/ignored-content"}},
					"_expandable": map[string]any{"view": "", "editor": ""},
				}
			}
			raw := brokerPageFixtureJSON(t, fixture)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				expand := "space,version,ancestors"
				if projection == domain.BrokerConfluenceProjectionStorage {
					expand += ",body.storage"
				}
				if r.Method != http.MethodGet || r.URL.Path != "/rest/api/content/123" || len(r.URL.Query()) != 2 ||
					r.URL.Query().Get("status") != "current" || r.URL.Query().Get("expand") != expand || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Errorf("unexpected exact read: %s %s", r.Method, r.URL.RequestURI())
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			var trace bytes.Buffer
			reader := New(server.URL, "fixture-token", "test", WithTrace(&trace))
			wantOrigin, originErr := backendid.OriginSHA256(reader.base)
			gotOrigin, gotOriginErr := reader.BrokerOriginSHA256()
			if originErr != nil || gotOriginErr != nil || gotOrigin != strings.TrimPrefix(wantOrigin, backendid.Prefix) {
				t.Fatalf("origin=%q/%q err=%v/%v", gotOrigin, wantOrigin, gotOriginErr, originErr)
			}
			budget, _ := domain.NewReadBudget(1, brokerPageBusinessBytes)
			result, err := reader.ReadBrokerPage(domain.WithReadBudget(t.Context(), budget), "123", projection)
			if err != nil || !result.Complete || !result.Identity.Complete || !result.Identity.AncestorsPresent ||
				result.Identity.ID != "123" || result.Identity.Space != "DOC" || result.Identity.Version != 4 ||
				!reflect.DeepEqual(result.Identity.AncestorIDs, []string{"10"}) || result.Title != "Synthetic title" || result.Projection != projection {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if projection == domain.BrokerConfluenceProjectionStorage {
				if !result.StoragePresent || !bytes.Equal(result.Storage, []byte(native)) {
					t.Fatalf("native bytes changed: %q", result.Storage)
				}
			} else if result.StoragePresent || result.Storage != nil {
				t.Fatalf("metadata contains storage: %+v", result)
			}
			if requests.Load() != 1 || budget.Usage().Attempts != 1 || budget.Usage().ResponseBytes != int64(len(raw)) {
				t.Fatalf("requests=%d usage=%+v", requests.Load(), budget.Usage())
			}
			if strings.Contains(trace.String(), server.URL) || strings.Contains(trace.String(), "/rest/api/") {
				t.Fatalf("trace exposed route: %q", trace.String())
			}
		})
	}
}

func TestBrokerPageQualificationDiscardsUnavoidableContent(t *testing.T) {
	fixture := brokerPageFixture()
	fixture["title"] = "discarded-title-canary"
	fixture["version"].(map[string]any)["message"] = "discarded-message-canary"
	fixture["version"].(map[string]any)["by"] = map[string]any{"displayName": "discarded-actor-canary"}
	fixture["ancestors"] = []any{}
	fixture["_links"] = map[string]any{"self": "https://unused.example.invalid/discarded-route-canary"}
	for _, withTitle := range []bool{true, false} {
		t.Run(fmt.Sprint(withTitle), func(t *testing.T) {
			if !withTitle {
				delete(fixture, "title")
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/rest/api/content/123" || len(r.URL.Query()) != 2 ||
					r.URL.Query().Get("expand") != "space,version,ancestors" || r.URL.Query().Get("status") != "current" {
					t.Errorf("unexpected qualification: %s %s", r.Method, r.URL.RequestURI())
				}
				_, _ = w.Write(brokerPageFixtureJSON(t, fixture))
			}))
			defer server.Close()
			identity, err := New(server.URL, "fixture-token", "test").QualifyBrokerPage(t.Context(), "123")
			if err != nil || !identity.Complete || !identity.AncestorsPresent || identity.AncestorIDs == nil || len(identity.AncestorIDs) != 0 || requests.Load() != 1 {
				t.Fatalf("identity=%+v err=%v requests=%d", identity, err, requests.Load())
			}
			if raw := brokerPageFixtureJSON(t, identity); bytes.Contains(raw, []byte("canary")) || bytes.Contains(raw, []byte("unused.example.invalid")) {
				t.Fatalf("qualification leaked discarded content: %s", raw)
			}
		})
	}
}

func TestBrokerPageReadsRejectUnqualifiedShape(t *testing.T) {
	tests := []struct {
		name string
		edit func(map[string]any)
	}{
		{"wrong id", func(v map[string]any) { v["id"] = "124" }},
		{"noncanonical id", func(v map[string]any) { v["id"] = "0123" }},
		{"numeric id", func(v map[string]any) { v["id"] = 123 }},
		{"missing id", func(v map[string]any) { delete(v, "id") }},
		{"null id", func(v map[string]any) { v["id"] = nil }},
		{"wrong type", func(v map[string]any) { v["type"] = "blogpost" }},
		{"wrong status", func(v map[string]any) { v["status"] = "trashed" }},
		{"invalid updated", func(v map[string]any) { v["version"].(map[string]any)["when"] = "not-a-timestamp" }},
		{"missing status", func(v map[string]any) { delete(v, "status") }},
		{"missing ancestors", func(v map[string]any) { delete(v, "ancestors") }},
		{"null ancestors", func(v map[string]any) { v["ancestors"] = nil }},
		{"object ancestors", func(v map[string]any) { v["ancestors"] = map[string]any{"results": []any{}} }},
		{"null ancestor", func(v map[string]any) { v["ancestors"] = []any{nil} }},
		{"missing ancestor id", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"type": "page"}} }},
		{"null ancestor id", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"id": nil}} }},
		{"noncanonical ancestor", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"id": "010"}} }},
		{"wrong ancestor type", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"id": "10", "type": "attachment"}} }},
		{"empty ancestor type", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"id": "10", "type": ""}} }},
		{"wrong ancestor status", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"id": "10", "status": "draft"}} }},
		{"duplicate ancestors", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"id": "10"}, map[string]any{"id": "10"}} }},
		{"self ancestor", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"id": "123"}} }},
		{"expanded ancestor body", func(v map[string]any) { v["ancestors"] = []any{map[string]any{"id": "10", "body": map[string]any{}}} }},
		{"too many ancestors", func(v map[string]any) {
			ancestors := make([]any, brokerPageMaxAncestors+1)
			for index := range ancestors {
				ancestors[index] = map[string]any{"id": fmt.Sprint(index + 1)}
			}
			v["ancestors"] = ancestors
		}},
		{"missing space", func(v map[string]any) { delete(v, "space") }},
		{"null space", func(v map[string]any) { v["space"] = nil }},
		{"null space key", func(v map[string]any) { v["space"].(map[string]any)["key"] = nil }},
		{"empty space key", func(v map[string]any) { v["space"].(map[string]any)["key"] = "" }},
		{"unnormalized space key", func(v map[string]any) { v["space"].(map[string]any)["key"] = " DOC" }},
		{"oversized space key", func(v map[string]any) { v["space"].(map[string]any)["key"] = strings.Repeat("X", 129) }},
		{"wrong space id type", func(v map[string]any) { v["space"].(map[string]any)["id"] = "10" }},
		{"zero space id", func(v map[string]any) { v["space"].(map[string]any)["id"] = 0 }},
		{"unexpected space expansion", func(v map[string]any) { v["space"].(map[string]any)["homepage"] = map[string]any{"id": "10"} }},
		{"missing version", func(v map[string]any) { delete(v, "version") }},
		{"null version", func(v map[string]any) { v["version"] = nil }},
		{"zero version", func(v map[string]any) { v["version"].(map[string]any)["number"] = 0 }},
		{"fractional version", func(v map[string]any) { v["version"].(map[string]any)["number"] = 1.5 }},
		{"string version", func(v map[string]any) { v["version"].(map[string]any)["number"] = "4" }},
		{"null version number", func(v map[string]any) { v["version"].(map[string]any)["number"] = nil }},
		{"missing updated", func(v map[string]any) { delete(v["version"].(map[string]any), "when") }},
		{"null updated", func(v map[string]any) { v["version"].(map[string]any)["when"] = nil }},
		{"blank updated", func(v map[string]any) { v["version"].(map[string]any)["when"] = " " }},
		{"wrong by type", func(v map[string]any) { v["version"].(map[string]any)["by"] = "actor" }},
		{"unknown actor member", func(v map[string]any) { v["version"].(map[string]any)["by"] = map[string]any{"secret": "value"} }},
		{"null actor name", func(v map[string]any) { v["version"].(map[string]any)["by"] = map[string]any{"displayName": nil} }},
		{"wrong profile shape", func(v map[string]any) {
			v["version"].(map[string]any)["by"] = map[string]any{"profilePicture": []any{}}
		}},
		{"wrong minor edit type", func(v map[string]any) { v["version"].(map[string]any)["minorEdit"] = "false" }},
		{"null default title", func(v map[string]any) { v["title"] = nil }},
		{"unknown root", func(v map[string]any) { v["unknown"] = "value" }},
		{"case aliased field", func(v map[string]any) { v["ID"] = "123" }},
		{"case aliased nested field", func(v map[string]any) { v["version"].(map[string]any)["Number"] = 4 }},
		{"labels expansion", func(v map[string]any) { v["metadata"] = map[string]any{"labels": []any{}} }},
		{"restrictions expansion", func(v map[string]any) { v["restrictions"] = map[string]any{} }},
		{"unexpected body", func(v map[string]any) {
			v["body"] = map[string]any{"storage": map[string]any{"value": "<p>extra</p>", "representation": "storage"}}
		}},
		{"null body", func(v map[string]any) { v["body"] = nil }},
		{"wrong link type", func(v map[string]any) { v["_links"] = map[string]any{"self": []any{}} }},
		{"unknown link", func(v map[string]any) { v["_links"] = map[string]any{"next": "/more-ancestors"} }},
		{"unknown expandable", func(v map[string]any) { v["_expandable"] = map[string]any{"unexpected": ""} }},
		{"unexpanded ancestors", func(v map[string]any) { v["_expandable"] = map[string]any{"ancestors": "/more"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := brokerPageFixture()
			test.edit(fixture)
			raw := brokerPageFixtureJSON(t, fixture)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
			defer server.Close()
			reader := New(server.URL, "fixture-token", "test")
			identity, err := reader.QualifyBrokerPage(t.Context(), "123")
			if !errors.Is(err, domain.ErrCheckFailed) || !reflect.DeepEqual(identity, domain.BrokerConfluencePageIdentity{}) {
				t.Fatalf("unqualified identity=%+v err=%v", identity, err)
			}
			result, err := reader.ReadBrokerPage(t.Context(), "123", domain.BrokerConfluenceProjectionMetadata)
			if !errors.Is(err, domain.ErrCheckFailed) || !reflect.DeepEqual(result, domain.BrokerConfluencePageSnapshot{}) {
				t.Fatalf("unqualified result=%+v err=%v", result, err)
			}
		})
	}
}

func TestBrokerPageStoragePresenceAndExactBytes(t *testing.T) {
	for name, storage := range map[string]any{
		"empty value":            map[string]any{"value": "", "representation": "storage"},
		"missing value":          map[string]any{"representation": "storage"},
		"null value":             map[string]any{"value": nil, "representation": "storage"},
		"number value":           map[string]any{"value": 1, "representation": "storage"},
		"missing representation": map[string]any{"value": "<p/>"},
		"wrong representation":   map[string]any{"value": "<p/>", "representation": "view"},
		"unknown member":         map[string]any{"value": "<p/>", "representation": "storage", "extra": "discard?"},
		"null storage":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := brokerPageFixture()
			fixture["body"] = map[string]any{"storage": storage}
			raw := brokerPageFixtureJSON(t, fixture)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
			defer server.Close()
			result, err := New(server.URL, "fixture-token", "test").ReadBrokerPage(t.Context(), "123", domain.BrokerConfluenceProjectionStorage)
			if name == "empty value" {
				if err != nil || !result.StoragePresent || result.Storage == nil || len(result.Storage) != 0 {
					t.Fatalf("empty storage result=%+v err=%v", result, err)
				}
			} else if !errors.Is(err, domain.ErrCheckFailed) || !reflect.DeepEqual(result, domain.BrokerConfluencePageSnapshot{}) {
				t.Fatalf("unsupported storage result=%+v err=%v", result, err)
			}
		})
	}
}

func TestBrokerPageBusinessRejectsIncompleteOrExpandedProjection(t *testing.T) {
	for _, projection := range []domain.BrokerConfluenceProjection{domain.BrokerConfluenceProjectionMetadata, domain.BrokerConfluenceProjectionStorage} {
		t.Run(string(projection), func(t *testing.T) {
			tests := []struct {
				name string
				edit func(map[string]any)
			}{
				{"missing title", func(v map[string]any) { delete(v, "title") }},
				{"empty title", func(v map[string]any) { v["title"] = "" }},
				{"control in title", func(v map[string]any) { v["title"] = "before\x00after" }},
				{"oversized title", func(v map[string]any) { v["title"] = strings.Repeat("t", brokerPageQualificationBytes+1) }},
			}
			if projection == domain.BrokerConfluenceProjectionStorage {
				tests = append(tests, []struct {
					name string
					edit func(map[string]any)
				}{
					{"missing body", func(v map[string]any) { delete(v, "body") }},
					{"missing storage", func(v map[string]any) { v["body"] = map[string]any{} }},
					{"extra view", func(v map[string]any) { v["body"].(map[string]any)["view"] = map[string]any{"value": "<p>view</p>"} }},
					{"unexpanded body", func(v map[string]any) { v["_expandable"] = map[string]any{"body": "/unexpanded"} }},
					{"unexpanded storage", func(v map[string]any) {
						v["body"].(map[string]any)["_expandable"] = map[string]any{"storage": "/unexpanded"}
					}},
				}...)
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					fixture := brokerPageFixture()
					if projection == domain.BrokerConfluenceProjectionStorage {
						fixture["body"] = map[string]any{"storage": map[string]any{"value": "<p>native</p>", "representation": "storage"}}
					}
					test.edit(fixture)
					raw := brokerPageFixtureJSON(t, fixture)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
					defer server.Close()
					result, err := New(server.URL, "fixture-token", "test").ReadBrokerPage(t.Context(), "123", projection)
					if !errors.Is(err, domain.ErrCheckFailed) || !reflect.DeepEqual(result, domain.BrokerConfluencePageSnapshot{}) {
						t.Fatalf("unqualified business result=%+v err=%v", result, err)
					}
				})
			}
		})
	}
}

func TestBrokerPageStrictJSONRejectsLossyEvidence(t *testing.T) {
	valid := string(brokerPageFixtureJSON(t, brokerPageFixture()))
	for name, raw := range map[string][]byte{
		"duplicate":          []byte(strings.Replace(valid, `"id":"123"`, `"id":"123","id":"123"`, 1)),
		"nested duplicate":   []byte(strings.Replace(valid, `"number":4`, `"number":4,"number":4`, 1)),
		"trailing":           []byte(valid + `{}`),
		"invalid UTF8":       bytes.Replace([]byte(valid), []byte("Synthetic title"), []byte{0xff}, 1),
		"unpaired surrogate": []byte(strings.Replace(valid, "Synthetic title", `\ud800`, 1)),
		"overdeep":           []byte(`{"unknown":` + strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65) + `}`),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
			defer server.Close()
			_, err := New(server.URL, "fixture-token", "test").QualifyBrokerPage(t.Context(), "123")
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("lossy evidence accepted: %v", err)
			}
		})
	}
}

func TestBrokerPageInvalidSelectorsHaveNoIO(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	reader := New(server.URL, "fixture-token", "test")
	for _, id := range []string{"", "0", "0123", " 123", "123/child", "https://example.invalid/123", "18446744073709551616"} {
		if _, err := reader.QualifyBrokerPage(t.Context(), id); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("id=%q err=%v", id, err)
		}
		if _, err := reader.ReadBrokerPage(t.Context(), id, domain.BrokerConfluenceProjectionMetadata); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("id=%q err=%v", id, err)
		}
	}
	if _, err := reader.ReadBrokerPage(t.Context(), "123", "view"); !errors.Is(err, domain.ErrUsage) {
		t.Errorf("projection err=%v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid selectors made %d requests", requests.Load())
	}
}

func TestBrokerPageTransportSingleAttemptBudgetAndSafeErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "discarded-response-canary")
			}))
			defer server.Close()
			budget, _ := domain.NewReadBudget(3, brokerPageQualificationBytes)
			_, err := New(server.URL, "fixture-token", "test").QualifyBrokerPage(domain.WithReadBudget(t.Context(), budget), "123")
			if err == nil || requests.Load() != 1 || budget.Usage().Attempts != 1 {
				t.Fatalf("err=%v requests=%d usage=%+v", err, requests.Load(), budget.Usage())
			}
			var apiError *httpx.APIError
			if errors.As(err, &apiError) {
				t.Fatal("raw API response remained reachable")
			}
			for _, formatted := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprint(errors.Unwrap(err))} {
				if strings.Contains(formatted, "canary") || strings.Contains(formatted, server.URL) || strings.Contains(formatted, "/rest/api/") {
					t.Fatalf("unsafe error: %s", formatted)
				}
			}
			if status == http.StatusUnauthorized && !errors.Is(err, domain.ErrAuth) || status == http.StatusForbidden && !errors.Is(err, domain.ErrForbidden) || status == http.StatusNotFound && !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("classification lost: %v", err)
			}
		})
	}
	for _, maximum := range []int{0, 1} {
		t.Run(fmt.Sprintf("attempts-%d", maximum), func(t *testing.T) {
			var requests atomic.Int32
			raw := brokerPageFixtureJSON(t, brokerPageFixture())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = w.Write(raw) }))
			defer server.Close()
			budget, _ := domain.NewReadBudget(maximum, brokerPageQualificationBytes)
			reader := New(server.URL, "fixture-token", "test")
			ctx := domain.WithReadBudget(t.Context(), budget)
			if maximum == 1 {
				if _, err := reader.QualifyBrokerPage(ctx, "123"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reader.ReadBrokerPage(ctx, "123", domain.BrokerConfluenceProjectionMetadata); !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
				t.Fatalf("budget err=%v", err)
			}
			if int(requests.Load()) != maximum {
				t.Fatalf("requests=%d maximum=%d", requests.Load(), maximum)
			}
		})
	}
}

func TestBrokerPageResponseBounds(t *testing.T) {
	raw := brokerPageFixtureJSON(t, brokerPageFixture())
	for _, size := range []int64{brokerPageQualificationBytes, brokerPageQualificationBytes + 1, brokerPageBusinessBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(raw)
				_, _ = io.CopyN(w, brokerPagePaddingReader{}, size-int64(len(raw)))
			}))
			defer server.Close()
			reader := New(server.URL, "fixture-token", "test")
			var err error
			if size > brokerPageBusinessBytes {
				_, err = reader.ReadBrokerPage(t.Context(), "123", domain.BrokerConfluenceProjectionMetadata)
			} else {
				_, err = reader.QualifyBrokerPage(t.Context(), "123")
			}
			if (err == nil) != (size == brokerPageQualificationBytes) {
				t.Fatalf("size=%d err=%v", size, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	defer server.Close()
	budget, _ := domain.NewReadBudget(1, int64(len(raw)-1))
	_, err := New(server.URL, "fixture-token", "test").QualifyBrokerPage(domain.WithReadBudget(t.Context(), budget), "123")
	if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) || budget.Usage().ResponseBytes > int64(len(raw)-1) {
		t.Fatalf("byte budget err=%v usage=%+v", err, budget.Usage())
	}
}

type brokerPagePaddingReader struct{}

func (brokerPagePaddingReader) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = ' '
	}
	return len(p), nil
}

func TestBrokerPageCanceledContextPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	_, err := New(server.URL, "fixture-token", "test").QualifyBrokerPage(ctx, "123")
	if !errors.Is(err, context.Canceled) || requests.Load() != 0 {
		t.Fatalf("err=%v requests=%d", err, requests.Load())
	}
}

func FuzzBrokerPageEvidence(f *testing.F) {
	f.Add([]byte(`{"id":"123","type":"page","status":"current","title":"Title","space":{"key":"DOC"},"version":{"number":1,"when":"2026-09-01T00:00:00Z"},"ancestors":[]}`))
	f.Add([]byte(`{"id":"123","id":"124"}`))
	f.Add([]byte(`{"ancestors":[null]}`))
	f.Add([]byte(`{"title":"\ud800"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > brokerPageQualificationBytes {
			t.Skip()
		}
		result, err := decodeBrokerPage(raw, "123", domain.BrokerConfluenceProjectionMetadata, true)
		if err != nil {
			if !reflect.DeepEqual(result, domain.BrokerConfluencePageSnapshot{}) || err.Error() != "check failed: incomplete or unsupported Confluence broker page evidence" {
				t.Fatalf("unsafe failure result=%+v err=%v", result, err)
			}
			return
		}
		if !result.Complete || !result.Identity.Complete || !result.Identity.AncestorsPresent || result.Identity.AncestorIDs == nil || result.Identity.ID != "123" || result.StoragePresent || result.Storage != nil {
			t.Fatalf("unqualified success=%+v", result)
		}
	})
}
