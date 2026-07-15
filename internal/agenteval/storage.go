package agenteval

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	maxWorkspaceBytes   = 32 << 20
	maxWorkspaceEntries = 4096
)

func PreparePrivateOutputRoot(root, repositoryRoot string) (string, error) {
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
	if err := mkdirPrivate(absRoot); err != nil {
		return "", err
	}
	return absRoot, nil
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
	return os.Chmod(path, 0o700)
}

func writePrivateFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
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

func pathWithin(root, target string) (bool, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}
