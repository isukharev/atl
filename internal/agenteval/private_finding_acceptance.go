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
	PrivateFindingAcceptanceSchemaVersion = 1
	PrivateFindingAcceptanceRelativePath  = "reports/finding-acceptance.v1.json"

	PrivateFindingAcceptanceV2SchemaVersion = 2
	PrivateFindingAcceptanceV2RelativePath  = "reports/finding-acceptance.v2.json"

	PrivateFindingAcceptanceSourcePrivateLive   = "private-live"
	PrivateFindingAcceptanceSourceSyntheticRoot = "synthetic-root"
)

type PrivateFindingAcceptanceIndex struct {
	SchemaVersion int                             `json:"schema_version"`
	Entries       []PrivateFindingAcceptanceEntry `json:"entries"`
}

type PrivateFindingAcceptanceEntry struct {
	FindingID        string `json:"finding_id"`
	AssessmentSHA256 string `json:"assessment_sha256"`
}

type PrivateFindingAcceptanceV2Index struct {
	SchemaVersion int                               `json:"schema_version"`
	Entries       []PrivateFindingAcceptanceV2Entry `json:"entries"`
}

type PrivateFindingAcceptanceV2Entry struct {
	FindingID            string `json:"finding_id"`
	AssessmentSHA256     string `json:"assessment_sha256"`
	AssessmentSource     string `json:"assessment_source"`
	PromptContractSHA256 string `json:"prompt_contract_sha256,omitempty"`
}

type privateFindingAcceptanceBinding struct {
	AssessmentSHA256     string
	AssessmentSource     string
	PromptContractSHA256 string
}

func loadPrivateFindingAcceptance(root string, ledger PrivateFindingLedger) (map[string]privateFindingAcceptanceBinding, []byte, error) {
	fixed := make(map[string]struct{})
	for _, entry := range ledger.Entries {
		if entry.Decision == PrivateFindingDecisionFixed {
			fixed[entry.FindingID] = struct{}{}
		}
	}
	version, data, err := readPrivateFindingAcceptance(root)
	if err != nil {
		return nil, nil, err
	}
	if len(fixed) == 0 {
		if version == 0 {
			return map[string]privateFindingAcceptanceBinding{}, nil, nil
		}
		return nil, nil, privateFindingError("unexpected_acceptance")
	}
	if version == 0 {
		return nil, nil, privateFindingError("acceptance_file")
	}
	var entries []PrivateFindingAcceptanceV2Entry
	var canonical []byte
	switch version {
	case PrivateFindingAcceptanceSchemaVersion:
		index, encoded, decodeErr := decodePrivateFindingAcceptance(data)
		if decodeErr != nil || !bytes.Equal(data, encoded) {
			return nil, nil, privateFindingError("acceptance_contract")
		}
		canonical = encoded
		entries = make([]PrivateFindingAcceptanceV2Entry, 0, len(index.Entries))
		for _, entry := range index.Entries {
			entries = append(entries, PrivateFindingAcceptanceV2Entry{
				FindingID: entry.FindingID, AssessmentSHA256: entry.AssessmentSHA256,
				AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive,
			})
		}
	case PrivateFindingAcceptanceV2SchemaVersion:
		index, encoded, decodeErr := decodePrivateFindingAcceptanceV2(data)
		if decodeErr != nil || !bytes.Equal(data, encoded) {
			return nil, nil, privateFindingError("acceptance_contract")
		}
		canonical = encoded
		entries = index.Entries
	default:
		return nil, nil, privateFindingError("acceptance_contract")
	}
	if len(entries) != len(fixed) {
		return nil, nil, privateFindingError("acceptance_contract")
	}
	bindings := make(map[string]privateFindingAcceptanceBinding, len(entries))
	seenAssessments := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, exists := fixed[entry.FindingID]; !exists {
			return nil, nil, privateFindingError("acceptance_finding")
		}
		if _, exists := seenAssessments[entry.AssessmentSHA256]; exists {
			return nil, nil, privateFindingError("acceptance_reuse")
		}
		bindings[entry.FindingID] = privateFindingAcceptanceBinding{
			AssessmentSHA256:     entry.AssessmentSHA256,
			AssessmentSource:     entry.AssessmentSource,
			PromptContractSHA256: entry.PromptContractSHA256,
		}
		seenAssessments[entry.AssessmentSHA256] = struct{}{}
	}
	if len(bindings) != len(fixed) {
		return nil, nil, privateFindingError("acceptance_missing")
	}
	return bindings, canonical, nil
}

func readPrivateFindingAcceptance(root string) (int, []byte, error) {
	return readPrivateFindingAcceptanceWithHook(root, nil)
}

