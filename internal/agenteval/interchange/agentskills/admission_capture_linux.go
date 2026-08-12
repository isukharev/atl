package agentskills

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const structuralAdmissionSupported = true

type linuxStructuralRoot struct {
	directory           *os.File
	descriptorDirectory *os.File
	info                fs.FileInfo
	absolute            string
}

func openStructuralRoot(rootPath string) (structuralRoot, *structuralSourceRefusal) {
	if rootPath == "" || strings.ContainsRune(rootPath, 0) {
		return nil, structuralRootRefusal(FindingInvalidRoot)
	}
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, structuralRootRefusal(FindingInvalidRoot)
	}
	directory, err := openLinuxRootNoFollow(absolute)
	if err != nil {
		return nil, structuralRootRefusal(linuxRootOpenFinding(err))
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		_ = directory.Close()
		return nil, structuralRootRefusal(FindingInvalidRoot)
	}
	descriptorDirectory, err := openLinuxDescriptorDirectory()
	if err != nil {
		_ = directory.Close()
		return nil, structuralRootRefusal(FindingPlatformUnsupported)
	}
	root := &linuxStructuralRoot{
		directory: directory, descriptorDirectory: descriptorDirectory,
		info: info, absolute: absolute,
	}
	if !root.selectedRootStable() {
		_ = root.Close()
		return nil, structuralRootRefusal(FindingRootChanged)
	}
	return root, nil
}

func openLinuxDescriptorDirectory() (*os.File, error) {
	fd, err := unix.Open("/proc/self/fd", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil || stat.Type != unix.PROC_SUPER_MAGIC {
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		return nil, unix.ENOTSUP
	}
	result := os.NewFile(uintptr(fd), "proc-descriptors")
	if result == nil {
		_ = unix.Close(fd)
		return nil, unix.EBADF
	}
	return result, nil
}

func structuralRootRefusal(code StructuralFindingCode) *structuralSourceRefusal {
	return &structuralSourceRefusal{
		code: code, class: structuralFindingClass(code), location: ".",
	}
}

func linuxRootOpenFinding(err error) StructuralFindingCode {
	switch {
	case errors.Is(err, unix.ELOOP):
		return FindingRootSymlink
	case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EXDEV):
		return FindingRootChanged
	case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EPERM),
		errors.Is(err, unix.EINVAL), errors.Is(err, unix.E2BIG):
		return FindingPlatformUnsupported
	default:
		return FindingInvalidRoot
	}
}

func openLinuxRootNoFollow(absolute string) (*os.File, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	if current == nil {
		_ = unix.Close(fd)
		return nil, unix.EBADF
	}
	trimmed := strings.TrimPrefix(filepath.Clean(absolute), string(filepath.Separator))
	if trimmed == "" || trimmed == "." {
		return current, nil
	}
	for _, component := range strings.Split(trimmed, string(filepath.Separator)) {
		how := &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
		}
		nextFD, openErr := unix.Openat2(int(current.Fd()), component, how)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), component)
		if next == nil {
			_ = unix.Close(nextFD)
			_ = current.Close()
			return nil, unix.EBADF
		}
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, err
		}
		current = next
	}
	return current, nil
}

func (root *linuxStructuralRoot) Close() error {
	return errors.Join(root.descriptorDirectory.Close(), root.directory.Close())
}

func (root *linuxStructuralRoot) selectedRootStable() bool {
	held, err := root.directory.Stat()
	if err != nil || !sameStableInfo(root.info, held) {
		return false
	}
	selected, err := openLinuxRootNoFollow(root.absolute)
	if err != nil {
		return false
	}
	current, statErr := selected.Stat()
	closeErr := selected.Close()
	return statErr == nil && closeErr == nil && sameStableInfo(root.info, current)
}

func (root *linuxStructuralRoot) Inventory(pass int, limit structuralCaptureBudget,
	hooks structuralCaptureHooks,
) (capturedTree, error) {
	if !root.selectedRootStable() {
		return capturedTree{}, newStructuralRefusal(FindingRootChanged, ".")
	}
	budget := limit
	tree := capturedTree{files: make(map[string]capturedEntry)}
	var regular []capturedEntry
	if err := root.readDirectory(pass, ".", root.directory, &budget, &tree, &regular, hooks); err != nil {
		return capturedTree{}, err
	}
	if !root.selectedRootStable() {
		return capturedTree{}, newStructuralRefusal(FindingRootChanged, ".")
	}
	finishStructuralTree(&tree)
	return tree, nil
}

