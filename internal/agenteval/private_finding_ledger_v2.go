package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateFindingLedgerV2SchemaVersion = 2
	PrivateFindingLedgerV2RelativePath  = "reports/finding-ledger.v2.json"

	PrivateFindingContractPlan      = "plan-contract"
	PrivateFindingContractPrompt    = "prompt-contract"
	PrivateFindingContractTask      = "task-contract"
	PrivateFindingContractExecution = "execution-contract"
	PrivateFindingContractATLBinary = "atl-executable"
	PrivateFindingContractRunner    = "runner-executable"
	PrivateFindingContractSkill     = "skill-digest"
)

type PrivateFindingLedgerV2 struct {
	SchemaVersion int                     `json:"schema_version"`
	Entries       []PrivateFindingEntryV2 `json:"entries"`
}

type PrivateFindingEntryV2 struct {
	FindingID        string                             `json:"finding_id"`
	Failure          PrivateFindingEvidenceRef          `json:"failure"`
	FailureClass     string                             `json:"failure_class"`
	ProductIssues    []int                              `json:"product_issues"`
	PullRequests     []int                              `json:"pull_requests,omitempty"`
	ChangedContracts []PrivateFindingContractTransition `json:"changed_contracts,omitempty"`
	Regression       *PrivateFindingEvidenceRef         `json:"regression,omitempty"`
	Decision         string                             `json:"decision"`
}

type PrivateFindingEvidenceRef struct {
	Source           string `json:"source"`
	PlanID           string `json:"plan_id,omitempty"`
	Surface          string `json:"surface,omitempty"`
	Baseline         string `json:"baseline,omitempty"`
	AssessmentSHA256 string `json:"assessment_sha256,omitempty"`
}

