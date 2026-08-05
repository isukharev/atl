package agenteval

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	maxWorkspaceBytes               = 32 << 20
	maxWorkspaceEntries             = 4096
	privateOutputRootMarker         = ".atl-agent-eval-private-root"
	privateOutputRootMarkerContents = "atl-agent-eval-private-root-v1\n"
)

func PreparePrivateOutputRoot(root, repositoryRoot string) (string, error) {
	requestedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Lstat(requestedRoot); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("evaluation output root must not be a symlink")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", statErr
	}
	absRoot, err := canonicalizeForCreation(root)
	if err != nil {
		return "", err
	}
	absRepository, err := canonicalizeForCreation(repositoryRoot)
	if err != nil {
		return "", err
	}
	inside, err := pathWithin(absRepository, absRoot)
	if err != nil {
		return "", err
	}
	if inside {
		if absRoot == absRepository {
			return "", fmt.Errorf("evaluation output root cannot be the repository root")
		}
		command := exec.Command("git", "-C", absRepository, "check-ignore", "--quiet", "--no-index", "--", absRoot)
		if err := command.Run(); err != nil {
			return "", fmt.Errorf("evaluation output root inside the worktree must be Git-ignored")
		}
	}
	if err := prepareMarkedPrivateRoot(absRoot); err != nil {
		return "", err
	}
	return absRoot, nil
}

func prepareMarkedPrivateRoot(root string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		if err := mkdirPrivate(root); err != nil {
			return err
		}
		return initializePrivateRootMarker(root)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("evaluation output root must be a directory")
	}
	if err := requirePrivateDirectory("evaluation output root", root); err != nil {
		return err
	}
	marker := filepath.Join(root, privateOutputRootMarker)
	if _, err := os.Lstat(marker); os.IsNotExist(err) {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return fmt.Errorf("existing evaluation output root is not initialized")
		}
		return initializePrivateRootMarker(root)
	} else if err != nil {
		return err
	}
	if err := requireOwnerOnly("evaluation output root marker", marker, false); err != nil {
		return err
	}
	data, err := hardenedReadFileWithinLimit(root, marker, int64(len(privateOutputRootMarkerContents)))
	if err != nil {
		return err
	}
	if string(data) != privateOutputRootMarkerContents {
		return fmt.Errorf("evaluation output root marker is invalid")
	}
	return nil
}

func initializePrivateRootMarker(root string) error {
	marker := filepath.Join(root, privateOutputRootMarker)
	if err := hardenedWriteFileExclusiveWithin(root, marker, []byte(privateOutputRootMarkerContents), 0o600); err != nil {
		return fmt.Errorf("initialize evaluation output root: %w", err)
	}
	return nil
}

func requirePrivateLiveInputs(specPath, liveConfigDir, repositoryRoot string) error {
	return requirePrivateLiveInputsForWorkspace(specPath, liveConfigDir, repositoryRoot, "")
}

func requirePrivateLiveInputsForWorkspace(specPath, liveConfigDir, repositoryRoot, privateWorkspaceRoot string) error {
	repositoryRoot, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return err
	}
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return fmt.Errorf("private-live repository root: %w", err)
	}
	specPath, err = filepath.Abs(specPath)
	if err != nil {
		return err
	}
	specPath, err = filepath.EvalSymlinks(specPath)
	if err != nil {
		return fmt.Errorf("private-live spec: %w", err)
	}
	inside, err := pathWithin(repositoryRoot, specPath)
	if err != nil {
		return err
	}
	if inside {
		command := exec.Command("git", "-C", repositoryRoot, "check-ignore", "--quiet", "--no-index", "--", specPath)
		if err := command.Run(); err != nil {
			return fmt.Errorf("private-live spec and its referenced inputs must be outside Git or Git-ignored")
		}
	}
	canonicalConfigDir, err := filepath.Abs(liveConfigDir)
	if err != nil {
		return err
	}
	canonicalConfigDir, err = filepath.EvalSymlinks(canonicalConfigDir)
	if err != nil {
		return fmt.Errorf("private-live config directory: %w", err)
	}
	configInside, err := pathWithin(repositoryRoot, canonicalConfigDir)
	if err != nil {
		return err
	}
	if configInside {
		if !privateRuntimePathAllowed(privateWorkspaceRoot, canonicalConfigDir) {
			return fmt.Errorf("private-live config directory must be outside the repository")
		}
	}
	if err := requireOwnerOnly("private-live config directory", canonicalConfigDir, true); err != nil {
		return err
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		path := filepath.Join(canonicalConfigDir, name)
		if err := requireOwnerOnly("private-live "+name, path, false); err != nil {
			return err
		}
	}
	return nil
}