func (root *linuxStructuralRoot) readDirectory(pass int, relative string, directory *os.File,
	budget *structuralCaptureBudget, tree *capturedTree, regular *[]capturedEntry, hooks structuralCaptureHooks,
) error {
	readableFD, err := unix.Openat(int(directory.Fd()), ".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return newStructuralRefusal(linuxEntryOpenFinding(err), relative)
	}
	readable := os.NewFile(uintptr(readableFD), relative)
	if readable == nil {
		_ = unix.Close(readableFD)
		return newStructuralRefusal(FindingEntryUnreadable, relative)
	}
	children, readErr := readable.ReadDir(budget.remainingEntries + 1)
	closeErr := readable.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return newStructuralRefusal(FindingEntryUnreadable, relative)
	}
	if len(children) > budget.remainingEntries {
		return newStructuralRefusal(FindingEntryCountLimit, relative)
	}
	budget.remainingEntries -= len(children)
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		location := child.Name()
		if relative != "." {
			location = relative + "/" + child.Name()
		}
		if err := validateStructuralLocation(location, *budget); err != nil {
			return err
		}
		if hooks.beforeOpen != nil {
			hooks.beforeOpen(pass, location)
		}
		opened, err := openLinuxStructuralEntry(directory, child.Name(), location)
		if err != nil {
			return err
		}
		info, statErr := opened.Stat()
		if statErr != nil {
			_ = opened.Close()
			return newStructuralRefusal(linuxEntryOpenFinding(statErr), location)
		}
		entry := capturedEntry{path: location, info: info, isDir: info.IsDir()}
		if entry.isDir {
			var addErr error
			*regular, addErr = addStructuralEntry(tree, entry, budget, *regular)
			if addErr == nil {
				addErr = root.readDirectory(pass, location, opened, budget, tree, regular, hooks)
			}
			if addErr == nil {
				addErr = revalidateLinuxStructuralEntry(directory, child.Name(), info, location)
			}
			closeErr := opened.Close()
			if addErr != nil {
				return addErr
			}
			if closeErr != nil {
				return newStructuralRefusal(FindingEntryUnreadable, location)
			}
			continue
		}
		if err := validateStructuralFile(info, location, *budget); err != nil {
			_ = opened.Close()
			return err
		}
		if hooks.beforeRead != nil {
			hooks.beforeRead(pass, location)
		}
		data, readErr := readLinuxStructuralFile(root.descriptorDirectory, opened, info, location)
		if readErr == nil {
			readErr = revalidateLinuxStructuralEntry(directory, child.Name(), info, location)
		}
		closeErr := opened.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return newStructuralRefusal(FindingEntryUnreadable, location)
		}
		entry.data = data
		entry.digest = digestBytes(data)
		*regular, err = addStructuralEntry(tree, entry, budget, *regular)
		if err != nil {
			return err
		}
	}
	return nil
}

func openLinuxStructuralEntry(directory *os.File, name, location string) (*os.File, error) {
	how := &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(int(directory.Fd()), name, how)
	if err != nil {
		return nil, newStructuralRefusal(linuxEntryOpenFinding(err), location)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, newStructuralRefusal(FindingEntryUnreadable, location)
	}
	return file, nil
}

func linuxEntryOpenFinding(err error) StructuralFindingCode {
	switch {
	case errors.Is(err, unix.ELOOP):
		return FindingEntrySymlink
	case errors.Is(err, unix.EXDEV):
		return FindingMountBoundary
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR),
		errors.Is(err, unix.ESTALE), errors.Is(err, unix.EAGAIN):
		return FindingEntryChanged
	case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EPERM),
		errors.Is(err, unix.EINVAL), errors.Is(err, unix.E2BIG):
		return FindingPlatformUnsupported
	default:
		return FindingEntryUnreadable
	}
}

func revalidateLinuxStructuralEntry(directory *os.File, name string, expected fs.FileInfo, location string) error {
	current, err := openLinuxStructuralEntry(directory, name, location)
	if err != nil {
		return newStructuralRefusal(FindingEntryChanged, location)
	}
	info, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil || closeErr != nil || !sameStableInfo(expected, info) {
		return newStructuralRefusal(FindingEntryChanged, location)
	}
	return nil
}

func readLinuxStructuralFile(descriptorDirectory *os.File, held *os.File,
	expected fs.FileInfo, location string,
) ([]byte, error) {
	// The O_PATH descriptor already pins a regular input leaf reached under the
	// no-link/no-mount policy. Follow only the kernel-owned procfs reference to
	// that exact descriptor: no mutable bundle pathname is opened for reading.
	name := strconv.FormatUint(uint64(held.Fd()), 10)
	fd, err := unix.Openat(int(descriptorDirectory.Fd()), name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		code := FindingPlatformUnsupported
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
			code = FindingEntryUnreadable
		}
		return nil, newStructuralRefusal(code, location)
	}
	file := os.NewFile(uintptr(fd), location)
	if file == nil {
		_ = unix.Close(fd)
		return nil, newStructuralRefusal(FindingEntryUnreadable, location)
	}
	opened, statErr := file.Stat()
	heldInfo, heldErr := held.Stat()
	if statErr != nil || heldErr != nil || !sameStableInfo(expected, heldInfo) ||
		!sameStableInfo(heldInfo, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, newStructuralRefusal(FindingEntryChanged, location)
	}
	reader := &io.LimitedReader{R: file, N: expected.Size() + 1}
	data, readErr := io.ReadAll(reader)
	final, finalErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || finalErr != nil || closeErr != nil {
		return nil, newStructuralRefusal(FindingEntryUnreadable, location)
	}
	if int64(len(data)) != expected.Size() || reader.N == 0 || !sameStableInfo(opened, final) {
		return nil, newStructuralRefusal(FindingEntryChanged, location)
	}
	return data, nil
}
