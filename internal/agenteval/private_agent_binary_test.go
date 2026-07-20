package agenteval

import (
	"context"
	"debug/macho"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateAgentMachOExecutableRequiresMainEntryPoint(t *testing.T) {
	file := &macho.File{FileHeader: macho.FileHeader{Type: macho.TypeExec, Cpu: privateAgentMachOCPU(runtime.GOARCH)}, ByteOrder: binary.LittleEndian}
	if validPrivateAgentMachOExecutable(file, runtime.GOARCH) {
		t.Fatal("Mach-O executable without LC_MAIN was accepted")
	}
}

func TestInspectPrivateAgentBinaryAcceptsNativeExecutableAndSymlink(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	direct, _, err := inspectPrivateAgentBinary(executable, "")
	if err != nil {
		t.Fatalf("direct native executable: %v", err)
	}
	if !validSHA256(direct.bytesSHA256) || !validSHA256(direct.provenanceSHA256) || direct.identity != "binary-sha256:"+direct.bytesSHA256 {
		t.Fatalf("invalid contract: %+v", direct)
	}

	link := filepath.Join(t.TempDir(), "reviewed-agent")
	if runtime.GOOS == "windows" {
		link += ".exe"
	}
	if err := os.Symlink(executable, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linked, _, err := inspectPrivateAgentBinary(link, "")
	if err != nil {
		t.Fatalf("symlink to native executable: %v", err)
	}
	if linked != direct {
		t.Fatalf("symlink contract differs from direct contract")
	}
}

func TestPrivateAgentLinuxSandboxResourceIsBoundCopiedAndDriftChecked(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Codex bwrap companion is Linux-only")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agentData, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	agentData = append(agentData, []byte("\x00"+privateAgentLinuxSandboxMarker+"\x00")...)

	sourceRoot := t.TempDir()
	sourceBin := filepath.Join(sourceRoot, "bin")
	resourceRoot := filepath.Join(sourceRoot, "codex-resources")
	if err := os.MkdirAll(sourceBin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(sourceBin, "codex")
	resourcePath := filepath.Join(resourceRoot, "bwrap")
	if err := os.WriteFile(agentPath, agentData, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resourcePath, agentData[:len(agentData)-len(privateAgentLinuxSandboxMarker)-2], 0o700); err != nil {
		t.Fatal(err)
	}

	reviewed, _, err := inspectPrivateAgentBinary(agentPath, "")
	if err != nil {
		t.Fatalf("inspect reviewed agent: %v", err)
	}
	if reviewed.resourceCanonicalPath != resourcePath || reviewed.resourceRelativePath != privateAgentLinuxSandboxRelativePath || !validSHA256(reviewed.resourceBytesSHA256) {
		t.Fatalf("resource contract not bound: %+v", reviewed)
	}

	privateRoot := t.TempDir()
	snapshotRoot := filepath.Join(privateRoot, "snapshot")
	destination := filepath.Join(snapshotRoot, "bin", "agent")
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyReviewedPrivateAgent(privateRoot, snapshotRoot, reviewed, destination); err != nil {
		t.Fatalf("copy reviewed agent: %v", err)
	}
	if err := verifyPrivateAgentResourceSnapshot(destination, reviewed); err != nil {
		t.Fatalf("verify copied resource: %v", err)
	}
	copiedResource := filepath.Join(snapshotRoot, filepath.FromSlash(privateAgentLinuxSandboxRelativePath))
	if info, err := os.Lstat(copiedResource); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 {
		t.Fatalf("copied resource mode: info=%v err=%v", info, err)
	}

	if err := os.WriteFile(resourcePath, append(agentData, 'x'), 0o700); err != nil {
		t.Fatal(err)
	}
	secondSnapshot := filepath.Join(privateRoot, "second-snapshot")
	secondDestination := filepath.Join(secondSnapshot, "bin", "agent")
	if err := os.MkdirAll(filepath.Dir(secondDestination), 0o700); err != nil {
		t.Fatal(err)
	}
	err = copyReviewedPrivateAgent(privateRoot, secondSnapshot, reviewed, secondDestination)
	assertPrivatePlanError(t, err, "agent_binary_resource_drift")
}

func TestPrivateAgentLinuxSandboxResourceRejectsMissingAndMalformedHelper(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Codex bwrap companion is Linux-only")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\x00"+privateAgentLinuxSandboxMarker+"\x00")...)

	for _, test := range []struct {
		name       string
		helperData []byte
		want       string
	}{
		{name: "missing", want: "agent_binary_resource"},
		{name: "malformed", helperData: []byte("not a native executable"), want: "agent_binary_resource_format"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
				t.Fatal(err)
			}
			agent := filepath.Join(root, "bin", "codex")
			if err := os.WriteFile(agent, data, 0o700); err != nil {
				t.Fatal(err)
			}
			if test.helperData != nil {
				if err := os.MkdirAll(filepath.Join(root, "codex-resources"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "codex-resources", "bwrap"), test.helperData, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := inspectPrivateAgentBinary(agent, "")
			assertPrivatePlanError(t, err, test.want)
		})
	}
}

func TestExecutePrivatePlanRejectsChangedLinuxSandboxResourceAsInputDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Codex bwrap companion is Linux-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	distribution := t.TempDir()
	if err := os.MkdirAll(filepath.Join(distribution, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(distribution, "codex-resources"), 0o700); err != nil {
		t.Fatal(err)
	}
	agentData, err := os.ReadFile(fixture.agent)
	if err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(distribution, "bin", "codex")
	helper := filepath.Join(distribution, "codex-resources", "bwrap")
	if err := os.WriteFile(codex, append(agentData, []byte("\x00"+privateAgentLinuxSandboxMarker+"\x00")...), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, agentData, 0o700); err != nil {
		t.Fatal(err)
	}

	create := fixture.createOptions()
	create.AgentBinary = codex
	preview, err := CreatePrivatePlan(context.Background(), create)
	if err != nil {
		t.Fatalf("create resource-bound plan: %v", err)
	}
	if err := os.WriteFile(helper, append(agentData, 'x'), 0o700); err != nil {
		t.Fatal(err)
	}
	execute := fixture.executeOptions(preview)
	execute.AgentBinary = codex
	_, err = ExecutePrivatePlan(context.Background(), execute)
	assertPrivatePlanError(t, err, "input_drift")
	assertPrivatePlanNoRuntimeInvocation(t, fixture)
}

func TestCreatePrivatePlanRejectsScriptBeforePersistenceOrExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell launcher fixture is Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	launcherRoot := t.TempDir()
	launcher := filepath.Join(launcherRoot, "agent-launcher")
	sideEffect := filepath.Join(launcherRoot, "version-called")
	helper := filepath.Join(launcherRoot, "normal-helper")
	writeTestFile(t, helper, "#!/bin/sh\nexit 0\n", 0o700)
	writeTestFile(t, launcher, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf x >"+shellQuote(sideEffect)+"; printf '%s\\n' 'dependent-agent 1.0'; exit 0; fi\nexec \"${0%/*}/normal-helper\"\n", 0o700)
	options := fixture.createOptions()
	options.AgentBinary = launcher
	_, err := CreatePrivatePlan(context.Background(), options)
	assertPrivatePlanError(t, err, "agent_binary_format")
	entries, readErr := os.ReadDir(filepath.Join(fixture.root, "plans"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("rejected launcher persisted a plan: entries=%d err=%v", len(entries), readErr)
	}
	if _, err := os.Stat(sideEffect); !os.IsNotExist(err) {
		t.Fatalf("static preflight executed launcher --version: %v", err)
	}
	assertPrivatePlanNoRuntimeInvocation(t, fixture)
}

func TestInspectPrivateAgentBinaryRejectsMalformedWrongFormatAndWrongArch(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), nativeAgentTestName("malformed"))
		writeTestFile(t, path, malformedNativeAgentBytes(), 0o700)
		_, _, err := inspectPrivateAgentBinary(path, "")
		assertPrivatePlanError(t, err, "agent_binary_format")
	})
	t.Run("wrong format", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), nativeAgentTestName("wrong-format"))
		writeTestFile(t, path, wrongPlatformAgentBytes(), 0o700)
		_, _, err := inspectPrivateAgentBinary(path, "")
		assertPrivatePlanError(t, err, "agent_binary_platform")
	})
	t.Run("wrong arch", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "main.go")
		binary := filepath.Join(root, nativeAgentTestName("wrong-arch"))
		writeTestFile(t, source, "package main\nfunc main() {}\n", 0o600)
		arch := "amd64"
		if runtime.GOARCH == "amd64" {
			arch = "arm64"
		}
		command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, source)
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+runtime.GOOS, "GOARCH="+arch)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build wrong-arch fixture: %v: %s", err, output)
		}
		_, _, err := inspectPrivateAgentBinary(binary, "")
		assertPrivatePlanError(t, err, "agent_binary_platform")
	})
}

