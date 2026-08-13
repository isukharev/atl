package agenteval

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/extension"
)

const (
	extensionHostHelperMarker                = "--atl-extension-host-helper"
	extensionUnixTrustedTemporaryBaseForTest = "/tmp"
)

func TestExtensionHostAdmissionMaterializesNativeExecutableWithClosedEnvironment(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := stableReadExtensionExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_EXTENSION_SECRET_CANARY", "must-not-reach-child")
	admitted, err := admitExtensionExecutable(executable, digest)
	if err != nil {
		t.Fatal(err)
	}
	root := admitted.root
	t.Cleanup(func() { _ = admitted.remove() })
	if admitted.path == executable || !strings.HasPrefix(admitted.path, root+string(filepath.Separator)) {
		t.Fatalf("execution copy escaped its runtime")
	}
	_, copiedDigest, err := stableReadExtensionExecutable(admitted.path)
	if err != nil || copiedDigest != digest {
		t.Fatalf("execution copy digest=%q err=%v", copiedDigest, err)
	}
	environmentValues, err := admitted.environment()
	if err != nil {
		t.Fatal(err)
	}
	environment := environmentMap(environmentValues)
	if environment["ATL_EXTENSION_SECRET_CANARY"] != "" || environment["PATH"] != "" || environment["HTTP_PROXY"] != "" || environment["HOME"] != admitted.homeDir {
		t.Fatalf("extension environment was not closed: names=%v", sortedEnvironmentNames(environment))
	}
	for _, name := range []string{"HOME", "TMPDIR", "TMP", "TEMP", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		value := environment[name]
		if value == "" || !strings.HasPrefix(value, root+string(filepath.Separator)) {
			t.Fatalf("%s escaped extension runtime", name)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(admitted.path)
		if err != nil || info.Mode().Perm() != 0o500 {
			t.Fatalf("execution copy mode=%v err=%v", info, err)
		}
	}
	if err := admitted.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("extension runtime survived cleanup: %v", err)
	}
}

func TestExtensionHostAdmissionRejectsUnsafeExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := stableReadExtensionExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	link := filepath.Join(root, "component-link")
	if runtime.GOOS == "windows" {
		link += ".exe"
	}
	if err := os.Symlink(executable, link); err != nil {
		link = ""
	}
	script := filepath.Join(root, "component-script")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	scriptData, scriptDigest, err := stableReadExtensionExecutable(script)
	if err != nil {
		t.Fatal(err)
	}
	clear(scriptData)
	tests := map[string]struct {
		path   string
		digest string
	}{
		"relative":        {path: filepath.Base(executable), digest: digest},
		"digest mismatch": {path: executable, digest: strings.Repeat("0", 64)},
		"directory":       {path: root, digest: digest},
		"script":          {path: script, digest: scriptDigest},
	}
	if link != "" {
		tests["symlink"] = struct {
			path   string
			digest string
		}{path: link, digest: digest}
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := admitExtensionExecutable(test.path, test.digest); !errors.Is(err, errExtensionInvalidExecutable) {
				t.Fatalf("admission error=%v", err)
			}
		})
	}

	if runtime.GOOS != "windows" {
		for name, ambient := range map[string]string{
			"absolute trailing separator": t.TempDir() + string(filepath.Separator),
			"relative":                    "relative-extension-temp-must-not-exist",
		} {
			t.Run("ambient temp root ignored/"+name, func(t *testing.T) {
				t.Setenv("TMPDIR", ambient)
				admitted, err := admitExtensionExecutable(executable, digest)
				if err != nil {
					t.Fatal(err)
				}
				trustedBase, err := filepath.EvalSymlinks(extensionUnixTrustedTemporaryBaseForTest)
				if err != nil || filepath.Dir(admitted.root) != trustedBase {
					t.Fatalf("runtime root=%q trusted base=%q err=%v", admitted.root, trustedBase, err)
				}
				if err := admitted.remove(); err != nil {
					t.Fatal(err)
				}
				if !filepath.IsAbs(ambient) {
					if _, err := os.Stat(ambient); !os.IsNotExist(err) {
						t.Fatalf("admission created a relative ambient root: %v", err)
					}
				}
			})
		}
	}

	t.Run("late materialization failure removes runtime", func(t *testing.T) {
		runtimeRoot, runtimeGuard, err := makePrivateExtensionRuntimeRoot(os.TempDir(), extensionRuntimePrefix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runtimeRoot, "work"), []byte("block directory creation"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = admitExtensionExecutableWithRoot(executable, digest, func() (string, *extensionRuntimeRootGuard, error) {
			return runtimeRoot, runtimeGuard, nil
		})
		if !errors.Is(err, errExtensionInvalidExecutable) {
			t.Fatalf("late admission error=%v", err)
		}
		if _, err := os.Stat(runtimeRoot); !os.IsNotExist(err) {
			t.Fatalf("failed admission runtime survived cleanup: %v", err)
		}
	})
}

