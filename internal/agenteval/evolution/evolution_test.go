package evolution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func digest(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(hash[:])
}

func testRequest(selfFeedbackOnly bool) Request {
	return Request{
		LineageSHA256: digest("lineage"), SkillSHA256: digest("skill"), EvaluationSHA256: digest("evaluation"),
		SelfFeedbackOnly: selfFeedbackOnly,
		Failures: []FailureSummary{
			{Class: FailureCoverage, Count: 2, EvidenceSHA256: []string{digest("coverage")}},
			{Class: FailureSafety, Count: 1, EvidenceSHA256: []string{digest("safety")}},
		},
	}
}

func TestGenerateSeparatesReviewOnlyChangesAndSelfFeedback(t *testing.T) {
	proposal, err := Generate(testRequest(true))
	if err != nil {
		t.Fatal(err)
	}
	if !proposal.Exploratory || proposal.ReusableImprovement || !proposal.SelfFeedbackOnly {
		t.Fatalf("self-feedback was not exploratory: %+v", proposal)
	}
	if len(proposal.SkillChanges) != 2 || len(proposal.EvaluationChanges) != 2 || proposal.SkillChanges[0].Action == proposal.EvaluationChanges[0].Action {
		t.Fatalf("skill/evaluation changes were not separate: %+v", proposal)
	}
	data, err := Encode(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "prompt") || strings.Contains(string(data), "expected") || strings.Contains(string(data), "secret") {
		t.Fatalf("proposal leaked content: %s", data)
	}
	decoded, err := Decode(bytes.NewReader(data))
	if err != nil || decoded.ProposalSHA256 != proposal.ProposalSHA256 {
		t.Fatalf("Decode(): proposal=%+v err=%v", decoded, err)
	}
	other, err := Generate(testRequest(true))
	if err != nil || other.ProposalSHA256 != proposal.ProposalSHA256 {
		t.Fatalf("generation was not deterministic: first=%s second=%s err=%v", proposal.ProposalSHA256, other.ProposalSHA256, err)
	}
}

func TestEvolutionProposalContractIsClosedCanonicalBoundedAndReviewOnly(t *testing.T) {
	proposal, err := Generate(testRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "prompt") || strings.Contains(string(encoded), "expected") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("proposal exposed non-content-minimized data: %s", encoded)
	}
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil || decoded.ProposalSHA256 != proposal.ProposalSHA256 {
		t.Fatalf("closed canonical round trip failed: proposal=%+v err=%v", decoded, err)
	}
	unknown := bytes.Replace(encoded, []byte(`"proposal_sha256":"`), []byte(`"future":1,"proposal_sha256":"`), 1)
	if _, err := Decode(bytes.NewReader(unknown)); err == nil {
		t.Fatal("future member was accepted")
	}
	tampered := proposal
	tampered.InputSHA256 = digest("different-input")
	tampered.ProposalSHA256, err = digestValue("proposal", proposalWithoutDigest(tampered))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(tampered); err == nil {
		t.Fatal("proposal input digest was not bound to its failure projection")
	}
	request := testRequest(false)
	request.Failures = make([]FailureSummary, MaxFailures+1)
	for index := range request.Failures {
		request.Failures[index] = FailureSummary{Class: FailureClass("safety"), Count: 1, EvidenceSHA256: []string{digest("evidence")}}
	}
	if _, err := Generate(request); err == nil {
		t.Fatal("bounded failure collection was not enforced")
	}
	parent := t.TempDir()
	destination := filepath.Join(parent, "proposal")
	plan, err := PlanPublication(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteNew(destination); err != nil {
		t.Fatal(err)
	}
}

func TestProposalRejectsUnboundedOrNonCanonicalInputs(t *testing.T) {
	request := testRequest(false)
	request.Failures[0].EvidenceSHA256 = []string{"latest"}
	if _, err := Generate(request); err == nil {
		t.Fatal("alias evidence identity was accepted")
	}
	request = testRequest(false)
	request.Failures[0].Class, request.Failures[1].Class = request.Failures[1].Class, request.Failures[0].Class
	proposal, err := Generate(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(proposal)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(encoded, []byte(`"proposal_sha256":"`), []byte(`"future":1,"proposal_sha256":"`), 1)
	if _, err := Decode(bytes.NewReader(unknown)); err == nil {
		t.Fatal("unknown member was accepted")
	}
	failureUnknown := bytes.Replace(encoded, []byte(`"evidence_sha256":[`), []byte(`"action":"unexpected","evidence_sha256":[`), 1)
	if _, err := Decode(bytes.NewReader(failureUnknown)); err == nil {
		t.Fatal("failure change-only member was accepted")
	}
	changeUnknown := bytes.Replace(encoded, []byte(`"action":"clarify_coverage_boundary"`), []byte(`"count":1,"action":"clarify_coverage_boundary"`), 1)
	if _, err := Decode(bytes.NewReader(changeUnknown)); err == nil {
		t.Fatal("proposal change count member was accepted")
	}
	caseAlias := bytes.Replace(encoded, []byte(`"class":"coverage"`), []byte(`"Class":"coverage"`), 1)
	if _, err := Decode(bytes.NewReader(caseAlias)); err == nil {
		t.Fatal("case-folded nested member was accepted")
	}
	duplicate := bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	if _, err := Decode(bytes.NewReader(duplicate)); err == nil {
		t.Fatal("duplicate member was accepted")
	}
	request = testRequest(false)
	request.Failures = make([]FailureSummary, MaxFailures+1)
	for index := range request.Failures {
		request.Failures[index] = FailureSummary{Class: FailureClass("safety"), Count: 1, EvidenceSHA256: []string{digest("evidence")}}
	}
	if _, err := Generate(request); err == nil {
		t.Fatal("failure expansion was accepted")
	}
}

func TestPublicationWritesOnlyNewDestinationAndKeepsMarkerOnFailure(t *testing.T) {
	proposal, err := Generate(testRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanPublication(proposal)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	destination := filepath.Join(parent, "proposal")
	if err := plan.WriteNew(destination); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadPublished(destination)
	if err != nil || decoded.ProposalSHA256 != proposal.ProposalSHA256 {
		t.Fatalf("ReadPublished(): proposal=%+v err=%v", decoded, err)
	}
	if err := plan.WriteNew(destination); !errors.Is(err, ErrInvalid) {
		t.Fatalf("existing destination was accepted: %v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 1 || entries[0].Name() != proposalFileName {
		t.Fatalf("unexpected completed destination: entries=%v err=%v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(destination, "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPublished(destination); err == nil {
		t.Fatal("unexpected extra member was accepted")
	}
	if err := os.Remove(filepath.Join(destination, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(destination, proposalFileName), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPublished(destination); err == nil {
			t.Fatal("non-private proposal file was accepted")
		}
		if err := os.Chmod(filepath.Join(destination, proposalFileName), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadPublished(destination); err != nil {
		t.Fatalf("valid publication stopped reading after adversarial rows: %v", err)
	}
	partial := filepath.Join(parent, "partial")
	if err := os.Mkdir(partial, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, proposalMarkerName), []byte("incomplete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPublished(partial); err == nil {
		t.Fatal("incomplete destination was accepted")
	}
}
