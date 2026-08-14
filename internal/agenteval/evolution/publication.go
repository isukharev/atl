package evolution

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const (
	proposalMarkerName = ".incomplete"
	proposalFileName   = "proposal.json"
)

// PublicationPlan is a validated, content-minimized proposal ready to write
// to one new destination. It has no source-root or apply capability.
type PublicationPlan struct {
	proposal Proposal
	data     []byte
}

func PlanPublication(proposal Proposal) (PublicationPlan, error) {
	data, err := Encode(proposal)
	if err != nil {
		return PublicationPlan{}, err
	}
	return PublicationPlan{proposal: cloneProposal(proposal), data: append([]byte(nil), data...)}, nil
}

// WriteNew creates exactly one absent absolute destination. It creates and
// durably records the incomplete marker before writing the proposal payload;
// once that marker exists, a partial publication cannot be mistaken for a
// completed proposal. The small pre-marker window (destination creation
// itself cannot be atomic with marker creation) is fail-closed: an empty or
// markerless destination is never accepted by ReadPublished and must be
// reconciled before retry. The package intentionally does not promise
// power-loss atomicity beyond the file and directory syncs available here.
func (plan PublicationPlan) WriteNew(destination string) error {
	if err := Validate(plan.proposal); err != nil || len(plan.data) == 0 {
		return fail(ErrorInvalidProposal)
	}
	canonical, err := Encode(plan.proposal)
	if err != nil || !bytes.Equal(canonical, plan.data) {
		return fail(ErrorInvalidProposal)
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return fail(ErrorConflict)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fail(ErrorConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(ErrorConflict)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fail(ErrorConflict)
	}
	if err := syncParentDirectory(destination); err != nil {
		return publicationError(err)
	}
	root, destinationInfo, err := openStableDestination(destination)
	if err != nil {
		return fail(ErrorConflict)
	}
	defer func() { _ = root.Close() }()
	if runtime.GOOS != "windows" && destinationInfo.Mode().Perm() != 0o700 {
		return fail(ErrorConflict)
	}
	if err := writeNewRegular(root, proposalMarkerName, []byte("incomplete\n")); err != nil {
		return publicationError(err)
	}
	if err := syncRootDirectory(root); err != nil {
		return publicationError(err)
	}
	if err := writeNewRegular(root, proposalFileName, plan.data); err != nil {
		return publicationError(err)
	}
	if err := validateProposalRoot(root, plan.data, true); err != nil {
		return publicationError(err)
	}
	if err := syncRootDirectory(root); err != nil {
		return publicationError(err)
	}
	if err := root.Remove(proposalMarkerName); err != nil {
		return publicationError(err)
	}
	if err := validateProposalRoot(root, plan.data, false); err != nil {
		return publicationFailureAfterMarkerRemoval(root, err)
	}
	if err := syncRootDirectory(root); err != nil {
		return publicationFailureAfterMarkerRemoval(root, err)
	}
	if !stableDestination(destination, root, destinationInfo) {
		return publicationFailureAfterMarkerRemoval(root, fail(ErrorConflict))
	}
	return nil
}

func publicationError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := CodeOf(err); ok {
		return err
	}
	return fail(ErrorConflict)
}

func publicationFailureAfterMarkerRemoval(root *os.Root, err error) error {
	if restoreErr := restoreProposalMarkerDurable(root); restoreErr != nil {
		return fail(ErrorOutcomeUnknown)
	}
	return publicationError(err)
}