func TestExtensionProcessHostBoundsAndCleanup(t *testing.T) {
	maximumDeadline := extensionVerificationLimit
	if maximumDeadline != time.Duration(extension.MaxDeadlineMilliseconds)*time.Millisecond {
		t.Fatalf("verification limit=%v, protocol maximum=%dms", maximumDeadline, extension.MaxDeadlineMilliseconds)
	}
	verificationContext, cancelVerification, err := extensionVerificationContext(context.Background())
	if err != nil {
		t.Fatalf("verification deadline: %v", err)
	}
	verificationDeadline, ok := verificationContext.Deadline()
	cancelVerification()
	if !ok || time.Until(verificationDeadline) <= 0 || time.Until(verificationDeadline) > extensionVerificationLimit {
		t.Fatalf("verification deadline=%v, want one shared bound <=%v", verificationDeadline, extensionVerificationLimit)
	}
	deadlineContext, cancelDeadline, err := extensionContextDeadline(context.Background(), maximumDeadline)
	if err != nil || deadlineContext == nil {
		t.Fatalf("maximum protocol deadline: context=%v err=%v", deadlineContext, err)
	}
	cancelDeadline()
	if _, _, err := extensionContextDeadline(context.Background(), maximumDeadline+time.Millisecond); !errors.Is(err, errExtensionInvalidDeadline) {
		t.Fatalf("over-maximum protocol deadline error=%v", err)
	}
	for _, test := range []struct {
		name    string
		mode    string
		wantErr error
	}{
		{name: "valid", mode: "valid"},
		{name: "bounded stderr content", mode: "stderr-content"},
		{name: "partial stdout", mode: "partial", wantErr: errExtensionPartialFrame},
		{name: "oversized stdout", mode: "oversized", wantErr: errExtensionFrameOverflow},
		{name: "stderr flood", mode: "stderr-flood", wantErr: errExtensionStderrOverflow},
		{name: "timeout", mode: "hang", wantErr: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			_, digest, err := stableReadExtensionExecutable(executable)
			if err != nil {
				t.Fatal(err)
			}
			admitted, err := admitExtensionExecutable(executable, digest)
			if err != nil {
				t.Fatal(err)
			}
			root := admitted.root
			defer func() { _ = admitted.remove() }()
			session, err := startExtensionProcess(admitted, extensionHostHelperArgs(test.mode))
			if err != nil {
				t.Fatal(err)
			}
			timeout := 5 * time.Second
			if test.mode == "hang" {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if test.mode == "valid" || test.mode == "stderr-content" {
				if err := session.writeFrame(ctx, []byte(`{"sequence":1}`)); err != nil {
					t.Fatal(err)
				}
			}
			_, gotErr := session.readFrame(ctx)
			if test.mode == "stderr-flood" && gotErr == nil {
				select {
				case <-session.stderr.overflowed():
					gotErr = errExtensionStderrOverflow
				case <-ctx.Done():
					gotErr = ctx.Err()
				}
			}
			if test.wantErr == nil && gotErr != nil {
				t.Fatalf("read valid frame: %v", gotErr)
			}
			if test.wantErr != nil && !errors.Is(gotErr, test.wantErr) {
				t.Fatalf("read error=%v want=%v", gotErr, test.wantErr)
			}
			cleanup := session.cleanup(extensionCancelGrace)
			if cleanup.assurance != extensionCleanupAssurance() {
				t.Fatalf("cleanup assurance=%q", cleanup.assurance)
			}
			if err := admitted.remove(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("extension runtime survived cleanup: %v", err)
			}
		})
	}

	t.Run("descendant cleanup", func(t *testing.T) {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		_, digest, err := stableReadExtensionExecutable(executable)
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := admitExtensionExecutable(executable, digest)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = admitted.remove() }()
		marker := filepath.Join(t.TempDir(), "escaped-descendant")
		arguments := append(extensionHostHelperArgs("descendant"), marker)
		session, err := startExtensionProcess(admitted, arguments)
		if err != nil {
			t.Fatal(err)
		}
		startCtx, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelStart()
		frame, err := session.readFrame(startCtx)
		if err != nil || string(frame) != `{"descendant":"started"}` {
			t.Fatalf("descendant start frame=%q err=%v", frame, err)
		}
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancelWait()
		if _, err := session.readFrame(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("descendant parent read error=%v", err)
		}
		cleanup := session.cleanup(extensionCancelGrace)
		if cleanup.assurance != extensionCleanupAssurance() || !cleanup.complete || cleanup.err != nil {
			t.Fatalf(
				"descendant cleanup assurance=%q complete=%t wait error=%v cleanup error=%v",
				cleanup.assurance, cleanup.complete, cleanup.waitErr, cleanup.err,
			)
		}
		time.Sleep(time.Second)
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("descendant survived bounded cleanup: %v", err)
		}
	})
}