func TestExecutePrivatePlanRejectsRetargetedAgentSymlinkAsInputDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink replacement fixture is Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	link := filepath.Join(filepath.Dir(fixture.agent), "reviewed-agent")
	if err := os.Symlink(fixture.agent, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	create := fixture.createOptions()
	create.AgentBinary = link
	preview, err := CreatePrivatePlan(context.Background(), create)
	if err != nil {
		t.Fatalf("create symlink plan: %v", err)
	}
	second := fixture.agent + "-identical"
	data, err := os.ReadFile(fixture.agent)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, second, string(data), 0o700)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	execute := fixture.executeOptions(preview)
	execute.AgentBinary = link
	_, err = ExecutePrivatePlan(context.Background(), execute)
	assertPrivatePlanError(t, err, "input_drift")
	if strings.Contains(err.Error(), link) || strings.Contains(err.Error(), second) {
		t.Fatalf("drift error leaked executable path: %v", err)
	}
	assertPrivatePlanNoRuntimeInvocation(t, fixture)
}

func TestExecutePrivatePlanSnapshotsCanonicalTargetWithoutVersionInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture is Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	link := filepath.Join(filepath.Dir(fixture.agent), "stable-reviewed-agent")
	if err := os.Symlink(fixture.agent, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	create := fixture.createOptions()
	create.AgentBinary = link
	preview, err := CreatePrivatePlan(context.Background(), create)
	if err != nil {
		t.Fatalf("create symlink plan: %v", err)
	}
	execute := fixture.executeOptions(preview)
	execute.AgentBinary = link
	summary, err := ExecutePrivatePlan(context.Background(), execute)
	if err != nil {
		t.Fatalf("execute symlink plan: %v", err)
	}
	if summary.Status != "completed" || summary.Completed != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	assertPrivatePlanInvocationCount(t, fixture.agent, 1)
	if _, err := os.Stat(fixture.agent + ".calls.version"); !os.IsNotExist(err) {
		t.Fatalf("private lifecycle invoked agent --version: %v", err)
	}
}

