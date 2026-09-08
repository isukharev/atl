package mirror

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	CompletePullInspectionMaxEntries = 128
	CompletePullInspectionMaxBytes   = int64(64 << 20)
)

// CompletePullInspection describes metadata only. RecoveryPending does not
// assert that native artifacts or interrupted writes can be recovered safely.
type CompletePullInspection struct {
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	Recovery     string `json:"recovery"`
	Checkpoints  int    `json:"checkpoints"`
	Selected     int    `json:"selected"`
	Completed    int    `json:"completed"`
	Remaining    int    `json:"remaining"`
	Journals     int    `json:"journals"`
	Publications int    `json:"publications"`
	Complete     bool   `json:"complete"`
	Healthy      bool   `json:"healthy"`
}

type completePullInspector struct {
	m          *Mirror
	remaining  int64
	result     CompletePullInspection
	ownedTemps map[string]int
	siblings   []CompletePullCheckpoint
}

type completePullFamily struct{ checkpoint, progress, journal, publication bool }

// InspectJiraCompletePulls must run under the caller's existing Jira mirror
// snapshot guard. It never creates a lock, recovers, retires or writes state.
func (m *Mirror) InspectJiraCompletePulls() (CompletePullInspection, error) {
	i := completePullInspector{m: m, remaining: CompletePullInspectionMaxBytes, ownedTemps: make(map[string]int), result: CompletePullInspection{Status: "empty", Recovery: "none", Complete: true, Healthy: true}}
	err := i.inspect()
	if err != nil {
		if i.result.Reason == "" {
			i.result.Status = "invalid"
			i.result.Reason = "unreadable"
		}
		i.result.Recovery = "preserve_for_inspection"
		i.result.Complete = false
		i.result.Healthy = false
		return i.result, domain.ErrCheckFailed
	}
	if i.result.Journals+i.result.Publications > 0 {
		i.result.Status = "recovery_pending"
		i.result.Recovery = "preserve_for_inspection"
		i.result.Healthy = false
		return i.result, domain.ErrCheckFailed
	}
	if i.result.Checkpoints > 0 {
		i.result.Status = "resumable"
		i.result.Recovery = "rerun_original_command"
	}
	return i.result, nil
}

func (i *completePullInspector) reject(reason string) error {
	i.result.Status = "invalid"
	if reason == "entry_limit" || reason == "byte_limit" {
		i.result.Status = "inventory_limit"
	}
	i.result.Reason = reason
	return domain.ErrCheckFailed
}

func (i *completePullInspector) inspect() error {
	directory := filepath.Join(i.m.Root, ".atl", "complete-pulls")
	entries, err := i.directory(directory, CompletePullInspectionMaxEntries)
	if err != nil {
		return err
	}
	families := map[string]*completePullFamily{}
	var temps []os.DirEntry
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".atl-cp-") {
			temps = append(temps, entry)
			continue
		}
		kind := "checkpoint"
		selector := ""
		switch {
		case strings.HasSuffix(name, ".progress.json"):
			selector = strings.TrimSuffix(name, ".progress.json")
			kind = "progress"
		case strings.HasSuffix(name, ".journal.json"):
			selector = strings.TrimSuffix(name, ".journal.json")
			kind = "journal"
		case strings.HasSuffix(name, ".publish"):
			selector = strings.TrimSuffix(name, ".publish")
			kind = "publication"
		case strings.HasSuffix(name, ".json"):
			selector = strings.TrimSuffix(name, ".json")
		default:
			return i.reject("orphaned")
		}
		if !validSHA256(selector) {
			return i.reject("orphaned")
		}
		family := families[selector]
		if family == nil {
			family = &completePullFamily{}
			families[selector] = family
		}
		switch kind {
		case "checkpoint":
			family.checkpoint = true
		case "progress":
			family.progress = true
		case "journal":
			family.journal = true
		case "publication":
			family.publication = true
		}
	}
	selectors := make([]string, 0, len(families))
	for selector := range families {
		selectors = append(selectors, selector)
	}
	sort.Strings(selectors)
	for _, selector := range selectors {
		if err := i.family(selector, *families[selector]); err != nil {
			return err
		}
	}
	for _, checkpoint := range i.siblings {
		if i.tempsOwned(temps) {
			break
		}
		if err := i.siblingOwnership(checkpoint, temps); err != nil {
			return err
		}
	}
	for _, entry := range temps {
		maximum, owned := i.ownedTemps[entry.Name()]
		if !owned {
			return i.reject("orphaned")
		}
		info, err := safepath.StatWithin(i.m.Root, filepath.Join(directory, entry.Name()))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 0 {
			return i.reject("malformed")
		}
		if info.Size() > min(int64(maximum), i.remaining) {
			return i.reject("byte_limit")
		}
		i.remaining -= info.Size()
	}
	return nil
}

