package atif

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ExportRequest makes the owner-private and repository-exclusion decisions
// explicit. No destination is discovered from environment, provider, or CLI
// state.
type ExportRequest struct {
	OwnerPrivateRoot string
	RepositoryRoot   string
	RelativePath     string
	Projection       Projection
}

type exportHookPoint uint8

const (
	exportBeforePublish exportHookPoint = iota + 1
	exportAfterPublish
)

// exportHook is nil in production. Tests use the private hook to make
// directory and final-inode races deterministic without widening the API.
var exportHook func(exportHookPoint)

var exportTemporaryRemove func(*os.Root, string) error

func invokeExportHook(point exportHookPoint) {
	if exportHook != nil {
		exportHook(point)
	}
}

func removeTemporary(root *os.Root, name string) error {
	if exportTemporaryRemove != nil {
		return exportTemporaryRemove(root, name)
	}
	return root.Remove(name)
}

// ExportOwnerPrivate writes one new canonical ATIF file beneath an existing
// owner-private root. The root must be an existing non-symlink 0700 directory
// outside the repository; the new file is exclusive, 0600, synced, and never
// uploaded or linked into a public report. A successful Link is the commit
// point. If the private staging link cannot be removed after a bounded retry,
// the final file remains authoritative and ErrorExportCleanupPending is
// returned; the hidden staging link is retained as owner-private recovery
// state and the caller must not retry publication into the same destination.
// Any later validation failure is a post-commit outcome: ErrorExportCommitted
// is used only when the exact final inode is still proven, otherwise
// ErrorExportOutcomeUnknown tells the caller to inspect before retrying.
func ExportOwnerPrivate(request ExportRequest) error {
	if err := Validate(request.Projection); err != nil {
		return err
	}
	data, err := Encode(request.Projection)
	if err != nil {
		return err
	}
	rootPath, repositoryPath, relative, err := validateExportPaths(request)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(rootPath)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&fs.ModeSymlink != 0 || rootInfo.Mode().Perm() != 0o700 {
		return fail(ErrorInvalidDestination)
	}
	repositoryInfo, err := os.Lstat(repositoryPath)
	if err != nil || !repositoryInfo.IsDir() || repositoryInfo.Mode()&fs.ModeSymlink != 0 {
		return fail(ErrorInvalidDestination)
	}
	if !disjointPhysicalDirectories(rootPath, repositoryPath) {
		return fail(ErrorInvalidDestination)
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fail(ErrorInvalidDestination)
	}
	defer func() { _ = root.Close() }()
	repository, err := os.OpenRoot(repositoryPath)
	if err != nil {
		return fail(ErrorInvalidDestination)
	}
	defer func() { _ = repository.Close() }()
	if !stableDirectory(rootPath, rootInfo, root, true) || !stableDirectory(repositoryPath, repositoryInfo, repository, false) || !disjointPhysicalDirectories(rootPath, repositoryPath) {
		return fail(ErrorInvalidDestination)
	}
	if err := ensurePrivateParents(root, relative); err != nil {
		return err
	}
	if _, err := root.Lstat(relative); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return fail(ErrorInvalidDestination)
	}
	temporary, err := temporaryRelativePath(relative)
	if err != nil {
		return fail(ErrorExportFailed)
	}
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(ErrorInvalidDestination)
	}
	closeFile := true
	temporaryPresent := true
	defer func() {
		if closeFile {
			_ = file.Close()
		}
		if temporaryPresent {
			_ = removeTemporary(root, temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fail(ErrorExportFailed)
	}
	createdInfo, statErr := file.Stat()
	if statErr != nil || !createdInfo.Mode().IsRegular() || createdInfo.Mode().Perm() != 0o600 {
		return fail(ErrorExportFailed)
	}
	written := 0
	for written < len(data) {
		count, writeErr := file.Write(data[written:])
		if writeErr != nil || count <= 0 || count > len(data)-written {
			return fail(ErrorExportFailed)
		}
		written += count
	}
	createdInfo, statErr = file.Stat()
	if statErr != nil || !createdInfo.Mode().IsRegular() || createdInfo.Mode().Perm() != 0o600 || createdInfo.Size() != int64(len(data)) {
		return fail(ErrorExportFailed)
	}
	if err := file.Sync(); err != nil {
		return fail(ErrorExportFailed)
	}
	if err := file.Close(); err != nil {
		return fail(ErrorExportFailed)
	}
	closeFile = false
	invokeExportHook(exportBeforePublish)
	if !stableDirectory(rootPath, rootInfo, root, true) || !stableDirectory(repositoryPath, repositoryInfo, repository, false) || !disjointPhysicalDirectories(rootPath, repositoryPath) {
		return fail(ErrorExportFailed)
	}
	if err := root.Link(temporary, relative); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fail(ErrorInvalidDestination)
		}
		return fail(ErrorExportFailed)
	}
	cleanupPending := false
	cleanupUncertain := false
	if err := removeTemporary(root, temporary); err != nil {
		if retryErr := removeTemporary(root, temporary); retryErr != nil {
			// Distinguish an actually retained staging inode from an
			// ambiguous unlink outcome before reporting recovery state.
			if info, statErr := root.Lstat(temporary); statErr == nil {
				if os.SameFile(createdInfo, info) {
					cleanupPending = true
				} else {
					cleanupUncertain = true
				}
			} else if !errors.Is(statErr, fs.ErrNotExist) {
				cleanupUncertain = true
			}
			temporaryPresent = false
		}
	}
	temporaryPresent = false
	invokeExportHook(exportAfterPublish)
	if err := ensurePrivateParents(root, relative); err != nil {
		return publishedExportOutcome(root, relative, createdInfo)
	}
	info, statErr := root.Lstat(relative)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || !os.SameFile(createdInfo, info) {
		return publishedExportOutcome(root, relative, createdInfo)
	}
	if !stableDirectory(rootPath, rootInfo, root, true) || !stableDirectory(repositoryPath, repositoryInfo, repository, false) || !disjointPhysicalDirectories(rootPath, repositoryPath) {
		return publishedExportOutcome(root, relative, createdInfo)
	}
	if cleanupPending {
		return fail(ErrorExportCleanupPending)
	}
	if cleanupUncertain {
		return fail(ErrorExportOutcomeUnknown)
	}
	return nil
}

