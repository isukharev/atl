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

func requirePrivateLiveInputs(specPath, liveConfigDir, repositoryRoot string) error {
	specPath, err := filepath.Abs(specPath)
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
	configInside, err := pathWithin(repositoryRoot, liveConfigDir)
	if err != nil {
		return err
	}
	if configInside {
		return fmt.Errorf("private-live config directory must be outside the repository")
	}
	if err := requireOwnerOnly("private-live config directory", liveConfigDir, true); err != nil {
		return err
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		path := filepath.Join(liveConfigDir, name)
		if err := requireOwnerOnly("private-live "+name, path, false); err != nil {
			return err
		}
	}
	return nil
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
