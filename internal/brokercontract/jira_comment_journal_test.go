package brokercontract

import (
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerJiraCommentDefinitionPinsSingleSnapshotProfile(t *testing.T) {
	for _, operation := range []domain.BrokerOperationID{domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply} {
		definition, ok := Definition(operation, 1)
		if !ok || ValidateBrokerJiraCommentDefinitionV1(definition, operation) != nil {
			t.Fatalf("definition %s not pinned", operation)
		}
		changed := definition
		changed.QualificationProfile = "current_project_membership"
		if err := ValidateBrokerJiraCommentDefinitionV1(changed, operation); err == nil {
			t.Fatal("stronger profile was accepted")
		}
	}
}

func TestBrokerJiraCommentJournalDigestsBindExactIdentity(t *testing.T) {
	identity := domain.BrokerJiraIssueIdentity{ID: "101", Key: "PROJ-1", Project: "PROJ", Updated: "2026-09-09T10:00:00Z", Complete: true}
	resource, err := BrokerJiraCommentResourceV1(identity)
	if err != nil {
		t.Fatal(err)
	}
	effects, effectSHA, err := BrokerJiraCommentEffectsV1(resource, true)
	if err != nil || len(effects) != 2 || len(effectSHA) != 64 {
		t.Fatalf("effects=%+v digest=%q err=%v", effects, effectSHA, err)
	}
	backend := domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("a", 64), WorkloadBackendID: "jira-primary"}
	target, err := BrokerJiraCommentTargetSHA256V1(backend, identity.ID)
	if err != nil || len(target) != 64 {
		t.Fatalf("target=%q err=%v", target, err)
	}
	backend.WorkloadBackendID = "jira-secondary"
	changed, _ := BrokerJiraCommentTargetSHA256V1(backend, identity.ID)
	if changed == target {
		t.Fatal("complete backend did not affect target")
	}
}

func TestBrokerJiraCommentTargetRequiresCanonicalNumericIdentity(t *testing.T) {
	backend := domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("a", 64), WorkloadBackendID: "jira-primary"}
	for _, issueID := range []string{"1", "18446744073709551615"} {
		if digest, err := BrokerJiraCommentTargetSHA256V1(backend, issueID); err != nil || len(digest) != 64 {
			t.Fatalf("valid issue ID %q: digest=%q error=%v", issueID, digest, err)
		}
	}
	for _, issueID := range []string{"", "0", "01", "issue", "-1", "+1", "1.0", " 1", "1 ", "18446744073709551616"} {
		if digest, err := BrokerJiraCommentTargetSHA256V1(backend, issueID); err == nil || digest != "" {
			t.Errorf("invalid issue ID %q: digest=%q error=%v", issueID, digest, err)
		}
	}
}

func TestBrokerJiraCommentResultAndNoDispatchDomainsAreDistinct(t *testing.T) {
	result := domain.BrokerJiraCommentResult{SchemaVersion: 1, ArgumentsSHA256: strings.Repeat("a", 64), OperationTicket: "ticket", Mode: "apply", Status: "not_applied", ProposalHash: strings.Repeat("b", 64), NativeCandidateSHA256: strings.Repeat("c", 64), VersionEvidenceSHA256: strings.Repeat("d", 64), WriteAttempted: true, Complete: true}
	resultSHA, err := BrokerJiraCommentResultSHA256V1(result)
	record := domain.BrokerJournalRecord{OperationID: strings.Repeat("e", 64), BindingSHA256: strings.Repeat("f", 64), Sequence: 4, Phase: domain.BrokerOperationDispatching, DispatchClaimed: true}
	noDispatchSHA, noDispatchErr := BrokerJiraCommentNoDispatchSHA256V1(record)
	if err != nil || noDispatchErr != nil || resultSHA == noDispatchSHA {
		t.Fatalf("result=%q no_dispatch=%q errors=%v/%v", resultSHA, noDispatchSHA, err, noDispatchErr)
	}
}
