//go:build !linux

package main

import (
	"errors"
	"os"
	"os/exec"
)

func terminationSignal() os.Signal { return os.Interrupt }

func configureCommand(_ *exec.Cmd) error {
	return errors.New("hosted race process ownership requires linux")
}

func cleanupCommand(_ *exec.Cmd) {}
