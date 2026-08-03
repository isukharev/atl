package agenteval

import (
	"fmt"
	"io"
	"io/fs"
	"os"
)

type stableReadFile interface {
	io.Reader
	Stat() (fs.FileInfo, error)
	Close() error
}

// stableRootFile binds one regular file opened relative to a retained root to
// the identity and metadata observed by the caller's complete inventory. The
// caller still owns root identity, tree completeness, byte digests, and error
// classification.
type stableRootFile struct {
	file       stableReadFile
	openedInfo fs.FileInfo
}

func openStableRootFile(root *os.Root, relativePath string, expected fs.FileInfo) (stableRootFile, error) {
	file, err := root.Open(relativePath)
	if err != nil {
		return stableRootFile{}, err
	}
	return bindStableRootFile(file, expected)
}

func bindStableRootFile(file stableReadFile, expected fs.FileInfo) (stableRootFile, error) {
	openedInfo, statErr := file.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(expected, openedInfo) || !sameSyntheticRootInfo(expected, openedInfo) {
		_ = file.Close()
		return stableRootFile{}, fmt.Errorf("entry changed")
	}
	return stableRootFile{file: file, openedInfo: openedInfo}, nil
}

func (opened stableRootFile) readWithinLimit(limit int64) ([]byte, error) {
	data, readErr := ioReadAllLimit(opened.file, limit)
	finalInfo, finalStatErr := opened.file.Stat()
	closeErr := opened.file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if finalStatErr != nil || !os.SameFile(opened.openedInfo, finalInfo) ||
		!sameSyntheticRootInfo(opened.openedInfo, finalInfo) || finalInfo.Size() != int64(len(data)) {
		return nil, fmt.Errorf("entry changed")
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}

func readStableRootFile(root *os.Root, relativePath string, expected fs.FileInfo, limit int64) ([]byte, error) {
	opened, err := openStableRootFile(root, relativePath, expected)
	if err != nil {
		return nil, err
	}
	return opened.readWithinLimit(limit)
}