func publishedExportOutcome(root *os.Root, relative string, createdInfo fs.FileInfo) error {
	info, err := root.Lstat(relative)
	if err == nil && info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0 && info.Mode().Perm() == 0o600 && os.SameFile(createdInfo, info) {
		return fail(ErrorExportCommitted)
	}
	return fail(ErrorExportOutcomeUnknown)
}

func temporaryRelativePath(relative string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	base := path.Base(filepath.ToSlash(relative))
	name := "." + base + ".atif-" + hex.EncodeToString(suffix[:])
	directory := path.Dir(filepath.ToSlash(relative))
	if directory == "." {
		return filepath.FromSlash(name), nil
	}
	return filepath.FromSlash(path.Join(directory, name)), nil
}

func validateExportPaths(request ExportRequest) (string, string, string, error) {
	rootPath := request.OwnerPrivateRoot
	repositoryPath := request.RepositoryRoot
	if rootPath == "" || repositoryPath == "" || !filepath.IsAbs(rootPath) || !filepath.IsAbs(repositoryPath) ||
		filepath.Clean(rootPath) != rootPath || filepath.Clean(repositoryPath) != repositoryPath {
		return "", "", "", fail(ErrorInvalidDestination)
	}
	relative := request.RelativePath
	if relative == "" || strings.Contains(relative, "\\") || filepath.IsAbs(relative) || path.IsAbs(relative) ||
		path.Clean(relative) != relative || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", "", "", fail(ErrorInvalidDestination)
	}
	return rootPath, repositoryPath, filepath.FromSlash(relative), nil
}

func ensurePrivateParents(root *os.Root, relative string) error {
	directory := path.Dir(filepath.ToSlash(relative))
	if directory == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(directory, "/") {
		if component == "" || component == "." || component == ".." {
			return fail(ErrorInvalidDestination)
		}
		if current == "" {
			current = component
		} else {
			current = filepath.Join(current, component)
		}
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.Mkdir(current, 0o700); err != nil {
				return fail(ErrorInvalidDestination)
			}
			info, err = root.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return fail(ErrorInvalidDestination)
		}
	}
	return nil
}

func stableDirectory(path string, initial fs.FileInfo, root *os.Root, requirePrivateMode bool) bool {
	ambient, ambientErr := os.Lstat(path)
	opened, openedErr := root.Stat(".")
	return ambientErr == nil && openedErr == nil && ambient.IsDir() && opened.IsDir() &&
		ambient.Mode()&fs.ModeSymlink == 0 && (!requirePrivateMode || ambient.Mode().Perm() == 0o700) && os.SameFile(initial, ambient) && os.SameFile(initial, opened)
}

func disjointPhysicalDirectories(left, right string) bool {
	leftPhysical, leftErr := filepath.EvalSymlinks(left)
	rightPhysical, rightErr := filepath.EvalSymlinks(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftPhysical, leftErr = filepath.Abs(leftPhysical)
	rightPhysical, rightErr = filepath.Abs(rightPhysical)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return !containsPath(leftPhysical, rightPhysical) && !containsPath(rightPhysical, leftPhysical)
}

func containsPath(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
