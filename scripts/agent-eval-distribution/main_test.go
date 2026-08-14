package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testGoldenBundlePath = "testdata/standalone-readability-golden.v1.json"

func TestDistributionBuildVerifySignInstallUninstall(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("distribution candidate contour is Linux/amd64")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "one.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestGoldenBundle(t, source)
	binary := filepath.Join(root, "agent-eval")
	compatibility := filepath.Join(source, "compat.json")
	registry := filepath.Join(source, "one.txt")
	protocol := filepath.Join(source, "one.txt")
	if err := os.WriteFile(binary, testBinaryData("0.1.0-pre-release", strings.Repeat("a", 40), "initial"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compatibility, testCompatibilityBundle(), 0o600); err != nil {
		t.Fatal(err)
	}
	distribution := filepath.Join(root, "dist")
	if err := buildDistribution(buildOptions{
		Binary: binary, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt", "compat.json", testGoldenBundlePath}, SchemaRegistry: registry, Protocol: protocol,
		Output: distribution, Version: "0.1.0-pre-release", ContractVersion: "0.1.0-pre-release",
		SourceCommit: strings.Repeat("a", 40), Platform: "linux", Architecture: "amd64", DeferMarker: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := verifyDistribution(verifyOptions{Distribution: distribution, AllowUnsigned: true}); err == nil {
		t.Fatal("deferred build was accepted before its marker commit")
	}
	if err := commitBuildDistribution(commitOptions{
		Output: distribution, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt", "compat.json", testGoldenBundlePath}, SchemaRegistry: registry, Protocol: protocol,
		SourceCommit: strings.Repeat("a", 40), ContractVersion: "0.1.0-pre-release",
	}); err != nil {
		t.Fatal(err)
	}
	if err := verifyDistribution(verifyOptions{Distribution: distribution, AllowUnsigned: true}); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(root, "private.key")
	publicPath := filepath.Join(root, "public.key")
	if err := os.WriteFile(privatePath, []byte(base64.StdEncoding.EncodeToString(privateKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, []byte(base64.StdEncoding.EncodeToString(publicKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := signDistribution(distribution, privatePath); err != nil {
		t.Fatal(err)
	}
	if err := verifyDistribution(verifyOptions{Distribution: distribution, PublicKey: publicPath}); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(root, "install")
	if err := installDistribution(verifyOptions{Distribution: distribution, PublicKey: publicPath}, prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "bin", binaryName)); err != nil {
		t.Fatal(err)
	}
	if err := uninstallDistribution(prefix, installerConfirmation, publicPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "bin", binaryName)); !os.IsNotExist(err) {
		t.Fatalf("binary remained after uninstall: %v", err)
	}
	if _, err := os.Stat(prefix); !os.IsNotExist(err) {
		t.Fatalf("install prefix remained after uninstall: %v", err)
	}
}

func TestDistributionDeferredCommitRejectsSourceDrift(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "testdata"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "one.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestGoldenBundle(t, source)
	compatibility := filepath.Join(source, "compat.json")
	if err := os.WriteFile(compatibility, testCompatibilityBundle(), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "agent-eval")
	commit := strings.Repeat("a", 40)
	if err := os.WriteFile(binary, testBinaryData(distributionContractVersion, commit, "deferred"), 0o700); err != nil {
		t.Fatal(err)
	}
	distribution := filepath.Join(root, "dist")
	options := buildOptions{
		Binary: binary, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt", "compat.json", testGoldenBundlePath}, SchemaRegistry: filepath.Join(source, "one.txt"), Protocol: filepath.Join(source, "one.txt"),
		Output: distribution, Version: distributionContractVersion, ContractVersion: distributionContractVersion,
		SourceCommit: commit, Platform: "linux", Architecture: "amd64", DeferMarker: true,
	}
	if err := buildDistribution(options); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "one.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitBuildDistribution(commitOptions{
		Output: distribution, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: options.SourceFiles, SchemaRegistry: options.SchemaRegistry, Protocol: options.Protocol,
		SourceCommit: commit, ContractVersion: distributionContractVersion,
	}); err == nil {
		t.Fatal("source drift was accepted by the deferred commit")
	}
	if _, err := os.Stat(filepath.Join(distribution, distributionBuildMark)); err != nil {
		t.Fatalf("deferred output lost its recovery marker: %v", err)
	}
}

func TestDistributionBuildCommandTargetsExecutableCLI(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	runbook, err := os.ReadFile(filepath.Join(root, "docs", "maintainers", "agent-eval-distribution.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{"Makefile": makefile, "runbook": runbook} {
		text := string(contents)
		buildIndex := strings.Index(text, "go -C internal/agenteval build")
		if buildIndex < 0 {
			t.Errorf("%s must contain the evaluator build command", name)
			continue
		}
		command := text[buildIndex:]
		cliIndex := strings.Index(command, "./cmd/agent-eval")
		if cliIndex < 0 {
			t.Errorf("%s must build ./cmd/agent-eval, not the nested module archive", name)
			continue
		}
		outputIndex := strings.Index(command[:cliIndex], "-o ")
		if outputIndex < 0 {
			t.Errorf("%s must build ./cmd/agent-eval, not the nested module archive", name)
		}
	}
}

func TestDistributionMakeRunsCleanBeforeFullGate(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "agent-eval-distribution-full: agent-eval-distribution-clean") {
		t.Fatal("distribution full gate is not ordered after the clean gate")
	}
	for _, want := range []string{
		"before=\"$$(git rev-parse HEAD)\"",
		"$(MAKE) agent-eval-full",
		"test \"$$before\" = \"$$after\"",
		"git diff --name-only",
		"git diff --cached --name-only",
		"git status --porcelain=v1 --untracked-files=all",
		"AGENT_EVAL_DISTRIBUTION_STATE",
		"agent-eval distribution source commit changed during build",
		"AGENT_EVAL_DISTRIBUTION_DEFER_MARKER := 1",
		"--mode commit",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("distribution full gate omitted source stability check %q", want)
		}
	}
	if strings.Contains(text, "agent-eval-distribution: agent-eval-distribution-clean agent-eval-full") {
		t.Fatal("distribution clean and full gates are unordered peer prerequisites")
	}
	if !strings.Contains(text, "agent-eval-distribution: agent-eval-distribution-clean") {
		t.Fatal("distribution build must own the full-gate boundary and start from the clean gate")
	}
	outputIndex := strings.Index(text, "--output \"$(AGENT_EVAL_DISTRIBUTION_OUTPUT)\"")
	if outputIndex < 0 || !strings.Contains(text[outputIndex:], "source commit changed during build") {
		t.Fatal("distribution build must reconcile source identity after publishing")
	}
}

func TestDistributionDefaultSourceSelectionIncludesCompatibilityInputs(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	defaultSelection := "internal/agenteval/cmd/agent-eval,internal/agenteval/go.mod,internal/agenteval/schemaregistry/registry.v1.json,internal/agenteval/testdata/standalone-conformance.v1.json,internal/agenteval/testdata/standalone-readability-golden.v1.json"
	if !strings.Contains(string(data), `flag.String("source-files", "`+defaultSelection+`"`) {
		t.Fatal("default source selection does not cover the evaluator module, registry, conformance, and golden inputs")
	}
}

func TestDistributionRejectsCanonicalAndPathDrift(t *testing.T) {
	manifest := distributionManifest{
		Schema: distributionSchema, SchemaVersion: distributionSchemaV1, ContractVersion: "0.1.0-pre-release",
		Version: "0.1.0-pre-release", Platform: "linux", Architecture: "amd64", SourceCommit: strings.Repeat("a", 40),
		SourceTreeSHA256: strings.Repeat("b", 64), SchemaRegistrySHA256: strings.Repeat("c", 64), ProtocolSHA256: strings.Repeat("d", 64),
		CompatibilityBundleSHA256: strings.Repeat("e", 64), SignatureRequired: true, ContainerBase: "scratch", ContainerEntrypoint: "/agent-eval", ActionVersion: "0.1.0-pre-release",
		Files: []fileEntry{{Name: "../escape", SizeBytes: 1, SHA256: strings.Repeat("f", 64)}},
	}
	if err := validateManifest(manifest); err == nil {
		t.Fatal("path escape was accepted")
	}
	manifest.Files = []fileEntry{{Name: binaryName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)}}
	if err := validateManifest(manifest); err == nil {
		t.Fatal("required artifact omission was accepted")
	}
	manifest.Files = []fileEntry{{Name: actionName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: binaryName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: compatibilityName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: containerfileName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: "extra.txt", SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: provenanceName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: sbomName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)}}
	if err := validateManifest(manifest); err == nil {
		t.Fatal("unsupported manifest member was accepted")
	}
	manifest.Files = []fileEntry{
		{Name: containerfileName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: actionName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: binaryName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: compatibilityName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: provenanceName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
		{Name: sbomName, SizeBytes: 1, SHA256: strings.Repeat("f", 64)},
	}
	for name, mutate := range map[string]func(*distributionManifest){
		"contract":     func(value *distributionManifest) { value.ContractVersion = "9.9.9" },
		"platform":     func(value *distributionManifest) { value.Platform = "solaris" },
		"architecture": func(value *distributionManifest) { value.Architecture = "mips64" },
	} {
		candidate := manifest
		mutate(&candidate)
		if err := validateManifest(candidate); err == nil {
			t.Fatalf("unsupported %s metadata was accepted", name)
		}
	}
}

func TestDistributionBuildBindsBinaryAndSourceBoundary(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "one.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestGoldenBundle(t, source)
	compatibility := filepath.Join(source, "compat.json")
	if err := os.WriteFile(compatibility, testCompatibilityBundle(), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	options := buildOptions{
		Binary: "/bin/true", Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt", "compat.json", testGoldenBundlePath}, SchemaRegistry: filepath.Join(source, "one.txt"),
		Protocol: filepath.Join(source, "one.txt"), Output: filepath.Join(root, "dist"),
		Version: "0.1.0-pre-release", ContractVersion: "0.1.0-pre-release",
		SourceCommit: commit, Platform: "linux", Architecture: "amd64",
	}
	if err := buildDistribution(options); err == nil {
		t.Fatal("unrelated executable was accepted as the evaluator binary")
	}

	validBinary := filepath.Join(root, "agent-eval")
	if err := os.WriteFile(validBinary, testBinaryData(options.Version, commit, "valid"), 0o700); err != nil {
		t.Fatal(err)
	}
	options.Binary = validBinary
	externalCompatibility := filepath.Join(root, "external-compat.json")
	if err := os.WriteFile(externalCompatibility, testCompatibilityBundle(), 0o600); err != nil {
		t.Fatal(err)
	}
	options.Compatibility = externalCompatibility
	options.Output = filepath.Join(root, "dist-external-compat")
	if err := buildDistribution(options); err == nil {
		t.Fatal("compatibility input outside the selected source tree was accepted")
	}
	options.Compatibility = filepath.Join(source, "compat.json")
	options.Output = filepath.Join(source, "nested-output")
	if err := buildDistribution(options); err == nil {
		t.Fatal("distribution output overlapping source tree was accepted")
	}

	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("secret=refuse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options.Output = filepath.Join(root, "dist-sensitive")
	options.SourceFiles = []string{"."}
	if err := buildDistribution(options); err == nil {
		t.Fatal("sensitive source member was accepted")
	}
	if err := os.Mkdir(filepath.Join(source, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "config"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options.SourceFiles = []string{".git/config"}
	options.Output = filepath.Join(root, "dist-git")
	if err := buildDistribution(options); err == nil {
		t.Fatal("direct sensitive source member was accepted")
	}
	if runtime.GOOS != "windows" {
		linkedSource := filepath.Join(root, "source-link")
		if err := os.Symlink(source, linkedSource); err != nil {
			t.Fatal(err)
		}
		options.SourceRoot = linkedSource
		options.SourceFiles = []string{"one.txt"}
		options.Output = filepath.Join(root, "dist-linked-source")
		if err := buildDistribution(options); err == nil {
			t.Fatal("symlinked source root was accepted")
		}
		intermediate := filepath.Join(root, "intermediate-link")
		if err := os.Symlink(source, intermediate); err != nil {
			t.Fatal(err)
		}
		options.SourceRoot = root
		options.SourceFiles = []string{"intermediate-link/one.txt"}
		options.Output = filepath.Join(root, "dist-intermediate-link")
		if err := buildDistribution(options); err == nil {
			t.Fatal("source selection through an intermediate symlink was accepted")
		}
		outputParent := filepath.Join(root, "output-link")
		if err := os.Symlink(source, outputParent); err != nil {
			t.Fatal(err)
		}
		options.SourceRoot = source
		options.Output = filepath.Join(outputParent, "nested")
		if err := buildDistribution(options); err == nil {
			t.Fatal("physical source/output overlap was accepted")
		}
	}
}

func TestDistributionBindsTheBinarySnapshotAcrossTheProbe(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("distribution candidate contour is Linux/amd64")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestGoldenBundle(t, source)
	compatibility := filepath.Join(source, "compat.json")
	if err := os.WriteFile(compatibility, testCompatibilityBundle(), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	binary := filepath.Join(root, "agent-eval")
	mutating := append(testBinaryData(distributionContractVersion, commit, "mutating"),
		[]byte("printf '#tampered\\n' >> \"$0\"\n")...)
	if err := os.WriteFile(binary, mutating, 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "distribution")
	err := buildDistribution(buildOptions{
		Binary: binary, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"compat.json", testGoldenBundlePath}, SchemaRegistry: compatibility, Protocol: compatibility,
		Output: output, Version: distributionContractVersion, ContractVersion: distributionContractVersion,
		SourceCommit: commit, Platform: "linux", Architecture: "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "changed during version probe") {
		t.Fatalf("self-mutating binary was not rejected: %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output was created after binary identity refusal: %v", statErr)
	}
}

func TestSelectedSourceDataUsesTheBoundSnapshot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compat.json"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := hashSelectedTree(root, []string{"compat.json"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compat.json"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := selectedSourceData(root, []string{"compat.json"}, snapshot, filepath.Join(root, "compat.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Fatalf("selected source data was reread after binding: %q", data)
	}
}

func TestDistributionRejectsCompatibilityAndTargetDrift(t *testing.T) {
	valid := testCompatibilityBundle()
	if err := validateCompatibilityBundle(valid, distributionContractVersion); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	compatibility := filepath.Join(root, "compat.json")
	if err := os.WriteFile(compatibility, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, snapshot, err := hashSelectedTree(root, []string{"compat.json"}); err != nil {
		t.Fatal(err)
	} else if err := validateCompatibilityBundleInSnapshot(valid, distributionContractVersion, root, compatibility, snapshot); err == nil {
		t.Fatal("compatibility bundle without its referenced golden was accepted")
	}
	validText := string(valid)
	for name, mutated := range map[string]string{
		"duplicate top-level member":     strings.Replace(validText, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"duplicate nested member":        strings.Replace(validText, `"path":"`+testGoldenBundlePath+`","sha256"`, `"path":"`+testGoldenBundlePath+`","path":"`+testGoldenBundlePath+`","sha256"`, 1),
		"missing required metric member": strings.Replace(validText, `"present":false,"representation":"standalone","required":false`, `"present":false,"representation":"standalone"`, 1),
	} {
		if mutated == validText {
			t.Fatalf("test mutation %q did not change the compatibility bundle", name)
		}
		if err := validateCompatibilityBundle([]byte(mutated), distributionContractVersion); err == nil {
			t.Fatalf("compatibility bundle with %s was accepted", name)
		}
	}
	for _, mutated := range [][]byte{
		[]byte(`{"schema_version":1,"contract_version":"0.1.0-pre-release","golden_bundle":{"path":"x","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"readability":[],"forward_rejection":[],"metric_vectors":[],"future":1}`),
		[]byte(`{"schema_version":1,"contract_version":"future","golden_bundle":{"path":"x","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"readability":[],"forward_rejection":[],"metric_vectors":[]}`),
	} {
		if err := validateCompatibilityBundle(mutated, distributionContractVersion); err == nil {
			t.Fatal("invalid compatibility bundle was accepted")
		}
	}
	manifest := distributionManifest{Platform: "darwin", Architecture: "arm64"}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if err := validateHostManifest(manifest); err == nil {
			t.Fatal("foreign distribution target was accepted for host operation")
		}
	}
}

func TestDistributionBuildIsReproducibleAndUninstallRefusesDrift(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("distribution candidate contour is Linux/amd64")
	}
	root := t.TempDir()
	distributionA := buildTestDistributionNamed(t, root, "repro", "repro-a", []byte("same-binary\n"))
	distributionB := buildTestDistributionNamed(t, root, "repro", "repro-b", []byte("same-binary\n"))
	entriesA, err := os.ReadDir(distributionA)
	if err != nil {
		t.Fatal(err)
	}
	entriesB, err := os.ReadDir(distributionB)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesA) != len(entriesB) {
		t.Fatalf("reproducible distributions have different member counts: %d vs %d", len(entriesA), len(entriesB))
	}
	for _, entry := range entriesA {
		left, err := os.ReadFile(filepath.Join(distributionA, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(distributionB, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if string(left) != string(right) {
			t.Fatalf("reproducible artifact %q differs", entry.Name())
		}
	}
	manifestChecksumPath := filepath.Join(distributionA, manifestChecksumName)
	manifestChecksum, err := os.ReadFile(manifestChecksumPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestChecksumPath, append([]byte(" "), manifestChecksum...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyDistribution(verifyOptions{Distribution: distributionA, AllowUnsigned: true}); err == nil {
		t.Fatal("verify accepted a non-canonical manifest checksum")
	}
	if err := os.WriteFile(manifestChecksumPath, manifestChecksum, 0o644); err != nil {
		t.Fatal(err)
	}
	publicPath, privatePath := generateTestKeyPair(t, root)
	if err := signDistribution(distributionA, privatePath); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(root, "install")
	if err := installDistribution(verifyOptions{Distribution: distributionA, PublicKey: publicPath}, prefix); err != nil {
		t.Fatal(err)
	}
	installedChecksumPath := filepath.Join(prefix, "share", installedSupportDir, installedChecksumName)
	installedChecksum, err := os.ReadFile(installedChecksumPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedChecksumPath, append([]byte(" "), installedChecksum...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := uninstallDistribution(prefix, installerConfirmation, publicPath); err == nil {
		t.Fatal("uninstall accepted a non-canonical installed manifest checksum")
	}
	if err := os.WriteFile(installedChecksumPath, installedChecksum, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", binaryName), []byte("tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := uninstallDistribution(prefix, installerConfirmation, publicPath); err == nil {
		t.Fatal("uninstall accepted a tampered installation")
	}
}

func TestDistributionSBOMUsesRequiredSPDXFields(t *testing.T) {
	data := renderSBOM(buildOptions{Version: "0.1.0-pre-release", SourceCommit: strings.Repeat("a", 40)}, strings.Repeat("b", 64), []fileEntry{{Name: binaryName, SizeBytes: 1, SHA256: strings.Repeat("c", 64)}})
	var document struct {
		DocumentNamespace string `json:"documentNamespace"`
		DataLicense       string `json:"dataLicense"`
		CreationInfo      struct {
			Created  string   `json:"created"`
			Creators []string `json:"creators"`
		} `json:"creationInfo"`
		Packages []map[string]any `json:"packages"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.DataLicense != "CC0-1.0" || document.CreationInfo.Created == "" || len(document.CreationInfo.Creators) != 1 || len(document.Packages) != 2 {
		t.Fatalf("SBOM omitted required SPDX document fields: %+v", document)
	}
	if !strings.Contains(document.DocumentNamespace, strings.Repeat("b", 64)) || !strings.Contains(document.DocumentNamespace, strings.Repeat("c", 64)) {
		t.Fatalf("SBOM namespace is not bound to source and binary identity: %q", document.DocumentNamespace)
	}
	other := renderSBOM(buildOptions{Version: "0.1.0-pre-release", SourceCommit: strings.Repeat("a", 40)}, strings.Repeat("d", 64), []fileEntry{{Name: binaryName, SizeBytes: 1, SHA256: strings.Repeat("c", 64)}})
	var otherDocument struct {
		DocumentNamespace string `json:"documentNamespace"`
	}
	if err := json.Unmarshal(other, &otherDocument); err != nil {
		t.Fatal(err)
	}
	if document.DocumentNamespace == otherDocument.DocumentNamespace {
		t.Fatal("SBOM namespace collided for distinct source identities")
	}
	for _, packageValue := range document.Packages {
		for _, name := range []string{"SPDXID", "name", "downloadLocation", "licenseConcluded", "licenseDeclared", "copyrightText", "filesAnalyzed", "checksums"} {
			if _, ok := packageValue[name]; !ok {
				t.Fatalf("SBOM package omitted required field %q: %+v", name, packageValue)
			}
		}
	}
}

func TestDistributionActionBindsAllExpectedIdentityInputs(t *testing.T) {
	action := renderAction("0.1.0-pre-release", strings.Repeat("a", 40), strings.Repeat("b", 64))
	if strings.Contains(action, "\t") {
		t.Fatalf("action contains tab indentation:\n%s", action)
	}
	if !strings.Contains(action, "\n        python3 - \"$tmp_json\"") || !strings.Contains(action, "\n        PY\n") {
		t.Fatalf("action heredoc is not validly indented:\n%s", action)
	}
	for _, want := range []string{
		"expected-version:", "expected-commit:", "expected-binary-sha256:",
		"test \"$EXPECTED_VERSION\" = \"0.1.0-pre-release\"",
		"test \"$EXPECTED_COMMIT\" = \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"",
		"test \"$EXPECTED_BINARY_SHA256\" = \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"",
		"sha256sum \"$tmp\"",
		"mktemp",
		"tmp_json=",
		"python3 - \"$tmp_json\"",
	} {
		if !strings.Contains(action, want) {
			t.Fatalf("action omitted identity binding %q:\n%s", want, action)
		}
	}
}

func TestDistributionRollbackRestoresVerifiedCandidate(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("distribution candidate contour is Linux/amd64")
	}
	root := t.TempDir()
	oldDistribution := buildTestDistribution(t, root, "a-old", []byte("old-binary\n"))
	newDistribution := buildTestDistribution(t, root, "b-new", []byte("new-binary\n"))
	publicPath, privatePath := generateTestKeyPair(t, root)
	for _, distribution := range []string{oldDistribution, newDistribution} {
		if err := signDistribution(distribution, privatePath); err != nil {
			t.Fatal(err)
		}
	}
	prefix := filepath.Join(root, "install")
	if err := installDistribution(verifyOptions{Distribution: newDistribution, PublicKey: publicPath}, prefix); err != nil {
		t.Fatal(err)
	}
	if err := rollbackDistribution(verifyOptions{Distribution: oldDistribution, PublicKey: publicPath}, prefix); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(prefix, "bin", binaryName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "old-binary") {
		t.Fatalf("rollback installed unexpected binary: %q", data)
	}
	if _, err := os.Stat(filepath.Join(prefix, rollbackInstallMark)); !os.IsNotExist(err) {
		t.Fatalf("rollback marker remained: %v", err)
	}
	if err := uninstallDistribution(prefix, installerConfirmation, publicPath); err != nil {
		t.Fatal(err)
	}
}

func buildTestDistribution(t *testing.T, root, label string, binaryData []byte) string {
	return buildTestDistributionNamed(t, root, label, label, binaryData)
}

func buildTestDistributionNamed(t *testing.T, root, label, outputLabel string, binaryData []byte) string {
	t.Helper()
	source := filepath.Join(root, "source-"+outputLabel)
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "one.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestGoldenBundle(t, source)
	binary := filepath.Join(root, "agent-eval-"+outputLabel)
	compatibility := filepath.Join(source, "compat.json")
	version := distributionContractVersion
	commit := strings.Repeat(testCommitCharacter(label), 40)
	if err := os.WriteFile(binary, testBinaryData(version, commit, string(binaryData)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compatibility, testCompatibilityBundle(), 0o600); err != nil {
		t.Fatal(err)
	}
	distribution := filepath.Join(root, "dist-"+outputLabel)
	if err := buildDistribution(buildOptions{
		Binary: binary, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt", "compat.json", testGoldenBundlePath}, SchemaRegistry: filepath.Join(source, "one.txt"), Protocol: filepath.Join(source, "one.txt"),
		Output: distribution, Version: version, ContractVersion: "0.1.0-pre-release",
		SourceCommit: commit, Platform: "linux", Architecture: "amd64",
	}); err != nil {
		t.Fatal(err)
	}
	return distribution
}

func testBinaryData(version, commit, marker string) []byte {
	return []byte("#!/bin/sh\n# " + marker + "\nprintf '%s\\n' '{\"result\":{\"build\":{\"version\":\"" + version + "\",\"commit\":\"" + commit + "\"},\"contract_version\":\"0.1.0-pre-release\"}}'\n")
}

func testCompatibilityBundle() []byte {
	observed := "observed"
	unknown := "unknown"
	unsupported := "unsupported"
	notApplicable := "not_applicable"
	zero := json.RawMessage("0")
	metric := func(id, representation string, present, required bool, state *string, coverage *bool, value json.RawMessage, valid bool) map[string]any {
		result := map[string]any{"id": id, "representation": representation, "present": present, "required": required, "valid": valid}
		if state != nil {
			result["state"] = *state
		}
		if coverage != nil {
			result["coverage"] = *coverage
		}
		if value != nil {
			result["value"] = value
		}
		return result
	}
	trueValue := true
	falseValue := false
	return mustCanonicalJSON(map[string]any{
		"schema_version": 1, "contract_version": distributionContractVersion,
		"golden_bundle":     map[string]any{"path": testGoldenBundlePath, "sha256": sha256Bytes(testGoldenBundleData())},
		"readability":       []any{map[string]any{"namespace": "standalone", "kind": "project-config", "versions": []int{1}}},
		"forward_rejection": []any{map[string]any{"namespace": "standalone", "kind": "project-config", "version": 2}},
		"metric_vectors": []any{
			metric("legacy-not-applicable-zero", "atl-profile-legacy", true, true, &notApplicable, &falseValue, zero, false),
			metric("legacy-unknown-zero", "atl-profile-legacy", true, true, &unknown, &falseValue, zero, true),
			metric("legacy-unsupported-zero", "atl-profile-legacy", true, true, &unsupported, &falseValue, zero, true),
			metric("missing-optional-entry", "standalone", false, false, nil, nil, nil, true),
			metric("missing-required-entry", "standalone", false, true, nil, nil, nil, false),
			metric("not-applicable-absent", "standalone", true, true, &notApplicable, nil, nil, true),
			metric("not-applicable-zero", "standalone", true, true, &notApplicable, &falseValue, zero, false),
			metric("observed-zero", "standalone", true, true, &observed, &trueValue, zero, true),
			metric("uncovered-nonzero", "atl-profile-legacy", true, true, &unknown, &falseValue, json.RawMessage("1"), false),
			metric("unknown-absent", "standalone", true, true, &unknown, nil, nil, true),
			metric("unknown-covered", "standalone", true, true, &unknown, &trueValue, zero, false),
			metric("unsupported-absent", "standalone", true, true, &unsupported, nil, nil, true),
		},
	})
}

func testGoldenBundleData() []byte {
	return []byte("{\"schema_version\":1,\"entries\":[{\"namespace\":\"standalone\",\"kind\":\"project-config\",\"version\":1,\"document\":{},\"expected_projection\":{}}]}\n")
}

func writeTestGoldenBundle(t *testing.T, source string) {
	t.Helper()
	path := filepath.Join(source, filepath.FromSlash(testGoldenBundlePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, testGoldenBundleData(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustCanonicalJSON(value any) []byte {
	data, err := canonicalJSON(value)
	if err != nil {
		panic(err)
	}
	return data
}

func testCommitCharacter(label string) string {
	if label == "" {
		return "a"
	}
	character := label[:1]
	if strings.Contains("0123456789abcdef", character) {
		return character
	}
	return "a"
}

func generateTestKeyPair(t *testing.T, root string) (publicPath, privatePath string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privatePath = filepath.Join(root, "private-"+strings.ReplaceAll(t.Name(), "/", "-")+".key")
	publicPath = filepath.Join(root, "public-"+strings.ReplaceAll(t.Name(), "/", "-")+".key")
	if err := os.WriteFile(privatePath, []byte(base64.StdEncoding.EncodeToString(privateKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, []byte(base64.StdEncoding.EncodeToString(publicKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return publicPath, privatePath
}
