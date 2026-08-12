package agenteval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/extension"
)

func TestVerifyExtensionProtocolReportIsContentMinimized(t *testing.T) {
	if extensionFrameMaxBytes != extension.MaxFrameBytes {
		t.Fatalf("host frame bound=%d, protocol bound=%d", extensionFrameMaxBytes, extension.MaxFrameBytes)
	}
	if extensionSessionMaxBytes != extension.MaxSessionBytes || extensionStderrMaxBytes != extension.MaxStderrBytes {
		t.Fatalf("host transport bounds session=%d stderr=%d", extensionSessionMaxBytes, extensionStderrMaxBytes)
	}
	firstSession, err := randomExtensionIdentity()
	if err != nil {
		t.Fatal(err)
	}
	secondSession, err := randomExtensionIdentity()
	if err != nil || firstSession == secondSession || !validSHA256(firstSession) || !validSHA256(secondSession) {
		t.Fatalf("fresh session identities first=%q second=%q err=%v", firstSession, secondSession, err)
	}
	executable := buildOutOfPackageExtensionSample(t)
	_, executableDigest, err := stableReadExtensionExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	manifest := extensionHostTestManifest(executableDigest)
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle := extensionHostTestBundle(manifestData, executableDigest, manifest)
	privateBundle := bundle
	privateBundle.Cases = append([]ExtensionConformanceCase(nil), bundle.Cases...)
	privateBundle.Cases[0].Inputs = []extension.ArtifactReference{{
		ID: "private-input", Schema: "agent-eval/synthetic-input", SchemaVersion: 1,
		SHA256: stringsRepeatHex('d'), Privacy: extension.PrivacyOwnerPrivate,
	}}
	if _, err := EncodeExtensionConformanceBundle(privateBundle); err == nil {
		t.Fatal("public conformance bundle accepted an owner-private input reference")
	}
	minimizedBundle := bundle
	minimizedBundle.Cases = append([]ExtensionConformanceCase(nil), bundle.Cases...)
	minimizedBundle.Cases[0].Policy.OutputPrivacy = extension.PrivacyContentMinimized
	if _, err := EncodeExtensionConformanceBundle(minimizedBundle); err == nil {
		t.Fatal("public conformance bundle granted content-minimized output authority")
	}
	nullCollectionBundle := bundle
	nullCollectionBundle.Cases = append([]ExtensionConformanceCase(nil), bundle.Cases...)
	nullCollectionBundle.Cases[0].Configuration = nil
	if _, err := EncodeExtensionConformanceBundle(nullCollectionBundle); err == nil {
		t.Fatal("conformance bundle accepted a null configuration collection")
	}
	bundleData, err := EncodeExtensionConformanceBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	manifestPath := directory + string(os.PathSeparator) + "manifest.json"
	bundlePath := directory + string(os.PathSeparator) + "bundle.json"
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, bundleData, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_EXTENSION_SECRET_CANARY", "must-not-reach-child-or-report")
	ledgerParent := t.TempDir()
	if err := os.Chmod(ledgerParent, 0o700); err != nil {
		t.Fatal(err)
	}
	verify := func(ledgerRoot string) (ExtensionConformanceReport, error) {
		if runtime.GOOS == "windows" {
			// The hosted Windows contour continues to prove the process protocol
			// itself. The public file facade fails closed before process entry on
			// Windows until the persistent ledger can prove directory durability.
			return verifyExtensionProtocol(context.Background(), manifestData, executable, nil, bundleData, nil, "")
		}
		return VerifyExtensionProtocolFiles(context.Background(), manifestPath, executable, bundlePath, ledgerRoot)
	}
	first, err := verify(filepath.Join(ledgerParent, "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := verify(filepath.Join(ledgerParent, "second"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !first.ProtocolConformant || first.Scope != extensionConformanceScope ||
		len(first.Cases) != len(manifest.Component.Operations)+1 || first.CleanupAssurance != extensionCleanupAssurance() {
		t.Fatalf("nondeterministic or incomplete reports: first=%+v second=%+v", first, second)
	}
	encoded, err := EncodeExtensionConformanceReport(first)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExtensionConformanceReport(encoded)
	if err != nil || !reflect.DeepEqual(decoded, first) {
		t.Fatalf("report round trip: %v", err)
	}
	for _, forbidden := range []string{
		executable, manifestPath, bundlePath, "ATL_EXTENSION_SECRET_CANARY",
		"must-not-reach-child-or-report", "session_id", "attempt_id", "stderr", "pid", "environment",
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("content-minimized report contains forbidden marker %q", forbidden)
		}
	}
	incomplete := first
	incomplete.Cases = append([]ExtensionConformanceCaseReport(nil), first.Cases[:len(first.Cases)-1]...)
	if err := ValidateExtensionConformanceReport(incomplete); err == nil {
		t.Fatal("protocol-conformant report omitted a supported operation")
	}
	partial := first
	partial.Capabilities = append([]extension.CapabilityClaim(nil), first.Capabilities...)
	partial.Capabilities[0].State = extension.CapabilityUnsupported
	partial.Cases = make([]ExtensionConformanceCaseReport, 0, len(first.Cases)-1)
	for _, testCase := range first.Cases {
		if testCase.Terminal != extensionExpectedCanceled && testCase.Operation == manifest.Component.Operations[0] {
			continue
		}
		partial.Cases = append(partial.Cases, testCase)
	}
	if err := ValidateExtensionConformanceReport(partial); err != nil {
		t.Fatalf("report covering exactly the reduced supported operation set: %v", err)
	}
	missingClaims := first
	missingClaims.Capabilities = nil
	if err := ValidateExtensionConformanceReport(missingClaims); err == nil {
		t.Fatal("protocol-conformant report omitted capability claims")
	}
	testExtensionHostileCasesDoNotReplay(t)
	testExtensionBundlePrefixIsAbsorbingUnknown(t)
	testExtensionCanceledContextRefusesBeforeSpawn(t)
	testExtensionUnsupportedIsolationRefusesBeforeExecutableAdmission(t)
}

func testExtensionUnsupportedIsolationRefusesBeforeExecutableAdmission(t *testing.T) {
	t.Helper()
	manifest := extensionHostileTestManifest(stringsRepeatHex('a'))
	manifest.Requirements = []extension.EnforcementRequirement{
		extension.EnforcementBestEffortProcessGroup, extension.EnforcementBoundedIO,
		extension.EnforcementDeadline, extension.EnforcementExactEnvironment,
		extension.EnforcementFilesystemIsolation, extension.EnforcementPrivateWorkingDirectory,
	}
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle := extensionHostileTestBundle(manifestData, manifest.ExecutableSHA256, manifest, time.Second)
	bundleData, err := EncodeExtensionConformanceBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	missingExecutable := t.TempDir() + string(os.PathSeparator) + "missing-component"
	if _, err := verifyExtensionProtocol(context.Background(), manifestData, missingExecutable, nil, bundleData, nil, ""); !errors.Is(err, errExtensionUnsupportedPolicy) {
		t.Fatalf("unsupported filesystem isolation reached executable admission: %v", err)
	}
}

func testExtensionHostileCasesDoNotReplay(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, executableDigest, err := stableReadExtensionExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	manifest := extensionHostileTestManifest(executableDigest)
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		mode string
		want error
	}{
		{mode: "initialized-identity-drift", want: errExtensionOutcomeUnknown},
		{mode: "capability-escalation", want: errExtensionOutcomeUnknown},
		{mode: "terminal-before-invoke", want: errExtensionOutcomeUnknown},
		{mode: "terminal-identity-drift", want: errExtensionOutcomeUnknown},
		{mode: "output-budget-escalation", want: errExtensionOutcomeUnknown},
		{mode: "malformed-terminal", want: errExtensionOutcomeUnknown},
		{mode: "duplicate-terminal", want: errExtensionOutcomeUnknown},
		{mode: "crash-after-invoke", want: errExtensionOutcomeUnknown},
		{mode: "block-invoke-read", want: errExtensionOutcomeUnknown},
		{mode: "missing-terminal", want: errExtensionOutcomeUnknown},
	} {
		t.Run(test.mode, func(t *testing.T) {
			deadline := 5 * time.Second
			bundle := extensionHostileTestBundle(manifestData, executableDigest, manifest, deadline)
			if test.mode == "block-invoke-read" {
				bundle.Cases[0].Inputs = largeExtensionInputReferences()
			}
			bundleData, err := EncodeExtensionConformanceBundle(bundle)
			if err != nil {
				t.Fatal(err)
			}
			counter := t.TempDir() + string(os.PathSeparator) + "spawns"
			arguments := []string{"-test.run=^TestExtensionProtocolHostileHelper$", "--", test.mode, counter}
			_, gotErr := verifyExtensionProtocol(context.Background(), manifestData, executable, arguments, bundleData, nil, "")
			if !errors.Is(gotErr, test.want) {
				t.Fatalf("error=%v, want %v", gotErr, test.want)
			}
			data, err := os.ReadFile(counter)
			if err != nil || !bytes.Equal(data, []byte("spawned\n")) {
				t.Fatalf("extension was replayed or not launched exactly once: %q err=%v", data, err)
			}
		})
	}
}