func TestExtensionHostNativeHelper(_ *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) < separator+3 || os.Args[separator+1] != extensionHostHelperMarker {
		return
	}
	if os.Getenv("ATL_EXTENSION_SECRET_CANARY") != "" || os.Getenv("PATH") != "" || os.Getenv("HTTP_PROXY") != "" {
		fmt.Fprintln(os.Stdout, `{"environment":"leaked"}`)
		os.Exit(91)
	}
	switch os.Args[separator+2] {
	case "valid":
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			os.Exit(92)
		}
		fmt.Fprintln(os.Stdout, `{"sequence":2}`)
	case "stderr-content":
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			os.Exit(92)
		}
		fmt.Fprint(os.Stderr, "ignored diagnostic")
		fmt.Fprintln(os.Stdout, `{"sequence":2}`)
	case "partial":
		fmt.Fprint(os.Stdout, `{`)
	case "oversized":
		fmt.Fprintln(os.Stdout, strings.Repeat("x", extensionFrameMaxBytes+1))
	case "stderr-flood":
		fmt.Fprint(os.Stderr, strings.Repeat("x", extensionStderrMaxBytes+1))
		fmt.Fprintln(os.Stdout, `{"sequence":2}`)
	case "hang":
		time.Sleep(30 * time.Second)
	case "descendant":
		if len(os.Args) != separator+4 {
			os.Exit(94)
		}
		// Keep both helper processes alive through the graceful interrupt so
		// this oracle deterministically exercises the hard group-kill path.
		signal.Ignore(os.Interrupt)
		command := exec.Command(os.Args[0], append(extensionHostHelperArgs("descendant-child"), os.Args[separator+3])...)
		command.Env = os.Environ()
		if err := command.Start(); err != nil {
			os.Exit(95)
		}
		fmt.Fprintln(os.Stdout, `{"descendant":"started"}`)
		time.Sleep(30 * time.Second)
	case "descendant-child":
		if len(os.Args) != separator+4 {
			os.Exit(96)
		}
		time.Sleep(750 * time.Millisecond)
		if err := os.WriteFile(os.Args[separator+3], []byte("escaped"), 0o600); err != nil {
			os.Exit(97)
		}
	default:
		os.Exit(93)
	}
	os.Exit(0)
}

func extensionHostHelperArgs(mode string) []string {
	return []string{"-test.run=^TestExtensionHostNativeHelper$", "--", extensionHostHelperMarker, mode}
}

func sortedEnvironmentNames(environment map[string]string) []string {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