func readPrivateFindingAcceptanceWithHook(root string, afterInventory func()) (int, []byte, error) {
	directory := filepath.Join(root, "reports")
	directoryInfo, err := safepath.StatWithin(root, directory)
	if err != nil || !directoryInfo.IsDir() ||
		(runtime.GOOS != "windows" && directoryInfo.Mode().Perm() != 0o700) {
		return 0, nil, privateFindingError("acceptance_directory")
	}
	handle, err := os.OpenRoot(directory)
	if err != nil {
		return 0, nil, privateFindingError("acceptance_directory")
	}
	defer func() { _ = handle.Close() }()
	openedDirectory, err := handle.Stat(".")
	if err != nil || !openedDirectory.IsDir() || !os.SameFile(directoryInfo, openedDirectory) ||
		!sameSyntheticRootInfo(directoryInfo, openedDirectory) {
		return 0, nil, privateFindingError("acceptance_directory")
	}
	legacyName := filepath.Base(PrivateFindingAcceptanceRelativePath)
	currentName := filepath.Base(PrivateFindingAcceptanceV2RelativePath)
	legacyBefore, legacyExists, err := privateFindingAcceptanceEntry(handle, legacyName)
	if err != nil {
		return 0, nil, err
	}
	currentBefore, currentExists, err := privateFindingAcceptanceEntry(handle, currentName)
	if err != nil {
		return 0, nil, err
	}
	if legacyExists && currentExists {
		return 0, nil, privateFindingError("acceptance_ambiguous")
	}
	if afterInventory != nil {
		afterInventory()
	}
	if !legacyExists && !currentExists {
		legacyAfter, legacyStillExists, legacyErr := privateFindingAcceptanceEntry(handle, legacyName)
		currentAfter, currentStillExists, currentErr := privateFindingAcceptanceEntry(handle, currentName)
		if legacyErr != nil || currentErr != nil || legacyStillExists || currentStillExists ||
			legacyAfter != nil || currentAfter != nil ||
			!privateFindingAcceptanceDirectoryStable(root, directory, handle, openedDirectory) {
			return 0, nil, privateFindingError("acceptance_file")
		}
		return 0, nil, nil
	}
	version, name, before := PrivateFindingAcceptanceSchemaVersion, legacyName, legacyBefore
	if currentExists {
		version, name, before = PrivateFindingAcceptanceV2SchemaVersion, currentName, currentBefore
	}
	file, err := handle.Open(name)
	if err != nil {
		return 0, nil, privateFindingError("acceptance_read")
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		!sameSyntheticRootInfo(before, opened) {
		_ = file.Close()
		return 0, nil, privateFindingError("acceptance_file")
	}
	data, readErr := ioReadAllLimit(file, privateFindingLedgerMaxBytes)
	final, finalErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return 0, nil, privateFindingError("acceptance_read")
	}
	if finalErr != nil || closeErr != nil || !os.SameFile(opened, final) ||
		!sameSyntheticRootInfo(opened, final) || final.Size() != int64(len(data)) {
		return 0, nil, privateFindingError("acceptance_file")
	}
	legacyAfter, legacyStillExists, err := privateFindingAcceptanceEntry(handle, legacyName)
	if err != nil {
		return 0, nil, err
	}
	currentAfter, currentStillExists, err := privateFindingAcceptanceEntry(handle, currentName)
	if err != nil {
		return 0, nil, err
	}
	if legacyExists != legacyStillExists || currentExists != currentStillExists ||
		legacyExists && (!os.SameFile(legacyBefore, legacyAfter) || !sameSyntheticRootInfo(legacyBefore, legacyAfter)) ||
		currentExists && (!os.SameFile(currentBefore, currentAfter) || !sameSyntheticRootInfo(currentBefore, currentAfter)) {
		return 0, nil, privateFindingError("acceptance_file")
	}
	if !privateFindingAcceptanceDirectoryStable(root, directory, handle, openedDirectory) {
		return 0, nil, privateFindingError("acceptance_directory")
	}
	return version, data, nil
}

func privateFindingAcceptanceDirectoryStable(root, directory string, handle *os.Root, opened os.FileInfo) bool {
	final, err := handle.Stat(".")
	ambient, ambientErr := safepath.StatWithin(root, directory)
	return err == nil && ambientErr == nil &&
		os.SameFile(opened, final) && sameSyntheticRootInfo(opened, final) &&
		os.SameFile(opened, ambient) && sameSyntheticRootInfo(opened, ambient)
}

func privateFindingAcceptanceEntry(root *os.Root, name string) (os.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return nil, false, privateFindingError("acceptance_file")
	}
	return info, true, nil
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

func decodePrivateFindingAcceptanceV2(data []byte) (PrivateFindingAcceptanceV2Index, []byte, error) {
	var index PrivateFindingAcceptanceV2Index
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

func (index PrivateFindingAcceptanceV2Index) validate() error {
	if index.SchemaVersion != PrivateFindingAcceptanceV2SchemaVersion ||
		len(index.Entries) == 0 || len(index.Entries) > 4096 {
		return fmt.Errorf("invalid acceptance envelope")
	}
	previous := ""
	for _, entry := range index.Entries {
		validPromptBinding := false
		switch entry.AssessmentSource {
		case PrivateFindingAcceptanceSourcePrivateLive:
			validPromptBinding = entry.PromptContractSHA256 == ""
		case PrivateFindingAcceptanceSourceSyntheticRoot:
			validPromptBinding = validSHA256(entry.PromptContractSHA256)
		}
		if !pathComponentIDRE.MatchString(entry.FindingID) || entry.FindingID == "." || entry.FindingID == ".." ||
			entry.FindingID <= previous || !validSHA256(entry.AssessmentSHA256) || !validPromptBinding {
			return fmt.Errorf("invalid acceptance entry")
		}
		previous = entry.FindingID
	}
	return nil
}
