package corpus

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestBuildActiveCanonicalRoundTrip(t *testing.T) {
	active := validBuildActive()
	data, err := CanonicalBuildActive(active, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("active bytes = %q", data)
	}
	parsed, err := ParseBuildActive(data, Limits{})
	if err != nil || parsed.AttemptID != active.AttemptID || len(parsed.Services) != 2 {
		t.Fatalf("parsed=%#v err=%v", parsed, err)
	}
	parsed.Services[0].ScopeDigest = ""
	if active.Services[0].ScopeDigest == "" {
		t.Fatal("parsed service storage aliases caller")
	}
}

func TestBuildActiveRejectsStrictAndSemanticViolations(t *testing.T) {
	base := validBuildActive()
	canonical, err := CanonicalBuildActive(base, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(canonical)
	for name, data := range map[string][]byte{
		"duplicate":  []byte(strings.Replace(text, `{"schema_version":1,`, `{"schema_version":1,"schema_version":1,`, 1)),
		"unknown":    []byte(strings.Replace(text, `{"schema_version":1,`, `{"unknown":1,"schema_version":1,`, 1)),
		"future":     []byte(strings.Replace(text, `"schema_version":1`, `"schema_version":2`, 1)),
		"null":       []byte(strings.Replace(text, `"services":[`, `"services":null,"ignored":[`, 1)),
		"missing LF": bytes.TrimSuffix(canonical, []byte("\n")),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseBuildActive(data, Limits{})
			assertRejected(t, err)
		})
	}

	tests := []struct {
		name   string
		limits Limits
		mutate func(*BuildActive)
		reason Reason
	}{
		{name: "attempt", mutate: func(a *BuildActive) { a.AttemptID = "short" }, reason: ReasonFormat},
		{name: "status", mutate: func(a *BuildActive) { a.Status = "unknown" }, reason: ReasonType},
		{name: "options", mutate: func(a *BuildActive) { a.OptionsDigest = "short" }, reason: ReasonDigest},
		{name: "time", mutate: func(a *BuildActive) { a.StartedAt = "private" }, reason: ReasonFormat},
		{name: "deadline", mutate: func(a *BuildActive) { a.Deadline = a.StartedAt }, reason: ReasonLineage},
		{name: "attempt limit", mutate: func(a *BuildActive) { a.MaxAttempts = 0 }, reason: ReasonBounds},
		{name: "usage attempts", mutate: func(a *BuildActive) { a.Usage.Attempts = a.MaxAttempts + 1 }, reason: ReasonBounds},
		{name: "usage bytes", mutate: func(a *BuildActive) { a.Usage.ResponseBytes = a.MaxResponseBytes + 1 }, reason: ReasonBounds},
		{name: "response limit", mutate: func(a *BuildActive) { a.MaxResponseBytes = maxCaptureResponseBytes + 1 }, reason: ReasonBounds},
		{name: "nil services", mutate: func(a *BuildActive) { a.Services = nil }, reason: ReasonMembership},
		{name: "unsorted services", mutate: func(a *BuildActive) { a.Services[0], a.Services[1] = a.Services[1], a.Services[0] }, reason: ReasonMembership},
		{name: "receipt without scope", mutate: func(a *BuildActive) { a.Services[0].ScopeDigest = "" }, reason: ReasonDigest},
		{name: "unknown remote service", mutate: func(a *BuildActive) { a.RemoteService = "aggregate" }, reason: ReasonMembership},
		{name: "remote service without flight", mutate: func(a *BuildActive) { a.RemoteInFlight = false }, reason: ReasonMembership},
		{name: "completed in flight", mutate: func(a *BuildActive) { a.Status = BuildAttemptCompleted }, reason: ReasonMembership},
		{name: "active generation", mutate: func(a *BuildActive) { a.GenerationDigest = digestByte('f') }, reason: ReasonMembership},
		{name: "completed missing receipt", mutate: func(a *BuildActive) {
			a.Status = BuildAttemptCompleted
			a.RemoteInFlight = false
			a.RemoteService = ""
			a.GenerationDigest = digestByte('f')
			a.Services[0].ReceiptDigest = ""
		}, reason: ReasonMembership},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active := base
			active.Services = append([]BuildServiceState(nil), base.Services...)
			if test.mutate != nil {
				test.mutate(&active)
			}
			_, err := CanonicalBuildActive(active, test.limits)
			assertReason(t, err, test.reason)
		})
	}
}

func validBuildActive() BuildActive {
	started := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	return BuildActive{
		SchemaVersion: BuildActiveSchemaV1,
		AttemptID:     strings.Repeat("1", 32), Status: BuildAttemptActive,
		OptionsDigest: digestByte('a'),
		Services: []BuildServiceState{
			{Service: ServiceConfluence, SelectorDigest: digestByte('b'), ScopeDigest: digestByte('c'), ReceiptDigest: digestByte('d')},
			{Service: ServiceJira, SelectorDigest: digestByte('e'), ScopeDigest: digestByte('f'), ReceiptDigest: digestByte('1')},
		},
		StartedAt: NewBuildActiveTime(started), Deadline: NewBuildActiveTime(started.Add(time.Hour)),
		MaxAttempts: 100, MaxResponseBytes: 1 << 20,
		Usage:          CaptureUsage{Attempts: 7, ResponseBytes: 4096},
		RemoteInFlight: true, RemoteService: ServiceJira,
	}
}
