package agenteval

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/isukharev/atl/internal/agenteval/extension"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const (
	extensionExecutableMaxBytes = 512 << 20
	extensionFrameMaxBytes      = extension.MaxFrameBytes
	extensionSessionMaxBytes    = extension.MaxSessionBytes
	extensionStderrMaxBytes     = extension.MaxStderrBytes
	extensionArgumentMaxCount   = 256
	extensionCancelGrace        = 250 * time.Millisecond
	extensionRuntimePrefix      = "atl-agent-eval-extension-"
	extensionVerificationLimit  = time.Duration(extension.MaxDeadlineMilliseconds) * time.Millisecond
)

var (
	errExtensionInvalidExecutable = errors.New("extension_invalid_executable")
	errExtensionAdmissionCleanup  = errors.New("extension_admission_cleanup")
	errExtensionInvalidArguments  = errors.New("extension_invalid_arguments")
	errExtensionInvalidDeadline   = errors.New("extension_invalid_deadline")
	errExtensionSpawnFailed       = errors.New("extension_spawn_failed")
	errExtensionProtocolIO        = errors.New("extension_protocol_io")
	errExtensionFrameOverflow     = errors.New("extension_frame_overflow")
	errExtensionSessionOverflow   = errors.New("extension_session_overflow")
	errExtensionStderrOverflow    = errors.New("extension_stderr_overflow")
	errExtensionPartialFrame      = errors.New("extension_partial_frame")
)

type admittedExtensionExecutable struct {
	root            string
	workingDir      string
	homeDir         string
	tempDir         string
	path            string
	sha256          string
	runtimeGuard    *extensionRuntimeRootGuard
	executableGuard *extensionExecutableLaunchGuard
}

func admitExtensionExecutable(path, expectedSHA256 string) (_ admittedExtensionExecutable, returnErr error) {
	return admitExtensionExecutableWithRoot(path, expectedSHA256, func() (string, *extensionRuntimeRootGuard, error) {
		return makePrivateExtensionRuntimeRoot(os.TempDir(), extensionRuntimePrefix)
	})
}

func admitExtensionExecutableWithRoot(
	path, expectedSHA256 string,
	makeRoot func() (string, *extensionRuntimeRootGuard, error),
) (_ admittedExtensionExecutable, returnErr error) {
	if makeRoot == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || !validSHA256(expectedSHA256) {
		return admittedExtensionExecutable{}, errExtensionInvalidExecutable
	}
	data, digest, err := stableReadExtensionExecutable(path)
	if err != nil || digest != expectedSHA256 {
		clear(data)
		return admittedExtensionExecutable{}, errExtensionInvalidExecutable
	}
	defer clear(data)
	if err := validatePrivateAgentNativeFormat(data); err != nil {
		clear(data)
		return admittedExtensionExecutable{}, errExtensionInvalidExecutable
	}

	root, runtimeGuard, err := makeRoot()
	rootValid := filepath.IsAbs(root) && filepath.Clean(root) == root &&
		strings.HasPrefix(filepath.Base(root), extensionRuntimePrefix)
	if err != nil || !rootValid || runtimeGuard == nil {
		cleanupFailed := errors.Is(err, errExtensionAdmissionCleanup)
		if runtimeGuard != nil && rootValid {
			cleanupFailed = runtimeGuard.remove(root) != nil || cleanupFailed
		} else if runtimeGuard != nil {
			cleanupFailed = runtimeGuard.close() != nil || cleanupFailed
		} else if root != "" {
			cleanupFailed = true
		}
		if cleanupFailed {
			return admittedExtensionExecutable{}, errExtensionAdmissionCleanup
		}
		return admittedExtensionExecutable{}, errExtensionInvalidExecutable
	}
	admitted := admittedExtensionExecutable{root: root, sha256: digest, runtimeGuard: runtimeGuard}
	defer func() {
		if returnErr != nil {
			if err := admitted.remove(); err != nil {
				returnErr = errExtensionAdmissionCleanup
			}
		}
	}()
	if err := preparePrivateExtensionRuntimeDirectory(root); err != nil {
		return admittedExtensionExecutable{}, errExtensionInvalidExecutable
	}
	admitted.workingDir = filepath.Join(root, "work")
	admitted.homeDir = filepath.Join(root, "home")
	admitted.tempDir = filepath.Join(root, "tmp")
	binDir := filepath.Join(root, "bin")
	for _, directory := range []string{
		admitted.workingDir,
		admitted.homeDir,
		admitted.tempDir,
		binDir,
		filepath.Join(admitted.homeDir, "config"),
		filepath.Join(admitted.homeDir, "cache"),
		filepath.Join(admitted.homeDir, "data"),
	} {
		if err := os.Mkdir(directory, 0o700); err != nil || preparePrivateExtensionRuntimeDirectory(directory) != nil {
			return admittedExtensionExecutable{}, errExtensionInvalidExecutable
		}
	}
	name := "component"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	admitted.path = filepath.Join(binDir, name)
	file, err := os.OpenFile(admitted.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		return admittedExtensionExecutable{}, errExtensionInvalidExecutable
	}
	dataLength := len(data)
	written, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	clear(data)
	if writeErr != nil || written != dataLength || syncErr != nil || closeErr != nil {
		return admittedExtensionExecutable{}, errExtensionInvalidExecutable
	}
	admitted.executableGuard, err = preparePrivateExtensionRuntimeExecutable(admitted.path, expectedSHA256)
	if err != nil {
		return admittedExtensionExecutable{}, errExtensionInvalidExecutable
	}
	return admitted, nil
}