func testExtensionBundlePrefixIsAbsorbingUnknown(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, executableDigest, err := stableReadExtensionExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	manifest := extensionHostileTestManifest(executableDigest)
	manifest.Component.Capabilities[1].State = extension.CapabilitySupported
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle := extensionHostileTestBundle(manifestData, executableDigest, manifest, 5*time.Second)
	bundle.Cases = []ExtensionConformanceCase{
		{ID: "a-report", Role: manifest.Component.Role, Operation: extension.OperationReport,
			Configuration: []extension.ConfigurationValue{}, Inputs: []extension.ArtifactReference{},
			Policy:               extension.InvocationPolicy{OutputPrivacy: extension.PrivacyPublic, Replay: extension.ReplayUnsafe},
			DeadlineMilliseconds: 5000, Expected: ExtensionConformanceExpected{Type: "result"}},
		{ID: "b-validate", Role: manifest.Component.Role, Operation: extension.OperationValidate,
			Configuration: []extension.ConfigurationValue{}, Inputs: []extension.ArtifactReference{},
			Policy:               extension.InvocationPolicy{OutputPrivacy: extension.PrivacyPublic, Replay: extension.ReplayUnsafe},
			DeadlineMilliseconds: 5000, Expected: ExtensionConformanceExpected{Type: "result"}},
		{ID: "z-cancel-validate", Role: manifest.Component.Role, Operation: extension.OperationValidate,
			Configuration: []extension.ConfigurationValue{}, Inputs: []extension.ArtifactReference{extensionCancelProbeInput()},
			Policy:               extension.InvocationPolicy{OutputPrivacy: extension.PrivacyPublic, Replay: extension.ReplayUnsafe},
			DeadlineMilliseconds: 5000, Expected: ExtensionConformanceExpected{Type: extensionExpectedCanceled}},
	}
	bundleData, err := EncodeExtensionConformanceBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	counter := t.TempDir() + string(os.PathSeparator) + "spawns"
	arguments := []string{"-test.run=^TestExtensionProtocolHostileHelper$", "--", "second-handshake-fails", counter}
	if _, err := verifyExtensionProtocol(context.Background(), manifestData, executable, arguments, bundleData, nil, ""); !errors.Is(err, errExtensionOutcomeUnknown) {
		t.Fatalf("successful non-replay-safe prefix did not make later admission failure unknown: %v", err)
	}
	data, err := os.ReadFile(counter)
	if err != nil || !bytes.Equal(data, []byte("spawned\nspawned\n")) {
		t.Fatalf("bundle cases were replayed or skipped: %q err=%v", data, err)
	}
}

