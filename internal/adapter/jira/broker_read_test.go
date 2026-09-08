package jira

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const brokerJiraIdentityJSON = `{"id":"9007199254740993","key":"PROJ-7","fields":{"project":{"key":"PROJ"},"updated":"2026-09-01T10:00:00.000+0000"}}`

func brokerJiraTestServer(t *testing.T, response string, status int, target string) (*Jira, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.RequestURI() != target || r.ContentLength != 0 {
			t.Errorf("unexpected broker request method=%s target=%s bytes=%d", r.Method, r.URL.RequestURI(), r.ContentLength)
		}
		if status == http.StatusFound {
			w.Header().Set("Location", "/redirected")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return New(server.URL, "synthetic-token", "test"), &requests
}

func TestBrokerJiraQualificationExactRead(t *testing.T) {
	adapter, requests := brokerJiraTestServer(t, brokerJiraIdentityJSON, http.StatusOK, "/rest/api/2/issue/PROJ-7?fields=project%2Cupdated")
	wantOrigin, err := backendid.OriginSHA256(adapter.base)
	gotOrigin, originErr := adapter.BrokerOriginSHA256()
	if err != nil || originErr != nil || gotOrigin != strings.TrimPrefix(wantOrigin, backendid.Prefix) {
		t.Fatalf("origin=%q/%q err=%v/%v", gotOrigin, wantOrigin, originErr, err)
	}
	budget, err := domain.NewReadBudget(1, brokerJiraQualificationBytes)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.QualifyBrokerIssue(domain.WithSingleAttempt(domain.WithReadBudget(t.Context(), budget)), "PROJ-7")
	if err != nil || !identity.Complete || identity.ID != "9007199254740993" || identity.Key != "PROJ-7" || identity.Project != "PROJ" || identity.Updated != "2026-09-01T10:00:00.000+0000" || *requests != 1 || budget.Usage().Attempts != 1 {
		t.Fatalf("identity=%+v err=%v requests=%d usage=%+v", identity, err, *requests, budget.Usage())
	}
}

func TestBrokerJiraRejectsMalformedQualification(t *testing.T) {
	for name, response := range map[string]string{
		"duplicate":           strings.Replace(brokerJiraIdentityJSON, `"id":`, `"id":"1","id":`, 1),
		"numeric id":          strings.Replace(brokerJiraIdentityJSON, `"9007199254740993"`, `9007199254740993`, 1),
		"noncanonical id":     strings.Replace(brokerJiraIdentityJSON, `9007199254740993`, `01`, 1),
		"key drift":           strings.Replace(brokerJiraIdentityJSON, `PROJ-7`, `PROJ-8`, 1),
		"project drift":       strings.Replace(brokerJiraIdentityJSON, `"key":"PROJ"`, `"key":"OTHER"`, 1),
		"null project":        strings.Replace(brokerJiraIdentityJSON, `{"key":"PROJ"}`, `null`, 1),
		"missing updated":     `{"id":"1","key":"PROJ-7","fields":{"project":{"key":"PROJ"}}}`,
		"null updated":        strings.Replace(brokerJiraIdentityJSON, `"2026-09-01T10:00:00.000+0000"`, `null`, 1),
		"invalid updated":     strings.Replace(brokerJiraIdentityJSON, `2026-09-01T10:00:00.000+0000`, `invalid`, 1),
		"unknown root":        strings.Replace(brokerJiraIdentityJSON, `"id":`, `"private":"hidden","id":`, 1),
		"unknown project":     strings.Replace(brokerJiraIdentityJSON, `"key":"PROJ"`, `"key":"PROJ","private":"hidden"`, 1),
		"extra field":         strings.Replace(brokerJiraIdentityJSON, `"project":`, `"summary":"hidden","project":`, 1),
		"wrong optional type": strings.Replace(brokerJiraIdentityJSON, `"id":`, `"self":null,"id":`, 1),
		"trailing":            brokerJiraIdentityJSON + `{}`,
		"invalid utf8":        brokerJiraIdentityJSON + string([]byte{0xff}),
		"surrogate":           strings.Replace(brokerJiraIdentityJSON, `"id":`, `"self":"\ud800","id":`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			adapter, requests := brokerJiraTestServer(t, response, http.StatusOK, "/rest/api/2/issue/PROJ-7?fields=project%2Cupdated")
			identity, err := adapter.QualifyBrokerIssue(t.Context(), "PROJ-7")
			if !errors.Is(err, domain.ErrCheckFailed) || identity.Complete || identity.ID != "" || *requests != 1 {
				t.Fatalf("identity=%+v err=%v requests=%d", identity, err, *requests)
			}
		})
	}
}