type PrivateFindingContractTransition struct {
	Kind         string `json:"kind"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
}

type privateFindingLedgerEntry struct {
	SchemaVersion               int
	FindingID                   string
	Failure                     PrivateFindingEvidenceRef
	FailureClass                string
	ProductIssues               []int
	PullRequests                []int
	ChangedContracts            []PrivateFindingContractTransition
	LegacyChangedContractSHA256 string
	Regression                  *PrivateFindingEvidenceRef
	Decision                    string
}

type privateFindingLedgerDocument struct {
	Entries   []privateFindingLedgerEntry
	Canonical []byte
}

func loadPrivateFindingLedger(root string) (privateFindingLedgerDocument, error) {
	version, data, err := readPrivateFindingLedger(root)
	if err != nil {
		return privateFindingLedgerDocument{}, err
	}
	switch version {
	case PrivateFindingLedgerSchemaVersion:
		// Mixed: decodable but non-canonical bytes are rejected by the byte
		// comparison alone and leave nil here. The decode or envelope-validation
		// failure is passed unguarded because codedError drops a nil cause.
		ledger, canonical, decodeErr := decodePrivateFindingLedger(data)
		if decodeErr != nil || !bytes.Equal(data, canonical) {
			return privateFindingLedgerDocument{}, privateFindingError("ledger_contract", decodeErr)
		}
		return privateFindingLedgerDocument{Entries: normalizePrivateFindingLedger(ledger), Canonical: canonical}, nil
	case PrivateFindingLedgerV2SchemaVersion:
		// Same mixed branch on the schema-v2 side.
		ledger, canonical, decodeErr := decodePrivateFindingLedgerV2(data)
		if decodeErr != nil || !bytes.Equal(data, canonical) {
			return privateFindingLedgerDocument{}, privateFindingError("ledger_contract", decodeErr)
		}
		return privateFindingLedgerDocument{Entries: normalizePrivateFindingLedgerV2(ledger), Canonical: canonical}, nil
	default:
		// The reader only reports the two known schema versions, so this stays a
		// defensive classification with nothing in hand.
		return privateFindingLedgerDocument{}, privateFindingError("ledger_file")
	}
}

func readPrivateFindingLedger(root string) (int, []byte, error) {
	return readPrivateFindingLedgerWithHook(root, nil)
}

func readPrivateFindingLedgerWithHook(root string, afterInventory func()) (int, []byte, error) {
	directory := filepath.Join(root, "reports")
	// Mixed: an observed directory type or permission rejects on its own and
	// leaves nil, while a failed probe keeps the failure it holds.
	directoryInfo, err := safepath.StatWithin(root, directory)
	if err != nil || !directoryInfo.IsDir() ||
		(runtime.GOOS != "windows" && directoryInfo.Mode().Perm() != 0o700) {
		return 0, nil, privateFindingError("ledger_directory", err)
	}
	handle, err := os.OpenRoot(directory)
	if err != nil {
		return 0, nil, privateFindingError("ledger_directory", err)
	}
	defer func() { _ = handle.Close() }()
	// Mixed again: only the stat can fail, and the directory-identity
	// comparisons beside it have nothing to attach.
	openedDirectory, err := handle.Stat(".")
	if err != nil || !openedDirectory.IsDir() || !os.SameFile(directoryInfo, openedDirectory) ||
		!sameSyntheticRootInfo(directoryInfo, openedDirectory) {
		return 0, nil, privateFindingError("ledger_directory", err)
	}
	legacyName := filepath.Base(PrivateFindingLedgerRelativePath)
	currentName := filepath.Base(PrivateFindingLedgerV2RelativePath)
	legacyBefore, legacyExists, err := privateFindingLedgerEntryInfo(handle, legacyName)
	if err != nil {
		return 0, nil, err
	}
	currentBefore, currentExists, err := privateFindingLedgerEntryInfo(handle, currentName)
	if err != nil {
		return 0, nil, err
	}
	if legacyExists && currentExists {
		// Both candidates probed cleanly; the pair itself is the rejection.
		return 0, nil, privateFindingError("ledger_ambiguous")
	}
	if !legacyExists && !currentExists {
		// Ordinary absence on both probes, which is not a failure to retain.
		return 0, nil, privateFindingError("ledger_file")
	}
	if afterInventory != nil {
		afterInventory()
	}
	version, name, before := PrivateFindingLedgerSchemaVersion, legacyName, legacyBefore
	if currentExists {
		version, name, before = PrivateFindingLedgerV2SchemaVersion, currentName, currentBefore
	}
	file, err := handle.Open(name)
	if err != nil {
		return 0, nil, privateFindingError("ledger_read", err)
	}
	// Mixed: the opened file's type and the identity comparison against the
	// inventoried candidate reject on their own; only the stat has a cause.
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		!sameSyntheticRootInfo(before, opened) {
		_ = file.Close()
		return 0, nil, privateFindingError("ledger_file", statErr)
	}
	data, readErr := ioReadAllLimit(file, privateFindingLedgerMaxBytes)
	final, finalErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return 0, nil, privateFindingError("ledger_read", readErr)
	}
	// The final stat and the close each hold a failure and are attached in that
	// fixed order; the identity and size comparisons beside them attach nothing.
	if finalErr != nil || closeErr != nil || !os.SameFile(opened, final) ||
		!sameSyntheticRootInfo(opened, final) || final.Size() != int64(len(data)) {
		return 0, nil, privateFindingError("ledger_file", finalErr, closeErr)
	}
	legacyAfter, legacyStillExists, err := privateFindingLedgerEntryInfo(handle, legacyName)
	if err != nil {
		return 0, nil, err
	}
	currentAfter, currentStillExists, err := privateFindingLedgerEntryInfo(handle, currentName)
	if err != nil {
		return 0, nil, err
	}
	// The entry-inventory comparisons run before the directory probes, preserving
	// the original short-circuit and observation order. They have nothing to
	// attach.
	if legacyExists != legacyStillExists || currentExists != currentStillExists ||
		legacyExists && (!os.SameFile(legacyBefore, legacyAfter) || !sameSyntheticRootInfo(legacyBefore, legacyAfter)) ||
		currentExists && (!os.SameFile(currentBefore, currentAfter) || !sameSyntheticRootInfo(currentBefore, currentAfter)) {
		return 0, nil, privateFindingError("ledger_file")
	}
	// The directory recheck observes the retained handle and then the ambient
	// path in the same order as before. Its two probe failures remain available
	// to the outer classification in final-then-ambient order.
	directoryStable, finalDirectoryErr, ambientDirectoryErr :=
		privateFindingLedgerDirectoryStable(root, directory, handle, openedDirectory)
	if !directoryStable {
		return 0, nil, privateFindingError("ledger_file", finalDirectoryErr, ambientDirectoryErr)
	}
	return version, data, nil
}

func privateFindingLedgerEntryInfo(root *os.Root, name string) (os.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	// An absent candidate is ordinary absence, not a failure to classify.
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	// Mixed: an observed file type or mode rejects on its own and leaves nil,
	// while any other probe failure keeps its cause.
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return nil, false, privateFindingError("ledger_file", err)
	}
	return info, true, nil
}

// privateFindingLedgerDirectoryStable rechecks that the reports directory read
// through the retained handle is still the same directory the ambient path
// resolves to. It reports its final-handle and ambient probe failures beside
// the verdict so the caller can retain them under its own classification; the
// identity rules that decide stability are unchanged.
func privateFindingLedgerDirectoryStable(
	root, directory string, handle *os.Root, opened os.FileInfo,
) (bool, error, error) {
	final, err := handle.Stat(".")
	ambient, ambientErr := safepath.StatWithin(root, directory)
	stable := err == nil && ambientErr == nil &&
		os.SameFile(opened, final) && sameSyntheticRootInfo(opened, final) &&
		os.SameFile(opened, ambient) && sameSyntheticRootInfo(opened, ambient)
	return stable, err, ambientErr
}

func decodePrivateFindingLedgerV2(data []byte) (PrivateFindingLedgerV2, []byte, error) {
	var ledger PrivateFindingLedgerV2
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ledger); err != nil {
		return ledger, nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ledger, nil, fmt.Errorf("trailing data")
	}
	if err := ledger.validate(); err != nil {
		return ledger, nil, err
	}
	canonical, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return ledger, nil, err
	}
	return ledger, append(canonical, '\n'), nil
}

func (ledger PrivateFindingLedgerV2) validate() error {
	if ledger.SchemaVersion != PrivateFindingLedgerV2SchemaVersion ||
		len(ledger.Entries) == 0 || len(ledger.Entries) > 4096 {
		return fmt.Errorf("invalid ledger envelope")
	}
	previous := ""
	for _, entry := range ledger.Entries {
		if !pathComponentIDRE.MatchString(entry.FindingID) || entry.FindingID == "." || entry.FindingID == ".." ||
			entry.FindingID <= previous || !validPrivateFindingEvidenceRef(entry.Failure) ||
			!validPrivateFailureClass(entry.FailureClass) || !validPrivateFindingDecision(entry.Decision) ||
			len(entry.ProductIssues) > 4096 || len(entry.PullRequests) > 4096 ||
			!validSortedPositiveIDs(entry.ProductIssues) || !validSortedPositiveIDsOptional(entry.PullRequests) ||
			!validPrivateFindingTransitions(entry.ChangedContracts) ||
			(len(entry.PullRequests) == 0) != (len(entry.ChangedContracts) == 0) {
			return fmt.Errorf("invalid finding")
		}
		previous = entry.FindingID
		if entry.Regression != nil {
			if !validPrivateFindingEvidenceRef(*entry.Regression) ||
				entry.Regression.Source != entry.Failure.Source ||
				privateFindingEvidenceRefKey(*entry.Regression) == privateFindingEvidenceRefKey(entry.Failure) {
				return fmt.Errorf("invalid regression")
			}
		}
		if len(entry.ChangedContracts) > 0 && entry.Regression == nil {
			return fmt.Errorf("change without regression")
		}
		if entry.Decision == PrivateFindingDecisionFixed &&
			(entry.Regression == nil || len(entry.PullRequests) == 0 || len(entry.ChangedContracts) == 0) {
			return fmt.Errorf("incomplete fixed finding")
		}
		if entry.Failure.Source == PrivateFindingAcceptanceSourcePrivateLive {
			if len(entry.ChangedContracts) > 0 &&
				(len(entry.ChangedContracts) != 1 || entry.ChangedContracts[0].Kind != PrivateFindingContractPlan) {
				return fmt.Errorf("invalid private-live transition")
			}
		} else {
			for _, transition := range entry.ChangedContracts {
				if transition.Kind == PrivateFindingContractPlan {
					return fmt.Errorf("invalid synthetic transition")
				}
			}
		}
	}
	return nil
}

func validPrivateFindingEvidenceRef(ref PrivateFindingEvidenceRef) bool {
	switch ref.Source {
	case PrivateFindingAcceptanceSourcePrivateLive:
		return validPrivateFindingRef(PrivateFindingRunRef{PlanID: ref.PlanID, Surface: ref.Surface, Baseline: ref.Baseline}) &&
			ref.AssessmentSHA256 == ""
	case PrivateFindingAcceptanceSourceSyntheticRoot:
		return ref.PlanID == "" && ref.Surface == "" && ref.Baseline == "" && validSHA256(ref.AssessmentSHA256)
	default:
		return false
	}
}

func validPrivateFindingTransitions(transitions []PrivateFindingContractTransition) bool {
	previous := ""
	for _, transition := range transitions {
		if transition.Kind <= previous || !validPrivateFindingContractKind(transition.Kind) ||
			!validSHA256(transition.BeforeSHA256) || !validSHA256(transition.AfterSHA256) ||
			transition.BeforeSHA256 == transition.AfterSHA256 {
			return false
		}
		previous = transition.Kind
	}
	return true
}

func validPrivateFindingContractKind(kind string) bool {
	switch kind {
	case PrivateFindingContractPlan, PrivateFindingContractPrompt, PrivateFindingContractTask,
		PrivateFindingContractExecution, PrivateFindingContractATLBinary, PrivateFindingContractRunner,
		PrivateFindingContractSkill:
		return true
	default:
		return false
	}
}

func validSortedPositiveIDs(values []int) bool {
	return len(values) > 0 && validSortedPositiveIDsOptional(values)
}

func validSortedPositiveIDsOptional(values []int) bool {
	previous := 0
	for _, value := range values {
		if value <= previous {
			return false
		}
		previous = value
	}
	return true
}

func normalizePrivateFindingLedger(ledger PrivateFindingLedger) []privateFindingLedgerEntry {
	entries := make([]privateFindingLedgerEntry, 0, len(ledger.Entries))
	for _, entry := range ledger.Entries {
		normalized := privateFindingLedgerEntry{
			SchemaVersion:               PrivateFindingLedgerSchemaVersion,
			FindingID:                   entry.FindingID,
			Failure:                     privateFindingEvidenceRefFromRun(entry.Failure),
			FailureClass:                entry.FailureClass,
			ProductIssues:               []int{entry.ProductIssue},
			LegacyChangedContractSHA256: entry.ChangedContractSHA256,
			Decision:                    entry.Decision,
		}
		if entry.PullRequest > 0 {
			normalized.PullRequests = []int{entry.PullRequest}
		}
		if entry.Regression != nil {
			regression := privateFindingEvidenceRefFromRun(*entry.Regression)
			normalized.Regression = &regression
		}
		entries = append(entries, normalized)
	}
	return entries
}

func normalizePrivateFindingLedgerV2(ledger PrivateFindingLedgerV2) []privateFindingLedgerEntry {
	entries := make([]privateFindingLedgerEntry, 0, len(ledger.Entries))
	for _, entry := range ledger.Entries {
		entries = append(entries, privateFindingLedgerEntry{
			SchemaVersion: PrivateFindingLedgerV2SchemaVersion,
			FindingID:     entry.FindingID, Failure: entry.Failure, FailureClass: entry.FailureClass,
			ProductIssues: entry.ProductIssues, PullRequests: entry.PullRequests,
			ChangedContracts: entry.ChangedContracts, Regression: entry.Regression, Decision: entry.Decision,
		})
	}
	return entries
}

func privateFindingEvidenceRefFromRun(ref PrivateFindingRunRef) PrivateFindingEvidenceRef {
	return PrivateFindingEvidenceRef{
		Source: PrivateFindingAcceptanceSourcePrivateLive,
		PlanID: ref.PlanID, Surface: ref.Surface, Baseline: ref.Baseline,
	}
}

func privateFindingRunRef(ref PrivateFindingEvidenceRef) PrivateFindingRunRef {
	return PrivateFindingRunRef{PlanID: ref.PlanID, Surface: ref.Surface, Baseline: ref.Baseline}
}

func privateFindingEvidenceRefKey(ref PrivateFindingEvidenceRef) string {
	if ref.Source == PrivateFindingAcceptanceSourceSyntheticRoot {
		return ref.Source + "\x00" + ref.AssessmentSHA256
	}
	return ref.Source + "\x00" + ref.PlanID + "\x00" + ref.Surface
}