func testExtensionCanceledContextRefusesBeforeSpawn(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, executableDigest, err := stableReadExtensionExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	manifest := extensionHostileTestManifest(executableDigest)
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundleData, err := EncodeExtensionConformanceBundle(extensionHostileTestBundle(manifestData, executableDigest, manifest, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	counter := t.TempDir() + string(os.PathSeparator) + "spawns"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	arguments := []string{"-test.run=^TestExtensionProtocolHostileHelper$", "--", "missing-terminal", counter}
	if _, err := verifyExtensionProtocol(ctx, manifestData, executable, arguments, bundleData, nil, ""); !errors.Is(err, errExtensionCompatibility) {
		t.Fatalf("canceled pre-spawn verification error=%v", err)
	}
	if _, err := os.Stat(counter); !os.IsNotExist(err) {
		t.Fatalf("canceled verifier spawned an extension: %v", err)
	}
}

func TestExtensionProtocolHostileHelper(_ *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) != separator+3 {
		return
	}
	mode, counter := os.Args[separator+1], os.Args[separator+2]
	existing, readErr := os.ReadFile(counter)
	if readErr != nil && !os.IsNotExist(readErr) {
		os.Exit(60)
	}
	spawnNumber := bytes.Count(existing, []byte("spawned\n")) + 1
	file, err := os.OpenFile(counter, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(61)
	}
	if _, err := file.WriteString("spawned\n"); err != nil || file.Close() != nil {
		os.Exit(62)
	}
	if os.Getenv("ATL_EXTENSION_SECRET_CANARY") != "" || os.Getenv("PATH") != "" {
		os.Exit(63)
	}
	reader := bufio.NewReader(os.Stdin)
	initializeLine, err := reader.ReadBytes('\n')
	if err != nil {
		os.Exit(64)
	}
	initialize, err := extension.DecodeFrameLine(initializeLine)
	if err != nil || initialize.Type != extension.MessageInitialize {
		os.Exit(65)
	}
	required := make(map[extension.CapabilityID]bool, len(initialize.Initialize.RequiredCapabilities))
	for _, capability := range initialize.Initialize.RequiredCapabilities {
		required[capability] = true
	}
	operations := extension.OperationsForRole(initialize.Role)
	claims := make([]extension.CapabilityClaim, len(operations))
	for index, operation := range operations {
		id := extension.CapabilityFor(initialize.Role, operation)
		state := extension.CapabilityUnsupported
		if required[id] {
			state = extension.CapabilitySupported
		}
		claims[index] = extension.CapabilityClaim{ID: id, State: state}
	}
	initialized := extension.Frame{
		Schema: initialize.Schema, SchemaVersion: initialize.SchemaVersion, ProtocolVersion: initialize.ProtocolVersion,
		Direction: extension.DirectionExtensionToHost, SessionID: initialize.SessionID, AttemptID: initialize.AttemptID,
		Sequence: 2, Role: initialize.Role, ComponentID: initialize.ComponentID,
		ComponentVersion: initialize.ComponentVersion, ExecutableSHA256: initialize.ExecutableSHA256,
		Type:        extension.MessageInitialized,
		Initialized: &extension.InitializedPayload{SelectedProtocolVersion: extension.ProtocolVersion, Capabilities: claims},
	}
	if mode == "initialized-identity-drift" {
		initialized.AttemptID = stringsRepeatHex('f')
	}
	if mode == "second-handshake-fails" && spawnNumber == 2 {
		initialized.AttemptID = stringsRepeatHex('f')
	}
	if mode == "capability-escalation" {
		for index := range initialized.Initialized.Capabilities {
			initialized.Initialized.Capabilities[index].State = extension.CapabilitySupported
		}
	}
	initializedLine, err := extension.EncodeFrameLine(initialized)
	if err != nil {
		os.Exit(66)
	}
	if _, err := os.Stdout.Write(initializedLine); err != nil {
		os.Exit(67)
	}
	if mode == "terminal-before-invoke" {
		terminal := extension.Frame{
			Schema: initialize.Schema, SchemaVersion: initialize.SchemaVersion, ProtocolVersion: initialize.ProtocolVersion,
			Direction: extension.DirectionExtensionToHost, SessionID: initialize.SessionID, AttemptID: initialize.AttemptID,
			Sequence: 4, Role: initialize.Role, ComponentID: initialize.ComponentID,
			ComponentVersion: initialize.ComponentVersion, ExecutableSHA256: initialize.ExecutableSHA256,
			Type: extension.MessageResult,
			Result: &extension.ResultPayload{
				InvocationID: stringsRepeatHex('f'), Operation: extension.OperationReport,
				Outputs: []extension.ArtifactReference{},
			},
		}
		terminalLine, encodeErr := extension.EncodeFrameLine(terminal)
		if encodeErr != nil {
			os.Exit(74)
		}
		_, _ = os.Stdout.Write(terminalLine)
		time.Sleep(30 * time.Second)
	}
	if mode == "initialized-identity-drift" || mode == "capability-escalation" ||
		(mode == "second-handshake-fails" && spawnNumber == 2) {
		time.Sleep(30 * time.Second)
	}
	if mode == "block-invoke-read" {
		time.Sleep(30 * time.Second)
	}
	invokeLine, err := reader.ReadBytes('\n')
	if err != nil {
		os.Exit(68)
	}
	invoke, err := extension.DecodeFrameLine(invokeLine)
	if err != nil || invoke.Type != extension.MessageInvoke {
		os.Exit(69)
	}
	switch mode {
	case "missing-terminal":
		time.Sleep(30 * time.Second)
	case "crash-after-invoke":
		os.Exit(70)
	case "malformed-terminal":
		_, _ = os.Stdout.Write([]byte("{}\n"))
	case "terminal-identity-drift", "output-budget-escalation", "duplicate-terminal":
		var outputs []extension.ArtifactReference
		if mode == "output-budget-escalation" {
			outputs = []extension.ArtifactReference{{
				ID: "output", Schema: "agent-eval/synthetic-output", SchemaVersion: 1,
				SHA256: stringsRepeatHex('e'), SizeBytes: 1, Privacy: extension.PrivacyContentMinimized,
			}}
		}
		result, err := extension.NewResult(invoke, outputs)
		if err != nil {
			os.Exit(71)
		}
		if mode == "terminal-identity-drift" {
			result.AttemptID = stringsRepeatHex('f')
		}
		resultLine, err := extension.EncodeFrameLine(result)
		if err != nil {
			os.Exit(72)
		}
		_, _ = os.Stdout.Write(resultLine)
		if mode == "duplicate-terminal" {
			_, _ = os.Stdout.Write(resultLine)
		}
	case "second-handshake-fails":
		result, err := extension.NewResult(invoke, nil)
		if err != nil {
			os.Exit(75)
		}
		resultLine, err := extension.EncodeFrameLine(result)
		if err != nil {
			os.Exit(76)
		}
		_, _ = os.Stdout.Write(resultLine)
	default:
		os.Exit(73)
	}
	_, _ = reader.ReadByte()
	os.Exit(0)
}

func buildOutOfPackageExtensionSample(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", "extension-sample", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte("module example.invalid/agent-eval-extension-sample\n\ngo 1.26.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "main.go"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	clear(source)
	name := "extension-sample"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	output := filepath.Join(moduleRoot, name)
	command := exec.Command("go", "build", "-mod=readonly", "-trimpath", "-o", output, ".")
	command.Dir = moduleRoot
	command.Env = append(extensionTestGoEnvironment(os.Environ()), "GOPROXY=off", "GOSUMDB=off")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build dependency-free extension sample: %v: %s", err, data)
	}
	return output
}

func extensionTestGoEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+2)
	for _, value := range environment {
		if bytes.HasPrefix([]byte(value), []byte("GOROOT=")) || bytes.HasPrefix([]byte(value), []byte("GOWORK=")) ||
			bytes.HasPrefix([]byte(value), []byte("GOTOOLCHAIN=")) {
			continue
		}
		result = append(result, value)
	}
	return append(result, "GOWORK=off", "GOTOOLCHAIN=auto")
}

func extensionHostTestManifest(executableDigest string) extension.Manifest {
	role := extension.RoleAgentAdapter
	operations := extension.OperationsForRole(role)
	capabilities := make([]extension.CapabilityClaim, len(operations))
	for index, operation := range operations {
		capabilities[index] = extension.CapabilityClaim{
			ID: extension.CapabilityFor(role, operation), State: extension.CapabilitySupported,
		}
	}
	return extension.Manifest{
		Schema: extension.ManifestSchema, SchemaVersion: extension.ManifestSchemaVersion,
		ContractVersion: extension.ContractVersion, ProtocolVersions: []int{extension.ProtocolVersion},
		Component: extension.Descriptor{
			ID: "synthetic/adapter", Version: "1.0.0", Role: role,
			Operations: operations, Capabilities: capabilities,
		},
		ExecutableSHA256:    executableDigest,
		ConfigurationSchema: []extension.ConfigurationField{},
		Platforms:           []extension.Platform{{OS: runtime.GOOS, Architecture: runtime.GOARCH}},
		Requirements: []extension.EnforcementRequirement{
			extension.EnforcementBestEffortProcessGroup, extension.EnforcementBoundedIO,
			extension.EnforcementDeadline, extension.EnforcementExactEnvironment,
			extension.EnforcementPrivateWorkingDirectory,
		},
	}
}

