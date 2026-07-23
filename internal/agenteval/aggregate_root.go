package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	SyntheticRootAggregateSchemaVersion = 1
	maxSyntheticRootEntries             = 65_536
	maxSyntheticRootResults             = 4_096
	maxSyntheticRootResultBytes         = 256 << 20
)

// SyntheticRootAggregate binds an aggregate to the complete, immutable set of
// current synthetic result contracts found in one marked private output root.
// It deliberately contains neither source paths nor private-live identities.
type SyntheticRootAggregate struct {
	SchemaVersion int       `json:"schema_version"`
	SourceSHA256  string    `json:"source_sha256"`
	Results       int       `json:"results"`
	Cohorts       int       `json:"cohorts"`
	Aggregate     Aggregate `json:"aggregate"`
}

type syntheticRootEntry struct {
	path string
	info fs.FileInfo
}

type syntheticResultSlot struct {
	path     string
	scenario string
	provider string
	variant  string
	info     fs.FileInfo
}

type syntheticCohortIdentity struct {
	taskClass string
	dataClass string
	category  string
	surface   string
	runtime   Runtime
}

// AggregateSyntheticOutputRoot inventories and aggregates one complete marked
// synthetic output root. Errors expose only a closed reason code so a rejected
// private root cannot leak paths or result identities.
func AggregateSyntheticOutputRoot(root string) (SyntheticRootAggregate, error) {
	return aggregateSyntheticOutputRootWithHooks(root, nil, nil)
}

