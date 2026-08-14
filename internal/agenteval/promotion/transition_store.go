package promotion

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const maxTransitionEntries = 4096

func transitionName(kind, requestSHA256 string) string {
	return filepath.Join(promotionTransitionDirectory, kind+"-"+requestSHA256+".json")
}

func (s Store) readTransition(root *os.Root, kind, requestSHA256 string) (transitionRecord, bool, error) {
	data, err := readRegularBounded(root, transitionName(kind, requestSHA256))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return transitionRecord{}, false, nil
		}
		return transitionRecord{}, false, err
	}
	record, err := decodeTransition(data)
	if err != nil {
		return transitionRecord{}, false, err
	}
	if record.Kind != kind || record.RequestSHA256 != requestSHA256 {
		return transitionRecord{}, false, fail(ErrorConflict)
	}
	return record, true, nil
}

// readTransitionByDigest resolves the transition named by the pointer. The
// durable filename remains keyed by request SHA so an interrupted request can
// be recovered before its applied transition digest is known; the pointer
// itself always carries the content digest. The bounded scan prevents that
// compatibility filename choice from becoming an unbounded read surface.
func (s Store) readTransitionByDigest(root *os.Root, kind, transitionSHA256 string) (transitionRecord, bool, error) {
	if !validDigest(transitionSHA256) {
		return transitionRecord{}, false, fail(ErrorInvalidReceipt)
	}
	entries, err := readTransitionEntries(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return transitionRecord{}, false, nil
		}
		return transitionRecord{}, false, err
	}
	var matched transitionRecord
	found := false
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || !strings.HasPrefix(entry.Name(), kind+"-") {
			continue
		}
		requestSHA256 := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), kind+"-"), ".json")
		record, readFound, err := s.readTransition(root, kind, requestSHA256)
		if err != nil {
			return transitionRecord{}, false, err
		}
		if !readFound {
			continue
		}
		if record.TransitionSHA256 == transitionSHA256 {
			if found {
				return transitionRecord{}, false, fail(ErrorConflict)
			}
			matched = record
			found = true
		}
	}
	return matched, found, nil
}

func readTransitionEntries(root *os.Root) ([]fs.DirEntry, error) {
	directory, err := root.Open(promotionTransitionDirectory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maxTransitionEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxTransitionEntries {
		return nil, fail(ErrorLimitExceeded)
	}
	return entries, nil
}

func checkTransitionCapacity(root *os.Root) error {
	entries, err := readTransitionEntries(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) >= maxTransitionEntries {
		return fail(ErrorLimitExceeded)
	}
	return nil
}
