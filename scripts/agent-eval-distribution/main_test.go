package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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
	if err := os.WriteFile(binary, []byte("binary\n"), 0o700); err != nil {
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
	if err := uninstallDistribution(prefix, installerConfirmation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "bin", binaryName)); !os.IsNotExist(err) {
		t.Fatalf("binary remained after uninstall: %v", err)
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
		if !strings.Contains(text, "go -C internal/agenteval build") || !strings.Contains(text, "./cmd/agent-eval") {
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
	if err := uninstallDistribution(prefix, installerConfirmation); err == nil {
		t.Fatal("uninstall accepted a tampered installation")
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
	if string(data) != "old-binary\n" {
		t.Fatalf("rollback installed unexpected binary: %q", data)
	}
	if _, err := os.Stat(filepath.Join(prefix, rollbackInstallMark)); !os.IsNotExist(err) {
		t.Fatalf("rollback marker remained: %v", err)
	}
	if err := uninstallDistribution(prefix, installerConfirmation); err != nil {
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
	if err := os.WriteFile(binary, binaryData, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compatibility, []byte("{\"schema\":\"compat\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	distribution := filepath.Join(root, "dist-"+outputLabel)
	if err := buildDistribution(buildOptions{
		Binary: binary, Compatibility: compatibility, SourceRoot: source,
		SourceFiles: []string{"one.txt"}, SchemaRegistry: filepath.Join(source, "one.txt"), Protocol: filepath.Join(source, "one.txt"),
		Output: distribution, Version: "0.1.0-" + label, ContractVersion: "0.1.0-pre-release",
		SourceCommit: strings.Repeat(testCommitCharacter(label), 40), Platform: "linux", Architecture: "amd64",
	}); err != nil {
		t.Fatal(err)
	}
	return distribution
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