func stableReadExtensionExecutable(path string) ([]byte, string, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		(runtime.GOOS != "windows" && before.Mode()&0o111 == 0) || before.Size() < 1 || before.Size() > extensionExecutableMaxBytes {
		return nil, "", errExtensionInvalidExecutable
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", errExtensionInvalidExecutable
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() != before.Size() || opened.Mode() != before.Mode() {
		_ = file.Close()
		return nil, "", errExtensionInvalidExecutable
	}
	data, readErr := io.ReadAll(io.LimitReader(file, extensionExecutableMaxBytes+1))
	after, afterErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || afterErr != nil || closeErr != nil || int64(len(data)) != opened.Size() ||
		!os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || after.Mode() != opened.Mode() {
		clear(data)
		return nil, "", errExtensionInvalidExecutable
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}

func (a admittedExtensionExecutable) environment() ([]string, error) {
	values := map[string]string{
		"HOME":            a.homeDir,
		"TMPDIR":          a.tempDir,
		"TMP":             a.tempDir,
		"TEMP":            a.tempDir,
		"XDG_CONFIG_HOME": filepath.Join(a.homeDir, "config"),
		"XDG_CACHE_HOME":  filepath.Join(a.homeDir, "cache"),
		"XDG_DATA_HOME":   filepath.Join(a.homeDir, "data"),
	}
	if runtime.GOOS == "windows" {
		values["USERPROFILE"] = a.homeDir
		values["APPDATA"] = filepath.Join(a.homeDir, "config")
		values["LOCALAPPDATA"] = filepath.Join(a.homeDir, "data")
	}
	platformValues, err := extensionPlatformEnvironment()
	if err != nil {
		return nil, errExtensionInvalidExecutable
	}
	for name, value := range platformValues {
		values[name] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment, nil
}

func (a admittedExtensionExecutable) remove() error {
	if a.root == "" || !filepath.IsAbs(a.root) || !strings.HasPrefix(filepath.Base(a.root), extensionRuntimePrefix) {
		return errExtensionInvalidExecutable
	}
	guardErr := a.executableGuard.close()
	runtimeErr := a.runtimeGuard.remove(a.root)
	return errors.Join(guardErr, runtimeErr)
}

func validateExtensionArguments(arguments []string) error {
	if len(arguments) > extensionArgumentMaxCount {
		return errExtensionInvalidArguments
	}
	total := 0
	for _, argument := range arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return errExtensionInvalidArguments
		}
		total += len(argument)
		if total > extensionSessionMaxBytes {
			return errExtensionInvalidArguments
		}
	}
	return nil
}

type extensionProcessSession struct {
	command *exec.Cmd
	tree    *boundedProcessTree
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	reader  *extensionFrameReader
	stderr  *extensionStderrBuffer

	waitDone chan struct{}
	waitErr  error

	mu          sync.Mutex
	stdinBytes  int64
	stdinClosed bool
	closeOnce   sync.Once
	closeResult extensionProcessCleanup
}

type extensionProcessCleanup struct {
	assurance string
	complete  bool
	waitErr   error
	err       error
}

type extensionProcessStartError struct {
	possibleEntry bool
}

func (e *extensionProcessStartError) Error() string { return errExtensionSpawnFailed.Error() }

func (e *extensionProcessStartError) Unwrap() error { return errExtensionSpawnFailed }

func startExtensionProcess(admitted admittedExtensionExecutable, arguments []string) (*extensionProcessSession, error) {
	return startExtensionProcessWithSession(admitted, arguments, nil)
}

func startExtensionProcessWithSession(admitted admittedExtensionExecutable, arguments []string, attempt *DurableAttemptSession) (*extensionProcessSession, error) {
	if err := validateExtensionArguments(arguments); err != nil {
		return nil, err
	}
	environment, err := admitted.environment()
	if err != nil {
		return nil, &extensionProcessStartError{}
	}
	command := exec.Command(admitted.path, append([]string(nil), arguments...)...)
	tree, err := prepareProcessTree(command)
	if err != nil {
		return nil, &extensionProcessStartError{}
	}
	command.Dir = admitted.workingDir
	command.Env = environment
	command.WaitDelay = extensionCancelGrace
	childStdin, stdin, err := os.Pipe()
	if err != nil {
		_ = tree.close()
		return nil, &extensionProcessStartError{}
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		_ = tree.close()
		return nil, &extensionProcessStartError{}
	}
	command.Stdin = childStdin
	command.Stdout = childStdout
	stderr := newExtensionStderrBuffer(extensionStderrMaxBytes)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = childStdin.Close()
		_ = childStdout.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = tree.close()
		startErr := &extensionProcessStartError{}
		if attempt != nil {
			return nil, joinAttemptLifecycleError(startErr, attempt.FailBeforeSpawn())
		}
		return nil, startErr
	}
	childStdinCloseErr := childStdin.Close()
	childStdoutCloseErr := childStdout.Close()
	session := &extensionProcessSession{
		command:  command,
		tree:     tree,
		stdin:    stdin,
		stdout:   stdout,
		reader:   newExtensionFrameReader(stdout, extensionFrameMaxBytes, extensionSessionMaxBytes),
		stderr:   stderr,
		waitDone: make(chan struct{}),
	}
	go func() {
		session.waitErr = command.Wait()
		close(session.waitDone)
	}()
	if err := admitted.executableGuard.close(); err != nil {
		_ = session.cleanup(extensionCancelGrace)
		startErr := &extensionProcessStartError{possibleEntry: true}
		if attempt != nil {
			return nil, joinAttemptLifecycleError(startErr, attempt.Unknown(lifecycle.ErrorCleanupAmbiguous, UnknownAttemptUsage()))
		}
		return nil, startErr
	}
	if childStdinCloseErr != nil || childStdoutCloseErr != nil {
		_ = session.cleanup(extensionCancelGrace)
		startErr := &extensionProcessStartError{possibleEntry: true}
		if attempt != nil {
			return nil, joinAttemptLifecycleError(startErr, attempt.Unknown(lifecycle.ErrorCleanupAmbiguous, UnknownAttemptUsage()))
		}
		return nil, startErr
	}
	if err := tree.attach(); err != nil {
		_ = session.cleanup(extensionCancelGrace)
		startErr := &extensionProcessStartError{possibleEntry: true}
		if attempt != nil {
			return nil, joinAttemptLifecycleError(startErr, attempt.Unknown(lifecycle.ErrorCleanupAmbiguous, UnknownAttemptUsage()))
		}
		return nil, startErr
	}
	if attempt != nil {
		identity, err := processAttemptIdentity(attempt.plan, command)
		if err == nil {
			err = attempt.Running(identity)
		}
		if err != nil {
			_ = session.cleanup(extensionCancelGrace)
			return nil, joinAttemptLifecycleError(&extensionProcessStartError{possibleEntry: true},
				attempt.Unknown(lifecycle.ErrorInternal, UnknownAttemptUsage()))
		}
	}
	return session, nil
}

