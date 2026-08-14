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

func TestDistributionBuildVerifySignInstallUninstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory Sync is intentionally best effort on Windows")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "one.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "agent-eval")
	compatibility := filepath.Join(root, "compat.json")
	registry := filepath.Join(source, "one.txt")
	protocol := filepath.Join(source, "one.txt")
	if err := os.WriteFile(binary, testBinaryData("0.1.0-pre-release", strings.Repeat("a", 40), "initial"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compatibility, []byte("{\"schema\":\"compat\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	distribution := filepath.Join(root, "dist")
	if err := buildDistribution(buildOptions{
		Binary: binary, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt"}, SchemaRegistry: registry, Protocol: protocol,
		Output: distribution, Version: "0.1.0-pre-release", ContractVersion: "0.1.0-pre-release",
		SourceCommit: strings.Repeat("a", 40), Platform: "linux", Architecture: "amd64",
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
	compatibility := filepath.Join(root, "compat.json")
	if err := os.WriteFile(compatibility, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	options := buildOptions{
		Binary: "/bin/true", Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt"}, SchemaRegistry: filepath.Join(source, "one.txt"),
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
}

func TestDistributionBuildIsReproducibleAndUninstallRefusesDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory Sync is intentionally best effort on Windows")
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
	publicPath, privatePath := generateTestKeyPair(t, root)
	if err := signDistribution(distributionA, privatePath); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(root, "install")
	if err := installDistribution(verifyOptions{Distribution: distributionA, PublicKey: publicPath}, prefix); err != nil {
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
		DataLicense  string `json:"dataLicense"`
		CreationInfo struct {
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
	for _, want := range []string{
		"expected-version:", "expected-commit:", "expected-binary-sha256:",
		"test \"$EXPECTED_VERSION\" = \"0.1.0-pre-release\"",
		"test \"$EXPECTED_COMMIT\" = \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"",
		"test \"$EXPECTED_BINARY_SHA256\" = \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"",
		"sha256sum \"$tmp\"",
		"mktemp",
		"tmp_json=",
	} {
		if !strings.Contains(action, want) {
			t.Fatalf("action omitted identity binding %q:\n%s", want, action)
		}
	}
}

func TestDistributionRollbackRestoresVerifiedCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory Sync is intentionally best effort on Windows")
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
	binary := filepath.Join(root, "agent-eval-"+outputLabel)
	compatibility := filepath.Join(root, "compat-"+outputLabel+".json")
	version := "0.1.0-" + label
	commit := strings.Repeat(testCommitCharacter(label), 40)
	if err := os.WriteFile(binary, testBinaryData(version, commit, string(binaryData)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compatibility, []byte("{\"schema\":\"compat\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	distribution := filepath.Join(root, "dist-"+outputLabel)
	if err := buildDistribution(buildOptions{
		Binary: binary, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt"}, SchemaRegistry: filepath.Join(source, "one.txt"), Protocol: filepath.Join(source, "one.txt"),
		Output: distribution, Version: version, ContractVersion: "0.1.0-pre-release",
		SourceCommit: commit, Platform: "linux", Architecture: "amd64",
	}); err != nil {
		t.Fatal(err)
	}
	return distribution
}

func testBinaryData(version, commit, marker string) []byte {
	return []byte("#!/bin/sh\n# " + marker + "\nprintf '%s\\n' '{\"result\":{\"build\":{\"version\":\"" + version + "\",\"commit\":\"" + commit + "\"}}}'\n")
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