func validatePrivateWorkspaceRootForRuntime(root string) error {
	if err := requirePrivateDirectory("private workspace root", root); err != nil {
		return err
	}
	marker := filepath.Join(root, privateOutputRootMarker)
	if err := requireOwnerOnly("private workspace marker", marker, false); err != nil {
		return err
	}
	data, err := hardenedReadFileWithinLimit(root, marker, int64(len(privateOutputRootMarkerContents)))
	if err != nil || string(data) != privateOutputRootMarkerContents {
		return fmt.Errorf("private workspace marker is invalid")
	}
	return nil
}

func privateRuntimePathAllowed(privateWorkspaceRoot, target string) bool {
	if privateWorkspaceRoot == "" || validatePrivateWorkspaceRootForRuntime(privateWorkspaceRoot) != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(privateWorkspaceRoot)
	if err != nil {
		return false
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	inside, err := pathWithin(filepath.Join(root, ".ephemeral"), target)
	return err == nil && inside
}

func requireOwnerOnly(name, path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symlink", name)
	}
	if directory != info.IsDir() || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("%s has the wrong file type", name)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must not be accessible by group or other users", name)
	}
	return nil
}

func canonicalizeForCreation(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	existing := absPath
	var missing []string
	for {
		_, statErr := os.Lstat(existing)
		if statErr == nil {
			break
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", statErr
		}
		missing = append(missing, filepath.Base(existing))
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Clean(resolved), nil
}

func mkdirPrivate(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return requirePrivateDirectory("private directory", path)
}

func mkdirPrivateWithin(root, path string) error {
	if _, err := hardenedStatWithin(root, path); err == nil {
		return fmt.Errorf("private run directory already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := hardenedMkdirAllWithin(root, path, 0o700); err != nil {
		return err
	}
	return requirePrivateDirectory("private run directory", path)
}

func writePrivateFile(path string, data []byte) error {
	return hardenedWriteFileAtomicPrivate(path, data, 0o600)
}

func requirePrivateDirectory(name, path string) error {
	if err := requireOwnerOnly(name, path, true); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%s must have mode 0700", name)
	}
	return nil
}

type workspaceCopyHooks struct {
	afterInventory func()
	beforeFileRead func(string)
}

type workspaceInventory struct {
	entries []workspaceInventoryEntry
	bytes   int64
}

type workspaceInventoryEntry struct {
	path   string
	info   fs.FileInfo
	digest [sha256.Size]byte
}

func copyWorkspace(source, target string) error {
	return copyWorkspaceWithHooks(source, target, workspaceCopyHooks{})
}

// copyWorkspaceWithHooks retains the production copy contract while allowing
// deterministic adversarial tests to mutate a source only after it has been
// inventoried or opened. Callers outside tests use copyWorkspace.
func copyWorkspaceWithHooks(source, target string, hooks workspaceCopyHooks) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("workspace template is not a plain directory")
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = sourceRoot.Close() }()
	if err := verifyWorkspaceCopyRoot(source, info, sourceRoot); err != nil {
		return err
	}
	inventory, err := inventoryWorkspace(sourceRoot)
	if err != nil {
		return err
	}
	if err := verifyWorkspaceCopyRoot(source, info, sourceRoot); err != nil {
		return err
	}
	if hooks.afterInventory != nil {
		hooks.afterInventory()
	}
	if err := verifyWorkspaceCopyRoot(source, info, sourceRoot); err != nil {
		return err
	}
	if err := mkdirPrivate(target); err != nil {
		return err
	}
	var copiedBytes int64
	for _, entry := range inventory.entries {
		if err := verifyWorkspaceInventoryEntry(sourceRoot, entry); err != nil {
			return err
		}
		destination := filepath.Join(target, entry.path)
		if entry.info.IsDir() {
			if err := os.Mkdir(destination, 0o700); err != nil {
				return err
			}
			continue
		}
		remaining := maxWorkspaceBytes - copiedBytes
		copied, digest, err := copyWorkspaceInventoryFile(sourceRoot, entry, destination, remaining, hooks.beforeFileRead)
		if err != nil {
			return err
		}
		if digest != entry.digest {
			return fmt.Errorf("workspace template file changed while it was copied")
		}
		copiedBytes += copied
	}
	if copiedBytes != inventory.bytes {
		return fmt.Errorf("workspace template byte inventory drifted while it was copied")
	}
	if err := verifyWorkspaceCopyRoot(source, info, sourceRoot); err != nil {
		return err
	}
	finalInventory, err := inventoryWorkspace(sourceRoot)
	if err != nil || !sameWorkspaceInventory(inventory, finalInventory) {
		return fmt.Errorf("workspace template changed while it was copied")
	}
	return verifyWorkspaceCopyRoot(source, info, sourceRoot)
}