func (i *completePullInspector) tempsOwned(temps []os.DirEntry) bool {
	for _, entry := range temps {
		if _, owned := i.ownedTemps[entry.Name()]; !owned {
			return false
		}
	}
	return true
}

// Only unresolved global temps require sibling transaction evidence. Validate
// their surviving checkpoint binding and token, without reading progress or
// payloads or asserting sibling health/recoverability. Concurrent replacement
// can refuse this qualification; it must never authorize cleanup.
func (i *completePullInspector) siblingOwnership(checkpoint CompletePullCheckpoint, temps []os.DirEntry) error {
	journalPath, _ := i.m.completePullJournalPath(checkpoint.SelectorSHA256)
	body, found, err := i.read(journalPath, maxCompletePullJournalBytes)
	if err != nil {
		return err
	}
	if found {
		var journal completePullJournal
		if err := i.decode(body, &journal, func(schema int) bool { return validCompletePullJournalSchema(checkpoint.Service, schema) }); err != nil {
			return err
		}
		if validateCompletePullJournal(journal, checkpoint) != nil {
			return i.reject("malformed")
		}
		i.ownTemps(journal.WriteToken)
	}
	if i.tempsOwned(temps) {
		return nil
	}
	directory, _ := i.m.completePullPublicationDir(checkpoint.SelectorSHA256)
	info, err := safepath.StatWithin(i.m.Root, directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return i.reject("malformed")
	}
	body, found, err = i.read(filepath.Join(directory, "intent.json"), maxCompletePullPublicationIntent)
	if err != nil || !found {
		return err
	}
	var intent completePullPublicationIntent
	if err := i.decode(body, &intent, func(schema int) bool { return validCompletePullPublicationSchema(checkpoint.Service, schema) }); err != nil {
		return err
	}
	if validateCompletePullPublication(intent, checkpoint, "") != nil {
		return i.reject("malformed")
	}
	i.ownTemps(intent.WriteToken)
	return nil
}

func (i *completePullInspector) ownTemps(token string) {
	i.ownedTemps[completePullJournalTemp(token)] = maxCompletePullJournalBytes
	i.ownedTemps[completePullProgressTemp(token)] = maxCompletePullProgressBytes
}

func (i *completePullInspector) debitMetadata(size int64) error {
	if size < 0 || size > i.remaining {
		return i.reject("byte_limit")
	}
	i.remaining -= size
	return nil
}

// Inspect the schema before strict decoding so a future schema with new
// members remains distinguishable from malformed current metadata.
func (i *completePullInspector) decode(body []byte, value any, supported func(int) bool) error {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if json.Unmarshal(body, &header) != nil {
		return i.reject("malformed")
	}
	if !supported(header.SchemaVersion) {
		return i.reject("unsupported_schema")
	}
	if decodeCompletePullJSON("", body, value) != nil {
		return i.reject("malformed")
	}
	return nil
}

// directory combines the existing no-symlink metadata qualification with a
// root-contained bounded read, retaining a typed local limit decision.
func (i *completePullInspector) directory(path string, maximum int) ([]os.DirEntry, error) {
	info, err := safepath.StatWithin(i.m.Root, path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, i.reject("malformed")
	}
	root, err := os.OpenRoot(i.m.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(i.m.Root, path)
	if err != nil {
		return nil, err
	}
	directory, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maximum {
		return nil, i.reject("entry_limit")
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Name() < entries[b].Name() })
	return entries, nil
}

