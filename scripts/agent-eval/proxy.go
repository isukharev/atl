package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

type proxyRecord struct {
	StdoutBytes int64 `json:"stdout_bytes"`
	StderrBytes int64 `json:"stderr_bytes"`
	ExitCode    int   `json:"exit_code"`
}

func runATLProxy(args []string) int {
	if os.Getenv("ATL_READ_ONLY") != "1" {
		fmt.Fprintln(os.Stderr, "atl evaluation proxy requires ATL_READ_ONLY=1")
		return 2
	}
	realBinary := os.Getenv("ATL_EVAL_REAL_BINARY")
	counterPath := os.Getenv("ATL_EVAL_COUNTER")
	if realBinary == "" || counterPath == "" {
		fmt.Fprintln(os.Stderr, "atl evaluation proxy is not configured")
		return 2
	}
	command := exec.Command(realBinary, args...)
	command.Stdin = os.Stdin
	stdout, err := command.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := command.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var stdoutBytes, stderrBytes int64
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		stdoutBytes, _ = io.Copy(os.Stdout, stdout)
	}()
	go func() {
		defer copyWG.Done()
		stderrBytes, _ = io.Copy(os.Stderr, stderr)
	}()
	waitErr := command.Wait()
	copyWG.Wait()
	exitCode := 0
	if waitErr != nil {
		if exitError, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
		}
	}
	if err := appendProxyRecord(counterPath, proxyRecord{StdoutBytes: stdoutBytes, StderrBytes: stderrBytes, ExitCode: exitCode}); err != nil {
		fmt.Fprintln(os.Stderr, "record atl evaluation metric:", err)
		return 1
	}
	return exitCode
}

func appendProxyRecord(path string, record proxyRecord) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encodeErr := encoder.Encode(record)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}
