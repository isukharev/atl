package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func pathsOverlap(sourceRoot, output string) bool {
	left, leftErr := physicalPath(sourceRoot)
	right, rightErr := physicalPath(output)
	if leftErr != nil || rightErr != nil {
		return true
	}
	relative, err := filepath.Rel(left, right)
	return err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func physicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	var suffix []string
	current := abs
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		return 0, errors.New("binary version output exceeds bound")
	}
	return b.Buffer.Write(data)
}

func validateBinaryIdentity(binary, version, commit, contractVersion, platform, architecture string) error {
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return errors.New("binary must be an executable regular file")
	}
	if platform != runtime.GOOS || architecture != runtime.GOARCH {
		return errors.New("distribution target must match the build host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "version", "--output", "json")
	command.Dir = os.TempDir()
	command.WaitDelay = 250 * time.Millisecond
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_TERMINAL_PROMPT=0"}
	var output boundedOutput
	output.limit = 64 << 10
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("binary version probe timed out")
		}
		return errors.New("binary version probe failed")
	}
	var envelope struct {
		Result struct {
			Build struct {
				Version string `json:"version"`
				Commit  string `json:"commit"`
			} `json:"build"`
			ContractVersion string `json:"contract_version"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || envelope.Result.Build.Version != version || envelope.Result.Build.Commit != commit || envelope.Result.ContractVersion != contractVersion {
		return errors.New("binary version identity does not match distribution metadata")
	}
	return nil
}

func validateBinaryForSigning(binary, platform, architecture string) error {
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return errors.New("binary must be an executable regular file")
	}
	if platform != runtime.GOOS || architecture != runtime.GOARCH {
		return errors.New("distribution target must match the signing host")
	}
	return nil
}