func TestBrokerJiraBusinessProjection(t *testing.T) {
	for _, description := range []string{`null`, `""`, `"native {panel}wiki{panel}"`} {
		t.Run(description, func(t *testing.T) {
			response := strings.Replace(brokerJiraIdentityJSON, `"project":`, `"summary":"Summary","description":`+description+`,"project":`, 1)
			adapter, requests := brokerJiraTestServer(t, response, http.StatusOK, "/rest/api/2/issue/9007199254740993?fields=description%2Cproject%2Csummary%2Cupdated")
			result, err := adapter.ReadBrokerIssue(t.Context(), "9007199254740993", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary, domain.BrokerJiraIssueFieldDescription, domain.BrokerJiraIssueFieldUpdated})
			if err != nil || !result.Complete || !result.Identity.Complete || len(result.Fields) != 3 || !result.Fields[1].Present || result.Fields[1].Null != (description == "null") || result.Fields[0].Value != "Summary" || result.Fields[2].Value != result.Identity.Updated || *requests != 1 {
				t.Fatalf("result=%+v err=%v requests=%d", result, err, *requests)
			}
		})
	}
	adapter, requests := brokerJiraTestServer(t, brokerJiraIdentityJSON, http.StatusOK, "/rest/api/2/issue/9007199254740993?fields=project%2Cupdated")
	result, err := adapter.ReadBrokerIssue(t.Context(), "9007199254740993", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldUpdated})
	if err != nil || len(result.Fields) != 1 || *requests != 1 {
		t.Fatalf("result=%+v err=%v requests=%d", result, err, *requests)
	}
}

func TestBrokerJiraBusinessRefusesUnprovedFields(t *testing.T) {
	for name, response := range map[string]string{
		"absent description":    brokerJiraIdentityJSON,
		"numeric description":   strings.Replace(brokerJiraIdentityJSON, `"project":`, `"description":42,"project":`, 1),
		"object description":    strings.Replace(brokerJiraIdentityJSON, `"project":`, `"description":{},"project":`, 1),
		"duplicate description": strings.Replace(brokerJiraIdentityJSON, `"project":`, `"description":null,"description":"x","project":`, 1),
		"extra field":           strings.Replace(brokerJiraIdentityJSON, `"project":`, `"description":null,"summary":"private","project":`, 1),
		"id drift":              strings.Replace(strings.Replace(brokerJiraIdentityJSON, `9007199254740993`, `2`, 1), `"project":`, `"description":null,"project":`, 1),
		"inconsistent project":  strings.Replace(strings.Replace(brokerJiraIdentityJSON, `"key":"PROJ"`, `"key":"OTHER"`, 1), `"project":`, `"description":null,"project":`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			adapter, requests := brokerJiraTestServer(t, response, http.StatusOK, "/rest/api/2/issue/9007199254740993?fields=description%2Cproject%2Cupdated")
			result, err := adapter.ReadBrokerIssue(t.Context(), "9007199254740993", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldDescription})
			if !errors.Is(err, domain.ErrCheckFailed) || result.Complete || result.Fields != nil || *requests != 1 {
				t.Fatalf("result=%+v err=%v requests=%d", result, err, *requests)
			}
		})
	}
}

func TestBrokerJiraReadBounds(t *testing.T) {
	for _, qualification := range []bool{true, false} {
		maximum, target := brokerJiraBusinessBytes, "/rest/api/2/issue/9007199254740993?fields=project%2Cupdated"
		if qualification {
			maximum, target = brokerJiraQualificationBytes, "/rest/api/2/issue/PROJ-7?fields=project%2Cupdated"
		}
		for _, excess := range []int64{0, 1} {
			response := brokerJiraIdentityJSON + strings.Repeat(" ", int(maximum+excess)-len(brokerJiraIdentityJSON))
			adapter, requests := brokerJiraTestServer(t, response, http.StatusOK, target)
			var err error
			if qualification {
				_, err = adapter.QualifyBrokerIssue(t.Context(), "PROJ-7")
			} else {
				_, err = adapter.ReadBrokerIssue(t.Context(), "9007199254740993", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldUpdated})
			}
			if (err != nil) != (excess != 0) || *requests != 1 {
				t.Fatalf("qualification=%t excess=%d err=%v requests=%d", qualification, excess, err, *requests)
			}
		}
	}
}

func TestBrokerJiraReadNeverRetriesOrRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		adapter, requests := brokerJiraTestServer(t, `{}`, status, "/rest/api/2/issue/PROJ-7?fields=project%2Cupdated")
		_, err := adapter.QualifyBrokerIssue(context.Background(), "PROJ-7")
		if err == nil || *requests != 1 {
			t.Fatalf("status=%d err=%v requests=%d", status, err, *requests)
		}
	}
}

func TestBrokerJiraReadRedactsTraceAndTransportFailure(t *testing.T) {
	adapter, requests := brokerJiraTestServer(t, `discarded-response-canary`, http.StatusForbidden, "/rest/api/2/issue/PROJ-7?fields=project%2Cupdated")
	var trace bytes.Buffer
	adapter.c = httpx.New(adapter.c.Base(), "synthetic-token", "test", httpx.WithTrace(&trace))
	_, err := adapter.QualifyBrokerIssue(t.Context(), "PROJ-7")
	if !errors.Is(err, domain.ErrForbidden) || *requests != 1 {
		t.Fatalf("err=%v requests=%d", err, *requests)
	}
	var apiError *httpx.APIError
	if errors.As(err, &apiError) {
		t.Fatal("raw API response remained reachable")
	}
	for _, formatted := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprint(errors.Unwrap(err)), trace.String()} {
		if strings.Contains(formatted, "canary") || strings.Contains(formatted, "/rest/api/") || strings.Contains(formatted, "PROJ-7") {
			t.Fatalf("unsafe diagnostic: %q", formatted)
		}
	}
}