func verifyWorkspaceCopyRoot(source string, initial fs.FileInfo, root *os.Root) error {
	current, err := os.Lstat(source)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !sameWorkspaceFileInfo(initial, current) {
		return fmt.Errorf("workspace template root changed while it was copied")
	}
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !sameWorkspaceFileInfo(initial, opened) {
		return fmt.Errorf("workspace template root changed while it was copied")
	}
	return nil
}

func inventoryWorkspace(root *os.Root) (workspaceInventory, error) {
	var inventory workspaceInventory
	err := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if !fs.ValidPath(path) {
			return fmt.Errorf("workspace template inventory path is invalid")
		}
		inventory.entries = append(inventory.entries, workspaceInventoryEntry{path: filepath.FromSlash(path)})
		if len(inventory.entries) > maxWorkspaceEntries {
			return fmt.Errorf("workspace template exceeds %d entries", maxWorkspaceEntries)
		}
		item := &inventory.entries[len(inventory.entries)-1]
		info, err := root.Lstat(item.path)
		if err != nil {
			return err
		}
		entryInfo, err := entry.Info()
		if err != nil || !sameWorkspaceFileInfo(entryInfo, info) {
			return fmt.Errorf("workspace template changed while it was inventoried")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace template contains symlink %q", path)
		}
		if info.IsDir() {
			item.info = info
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() < 0 {
			return fmt.Errorf("workspace template contains non-regular file %q", path)
		}
		remaining := maxWorkspaceBytes - inventory.bytes
		if info.Size() > remaining {
			return fmt.Errorf("workspace template exceeds %d bytes", maxWorkspaceBytes)
		}
		digest, err := hashWorkspaceInventoryFile(root, item.path, info, remaining)
		if err != nil {
			return err
		}
		item.info = info
		item.digest = digest
		inventory.bytes += info.Size()
		return nil
	})
	if err != nil {
		return workspaceInventory{}, err
	}
	return inventory, nil
}