func writeNewRegular(root *os.Root, name string, data []byte) error {
	if len(data) > MaxProposalBytes {
		return fail(ErrorLimitExceeded)
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncRootDirectory(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func syncParentDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := parent.Sync()
	closeErr := parent.Close()
	return errors.Join(syncErr, closeErr)
}

func openStableDestination(destination string) (*os.Root, os.FileInfo, error) {
	initial, err := os.Lstat(destination)
	if err != nil || !initial.IsDir() || initial.Mode()&os.ModeSymlink != 0 {
		if err == nil {
			err = errors.New("destination is not a directory")
		}
		return nil, nil, err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return nil, nil, err
	}
	opened, openedErr := root.Stat(".")
	ambient, ambientErr := os.Lstat(destination)
	if openedErr != nil || ambientErr != nil || !opened.IsDir() || !os.SameFile(initial, opened) || !os.SameFile(initial, ambient) {
		_ = root.Close()
		return nil, nil, errors.New("unstable destination")
	}
	return root, initial, nil
}

func stableDestination(destination string, root *os.Root, initial os.FileInfo) bool {
	opened, openedErr := root.Stat(".")
	ambient, ambientErr := os.Lstat(destination)
	return openedErr == nil && ambientErr == nil && opened.IsDir() &&
		os.SameFile(initial, opened) && os.SameFile(initial, ambient)
}

func restoreProposalMarker(root *os.Root) error {
	if _, err := root.Lstat(proposalMarkerName); err == nil {
		return nil
	}
	return writeNewRegular(root, proposalMarkerName, []byte("incomplete\n"))
}

func restoreProposalMarkerDurable(root *os.Root) error {
	if err := restoreProposalMarker(root); err != nil {
		return err
	}
	return syncRootDirectory(root)
}

func validateProposalRoot(root *os.Root, expected []byte, incomplete bool) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(3)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	wantEntries := 1
	if incomplete {
		wantEntries++
	}
	if len(entries) != wantEntries {
		return errors.New("unexpected proposal tree")
	}
	for _, entry := range entries {
		if entry.Name() == proposalMarkerName {
			if !incomplete || !regularProposalEntry(entry, 0o600) {
				return errors.New("invalid proposal marker")
			}
			continue
		}
		if entry.Name() != proposalFileName || !regularProposalEntry(entry, 0o600) {
			return errors.New("invalid proposal file")
		}
	}
	if incomplete {
		if _, err := root.Lstat(proposalMarkerName); err != nil {
			return err
		}
	} else if _, err := root.Lstat(proposalMarkerName); !errors.Is(err, os.ErrNotExist) {
		return errors.New("proposal marker remains")
	}
	actual, err := readProposalData(root)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("proposal bytes changed")
	}
	return nil
}

func regularProposalEntry(entry os.DirEntry, wantPerm os.FileMode) bool {
	if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
		return false
	}
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm() == wantPerm
}

func readProposalData(root *os.Root) ([]byte, error) {
	file, err := root.Open(proposalFileName)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxProposalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(data) > MaxProposalBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return data, nil
}

// ReadPublished reads only the content-minimized proposal from a completed
// destination and rejects an incomplete marker or any unexpected member.
func ReadPublished(destination string) (Proposal, error) {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return Proposal{}, fail(ErrorConflict)
	}
	root, destinationInfo, err := openStableDestination(destination)
	if err != nil {
		return Proposal{}, fail(ErrorConflict)
	}
	defer func() { _ = root.Close() }()
	if runtime.GOOS != "windows" && destinationInfo.Mode().Perm() != 0o700 {
		return Proposal{}, fail(ErrorConflict)
	}
	directory, err := root.Open(".")
	if err != nil {
		return Proposal{}, fail(ErrorConflict)
	}
	entries, readDirErr := directory.ReadDir(2)
	closeDirErr := directory.Close()
	if readDirErr != nil || closeDirErr != nil || len(entries) != 1 || entries[0].Name() != proposalFileName || !regularProposalEntry(entries[0], 0o600) {
		return Proposal{}, fail(ErrorConflict)
	}
	data, readErr := readProposalData(root)
	if readErr != nil {
		if _, ok := CodeOf(readErr); ok {
			return Proposal{}, readErr
		}
		return Proposal{}, fail(ErrorConflict)
	}
	proposal, err := Decode(bytes.NewReader(data))
	if err != nil || validateProposalRoot(root, data, false) != nil || !stableDestination(destination, root, destinationInfo) {
		return Proposal{}, fail(ErrorConflict)
	}
	return proposal, err
}
