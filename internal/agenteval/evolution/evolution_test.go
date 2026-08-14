package evolution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
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
	assertEvolutionProposalKnownAnswer(t)
	assertEvolutionPublicSurface(t)
}

func TestEvolutionProposalKnownAnswer(t *testing.T) {
	assertEvolutionProposalKnownAnswer(t)
}

func assertEvolutionProposalKnownAnswer(t *testing.T) {
	classes := []FailureClass{FailureSafety, FailureCoverage, FailureRuntime, FailureQuality, FailureResource, FailureLifecycle, FailureVerifier}
	failures := make([]FailureSummary, 0, len(classes))
	for index, class := range classes {
		failures = append(failures, FailureSummary{Class: class, Count: uint32(index + 1), EvidenceSHA256: []string{digest("known-" + string(class))}})
	}
	proposal, err := Generate(Request{LineageSHA256: digest("known-lineage"), SkillSHA256: digest("known-skill"), EvaluationSHA256: digest("known-evaluation"), Failures: failures})
	if err != nil {
		t.Fatal(err)
	}
	data, err := Encode(proposal)
	if err != nil {
		t.Fatal(err)
	}
	wireHash := sha256.Sum256(data)
	if got := hex.EncodeToString(wireHash[:]); got != "f92b331066f6193e301a891c2ef701d6afe175704668cbb36c31d60f1981fc87" {
		t.Fatalf("known-answer wire hash drifted: %s", got)
	}
	if proposal.ProposalSHA256 != "62dee7da19b50e891b41ca353968c81148ab385696c5dc3aa572b2b759475b6d" {
		t.Fatalf("known-answer proposal digest drifted: %s", proposal.ProposalSHA256)
	}
	wantSkillActions := []string{"reinforce_safety_boundary", "clarify_coverage_boundary", "document_runtime_boundary", "clarify_expected_behavior", "state_resource_boundary", "preserve_no_replay_lifecycle", "preserve_independent_verifier"}
	wantEvaluationActions := []string{"add_safety_assertion", "add_coverage_assertion", "add_runtime_assertion", "add_quality_assertion", "add_resource_assertion", "add_lifecycle_assertion", "add_verifier_assertion"}
	for index := range classes {
		if proposal.SkillChanges[index].Action != wantSkillActions[index] || proposal.EvaluationChanges[index].Action != wantEvaluationActions[index] {
			t.Fatalf("known-answer action drift at %d: skill=%q evaluation=%q", index, proposal.SkillChanges[index].Action, proposal.EvaluationChanges[index].Action)
		}
	}
}

func TestEvolutionPublicSurfaceIsReviewOnlyAndStdlibBound(t *testing.T) {
	assertEvolutionPublicSurface(t)
}

func assertEvolutionPublicSurface(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	allowedImports := map[string]bool{
		"bytes": true, "crypto/sha256": true, "encoding/hex": true, "encoding/json": true,
		"errors": true, "io": true, "os": true, "path/filepath": true, "runtime": true,
		"sort": true, "strings": true, "unicode/utf8": true,
	}
	allowedExports := map[string]bool{
		"Schema": true, "SchemaVersion": true, "ContractVersion": true, "MaxProposalBytes": true,
		"MaxFailures": true, "MaxEvidenceRefs": true, "MaxJSONDepth": true, "SHA256HexLength": true,
		"ErrInvalid": true, "Error": true, "ErrorCode": true, "ErrorInvalidInput": true, "ErrorInvalidProposal": true,
		"ErrorLimitExceeded": true, "ErrorConflict": true, "ErrorOutcomeUnknown": true, "CodeOf": true,
		"FailureClass": true, "FailureSafety": true, "FailureCoverage": true, "FailureRuntime": true,
		"FailureQuality": true, "FailureResource": true, "FailureLifecycle": true, "FailureVerifier": true,
		"SkillAction": true, "SkillReinforceSafety": true, "SkillClarifyCoverage": true,
		"SkillDocumentRuntime": true, "SkillClarifyQuality": true, "SkillStateResource": true,
		"SkillPreserveLifecycle": true, "SkillPreserveVerifier": true, "EvaluationAction": true,
		"EvaluationSafety": true, "EvaluationCoverage": true, "EvaluationRuntime": true,
		"EvaluationQuality": true, "EvaluationResource": true, "EvaluationLifecycle": true,
		"EvaluationVerifier": true, "FailureSummary": true, "Request": true, "ProposalChange": true,
		"Proposal": true, "Encode": true, "Decode": true, "PlanPublication": true,
		"PublicationPlan": true, "WriteNew": true, "ReadPublished": true, "Generate": true,
		"Validate": true, "Unwrap": true, "Code": true,
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(sourceFile), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if !allowedImports[path] {
				t.Fatalf("evolution production import %q is outside the review-only stdlib allowlist", path)
			}
		}
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if ast.IsExported(declaration.Name.Name) && !allowedExports[declaration.Name.Name] {
					t.Fatalf("unexpected exported evolution function/method %q", declaration.Name.Name)
				}
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					switch specification := specification.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(specification.Name.Name) && !allowedExports[specification.Name.Name] {
							t.Fatalf("unexpected exported evolution type %q", specification.Name.Name)
						}
					case *ast.ValueSpec:
						for _, name := range specification.Names {
							if ast.IsExported(name.Name) && !allowedExports[name.Name] {
								t.Fatalf("unexpected exported evolution value %q", name.Name)
							}
						}
					}
				}
			}
		}
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

func TestPublicationFailureAfterMarkerRemovalIsOutcomeUnknownWhenRestoreFails(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	err = publicationFailureAfterMarkerRemoval(root, errors.New("final readback failed"))
	code, ok := CodeOf(err)
	if !ok || code != ErrorOutcomeUnknown {
		t.Fatalf("unrecoverable marker restoration was not classified as unknown: code=%q err=%v", code, err)
	}
}