func aggregateSyntheticOutputRootWithHooks(root string, afterInitialInventory, afterPrimaryRead func()) (SyntheticRootAggregate, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("invalid_root")
	}
	if !ownerOnlyMode(rootInfo.Mode()) {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("unsafe_permissions")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("invalid_root")
	}
	defer func() { _ = rootHandle.Close() }()
	openedRootInfo, err := rootHandle.Stat(".")
	if err != nil || !openedRootInfo.IsDir() || !os.SameFile(rootInfo, openedRootInfo) {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("changed_during_read")
	}

	entries, slots, err := syntheticRootInventory(rootHandle)
	if err != nil {
		return SyntheticRootAggregate{}, err
	}
	if afterInitialInventory != nil {
		afterInitialInventory()
	}

	markerEntry, ok := findSyntheticRootEntry(entries, privateOutputRootMarker)
	if !ok {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("invalid_marker")
	}
	marker, err := readSyntheticRootFile(rootHandle, markerEntry, int64(len(privateOutputRootMarkerContents)))
	if err != nil || string(marker) != privateOutputRootMarkerContents {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("invalid_marker")
	}

	hash := sha256.New()
	_, _ = hash.Write([]byte("atl-agent-eval-synthetic-root-v1\x00"))
	var length [8]byte
	results := make([]Result, 0, len(slots))
	cohorts := map[string]syntheticCohortIdentity{}
	resultDigests := make(map[string][sha256.Size]byte, len(slots))
	totalBytes := int64(0)
	for _, slot := range slots {
		data, err := readSyntheticRootFile(rootHandle, syntheticRootEntry{path: slot.path, info: slot.info}, maxContractBytes)
		if err != nil {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("invalid_result")
		}
		totalBytes += int64(len(data))
		if totalBytes > maxSyntheticRootResultBytes {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("bounds")
		}
		result, err := DecodeResult(bytes.NewReader(data))
		if err != nil || result.SchemaVersion != ResultSchemaVersion ||
			result.Qualitative != nil || result.QualitativeReviewSet != nil {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("invalid_result")
		}
		if result.DataClass != "synthetic" {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("private_data")
		}
		if result.Runtime.PromptContractSHA256 == "" {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("invalid_result")
		}
		if _, public := publicCorpusTaskClasses[result.TaskClass]; !public {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("non_public_task_class")
		}
		if result.ScenarioID != slot.scenario || result.Runtime.Provider != slot.provider || result.Variant != slot.variant {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("identity_mismatch")
		}
		cohortPath := path.Join(slot.scenario, slot.provider, slot.variant)
		identity := syntheticCohortIdentity{
			taskClass: result.TaskClass,
			dataClass: result.DataClass,
			category:  result.EffectiveCategory(),
			surface:   result.EffectiveSurface(),
			runtime:   result.Runtime,
		}
		if previous, exists := cohorts[cohortPath]; exists && previous != identity {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("mixed_contract")
		}
		cohorts[cohortPath] = identity
		results = append(results, result)
		resultDigests[slot.path] = sha256.Sum256(data)

		relative := []byte(slot.path)
		binary.BigEndian.PutUint64(length[:], uint64(len(relative)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(relative)
		binary.BigEndian.PutUint64(length[:], uint64(len(data)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(data)
	}

	aggregate, err := AggregateResults(results)
	if err != nil {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("mixed_contract")
	}
	if len(aggregate.Groups) != len(cohorts) {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("mixed_contract")
	}
	if afterPrimaryRead != nil {
		afterPrimaryRead()
	}
	finalEntries, finalSlots, err := syntheticRootInventory(rootHandle)
	if err != nil || !sameSyntheticRootInventory(entries, finalEntries) {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("changed_during_read")
	}
	finalMarkerEntry, ok := findSyntheticRootEntry(finalEntries, privateOutputRootMarker)
	if !ok {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("changed_during_read")
	}
	finalMarker, err := readSyntheticRootFile(rootHandle, finalMarkerEntry, int64(len(privateOutputRootMarkerContents)))
	if err != nil || string(finalMarker) != privateOutputRootMarkerContents {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("changed_during_read")
	}
	if len(finalSlots) != len(resultDigests) {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("changed_during_read")
	}
	for _, slot := range finalSlots {
		data, err := readSyntheticRootFile(rootHandle, syntheticRootEntry{path: slot.path, info: slot.info}, maxContractBytes)
		expected, exists := resultDigests[slot.path]
		if err != nil || !exists || sha256.Sum256(data) != expected {
			return SyntheticRootAggregate{}, rejectSyntheticRoot("changed_during_read")
		}
	}
	verifiedEntries, _, err := syntheticRootInventory(rootHandle)
	if err != nil || !sameSyntheticRootInventory(finalEntries, verifiedEntries) {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("changed_during_read")
	}
	finalRootInfo, err := os.Lstat(root)
	if err != nil || finalRootInfo.Mode()&os.ModeSymlink != 0 || !finalRootInfo.IsDir() ||
		!os.SameFile(openedRootInfo, finalRootInfo) || !sameSyntheticRootInfo(openedRootInfo, finalRootInfo) {
		return SyntheticRootAggregate{}, rejectSyntheticRoot("changed_during_read")
	}
	return SyntheticRootAggregate{
		SchemaVersion: SyntheticRootAggregateSchemaVersion,
		SourceSHA256:  hex.EncodeToString(hash.Sum(nil)),
		Results:       len(results),
		Cohorts:       len(aggregate.Groups),
		Aggregate:     aggregate,
	}, nil
}

func syntheticRootInventory(root *os.Root) ([]syntheticRootEntry, []syntheticResultSlot, error) {
	var entries []syntheticRootEntry
	var slots []syntheticResultSlot
	runs := map[string]map[int]bool{}
	scenarioDirectories := map[string]bool{}
	providerDirectories := map[string]bool{}
	variantDirectories := map[string]bool{}
	markerSeen := false
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return rejectSyntheticRoot("unsafe_entry")
		}
		if name == "." {
			return nil
		}
		if len(entries) >= maxSyntheticRootEntries {
			return rejectSyntheticRoot("bounds")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return rejectSyntheticRoot("unsafe_entry")
		}
		info, err := entry.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return rejectSyntheticRoot("unsafe_entry")
		}
		if !ownerOnlyMode(info.Mode()) {
			return rejectSyntheticRoot("unsafe_permissions")
		}
		entryPath := path.Clean(strings.ReplaceAll(name, "\\", "/"))
		if !fs.ValidPath(entryPath) {
			return rejectSyntheticRoot("invalid_layout")
		}
		entries = append(entries, syntheticRootEntry{path: entryPath, info: info})
		parts := strings.Split(entryPath, "/")

		if parts[0] == privateOutputRootMarker {
			if len(parts) != 1 || info.IsDir() || markerSeen {
				return rejectSyntheticRoot("invalid_marker")
			}
			markerSeen = true
			return nil
		}
		if parts[0] == ".ephemeral" {
			if len(parts) == 1 && info.IsDir() {
				return nil
			}
			return rejectSyntheticRoot("scratch_residue")
		}
		if len(parts) == 1 {
			if !info.IsDir() || validatePathComponentID("scenario id", parts[0]) != nil {
				return rejectSyntheticRoot("invalid_layout")
			}
			scenarioDirectories[parts[0]] = false
			return nil
		}
		if len(parts) == 2 {
			if !info.IsDir() || parts[1] != "codex" && parts[1] != "claude-code" {
				return rejectSyntheticRoot("invalid_layout")
			}
			scenarioDirectories[parts[0]] = true
			providerDirectories[path.Join(parts[0], parts[1])] = false
			return nil
		}
		if len(parts) == 3 {
			if !info.IsDir() || validatePathComponentID("run variant", parts[2]) != nil {
				return rejectSyntheticRoot("invalid_layout")
			}
			providerDirectories[path.Join(parts[0], parts[1])] = true
			variantDirectories[path.Join(parts[0], parts[1], parts[2])] = false
			return nil
		}
		run, validRun := parseSyntheticRunDirectory(parts[3])
		cohortPath := path.Join(parts[0], parts[1], parts[2])
		if len(parts) == 4 {
			if !info.IsDir() || !validRun {
				return rejectSyntheticRoot("invalid_layout")
			}
			if runs[cohortPath] == nil {
				runs[cohortPath] = map[int]bool{}
			}
			if _, duplicate := runs[cohortPath][run]; duplicate {
				return rejectSyntheticRoot("invalid_layout")
			}
			runs[cohortPath][run] = false
			variantDirectories[cohortPath] = true
			return nil
		}
		if !validRun {
			return rejectSyntheticRoot("invalid_layout")
		}
		if parts[len(parts)-1] == "result.json" {
			if len(parts) != 5 || !info.Mode().IsRegular() || runs[cohortPath] == nil {
				return rejectSyntheticRoot("invalid_layout")
			}
			if runs[cohortPath][run] {
				return rejectSyntheticRoot("invalid_layout")
			}
			runs[cohortPath][run] = true
			slots = append(slots, syntheticResultSlot{
				path: entryPath, scenario: parts[0], provider: parts[1], variant: parts[2], info: info,
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if !markerSeen {
		return nil, nil, rejectSyntheticRoot("invalid_marker")
	}
	if len(slots) > maxSyntheticRootResults {
		return nil, nil, rejectSyntheticRoot("bounds")
	}
	for _, complete := range scenarioDirectories {
		if !complete {
			return nil, nil, rejectSyntheticRoot("incomplete_cohort")
		}
	}
	for _, complete := range providerDirectories {
		if !complete {
			return nil, nil, rejectSyntheticRoot("incomplete_cohort")
		}
	}
	for _, complete := range variantDirectories {
		if !complete {
			return nil, nil, rejectSyntheticRoot("incomplete_cohort")
		}
	}
	for _, cohortRuns := range runs {
		for run := 1; run <= len(cohortRuns); run++ {
			if complete, exists := cohortRuns[run]; !exists || !complete {
				return nil, nil, rejectSyntheticRoot("incomplete_cohort")
			}
		}
	}
	if len(slots) == 0 {
		return nil, nil, rejectSyntheticRoot("no_results")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	sort.Slice(slots, func(i, j int) bool { return slots[i].path < slots[j].path })
	return entries, slots, nil
}

func parseSyntheticRunDirectory(name string) (int, bool) {
	if len(name) != len("run-00") || !strings.HasPrefix(name, "run-") {
		return 0, false
	}
	run, err := strconv.Atoi(name[len("run-"):])
	return run, err == nil && run >= 1 && run <= 20
}

func readSyntheticRootFile(root *os.Root, entry syntheticRootEntry, limit int64) ([]byte, error) {
	file, err := root.Open(entry.path)
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(entry.info, openedInfo) || !sameSyntheticRootInfo(entry.info, openedInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("entry changed")
	}
	data, readErr := ioReadAllLimit(file, limit)
	finalInfo, finalStatErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if finalStatErr != nil || !os.SameFile(openedInfo, finalInfo) || !sameSyntheticRootInfo(openedInfo, finalInfo) || finalInfo.Size() != int64(len(data)) {
		return nil, fmt.Errorf("entry changed")
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}

func findSyntheticRootEntry(entries []syntheticRootEntry, name string) (syntheticRootEntry, bool) {
	for _, entry := range entries {
		if entry.path == name {
			return entry, true
		}
	}
	return syntheticRootEntry{}, false
}

func ownerOnlyMode(mode fs.FileMode) bool {
	return runtime.GOOS != "windows" && mode.Perm()&0o077 == 0
}

func sameSyntheticRootInfo(first, second fs.FileInfo) bool {
	return first.Size() == second.Size() && first.ModTime().Equal(second.ModTime()) && first.Mode() == second.Mode()
}

func sameSyntheticRootInventory(first, second []syntheticRootEntry) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index].path != second[index].path || !os.SameFile(first[index].info, second[index].info) ||
			!sameSyntheticRootInfo(first[index].info, second[index].info) {
			return false
		}
	}
	return true
}

func rejectSyntheticRoot(code string) error {
	return fmt.Errorf("synthetic output root rejected: %s", code)
}
