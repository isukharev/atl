package agenteval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
	"github.com/isukharev/atl/internal/agenteval/scheduler"
)

func (publication *sequentialReferencePublication) acquireActiveLock() error {
	if publication == nil || publication.activeLock != nil || !publication.stable() {
		return sequentialReferenceError("publication_lock", nil)
	}
	lock, acquired, err := hardenedTryReadOnlyLockFileWithin(publication.destination,
		filepath.Join(publication.destination, sequentialReferenceMarkerName))
	if err != nil {
		return sequentialReferenceError("publication_lock", err)
	}
	if !acquired {
		return sequentialReferenceError("publication_lock", ErrAttemptLedgerBusy)
	}
	lockedInfo, lockedErr := lock.file.Stat()
	markerInfo, markerErr := publication.root.Lstat(sequentialReferenceMarkerName)
	if lockedErr != nil || markerErr != nil || !os.SameFile(lockedInfo, markerInfo) || !publication.stable() {
		_ = lock.Unlock()
		return sequentialReferenceError("publication_lock", errors.Join(lockedErr, markerErr))
	}
	publication.activeLock = lock
	return nil
}

func (publication *sequentialReferencePublication) validateCompletedReadback(manifest experiment.Manifest,
	store *AttemptLedgerStore, result SequentialReferenceResult, schedule scheduler.Plan,
	recordValidator *experiment.TrialRecordValidator, assignments []sequentialReferenceAssignment,
	baseBindings []lifecycle.Binding, final bool,
) error {
	if store == nil || !publication.stable() {
		return sequentialReferenceError("publication_changed", nil)
	}
	before, err := publication.completedReadbackSnapshot(len(result.Trials))
	if err != nil {
		return sequentialReferenceError("publication_inventory", err)
	}
	marker, err := readSequentialReferenceFile(publication.root, sequentialReferenceMarkerName, 128)
	if err != nil || !bytes.Equal(marker, sequentialReferenceMarker(manifest.ManifestSHA256, schedule.Limits.Workers)) {
		return sequentialReferenceError("publication_marker", err)
	}
	manifestData, err := readSequentialReferenceFile(publication.root, sequentialReferenceManifestName, experiment.MaxManifestBytes)
	if err != nil {
		return sequentialReferenceError("publication_manifest", err)
	}
	readManifest, err := experiment.DecodeManifest(bytes.NewReader(manifestData))
	if err != nil || !reflect.DeepEqual(readManifest, manifest) {
		return sequentialReferenceError("publication_manifest_binding", err)
	}
	scheduleData, err := readSequentialReferenceFile(publication.root, sequentialReferenceSchedulerPlanName, scheduler.MaxPlanBytes)
	if err != nil {
		return sequentialReferenceError("publication_scheduler_plan", err)
	}
	readSchedule, err := scheduler.DecodePlan(bytes.NewReader(scheduleData))
	if err != nil || !reflect.DeepEqual(readSchedule, schedule) {
		return sequentialReferenceError("publication_scheduler_binding", err)
	}
	reportData, err := readSequentialReferenceFile(publication.root, sequentialReferenceSchedulerReportName, scheduler.MaxReportBytes)
	if err != nil {
		return sequentialReferenceError("publication_scheduler_report", err)
	}
	readReport, err := scheduler.DecodeReport(bytes.NewReader(reportData), readSchedule)
	if err != nil || !reflect.DeepEqual(readReport, result.Scheduler) {
		return sequentialReferenceError("publication_scheduler_report_binding", err)
	}
	if recordValidator == nil || len(assignments) != len(result.Trials) || len(baseBindings) != len(assignments) {
		return sequentialReferenceError("publication_roster", nil)
	}
	strictStore, err := OpenAttemptLedgerStoreStrictContext(context.Background(), store.root)
	if err != nil || !reflect.DeepEqual(strictStore.Header(), store.Header()) {
		return sequentialReferenceError("publication_attempt_store", err)
	}
	inspections, err := strictStore.InspectAllStrictContext(context.Background(), len(assignments))
	if err != nil || len(inspections) != len(result.Trials) {
		return sequentialReferenceError("publication_roster", err)
	}
	if final && publication.testHook != nil {
		if err := publication.testHook("during_final_readback"); err != nil {
			return sequentialReferenceError("publication_complete", err)
		}
	}
	for index := range result.Trials {
		read, readErr := publication.readTrial(manifest, recordValidator, assignments[index], baseBindings[index], inspections[index])
		if readErr != nil || !reflect.DeepEqual(read, result.Trials[index]) {
			return sequentialReferenceError("publication_readback",
				errors.Join(readErr, errSequentialReferenceArtifactDrift(read, result.Trials[index])))
		}
	}
	if err := validateSequentialReferencePublishedSchedule(manifest, assignments, inspections, result, readSchedule); err != nil {
		return err
	}
	if err := publication.validateShape(len(result.Trials), true); err != nil || !publication.stable() {
		return sequentialReferenceError("publication_shape", err)
	}
	after, err := publication.completedReadbackSnapshot(len(result.Trials))
	if err != nil || !sameSequentialReferenceSnapshot(before, after) {
		return sequentialReferenceError("publication_inventory_changed", err)
	}
	return nil
}