func TestBrokerJiraReadRejectsInvalidSelectorsBeforeHTTP(t *testing.T) {
	adapter, requests := brokerJiraTestServer(t, `{}`, http.StatusOK, "unused")
	for _, key := range []string{"proj-7", "PROJ-07", "https://example.invalid/PROJ-7", "PROJ-7?fields=*"} {
		if _, err := adapter.QualifyBrokerIssue(t.Context(), key); err == nil {
			t.Errorf("accepted key %q", key)
		}
	}
	for _, id := range []string{"PROJ-7", "01", "0", "1/2"} {
		if _, err := adapter.ReadBrokerIssue(t.Context(), id, []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldUpdated}); err == nil {
			t.Errorf("accepted id %q", id)
		}
	}
	for _, fields := range [][]domain.BrokerJiraIssueField{nil, {"unknown"}, {"updated", "updated"}} {
		if _, err := adapter.ReadBrokerIssue(t.Context(), "1", fields); err == nil {
			t.Errorf("accepted fields %v", fields)
		}
	}
	if *requests != 0 {
		t.Fatalf("invalid input made %d requests", *requests)
	}
}

func TestBrokerJiraDiscardsOnlyBoundedStandardMetadata(t *testing.T) {
	project := `{"id":"2","key":"PROJ","name":"Synthetic project","self":"https://example.invalid/project/2","projectTypeKey":"software","simplified":false,"avatarUrls":{"16x16":"https://example.invalid/avatar"},"projectCategory":{"id":"3","name":"Category","description":"Description","self":"https://example.invalid/category/3"}}`
	response := strings.Replace(brokerJiraIdentityJSON, `{"key":"PROJ"}`, project, 1)
	response = strings.Replace(response, `"id":`, `"self":"https://example.invalid/issue/1","expand":"renderedFields","id":`, 1)
	identity, _, err := decodeBrokerJiraIssue([]byte(response), map[string]bool{"project": true, "updated": true})
	if err != nil || !identity.Complete || identity.Project != "PROJ" {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	for name, invalid := range map[string]string{
		"numeric self":       strings.Replace(response, `"https://example.invalid/issue/1"`, `1`, 1),
		"null simplified":    strings.Replace(response, `"simplified":false`, `"simplified":null`, 1),
		"unknown avatar":     strings.Replace(response, `"16x16"`, `"unknown"`, 1),
		"numeric avatar":     strings.Replace(response, `"https://example.invalid/avatar"`, `42`, 1),
		"unknown category":   strings.Replace(response, `"description":"Description"`, `"unknown":"Description"`, 1),
		"numeric project id": strings.Replace(response, `"id":"2"`, `"id":2`, 1),
		"oversized metadata": strings.Replace(response, `"Synthetic project"`, `"`+strings.Repeat("x", 4097)+`"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			identity, _, err := decodeBrokerJiraIssue([]byte(invalid), map[string]bool{"project": true, "updated": true})
			if !errors.Is(err, domain.ErrCheckFailed) || identity.Complete {
				t.Fatalf("identity=%+v err=%v", identity, err)
			}
		})
	}
}

func TestBrokerJiraSummaryMustBePresentString(t *testing.T) {
	for _, summary := range []string{"null", "42", "true", "{}", "[]"} {
		response := strings.Replace(brokerJiraIdentityJSON, `"project":`, `"summary":`+summary+`,"project":`, 1)
		adapter, requests := brokerJiraTestServer(t, response, http.StatusOK, "/rest/api/2/issue/9007199254740993?fields=project%2Csummary%2Cupdated")
		result, err := adapter.ReadBrokerIssue(t.Context(), "9007199254740993", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary})
		if !errors.Is(err, domain.ErrCheckFailed) || result.Complete || *requests != 1 {
			t.Fatalf("summary=%s result=%+v err=%v requests=%d", summary, result, err, *requests)
		}
	}
}

func FuzzBrokerJiraIdentityEvidence(f *testing.F) {
	f.Add([]byte(brokerJiraIdentityJSON))
	f.Add([]byte(`{"id":"1","id":"2","key":"PROJ-7","fields":{}}`))
	f.Add([]byte(`{"id":9007199254740993,"key":"PROJ-7","fields":{"project":null}}`))
	f.Add([]byte(`{"id":"1","key":"PROJ-7","fields":{"project":{"key":"PROJ"},"updated":null}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > int(brokerJiraQualificationBytes) {
			return
		}
		identity, fields, err := decodeBrokerJiraIssue(data, map[string]bool{"project": true, "updated": true})
		if err == nil && (!identity.Complete || !guardedLinkID(identity.ID) || !guardedLinkKey(identity.Key) || !strings.HasPrefix(identity.Key, identity.Project+"-") || !guardedLabelUpdated(identity.Updated) || len(fields) != 2) {
			t.Fatalf("accepted incomplete identity=%+v", identity)
		}
	})
}