func extensionHostileTestManifest(executableDigest string) extension.Manifest {
	role := extension.RoleReporter
	operations := extension.OperationsForRole(role)
	capabilities := make([]extension.CapabilityClaim, len(operations))
	for index, operation := range operations {
		state := extension.CapabilityUnsupported
		if operation == extension.OperationReport {
			state = extension.CapabilitySupported
		}
		capabilities[index] = extension.CapabilityClaim{ID: extension.CapabilityFor(role, operation), State: state}
	}
	return extension.Manifest{
		Schema: extension.ManifestSchema, SchemaVersion: extension.ManifestSchemaVersion,
		ContractVersion: extension.ContractVersion, ProtocolVersions: []int{extension.ProtocolVersion},
		Component: extension.Descriptor{
			ID: "synthetic/hostile-reporter", Version: "1.0.0", Role: role,
			Operations: operations, Capabilities: capabilities,
		},
		ExecutableSHA256:    executableDigest,
		ConfigurationSchema: []extension.ConfigurationField{},
		Platforms:           []extension.Platform{{OS: runtime.GOOS, Architecture: runtime.GOARCH}},
		Requirements: []extension.EnforcementRequirement{
			extension.EnforcementBestEffortProcessGroup, extension.EnforcementBoundedIO,
			extension.EnforcementDeadline, extension.EnforcementExactEnvironment,
			extension.EnforcementPrivateWorkingDirectory,
		},
	}
}

