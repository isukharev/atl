package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

type tailBuffer struct {
	limit    int
	body     []byte
	overflow bool
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if written >= b.limit {
		b.body = append(b.body[:0], p[written-b.limit:]...)
		b.overflow = true
		return written, nil
	}
	if len(b.body)+written > b.limit {
		drop := len(b.body) + written - b.limit
		copy(b.body, b.body[drop:])
		b.body = b.body[:len(b.body)-drop]
		b.overflow = true
	}
	b.body = append(b.body, p...)
	return written, nil
}

func runCommand(ctx context.Context, spec commandSpec, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, spec.name, spec.args...)
	command.Dir = spec.dir
	command.Env = spec.env
	command.Stdout = stdout
	command.Stderr = stderr
	if err := configureCommand(command); err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	err := command.Wait()
	cleanupCommand(command)
	return err
}

func captureCommand(ctx context.Context, spec commandSpec, limit int) ([]byte, []byte, error) {
	stdout := newTailBuffer(limit)
	stderr := newTailBuffer(limit)
	err := runCommand(ctx, spec, stdout, stderr)
	if stdout.overflow || stderr.overflow {
		return nil, nil, errors.New("command output exceeds its reviewed bound")
	}
	return append([]byte(nil), stdout.body...), append([]byte(nil), stderr.body...), err
}

func writeFailureTail(output io.Writer, stdout, stderr *tailBuffer) {
	if len(stderr.body) > 0 {
		_, _ = output.Write(stderr.body)
		if stderr.body[len(stderr.body)-1] != '\n' {
			_, _ = io.WriteString(output, "\n")
		}
	}
	if len(stdout.body) > 0 {
		_, _ = output.Write(stdout.body)
		if stdout.body[len(stdout.body)-1] != '\n' {
			_, _ = io.WriteString(output, "\n")
		}
	}
}
