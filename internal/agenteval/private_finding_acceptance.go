package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateFindingAcceptanceSchemaVersion = 1
	PrivateFindingAcceptanceRelativePath  = "reports/finding-acceptance.v1.json"
)

type PrivateFindingAcceptanceIndex struct {
	SchemaVersion int                             `json:"schema_version"`
	Entries       []PrivateFindingAcceptanceEntry `json:"entries"`
}

type PrivateFindingAcceptanceEntry struct {
	FindingID        string `json:"finding_id"`
	AssessmentSHA256 string `json:"assessment_sha256"`
}

func loadPrivateFindingAcceptance(root string, ledger PrivateFindingLedger) (map[string]string, []byte, error) {
	fixed := make(map[string]struct{})
	for _, entry := range ledger.Entries {
		if entry.Decision == PrivateFindingDecisionFixed {
			fixed[entry.FindingID] = struct{}{}
		}
	}
	path := filepath.Join(root, filepath.FromSlash(PrivateFindingAcceptanceRelativePath))
	info, err := safepath.StatWithin(root, path)
	if len(fixed) == 0 {
		if os.IsNotExist(err) {
			return map[string]string{}, nil, nil
		}
		return nil, nil, privateFindingError("unexpected_acceptance")
	}
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return nil, nil, privateFindingError("acceptance_file")
	}
	data, err := safepath.ReadFileWithinLimit(root, path, privateFindingLedgerMaxBytes)
	if err != nil {
		return nil, nil, privateFindingError("acceptance_read")
	}
	index, canonical, err := decodePrivateFindingAcceptance(data)
	if err != nil || !bytes.Equal(data, canonical) || len(index.Entries) != len(fixed) {
		return nil, nil, privateFindingError("acceptance_contract")
	}
	bindings := make(map[string]string, len(index.Entries))
	seenAssessments := make(map[string]struct{}, len(index.Entries))
	for _, entry := range index.Entries {
		if _, exists := fixed[entry.FindingID]; !exists {
			return nil, nil, privateFindingError("acceptance_finding")
		}
		if _, exists := seenAssessments[entry.AssessmentSHA256]; exists {
			return nil, nil, privateFindingError("acceptance_reuse")
		}
		bindings[entry.FindingID] = entry.AssessmentSHA256
		seenAssessments[entry.AssessmentSHA256] = struct{}{}
	}
	if len(bindings) != len(fixed) {
		return nil, nil, privateFindingError("acceptance_missing")
	}
	return bindings, canonical, nil
}

func decodePrivateFindingAcceptance(data []byte) (PrivateFindingAcceptanceIndex, []byte, error) {
	var index PrivateFindingAcceptanceIndex
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return index, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return index, nil, fmt.Errorf("trailing data")
	}
	if err := index.validate(); err != nil {
		return index, nil, err
	}
	canonical, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return index, nil, err
	}
	return index, append(canonical, '\n'), nil
}

func (index PrivateFindingAcceptanceIndex) validate() error {
	if index.SchemaVersion != PrivateFindingAcceptanceSchemaVersion || len(index.Entries) == 0 || len(index.Entries) > 4096 {
		return fmt.Errorf("invalid acceptance envelope")
	}
	previous := ""
	for _, entry := range index.Entries {
		if !pathComponentIDRE.MatchString(entry.FindingID) || entry.FindingID == "." || entry.FindingID == ".." ||
			entry.FindingID <= previous || !validSHA256(entry.AssessmentSHA256) {
			return fmt.Errorf("invalid acceptance entry")
		}
		previous = entry.FindingID
	}
	return nil
}