func extensionHostileTestBundle(
	manifestData []byte,
	executableDigest string,
	manifest extension.Manifest,
	deadline time.Duration,
) ExtensionConformanceBundle {
	return ExtensionConformanceBundle{
		Schema: ExtensionConformanceBundleSchema, SchemaVersion: ExtensionConformanceBundleSchemaVersion,
		ContractVersion: extension.ContractVersion, ContractSHA256: extension.ContractSHA256(),
		ProtocolVersion: extension.ProtocolVersion, ProtocolSHA256: extension.ProtocolSHA256(),
		ManifestSHA256: sha256HexBytes(manifestData), ExecutableSHA256: executableDigest,
		Cases: []ExtensionConformanceCase{{
			ID: "a-report", Role: manifest.Component.Role, Operation: extension.OperationReport,
			Configuration:        []extension.ConfigurationValue{},
			Inputs:               []extension.ArtifactReference{},
			Policy:               extension.InvocationPolicy{OutputPrivacy: extension.PrivacyPublic, Replay: extension.ReplayUnsafe},
			DeadlineMilliseconds: int64(deadline / time.Millisecond), Expected: ExtensionConformanceExpected{Type: "result"},
		}, {
			ID: "z-cancel-report", Role: manifest.Component.Role, Operation: extension.OperationReport,
			Configuration:        []extension.ConfigurationValue{},
			Inputs:               []extension.ArtifactReference{extensionCancelProbeInput()},
			Policy:               extension.InvocationPolicy{OutputPrivacy: extension.PrivacyPublic, Replay: extension.ReplayUnsafe},
			DeadlineMilliseconds: int64(deadline / time.Millisecond), Expected: ExtensionConformanceExpected{Type: extensionExpectedCanceled},
		}},
	}
}