func verifyWorkspaceInventoryEntry(root *os.Root, entry workspaceInventoryEntry) error {
	info, err := root.Lstat(entry.path)
	if err != nil || !sameWorkspaceFileInfo(entry.info, info) {
		return fmt.Errorf("workspace template changed after inventory")
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != entry.info.IsDir() {
		return fmt.Errorf("workspace template changed after inventory")
	}
	return nil
}

func hashWorkspaceInventoryFile(root *os.Root, path string, expected fs.FileInfo, remaining int64) ([sha256.Size]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	digest, _, readErr := readWorkspaceInventoryFile(file, expected, remaining, io.Discard, nil)
	closeErr := file.Close()
	if readErr != nil {
		return [sha256.Size]byte{}, readErr
	}
	if closeErr != nil {
		return [sha256.Size]byte{}, closeErr
	}
	return digest, nil
}

func copyWorkspaceInventoryFile(
	root *os.Root,
	entry workspaceInventoryEntry,
	destination string,
	remaining int64,
	beforeRead func(string),
) (int64, [sha256.Size]byte, error) {
	input, err := root.Open(entry.path)
	if err != nil {
		return 0, [sha256.Size]byte{}, err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = input.Close()
		return 0, [sha256.Size]byte{}, err
	}
	digest, copied, readErr := readWorkspaceInventoryFile(input, entry.info, remaining, output, func() {
		if beforeRead != nil {
			beforeRead(entry.path)
		}
	})
	inputCloseErr := input.Close()
	outputCloseErr := output.Close()
	if readErr != nil {
		return 0, [sha256.Size]byte{}, readErr
	}
	if inputCloseErr != nil {
		return 0, [sha256.Size]byte{}, inputCloseErr
	}
	if outputCloseErr != nil {
		return 0, [sha256.Size]byte{}, outputCloseErr
	}
	return copied, digest, nil
}

func readWorkspaceInventoryFile(
	file *os.File,
	expected fs.FileInfo,
	remaining int64,
	destination io.Writer,
	beforeRead func(),
) ([sha256.Size]byte, int64, error) {
	if expected.Size() < 0 || remaining < expected.Size() || remaining < 0 {
		return [sha256.Size]byte{}, 0, fmt.Errorf("workspace template exceeds %d bytes", maxWorkspaceBytes)
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !sameWorkspaceFileInfo(expected, opened) {
		return [sha256.Size]byte{}, 0, fmt.Errorf("workspace template file changed while it was copied")
	}
	if beforeRead != nil {
		beforeRead()
	}
	hash := sha256.New()
	reader := &io.LimitedReader{R: file, N: remaining + 1}
	copied, copyErr := io.CopyN(io.MultiWriter(destination, hash), reader, expected.Size())
	if copyErr != nil || copied != expected.Size() {
		return [sha256.Size]byte{}, 0, fmt.Errorf("workspace template file changed while it was copied")
	}
	var probe [1]byte
	extra, probeErr := reader.Read(probe[:])
	if extra != 0 {
		return [sha256.Size]byte{}, 0, fmt.Errorf("workspace template file changed while it was copied")
	}
	if probeErr != io.EOF {
		return [sha256.Size]byte{}, 0, fmt.Errorf("read workspace template file")
	}
	final, err := file.Stat()
	if err != nil || !sameWorkspaceFileInfo(opened, final) {
		return [sha256.Size]byte{}, 0, fmt.Errorf("workspace template file changed while it was copied")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, copied, nil
}

func sameWorkspaceInventory(first, second workspaceInventory) bool {
	if first.bytes != second.bytes || len(first.entries) != len(second.entries) {
		return false
	}
	for index := range first.entries {
		left, right := first.entries[index], second.entries[index]
		if left.path != right.path || left.digest != right.digest || !sameWorkspaceFileInfo(left.info, right.info) {
			return false
		}
	}
	return true
}

func sameWorkspaceFileInfo(first, second fs.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Mode() == second.Mode() &&
		first.Size() == second.Size() && first.ModTime().Equal(second.ModTime())
}

func validatePrivateWorkspaceTemplate(source string) error {
	blockedRoots := map[string]struct{}{".agents": {}, ".claude": {}, ".codex": {}}
	blockedFiles := map[string]struct{}{".mcp.json": {}, "AGENTS.md": {}, "CLAUDE.md": {}}
	return filepath.WalkDir(source, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if _, blocked := blockedRoots[parts[0]]; blocked {
			return fmt.Errorf("private-live workspace contains provider control path %q", relative)
		}
		if len(parts) == 1 {
			if _, blocked := blockedFiles[parts[0]]; blocked {
				return fmt.Errorf("private-live workspace contains provider control path %q", relative)
			}
		}
		return nil
	})
}

func pathWithin(root, target string) (bool, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}
