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
	"strings"
	"time"
)

func pathsOverlap(sourceRoot, output string) bool {
	left, leftErr := filepath.Abs(sourceRoot)
	right, rightErr := filepath.Abs(output)
	if leftErr != nil || rightErr != nil {
		return true
	}
	relative, err := filepath.Rel(filepath.Clean(left), filepath.Clean(right))
	return err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
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

func validateBinaryIdentity(binary, version, commit string) error {
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return errors.New("binary must be an executable regular file")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "version", "--output", "json")
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
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || envelope.Result.Build.Version != version || envelope.Result.Build.Commit != commit {
		return errors.New("binary version identity does not match distribution metadata")
	}
	return nil
}