func largeExtensionInputReferences() []extension.ArtifactReference {
	inputs := make([]extension.ArtifactReference, extension.MaxCollectionEntries)
	for index := range inputs {
		inputs[index] = extension.ArtifactReference{
			ID:     fmt.Sprintf("input-%03d-%s", index, strings.Repeat("x", 110)),
			Schema: "agent-eval/" + strings.Repeat("s", 110), SchemaVersion: 1,
			SHA256: stringsRepeatHex('d'), Privacy: extension.PrivacyPublic,
		}
	}
	return inputs
}

func extensionHostTestBundle(manifestData []byte, executableDigest string, manifest extension.Manifest) ExtensionConformanceBundle {
	cases := make([]ExtensionConformanceCase, 0, len(manifest.Component.Operations)+1)
	for _, operation := range manifest.Component.Operations {
		cases = append(cases, ExtensionConformanceCase{
			ID: string(operation), Role: manifest.Component.Role, Operation: operation,
			Configuration: []extension.ConfigurationValue{},
			Inputs:        []extension.ArtifactReference{},
			Policy: extension.InvocationPolicy{
				MaxOutputArtifacts: 0, MaxOutputBytes: 0,
				OutputPrivacy: extension.PrivacyPublic, Replay: extension.ReplayUnsafe,
			},
			DeadlineMilliseconds: int64((5 * time.Second) / time.Millisecond),
			Expected:             ExtensionConformanceExpected{Type: "result"},
		})
	}
	cancelOperation := manifest.Component.Operations[len(manifest.Component.Operations)-1]
	cases = append(cases, ExtensionConformanceCase{
		ID: "cancel-" + string(cancelOperation), Role: manifest.Component.Role, Operation: cancelOperation,
		Configuration: []extension.ConfigurationValue{}, Inputs: []extension.ArtifactReference{extensionCancelProbeInput()},
		Policy:               extension.InvocationPolicy{OutputPrivacy: extension.PrivacyPublic, Replay: extension.ReplayUnsafe},
		DeadlineMilliseconds: int64((5 * time.Second) / time.Millisecond),
		Expected:             ExtensionConformanceExpected{Type: extensionExpectedCanceled},
	})
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return ExtensionConformanceBundle{
		Schema: ExtensionConformanceBundleSchema, SchemaVersion: ExtensionConformanceBundleSchemaVersion,
		ContractVersion: extension.ContractVersion, ContractSHA256: extension.ContractSHA256(),
		ProtocolVersion: extension.ProtocolVersion, ProtocolSHA256: extension.ProtocolSHA256(),
		ManifestSHA256: sha256HexBytes(manifestData), ExecutableSHA256: executableDigest, Cases: cases,
	}
}

func extensionCancelProbeInput() extension.ArtifactReference {
	return extension.ArtifactReference{
		ID: "cancel-probe", Schema: "agent-eval/synthetic-cancel-probe", SchemaVersion: 1,
		SHA256: stringsRepeatHex('d'), Privacy: extension.PrivacyPublic,
	}
}

func ExampleExtensionConformanceReport() {
	fmt.Println(extensionConformanceScope)
	// Output: extension_protocol
}
