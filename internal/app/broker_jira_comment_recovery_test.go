package app

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/adapter/brokerjournal"
	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerJiraCommentRecoveryMaximumFixtureFitsJournal(t *testing.T) {
	baseline := make([]brokerJiraCommentRecoveryEntry, domain.JiraCommentReadMaxItems)
	for index := range baseline {
		baseline[index] = brokerJiraCommentRecoveryEntry{
			// The guarded matcher parses uint64, so 20 decimal digits are the
			// longest accepted ID even though the wire ceiling is wider.
			ID:           "100000000000000" + fmt.Sprintf("%05d", index),
			RecordSHA256: strings.Repeat("a", domain.BrokerMaxDigestBytes),
		}
	}
	value := brokerJiraCommentRecoveryArtifact{
		OperationID: strings.Repeat("1", 64), TicketSHA256: strings.Repeat("2", 64), BindingSHA256: strings.Repeat("3", 64),
		NativeBody:  bytes.Repeat([]byte{'x'}, JiraCommentBodyMaxBytes),
		Issue:       domain.JiraGuardedCommentIssue{ID: "18446744073709551615", Key: strings.Repeat("A", 32) + "-" + strings.Repeat("9", jiraGuardedCommentMaxKeyBytes-33), Project: strings.Repeat("A", 32), Updated: "2027-01-15T08:00:00.999999999+14:00", Complete: true},
		ActorSHA256: strings.Repeat("4", 64), Baseline: baseline,
	}
	encoded, err := encodeBrokerJiraCommentRecoveryArtifact(value)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(encoded)) > brokerJiraCommentRecoveryMaxBytes || brokerJiraCommentRecoveryMaxBytes > brokerjournal.MaxArtifactBytes {
		t.Fatalf("encoded=%d app_max=%d journal_max=%d", len(encoded), brokerJiraCommentRecoveryMaxBytes, brokerjournal.MaxArtifactBytes)
	}
	decoded, err := decodeBrokerJiraCommentRecoveryArtifact(encoded)
	if err != nil || !bytes.Equal(decoded.NativeBody, value.NativeBody) || len(decoded.Baseline) != len(value.Baseline) {
		t.Fatalf("decode baseline=%d err=%v", len(decoded.Baseline), err)
	}
}

func FuzzBrokerJiraCommentRecoveryArtifact(f *testing.F) {
	seed := brokerJiraCommentRecoveryArtifact{
		OperationID: strings.Repeat("1", 64), TicketSHA256: strings.Repeat("2", 64), BindingSHA256: strings.Repeat("3", 64),
		NativeBody: []byte("native *wiki*\n"), ActorSHA256: strings.Repeat("4", 64),
		Issue:    domain.JiraGuardedCommentIssue{ID: "101", Key: "PROJ-1", Project: "PROJ", Updated: "2027-01-15T08:00:00Z", Complete: true},
		Baseline: []brokerJiraCommentRecoveryEntry{{ID: "9", RecordSHA256: strings.Repeat("5", 64)}},
	}
	encoded, err := encodeBrokerJiraCommentRecoveryArtifact(seed)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add([]byte(`{"schema_version":1,"schema_version":1}`))
	f.Add([]byte(`{"schema_version":2}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if int64(len(data)) > brokerJiraCommentRecoveryMaxBytes {
			return
		}
		value, err := decodeBrokerJiraCommentRecoveryArtifact(data)
		if err != nil {
			return
		}
		canonical, err := encodeBrokerJiraCommentRecoveryArtifact(value)
		if err != nil || int64(len(canonical)) > brokerJiraCommentRecoveryMaxBytes {
			t.Fatalf("accepted artifact cannot be encoded within its bound: %v", err)
		}
		roundTrip, err := decodeBrokerJiraCommentRecoveryArtifact(canonical)
		if err != nil || !bytes.Equal(roundTrip.NativeBody, value.NativeBody) {
			t.Fatalf("canonical decode changed native bytes: %v", err)
		}
		again, err := encodeBrokerJiraCommentRecoveryArtifact(roundTrip)
		if err != nil || !bytes.Equal(again, canonical) {
			t.Fatalf("artifact encoding is not canonical: %v", err)
		}
	})
}

func TestBrokerJiraCommentRecoveryCodecRejectsMutation(t *testing.T) {
	value := brokerJiraCommentRecoveryArtifact{
		OperationID: strings.Repeat("1", 64), TicketSHA256: strings.Repeat("2", 64), BindingSHA256: strings.Repeat("3", 64), NativeBody: []byte("native *wiki*\n"),
		Issue: domain.JiraGuardedCommentIssue{ID: "101", Key: "PROJ-1", Project: "PROJ", Updated: "2026-09-09T10:00:00Z", Complete: true}, ActorSHA256: strings.Repeat("4", 64),
		Baseline: []brokerJiraCommentRecoveryEntry{{ID: "9", RecordSHA256: strings.Repeat("5", 64)}},
	}
	encoded, err := encodeBrokerJiraCommentRecoveryArtifact(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"writer-key", "approval", "credential", "backend response"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("artifact exposed %q", forbidden)
		}
	}
	for _, data := range [][]byte{
		append(append([]byte(nil), encoded...), 'x'),
		bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		bytes.Replace(encoded, []byte(`"operation_id"`), []byte(`"unknown"`), 1),
		[]byte(`{"schema_version":1,"schema_version":1}`),
		make([]byte, brokerJiraCommentRecoveryMaxBytes+1),
	} {
		if _, err := decodeBrokerJiraCommentRecoveryArtifact(data); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	for _, invalid := range []brokerJiraCommentRecoveryArtifact{
		func() brokerJiraCommentRecoveryArtifact {
			changed := value
			changed.Baseline = append(append([]brokerJiraCommentRecoveryEntry(nil), value.Baseline...), value.Baseline[0])
			return changed
		}(),
		func() brokerJiraCommentRecoveryArtifact {
			changed := value
			changed.ActorSHA256 = strings.Repeat("A", domain.BrokerMaxDigestBytes)
			return changed
		}(),
		func() brokerJiraCommentRecoveryArtifact {
			changed := value
			changed.NativeBody = append(bytes.Repeat([]byte{'x'}, JiraCommentBodyMaxBytes), 'x')
			return changed
		}(),
	} {
		if _, err := encodeBrokerJiraCommentRecoveryArtifact(invalid); err == nil {
			t.Fatalf("encoded invalid artifact %+v", invalid)
		}
	}
}
