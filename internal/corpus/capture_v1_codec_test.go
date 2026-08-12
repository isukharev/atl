package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCaptureReceiptCanonicalRoundTripAndPrincipalScope(t *testing.T) {
	input := validCaptureReceiptInput()
	receipt, err := BuildCaptureReceipt(input, Limits{})
	if err != nil {
		t.Fatalf("BuildCaptureReceipt: %v", err)
	}
	data, err := CanonicalCaptureReceipt(receipt, Limits{})
	if err != nil {
		t.Fatalf("CanonicalCaptureReceipt: %v", err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) || bytes.Contains(data, []byte("synthetic-user")) ||
		bytes.Contains(data, []byte("example.test")) {
		t.Fatalf("canonical capture receipt leaked input or lost LF: %q", data)
	}
	parsed, err := ParseCaptureReceipt(data, Limits{})
	if err != nil || parsed.ReceiptDigest != receipt.ReceiptDigest || parsed.Service != ServiceJira {
		t.Fatalf("ParseCaptureReceipt = %#v, %v", parsed, err)
	}

	scope, err := PrincipalScopeDigest(ServiceJira, "sha256:"+digestByte('a'), "synthetic-user")
	if err != nil {
		t.Fatalf("PrincipalScopeDigest: %v", err)
	}
	if scope != input.ScopeDigest {
		t.Fatalf("scope = %q, want %q", scope, input.ScopeDigest)
	}
	otherService, _ := PrincipalScopeDigest(ServiceConfluence, "sha256:"+digestByte('a'), "synthetic-user")
	otherOrigin, _ := PrincipalScopeDigest(ServiceJira, "sha256:"+digestByte('b'), "synthetic-user")
	otherPrincipal, _ := PrincipalScopeDigest(ServiceJira, "sha256:"+digestByte('a'), "other-user")
	if scope == otherService || scope == otherOrigin || scope == otherPrincipal {
		t.Fatal("principal scope domain did not bind every input")
	}
}

func TestParseCaptureReceiptRejectsStrictJSONViolations(t *testing.T) {
	receipt := mustCaptureReceipt(t, validCaptureReceiptInput())
	canonical, err := CanonicalCaptureReceipt(receipt, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(canonical)
	tests := map[string][]byte{
		"duplicate":      []byte(strings.Replace(text, `{"schema_version":1,`, `{"schema_version":1,"schema_version":1,`, 1)),
		"case duplicate": []byte(strings.Replace(text, `{"schema_version":1,`, `{"SCHEMA_VERSION":1,"schema_version":1,`, 1)),
		"unknown":        []byte(strings.Replace(text, `{"schema_version":1,`, `{"unknown":1,"schema_version":1,`, 1)),
		"nested unknown": []byte(strings.Replace(text, `"usage":{"attempts":`, `"usage":{"unknown":0,"attempts":`, 1)),
		"future":         []byte(strings.Replace(text, `"schema_version":1`, `"schema_version":2`, 1)),
		"null list":      []byte(strings.Replace(text, `"dimensions":[`, `"dimensions":null,"ignored":[`, 1)),
		"space":          []byte(strings.Replace(text, `"schema_version":1`, `"schema_version": 1`, 1)),
		"missing LF":     bytes.TrimSuffix(canonical, []byte("\n")),
		"extra LF":       append(append([]byte(nil), canonical...), '\n'),
		"trailing":       append(append([]byte(nil), canonical...), []byte("{}\n")...),
		"invalid utf8":   append([]byte(`{"schema_version":"`), 0xff, '}'),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCaptureReceipt(data, Limits{})
			assertRejected(t, err)
		})
	}
}

