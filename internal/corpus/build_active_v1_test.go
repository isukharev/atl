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
	if !bytes.Contains(data, []byte(`"attachment_body_bytes":9`)) {
		t.Fatalf("active bytes omit durable attachment usage: %s", data)
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
		"duplicate":               []byte(strings.Replace(text, `{"schema_version":2,`, `{"schema_version":2,"schema_version":2,`, 1)),
		"unknown":                 []byte(strings.Replace(text, `{"schema_version":2,`, `{"unknown":1,"schema_version":2,`, 1)),
		"future":                  []byte(strings.Replace(text, `"schema_version":2`, `"schema_version":4`, 1)),
		"unversioned":             []byte(strings.Replace(text, `"schema_version":2,`, ``, 1)),
		"missing aggregate usage": []byte(strings.Replace(text, `,"attachment_body_bytes":9`, ``, 1)),
		"missing service usage":   []byte(strings.Replace(text, `,"attachment_body_bytes":3`, ``, 1)),
		"null":                    []byte(strings.Replace(text, `"services":[`, `"services":null,"ignored":[`, 1)),
		"missing LF":              bytes.TrimSuffix(canonical, []byte("\n")),
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
		{name: "attachment usage", mutate: func(a *BuildActive) { a.AttachmentBodyBytes = maxCaptureResponseBytes + 1 }, reason: ReasonBounds},
		{name: "service attachment exceeds aggregate", mutate: func(a *BuildActive) { a.AttachmentBodyBytes = 2 }, reason: ReasonBounds},
		{name: "service usage exceeds aggregate", mutate: func(a *BuildActive) { a.Usage.Attempts = 4 }, reason: ReasonLineage},
		{name: "response limit", mutate: func(a *BuildActive) { a.MaxResponseBytes = maxCaptureResponseBytes + 1 }, reason: ReasonBounds},
		{name: "nil services", mutate: func(a *BuildActive) { a.Services = nil }, reason: ReasonMembership},
		{name: "unsorted services", mutate: func(a *BuildActive) { a.Services[0], a.Services[1] = a.Services[1], a.Services[0] }, reason: ReasonMembership},
		{name: "receipt without scope", mutate: func(a *BuildActive) { a.Services[0].ScopeDigest = "" }, reason: ReasonDigest},
		{name: "usage without service start", mutate: func(a *BuildActive) {
			a.Services[0].StartedAt = ""
			a.Services[0].ScopeDigest = ""
			a.Services[0].AttachmentBodyBytes = 0
			a.Services[0].ReceiptDigest = ""
		}, reason: ReasonLineage},
		{name: "service starts before attempt", mutate: func(a *BuildActive) {
			a.Services[0].StartedAt = NewBuildActiveTime(time.Date(2026, 8, 12, 9, 59, 59, 0, time.UTC))
		}, reason: ReasonLineage},
		{name: "remote service without start", mutate: func(a *BuildActive) {
			a.Services[1].StartedAt = ""
			a.Services[1].ScopeDigest = ""
			a.Services[1].ReceiptDigest = ""
			a.Services[1].Usage = CaptureUsage{}
			a.Services[1].AttachmentBodyBytes = 0
		}, reason: ReasonLineage},
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

func TestBuildActiveReadsLegacyForExplicitMigrationOnly(t *testing.T) {
	current := validBuildActive()
	legacyBytes, err := marshalCanonical(buildActiveV1Projection(current))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := ParseBuildActive(legacyBytes, Limits{})
	if err != nil || legacy.SchemaVersion != BuildActiveSchemaV1 || legacy.AttachmentBodyBytes != 0 {
		t.Fatalf("legacy=%#v error=%v", legacy, err)
	}
	for _, state := range legacy.Services {
		if state.AttachmentBodyBytes != 0 {
			t.Fatalf("legacy service retained v2 usage: %#v", state)
		}
	}
	if _, err := CanonicalBuildActive(legacy, Limits{}); err == nil {
		t.Fatal("legacy active record was writable without migration")
	}
}

func validBuildActive() BuildActive {
	started := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	return BuildActive{
		SchemaVersion: BuildActiveSchemaV2,
		AttemptID:     strings.Repeat("1", 32), Status: BuildAttemptActive,
		OptionsDigest: digestByte('a'),
		Services: []BuildServiceState{
			{Service: ServiceConfluence, SelectorDigest: digestByte('b'), ScopeDigest: digestByte('c'), StartedAt: NewBuildActiveTime(started), Usage: CaptureUsage{Attempts: 2, ResponseBytes: 1000}, AttachmentBodyBytes: 3, ReceiptDigest: digestByte('d')},
			{Service: ServiceJira, SelectorDigest: digestByte('e'), ScopeDigest: digestByte('f'), StartedAt: NewBuildActiveTime(started), Usage: CaptureUsage{Attempts: 3, ResponseBytes: 2000}, AttachmentBodyBytes: 4, ReceiptDigest: digestByte('1')},
		},
		StartedAt: NewBuildActiveTime(started), Deadline: NewBuildActiveTime(started.Add(time.Hour)),
		MaxAttempts: 100, MaxResponseBytes: 1 << 20,
		Usage: CaptureUsage{Attempts: 7, ResponseBytes: 4096}, AttachmentBodyBytes: 9,
		RemoteInFlight: true, RemoteService: ServiceJira,
	}
}