func TestQualifiedPrivateAgentIdentityNeverExecutesVersionAcrossThreeSurfaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("side-effect shell fixture is Unix-only")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "version-called")
	binary := filepath.Join(root, "must-not-run")
	writeTestFile(t, binary, "#!/bin/sh\nprintf x >"+shellQuote(marker)+"\nexit 99\n", 0o700)
	identity := "binary-sha256:" + strings.Repeat("a", 64)
	options := RunOptions{AgentBinary: binary, PrivateWorkspaceRoot: root, qualifiedAgentVersion: identity}
	for surface := 0; surface < 3; surface++ {
		got, err := agentRuntimeVersion(context.Background(), options, nil)
		if err != nil || got != identity {
			t.Fatalf("surface %d identity=%q err=%v", surface, got, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("qualified identity path executed --version: %v", err)
	}
}

func nativeAgentTestName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func malformedNativeAgentBytes() string {
	switch runtime.GOOS {
	case "linux":
		return "\x7fELFmalformed"
	case "darwin":
		return "\xcf\xfa\xed\xfemalformed"
	case "windows":
		return "MZmalformed"
	default:
		return "malformed"
	}
}

func wrongPlatformAgentBytes() string {
	if runtime.GOOS == "linux" {
		return "MZnot-a-valid-pe"
	}
	return "\x7fELFnot-a-valid-elf"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