func TestCaptureReceiptRejectsSemanticViolations(t *testing.T) {
	base := mustCaptureReceipt(t, validCaptureReceiptInput())
	tests := []struct {
		name   string
		limits Limits
		mutate func(*CaptureReceipt)
		reason Reason
	}{
		{name: "schema", mutate: func(r *CaptureReceipt) { r.SchemaVersion++ }, reason: ReasonSchema},
		{name: "service", mutate: func(r *CaptureReceipt) { r.Service = ServiceAggregate }, reason: ReasonType},
		{name: "scope", mutate: func(r *CaptureReceipt) { r.ScopeDigest = "short" }, reason: ReasonDigest},
		{name: "uppercase", mutate: func(r *CaptureReceipt) { r.OptionsDigest = strings.ToUpper(r.OptionsDigest) }, reason: ReasonDigest},
		{name: "start format", mutate: func(r *CaptureReceipt) { r.StartedAt = "2026-01-02T03:04:05+00:00" }, reason: ReasonFormat},
		{name: "time order", mutate: func(r *CaptureReceipt) { r.CompletedAt = "2026-01-02T03:04:04Z" }, reason: ReasonLineage},
		{name: "negative total", mutate: func(r *CaptureReceipt) { r.Total = -1; r.Completed = -1 }, reason: ReasonMembership},
		{name: "incomplete", mutate: func(r *CaptureReceipt) { r.Completed-- }, reason: ReasonMembership},
		{name: "item bound", limits: Limits{MaxMembers: 1}, reason: ReasonMembership},
		{name: "attempts", mutate: func(r *CaptureReceipt) { r.Usage.Attempts = -1 }, reason: ReasonBounds},
		{name: "response bytes", mutate: func(r *CaptureReceipt) { r.Usage.ResponseBytes = -1 }, reason: ReasonBounds},
		{name: "nil dimensions", mutate: func(r *CaptureReceipt) { r.Dimensions = nil }, reason: ReasonMembership},
		{name: "missing dimension", mutate: func(r *CaptureReceipt) { r.Dimensions = r.Dimensions[:3] }, reason: ReasonMembership},
		{name: "duplicate dimension", mutate: func(r *CaptureReceipt) { r.Dimensions[1].Dimension = r.Dimensions[0].Dimension }, reason: ReasonMembership},
		{name: "unknown state", mutate: func(r *CaptureReceipt) { r.Dimensions[0].State = "unknown" }, reason: ReasonType},
		{name: "digest mismatch", mutate: func(r *CaptureReceipt) { r.Total++; r.Completed++ }, reason: ReasonDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receipt := cloneCaptureReceipt(base)
			if test.mutate != nil {
				test.mutate(&receipt)
			}
			if test.name != "digest mismatch" {
				if digest, err := captureReceiptDigest(receipt, test.limits); err == nil {
					receipt.ReceiptDigest = digest
				}
			}
			err := VerifyCaptureReceipt(receipt, test.limits)
			assertReason(t, err, test.reason)
		})
	}
}

func TestPrincipalScopeDigestRejectsSensitiveOrAmbiguousInputs(t *testing.T) {
	origin := "sha256:" + digestByte('a')
	for name, values := range map[string]struct {
		service   Service
		origin    string
		principal string
	}{
		"aggregate": {ServiceAggregate, origin, "user"},
		"origin":    {ServiceJira, digestByte('a'), "user"},
		"empty":     {ServiceJira, origin, ""},
		"space":     {ServiceJira, origin, " user"},
		"control":   {ServiceJira, origin, "user\n"},
		"long":      {ServiceJira, origin, strings.Repeat("a", maxCapturePrincipalBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if value, err := PrincipalScopeDigest(values.service, values.origin, values.principal); err == nil || value != "" || !errors.Is(err, ErrIntegrity) {
				t.Fatalf("value=%q err=%v", value, err)
			}
		})
	}
}

func TestCaptureSelectionDigestMatchesServiceOrdering(t *testing.T) {
	jira, err := CaptureSelectionDigest(ServiceJira, []string{"10", "2", "1"})
	if err != nil {
		t.Fatal(err)
	}
	confluence, err := CaptureSelectionDigest(ServiceConfluence, []string{"10", "2", "1"})
	if err != nil {
		t.Fatal(err)
	}
	if jira != corpusBytesSHA256ForTest([]byte(`["1","2","10"]`)) ||
		confluence != corpusBytesSHA256ForTest([]byte(`["1","10","2"]`)) || jira == confluence {
		t.Fatalf("jira=%q confluence=%q", jira, confluence)
	}
	for _, ids := range [][]string{nil, {"1", "1"}, {"01"}, {"private"}} {
		if digest, err := CaptureSelectionDigest(ServiceJira, ids); err == nil || digest != "" {
			t.Fatalf("ids=%v digest=%q err=%v", ids, digest, err)
		}
	}
}

func corpusBytesSHA256ForTest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validCaptureReceiptInput() CaptureReceiptInput {
	scope, err := PrincipalScopeDigest(ServiceJira, "sha256:"+digestByte('a'), "synthetic-user")
	if err != nil {
		panic(err)
	}
	return CaptureReceiptInput{
		Service: ServiceJira, ScopeDigest: scope,
		SelectorDigest: digestByte('1'), OptionsDigest: digestByte('a'),
		SelectionDigest: digestByte('3'), SnapshotDigest: digestByte('4'),
		StartedAt:   time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC),
		CompletedAt: time.Date(2026, 1, 2, 3, 5, 5, 7, time.UTC),
		Total:       2, Completed: 2, Usage: CaptureUsage{Attempts: 7, ResponseBytes: 4096},
		Dimensions: []CaptureDimensionEvidence{
			{Dimension: CaptureNative, State: CaptureComplete},
			{Dimension: CaptureMetadata, State: CaptureComplete},
			{Dimension: CaptureComments, State: CaptureNotRequested},
			{Dimension: CaptureAttachments, State: CaptureNotRequested},
		},
	}
}

func mustCaptureReceipt(t testing.TB, input CaptureReceiptInput) CaptureReceipt {
	t.Helper()
	receipt, err := BuildCaptureReceipt(input, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func cloneCaptureReceipt(receipt CaptureReceipt) CaptureReceipt {
	receipt.Dimensions = append([]CaptureDimensionEvidence(nil), receipt.Dimensions...)
	return receipt
}