func (s *extensionProcessSession) writeFrame(ctx context.Context, frame []byte) error {
	if len(frame) == 0 || len(frame) > extensionFrameMaxBytes || bytes.IndexByte(frame, '\n') >= 0 || bytes.IndexByte(frame, '\r') >= 0 {
		return errExtensionFrameOverflow
	}
	s.mu.Lock()
	if s.stdinClosed || s.stdinBytes+int64(len(frame)+1) > extensionSessionMaxBytes {
		s.mu.Unlock()
		return errExtensionSessionOverflow
	}
	s.stdinBytes += int64(len(frame) + 1)
	s.mu.Unlock()
	payload := make([]byte, len(frame)+1)
	copy(payload, frame)
	payload[len(frame)] = '\n'
	done := make(chan error, 1)
	go func() {
		written, err := s.stdin.Write(payload)
		clear(payload)
		if err == nil && written != len(frame)+1 {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	select {
	case err := <-done:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.stderr.didOverflow() {
			return errExtensionStderrOverflow
		}
		if err != nil {
			return errExtensionProtocolIO
		}
		return nil
	case <-s.stderr.overflowed():
		return errExtensionStderrOverflow
	case <-s.waitDone:
		return errExtensionProtocolIO
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *extensionProcessSession) readFrame(ctx context.Context) ([]byte, error) {
	type result struct {
		frame []byte
		err   error
	}
	done := make(chan result, 1)
	go func() {
		frame, err := s.reader.readFrame()
		done <- result{frame: frame, err: err}
	}()
	select {
	case value := <-done:
		if ctx.Err() != nil {
			clear(value.frame)
			return nil, ctx.Err()
		}
		if s.stderr.didOverflow() {
			clear(value.frame)
			return nil, errExtensionStderrOverflow
		}
		return value.frame, value.err
	case <-s.stderr.overflowed():
		return nil, errExtensionStderrOverflow
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *extensionProcessSession) closeStdin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdinClosed {
		return nil
	}
	s.stdinClosed = true
	return s.stdin.Close()
}

func (s *extensionProcessSession) cleanup(grace time.Duration) extensionProcessCleanup {
	s.closeOnce.Do(func() {
		_ = s.closeStdin()
		assurance := "best_effort"
		if runtime.GOOS == "windows" {
			assurance = "bounded_job"
		}
		result := extensionProcessCleanup{assurance: assurance}
		if grace > 0 {
			_ = s.tree.interrupt()
			select {
			case <-s.waitDone:
			case <-time.After(grace):
			}
		}
		killErr := s.tree.kill()
		if s.command.Process != nil {
			_ = s.command.Process.Kill()
		}
		select {
		case <-s.waitDone:
			result.waitErr = s.waitErr
			result.complete = killErr == nil
		case <-time.After(extensionCancelGrace):
			result.err = errExtensionProtocolIO
		}
		_ = s.stdout.Close()
		closeErr := s.tree.close()
		if killErr != nil || closeErr != nil {
			result.complete = false
			result.err = errExtensionProtocolIO
		}
		s.closeResult = result
	})
	return s.closeResult
}

type extensionFrameReader struct {
	reader       *bufio.Reader
	frameMaximum int64
	totalMaximum int64
	totalRead    int64
	mu           sync.Mutex
}

func newExtensionFrameReader(reader io.Reader, frameMaximum, totalMaximum int64) *extensionFrameReader {
	return &extensionFrameReader{
		reader: bufio.NewReaderSize(reader, 64<<10), frameMaximum: frameMaximum, totalMaximum: totalMaximum,
	}
}

func (r *extensionFrameReader) readFrame() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var frame bytes.Buffer
	for {
		fragment, err := r.reader.ReadSlice('\n')
		r.totalRead += int64(len(fragment))
		if r.totalRead > r.totalMaximum {
			return nil, errExtensionSessionOverflow
		}
		if int64(frame.Len()+len(fragment)) > r.frameMaximum+1 {
			return nil, errExtensionFrameOverflow
		}
		_, _ = frame.Write(fragment)
		if err == nil {
			data := frame.Bytes()
			if len(data) < 2 || data[len(data)-1] != '\n' || data[len(data)-2] == '\r' {
				return nil, errExtensionProtocolIO
			}
			return append([]byte(nil), data[:len(data)-1]...), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if frame.Len() == 0 {
				return nil, io.EOF
			}
			return nil, errExtensionPartialFrame
		}
		return nil, errExtensionProtocolIO
	}
}

type extensionStderrBuffer struct {
	mu       sync.Mutex
	size     int
	maximum  int
	overflow chan struct{}
	once     sync.Once
}

func newExtensionStderrBuffer(maximum int) *extensionStderrBuffer {
	return &extensionStderrBuffer{maximum: maximum, overflow: make(chan struct{})}
}

func (b *extensionStderrBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	remaining := b.maximum - b.size
	overflow := len(data) > remaining
	if overflow {
		b.size = b.maximum
	} else {
		b.size += len(data)
	}
	b.mu.Unlock()
	if overflow {
		b.once.Do(func() { close(b.overflow) })
	}
	return len(data), nil
}

func (b *extensionStderrBuffer) overflowed() <-chan struct{} {
	return b.overflow
}

func (b *extensionStderrBuffer) didOverflow() bool {
	select {
	case <-b.overflow:
		return true
	default:
		return false
	}
}

func extensionContextDeadline(ctx context.Context, requested time.Duration) (context.Context, context.CancelFunc, error) {
	if requested <= 0 || requested > extensionVerificationLimit {
		return nil, nil, errExtensionInvalidDeadline
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, requested)
	return deadlineCtx, cancel, nil
}

func extensionVerificationContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	return extensionContextDeadline(ctx, extensionVerificationLimit)
}

func extensionCleanupAssurance() string {
	if runtime.GOOS == "windows" {
		return "bounded_job"
	}
	return "best_effort"
}