func (i *completePullInspector) read(path string, maximum int) ([]byte, bool, error) {
	info, err := safepath.StatWithin(i.m.Root, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 0 {
		return nil, false, i.reject("malformed")
	}
	limit := min(int64(maximum), i.remaining)
	if info.Size() > limit {
		return nil, false, i.reject("byte_limit")
	}
	body, found, err := readCompletePullFile(i.m.Root, path, int(limit))
	if err == nil {
		i.remaining -= int64(len(body))
	}
	return body, found, err
}

func (i *completePullInspector) family(selector string, family completePullFamily) error {
	if !family.checkpoint {
		return i.reject("orphaned")
	}
	path, _ := i.m.completePullCheckpointPath(selector)
	body, found, err := i.read(path, maxCompletePullCheckpointBytes)
	if err != nil {
		return err
	}
	if !found {
		return i.reject("orphaned")
	}
	var checkpoint CompletePullCheckpoint
	if err := i.decode(body, &checkpoint, func(schema int) bool { return schema == completePullCheckpointSchema }); err != nil {
		return err
	}
	if validateCompletePullCheckpoint(checkpoint, selector) != nil || checkpoint.NextIndex != 0 {
		return i.reject("malformed")
	}
	// The shared directory also holds Confluence families. A qualified sibling
	// manifest identifies its names, but Jira's guard makes no claim about its
	// progress or transactions; a Confluence writer may be changing those.
	if checkpoint.Service != CompletePullServiceJira {
		i.siblings = append(i.siblings, checkpoint)
		return nil
	}
	if ValidateJiraCompletePullSelection(checkpoint) != nil {
		return i.reject("malformed")
	}
	if family.progress {
		path, _ = i.m.completePullProgressPath(selector)
		body, found, err = i.read(path, maxCompletePullProgressBytes)
		if err != nil {
			return err
		}
		if !found {
			return i.reject("orphaned")
		}
		var progress completePullProgress
		if err := i.decode(body, &progress, func(schema int) bool { return schema >= 1 && schema <= completePullConfluenceProgressSchema4 }); err != nil {
			return err
		}
		checkpoint, _, err = applyCompletePullProgress(checkpoint, progress, path)
		if err != nil {
			if progress.SchemaVersion < 1 || progress.SchemaVersion > completePullConfluenceProgressSchema4 {
				return i.reject("unsupported_schema")
			}
			return i.reject("malformed")
		}
	}
	if checkpoint.Service == CompletePullServiceJira {
		i.result.Checkpoints++
		i.result.Selected += len(checkpoint.IDs)
		i.result.Completed += checkpoint.NextIndex
		i.result.Remaining += len(checkpoint.IDs) - checkpoint.NextIndex
	}
	if family.journal {
		path, _ = i.m.completePullJournalPath(selector)
		body, found, err = i.read(path, maxCompletePullJournalBytes)
		if err != nil {
			return err
		}
		if !found {
			return i.reject("orphaned")
		}
		var journal completePullJournal
		if err := i.decode(body, &journal, func(schema int) bool { return validCompletePullJournalSchema(checkpoint.Service, schema) }); err != nil {
			return err
		}
		if validateCompletePullJournal(journal, checkpoint) != nil {
			return i.reject("malformed")
		}
		if checkpoint.NextIndex != journal.StartIndex && checkpoint.NextIndex != journal.StartIndex+len(journal.Entries) {
			return i.reject("malformed")
		}
		i.ownTemps(journal.WriteToken)
		if checkpoint.Service == CompletePullServiceJira {
			i.result.Journals++
		}
	}
	if family.publication {
		return i.publication(selector, checkpoint)
	}
	return nil
}

func (i *completePullInspector) publication(selector string, checkpoint CompletePullCheckpoint) error {
	directory, _ := i.m.completePullPublicationDir(selector)
	entries, err := i.directory(directory, maxCompletePullPublicationArtifacts+1)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "intent.json")
	body, found, err := i.read(path, maxCompletePullPublicationIntent)
	if err != nil {
		return err
	}
	if !found {
		var total int64
		for _, entry := range entries {
			info, owned := completePullPublicationResidue(entry)
			if !owned || info.Size() < 0 {
				return i.reject("orphaned")
			}
			if info.Size() > maxCompletePullPublicationBytes+maxCompletePullPublicationIntent-total {
				return i.reject("byte_limit")
			}
			total += info.Size()
			if strings.HasPrefix(entry.Name(), ".tmp-") {
				if err := i.debitMetadata(info.Size()); err != nil {
					return err
				}
			}
		}
	} else {
		var intent completePullPublicationIntent
		if err := i.decode(body, &intent, func(schema int) bool { return validCompletePullPublicationSchema(checkpoint.Service, schema) }); err != nil {
			return err
		}
		if validateCompletePullPublication(intent, checkpoint, "") != nil {
			return i.reject("malformed")
		}
		i.ownTemps(intent.WriteToken)
		payloads := make(map[string]int64, len(intent.Artifacts))
		for _, artifact := range intent.Artifacts {
			if !artifact.Remove {
				payloads[artifact.Payload] = artifact.Size
			}
		}
		if intent.Relocation != nil {
			for _, artifact := range intent.Relocation.Artifacts {
				if !artifact.Remove {
					payloads[artifact.Payload] = artifact.Size
				}
			}
		}
		for _, entry := range entries {
			if entry.Name() != "intent.json" {
				info, owned := completePullPublicationResidue(entry)
				if !owned {
					return i.reject("orphaned")
				}
				if strings.HasPrefix(entry.Name(), "payload-") {
					size, declared := payloads[entry.Name()]
					if !declared {
						return i.reject("orphaned")
					}
					if info.Size() != size {
						return i.reject("malformed")
					}
				} else if info.Size() < 0 || info.Size() > maxCompletePullPublicationIntent {
					return i.reject("byte_limit")
				} else if err := i.debitMetadata(info.Size()); err != nil {
					return err
				}
			}
		}
	}
	if checkpoint.Service == CompletePullServiceJira {
		i.result.Publications++
	}
	return nil
}
