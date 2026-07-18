package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/isukharev/atl/internal/safepath"
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
	data, err := safepath.ReadFileWithinLimit(root, marker, int64(len(privateOutputRootMarkerContents)))
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
	if err := safepath.WriteFileExclusiveWithin(root, marker, []byte(privateOutputRootMarkerContents), 0o600); err != nil {
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
	data, err := safepath.ReadFileWithinLimit(root, marker, int64(len(privateOutputRootMarkerContents)))
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

func copyLiveConfig(source, target string) error {
	if err := mkdirPrivate(target); err != nil {
		return err
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		data, err := readBoundedFile(filepath.Join(source, name), 4<<20)
		if err != nil {
			return fmt.Errorf("copy private-live %s: %w", name, err)
		}
		var object map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&object); err != nil || object == nil || decoder.Decode(new(any)) != io.EOF {
			return fmt.Errorf("private-live %s must contain one JSON object", name)
		}
		if err := writePrivateFile(filepath.Join(target, name), data); err != nil {
			return err
		}
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
	if _, err := safepath.StatWithin(root, path); err == nil {
		return fmt.Errorf("private run directory already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := safepath.MkdirAllWithin(root, path, 0o700); err != nil {
		return err
	}
	return requirePrivateDirectory("private run directory", path)
}

func writePrivateFile(path string, data []byte) error {
	return safepath.WriteFileAtomicPrivate(path, data, 0o600)
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

func copyWorkspace(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace template is not a directory")
	}
	if err := mkdirPrivate(target); err != nil {
		return err
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = sourceRoot.Close() }()
	var total int64
	var entries int
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		entries++
		if entries > maxWorkspaceEntries {
			return fmt.Errorf("workspace template exceeds %d entries", maxWorkspaceEntries)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace template contains symlink %q", relative)
		}
		if info.IsDir() {
			return os.Mkdir(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace template contains non-regular file %q", relative)
		}
		total += info.Size()
		if total > maxWorkspaceBytes {
			return fmt.Errorf("workspace template exceeds %d bytes", maxWorkspaceBytes)
		}
		input, err := sourceRoot.Open(relative)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		return closeErr
	})
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