type sequentialReferenceSnapshotEntry struct {
	path      string
	info      fs.FileInfo
	directory bool
	digest    [sha256.Size]byte
}

func (publication *sequentialReferencePublication) completedReadbackSnapshot(trials int) ([]sequentialReferenceSnapshotEntry, error) {
	if trials <= 0 || trials > lifecycle.MaxAttempts || !publication.stable() {
		return nil, sequentialReferenceError("publication_inventory", nil)
	}
	entries := []sequentialReferenceSnapshotEntry{}
	var walk func(string) error
	walk = func(directory string) error {
		var info fs.FileInfo
		var err error
		if directory == "." {
			info, err = publication.root.Stat(directory)
		} else {
			info, err = publication.root.Lstat(directory)
		}
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return sequentialReferenceError("publication_inventory_directory", err)
		}
		entries = append(entries, sequentialReferenceSnapshotEntry{path: directory, info: info, directory: true})
		maximum, ok := sequentialReferenceSnapshotDirectoryLimit(directory, trials)
		if !ok {
			return sequentialReferenceError("publication_inventory_directory", nil)
		}
		names, err := readSequentialReferenceDirectoryContext(context.Background(), publication.root, directory, maximum)
		if err != nil {
			return err
		}
		for _, name := range names {
			path := filepath.Join(directory, name)
			entryInfo, statErr := publication.root.Lstat(path)
			if statErr != nil || entryInfo.Mode()&fs.ModeSymlink != 0 {
				return sequentialReferenceError("publication_inventory_entry", statErr)
			}
			if entryInfo.IsDir() {
				if err := walk(path); err != nil {
					return err
				}
				continue
			}
			if !entryInfo.Mode().IsRegular() || entryInfo.Mode().Perm() != 0o600 {
				return sequentialReferenceError("publication_inventory_entry", nil)
			}
			data, readErr := readSequentialReferenceFile(publication.root, path, experiment.MaxManifestBytes)
			if readErr != nil {
				return readErr
			}
			entries = append(entries, sequentialReferenceSnapshotEntry{path: path, info: entryInfo, digest: sha256.Sum256(data)})
			clear(data)
		}
		return nil
	}
	if err := walk("."); err != nil || !publication.stable() {
		return nil, sequentialReferenceError("publication_inventory", err)
	}
	return entries, nil
}

func sequentialReferenceSnapshotDirectoryLimit(path string, trials int) (int, bool) {
	ledgerAttempts := filepath.Join(sequentialReferenceLedgerDirectory, attemptLedgerAttempts)
	switch path {
	case ".":
		return 7, true
	case sequentialReferenceLedgerDirectory:
		return 4, true
	case ledgerAttempts, sequentialReferenceTrialsDirectory:
		return trials + 1, true
	}
	parent := filepath.Dir(path)
	if parent == ledgerAttempts {
		return 3, true
	}
	if parent == sequentialReferenceTrialsDirectory {
		return 7, true
	}
	if filepath.Base(path) == attemptLedgerEvents && filepath.Dir(parent) == ledgerAttempts {
		return lifecycle.MaxEvents + 1, true
	}
	return 0, false
}

func sameSequentialReferenceSnapshot(left, right []sequentialReferenceSnapshotEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].path != right[index].path || left[index].directory != right[index].directory ||
			left[index].info.Mode() != right[index].info.Mode() || left[index].info.Size() != right[index].info.Size() ||
			!left[index].info.ModTime().Equal(right[index].info.ModTime()) || !os.SameFile(left[index].info, right[index].info) ||
			left[index].digest != right[index].digest {
			return false
		}
	}
	return true
}
