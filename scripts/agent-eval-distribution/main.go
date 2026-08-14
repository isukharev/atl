// Command agent-eval-distribution builds and verifies an offline standalone
// agent-eval distribution. It deliberately has no provider, backend, network,
// credential, or repository-import authority.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	distributionSchema      = "agent-eval/distribution-manifest"
	distributionSchemaV1    = 1
	maxManifestBytes        = 1 << 20
	maxArtifactBytes        = 64 << 20
	maxSourceFiles          = 4096
	maxSourceTreeBytes      = 64 << 20
	maxDistributionFiles    = 32
	manifestName            = "manifest.json"
	manifestChecksumName    = "manifest.json.sha256"
	binaryName              = "agent-eval"
	compatibilityName       = "compatibility-bundle.json"
	containerfileName       = "Containerfile"
	actionName              = "action.yml"
	sbomName                = "sbom.spdx.json"
	provenanceName          = "provenance.json"
	distributionInstallMark = ".incomplete"
	distributionBuildMark   = ".incomplete"
	rollbackInstallMark     = ".rollback-incomplete"
	installedManifestName   = "manifest.json"
	installedChecksumName   = "manifest.json.sha256"
	installedSignatureName  = "manifest.json.sig"
	installedCompatibility  = "compatibility-bundle.json"
	installedSupportDir     = "agent-eval"
	installerConfirmation   = "UNINSTALL AGENT-EVAL"
)

type fileEntry struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type distributionManifest struct {
	Schema                    string      `json:"schema"`
	SchemaVersion             int         `json:"schema_version"`
	ContractVersion           string      `json:"contract_version"`
	Version                   string      `json:"version"`
	Platform                  string      `json:"platform"`
	Architecture              string      `json:"architecture"`
	SourceCommit              string      `json:"source_commit"`
	SourceTreeSHA256          string      `json:"source_tree_sha256"`
	SchemaRegistrySHA256      string      `json:"schema_registry_sha256"`
	ProtocolSHA256            string      `json:"protocol_sha256"`
	CompatibilityBundleSHA256 string      `json:"compatibility_bundle_sha256"`
	SignatureRequired         bool        `json:"signature_required"`
	ContainerBase             string      `json:"container_base"`
	ContainerEntrypoint       string      `json:"container_entrypoint"`
	ActionVersion             string      `json:"action_version"`
	Files                     []fileEntry `json:"files"`
}

type buildOptions struct {
	Binary          string
	Compatibility   string
	SourceRoot      string
	SourceFiles     []string
	SchemaRegistry  string
	Protocol        string
	Output          string
	Version         string
	ContractVersion string
	SourceCommit    string
	Platform        string
	Architecture    string
}

type verifyOptions struct {
	Distribution  string
	PublicKey     string
	AllowUnsigned bool
}

type verifiedDistribution struct {
	Manifest     distributionManifest
	ManifestData []byte
	Files        map[string][]byte
	Signature    []byte
}

func main() {
	mode := flag.String("mode", "", "build, verify, sign, install, or uninstall")
	binary := flag.String("binary", "", "agent-eval binary for build")
	compatibility := flag.String("compatibility", "", "provider-free compatibility bundle for build")
	sourceRoot := flag.String("source-root", ".", "source root for the selected tree hash")
	sourceFiles := flag.String("source-files", "internal/agenteval/cmd/agent-eval,internal/agenteval/testdata/standalone-conformance.v1.json", "comma-separated source paths")
	schemaRegistry := flag.String("schema-registry", "internal/agenteval/schemaregistry/registry.v1.json", "schema registry path")
	protocol := flag.String("protocol", "internal/agenteval/cmd/agent-eval/standalone_process.go", "process protocol source path")
	output := flag.String("output", "dist/agent-eval", "distribution directory")
	version := flag.String("version", "", "pre-release or release version")
	contractVersion := flag.String("contract-version", "0.1.0-pre-release", "standalone contract version")
	sourceCommit := flag.String("source-commit", "", "exact 40-character source commit")
	platform := flag.String("platform", runtime.GOOS, "target platform")
	architecture := flag.String("architecture", runtime.GOARCH, "target architecture")
	publicKey := flag.String("public-key", "", "base64 public signing key file for verify")
	privateKey := flag.String("private-key", "", "base64 private signing key file for sign")
	distribution := flag.String("distribution", "", "distribution directory for verify/sign/install")
	prefix := flag.String("prefix", "", "absolute install prefix")
	confirm := flag.String("confirm", "", "required uninstall confirmation")
	flag.Parse()

	var err error
	switch *mode {
	case "build":
		err = buildDistribution(buildOptions{
			Binary: *binary, Compatibility: *compatibility, SourceRoot: *sourceRoot,
			SourceFiles: splitPaths(*sourceFiles), SchemaRegistry: *schemaRegistry,
			Protocol: *protocol, Output: *output, Version: *version,
			ContractVersion: *contractVersion, SourceCommit: *sourceCommit,
			Platform: *platform, Architecture: *architecture,
		})
	case "verify":
		err = verifyDistribution(verifyOptions{Distribution: distributionOrOutput(*distribution, *output), PublicKey: *publicKey, AllowUnsigned: false})
	case "sign":
		err = signDistribution(distributionOrOutput(*distribution, *output), *privateKey)
	case "install":
		err = installDistribution(verifyOptions{Distribution: distributionOrOutput(*distribution, *output), PublicKey: *publicKey, AllowUnsigned: false}, *prefix)
	case "rollback":
		err = rollbackDistribution(verifyOptions{Distribution: distributionOrOutput(*distribution, *output), PublicKey: *publicKey, AllowUnsigned: false}, *prefix)
	case "uninstall":
		err = uninstallDistribution(*prefix, *confirm, *publicKey)
	default:
		err = errors.New("agent-eval-distribution: --mode must be build, verify, sign, install, rollback, or uninstall")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func splitPaths(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func distributionOrOutput(distribution, output string) string {
	if distribution != "" {
		return distribution
	}
	return output
}

func buildDistribution(options buildOptions) error {
	if options.Binary == "" || options.Compatibility == "" || options.Output == "" || options.Version == "" || options.SourceCommit == "" || options.SchemaRegistry == "" || options.Protocol == "" || len(options.SourceFiles) == 0 {
		return errors.New("build requires binary, compatibility, output, version, source-commit, schema-registry, protocol, and source-files")
	}
	if !validVersion(options.Version) || !validCommit(options.SourceCommit) || options.ContractVersion == "" || options.Platform == "" || options.Architecture == "" {
		return errors.New("build metadata is not canonical")
	}
	root, err := createAbsentDirectory(options.Output)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := writeRootFile(root, distributionBuildMark, []byte("incomplete\n"), 0o600); err != nil {
		return fmt.Errorf("build marker: %w", err)
	}
	sourceTree, err := hashSelectedTree(options.SourceRoot, options.SourceFiles)
	if err != nil {
		return fmt.Errorf("source tree: %w", err)
	}
	schemaSHA, _, err := hashRegularFile(options.SchemaRegistry, maxArtifactBytes)
	if err != nil {
		return fmt.Errorf("schema registry: %w", err)
	}
	protocolSHA, _, err := hashRegularFile(options.Protocol, maxArtifactBytes)
	if err != nil {
		return fmt.Errorf("process protocol: %w", err)
	}
	if err := copyIntoRoot(root, binaryName, options.Binary, 0o755); err != nil {
		return fmt.Errorf("binary: %w", err)
	}
	if err := copyIntoRoot(root, compatibilityName, options.Compatibility, 0o644); err != nil {
		return fmt.Errorf("compatibility bundle: %w", err)
	}
	containerfile := []byte("FROM scratch\nCOPY agent-eval /agent-eval\nENTRYPOINT [\"/agent-eval\"]\n")
	if err := writeRootFile(root, containerfileName, containerfile, 0o644); err != nil {
		return fmt.Errorf("container descriptor: %w", err)
	}
	binarySHA, binarySize, err := hashRootFile(root, binaryName, maxArtifactBytes)
	if err != nil {
		return err
	}
	compatSHA, compatSize, err := hashRootFile(root, compatibilityName, maxArtifactBytes)
	if err != nil {
		return err
	}
	containerSHA, containerSize, err := hashRootFile(root, containerfileName, maxArtifactBytes)
	if err != nil {
		return err
	}
	action := []byte(renderAction(options.Version, options.SourceCommit, binarySHA))
	if err := writeRootFile(root, actionName, action, 0o644); err != nil {
		return fmt.Errorf("action descriptor: %w", err)
	}
	actionSHA, actionSize, err := hashRootFile(root, actionName, maxArtifactBytes)
	if err != nil {
		return err
	}
	provenance := renderProvenance(options, sourceTree, binarySHA, compatSHA)
	if err := writeRootFile(root, provenanceName, provenance, 0o644); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	sbom := renderSBOM(options, sourceTree, []fileEntry{
		{Name: binaryName, SizeBytes: binarySize, SHA256: binarySHA},
		{Name: compatibilityName, SizeBytes: compatSize, SHA256: compatSHA},
		{Name: containerfileName, SizeBytes: containerSize, SHA256: containerSHA},
		{Name: actionName, SizeBytes: actionSize, SHA256: actionSHA},
	})
	if err := writeRootFile(root, sbomName, sbom, 0o644); err != nil {
		return fmt.Errorf("SBOM: %w", err)
	}
	provenanceSHA, provenanceSize, err := hashRootFile(root, provenanceName, maxArtifactBytes)
	if err != nil {
		return err
	}
	sbomSHA, sbomSize, err := hashRootFile(root, sbomName, maxArtifactBytes)
	if err != nil {
		return err
	}
	manifest := distributionManifest{
		Schema: distributionSchema, SchemaVersion: distributionSchemaV1, ContractVersion: options.ContractVersion,
		Version: options.Version, Platform: options.Platform, Architecture: options.Architecture,
		SourceCommit: options.SourceCommit, SourceTreeSHA256: sourceTree,
		SchemaRegistrySHA256: schemaSHA, ProtocolSHA256: protocolSHA,
		CompatibilityBundleSHA256: compatSHA, SignatureRequired: true,
		ContainerBase: "scratch", ContainerEntrypoint: "/agent-eval", ActionVersion: options.Version,
		Files: []fileEntry{
			{Name: actionName, SizeBytes: actionSize, SHA256: actionSHA},
			{Name: binaryName, SizeBytes: binarySize, SHA256: binarySHA},
			{Name: compatibilityName, SizeBytes: compatSize, SHA256: compatSHA},
			{Name: containerfileName, SizeBytes: containerSize, SHA256: containerSHA},
			{Name: provenanceName, SizeBytes: provenanceSize, SHA256: provenanceSHA},
			{Name: sbomName, SizeBytes: sbomSize, SHA256: sbomSHA},
		},
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Name < manifest.Files[j].Name })
	manifestData, err := canonicalJSON(manifest)
	if err != nil {
		return err
	}
	if err := writeRootFile(root, manifestName, manifestData, 0o644); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	manifestSHA := sha256Bytes(manifestData)
	if err := writeRootFile(root, manifestChecksumName, []byte(manifestSHA+"\n"), 0o644); err != nil {
		return fmt.Errorf("manifest checksum: %w", err)
	}
	if err := root.Remove(distributionBuildMark); err != nil {
		return fmt.Errorf("build marker removal: %w", err)
	}
	return syncDirectory(root)
}

func renderAction(version, commit, binarySHA string) string {
	template := `name: agent-eval
description: Run a pre-verified provider-free agent-eval distribution
inputs:
  distribution:
    description: Absolute or workspace path to the already verified distribution
    required: true
  expected-version:
    description: Expected immutable distribution version
    required: true
  expected-commit:
    description: Expected source commit from the verified manifest
    required: true
  expected-binary-sha256:
    description: Expected binary digest from the verified manifest
    required: true
runs:
  using: composite
  steps:
    - shell: bash
      env:
        DISTRIBUTION: ${{ inputs.distribution }}
        EXPECTED_VERSION: ${{ inputs.expected-version }}
        EXPECTED_COMMIT: ${{ inputs.expected-commit }}
        EXPECTED_BINARY_SHA256: ${{ inputs.expected-binary-sha256 }}
      run: |
        test "$EXPECTED_VERSION" = %q
        test "$EXPECTED_COMMIT" = %q
        test -x "$DISTRIBUTION/agent-eval"
        tmp="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/agent-eval.$$"
        trap 'rm -f "$tmp" "$tmp.json"' EXIT
        umask 077
        cp -- "$DISTRIBUTION/agent-eval" "$tmp"
        chmod 700 "$tmp"
        test "$EXPECTED_BINARY_SHA256" = %q
        test "$(sha256sum "$tmp" | awk '{print $1}')" = "$EXPECTED_BINARY_SHA256"
        "$tmp" version --output json >"$tmp.json"
        python3 - "$tmp.json" "$EXPECTED_VERSION" "$EXPECTED_COMMIT" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as stream:
    payload = json.load(stream)
result = payload.get('result', payload)
build = result.get('build', {})
if build.get('version') != sys.argv[2] or build.get('commit') != sys.argv[3]:
    raise SystemExit('agent-eval build identity mismatch')
PY
`
	return fmt.Sprintf(template, version, commit, binarySHA)
}

func renderProvenance(options buildOptions, sourceTree, binarySHA, compatibilitySHA string) []byte {
	value := map[string]any{
		"schema": distributionSchema + "/provenance", "schema_version": 1,
		"source_commit": options.SourceCommit, "source_tree_sha256": sourceTree,
		"version": options.Version, "platform": options.Platform, "architecture": options.Architecture,
		"binary_sha256": binarySHA, "compatibility_bundle_sha256": compatibilitySHA,
		"builder": "scripts/agent-eval-distribution", "network": "none", "credentials": "none",
	}
	data, _ := canonicalJSON(value)
	return data
}

func renderSBOM(options buildOptions, sourceTree string, files []fileEntry) []byte {
	packages := make([]map[string]any, 0, len(files)+1)
	packages = append(packages, map[string]any{
		"SPDXID": "SPDXRef-agent-eval-source", "name": "agent-eval-source", "versionInfo": options.Version,
		"downloadLocation": "NOASSERTION", "filesAnalyzed": false,
		"licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION", "copyrightText": "NOASSERTION",
		"checksums": []map[string]string{{"algorithm": "SHA256", "checksumValue": sourceTree}},
	})
	for index, file := range files {
		packages = append(packages, map[string]any{
			"SPDXID": fmt.Sprintf("SPDXRef-file-%d", index+1), "name": file.Name, "versionInfo": options.Version,
			"downloadLocation": "NOASSERTION", "filesAnalyzed": false,
			"licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION", "copyrightText": "NOASSERTION",
			"checksums": []map[string]string{{"algorithm": "SHA256", "checksumValue": file.SHA256}},
		})
	}
	data, _ := canonicalJSON(map[string]any{
		"SPDXID": "SPDXRef-DOCUMENT", "spdxVersion": "SPDX-2.3", "name": "agent-eval-distribution",
		"documentNamespace": "https://github.com/isukharev/atl/agent-eval/" + options.SourceCommit,
		"dataLicense":       "CC0-1.0", "creationInfo": map[string]any{
			"created": "1970-01-01T00:00:00Z", "creators": []string{"Tool: scripts/agent-eval-distribution"},
		}, "packages": packages,
	})
	return data
}

func signDistribution(directory, privateKeyPath string) error {
	if privateKeyPath == "" {
		return errors.New("sign requires --private-key")
	}
	data, err := readFileBounded(filepath.Join(directory, manifestName), maxManifestBytes)
	if err != nil {
		return err
	}
	keyData, err := readFileBounded(privateKeyPath, 4096)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return errors.New("private signing key is not a base64 ed25519 key")
	}
	signature := ed25519.Sign(ed25519.PrivateKey(key), data)
	return writeExclusive(filepath.Join(directory, "manifest.json.sig"), signature, 0o644)
}

func verifyDistribution(options verifyOptions) error {
	_, err := loadVerifiedDistribution(options)
	return err
}

func verifyManifestSignature(manifestData, signature []byte, publicKeyPath string) error {
	if publicKeyPath == "" {
		return errors.New("manifest signature exists but --public-key was not supplied")
	}
	keyData, err := readFileBounded(publicKeyPath, 4096)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
	if err != nil || len(key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(key), manifestData, signature) {
		return errors.New("manifest signature verification failed")
	}
	return nil
}

func loadVerifiedDistribution(options verifyOptions) (verifiedDistribution, error) {
	var verified verifiedDistribution
	root, err := openDirectory(options.Distribution)
	if err != nil {
		return verified, err
	}
	defer func() { _ = root.Close() }()
	data, err := readRootFile(root, manifestName, maxManifestBytes)
	if err != nil {
		return verified, err
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return verified, err
	}
	checksum, err := readRootFile(root, manifestChecksumName, 256)
	if err != nil || strings.TrimSpace(string(checksum)) != sha256Bytes(data) {
		return verified, errors.New("manifest checksum mismatch")
	}
	if err := validateManifest(manifest); err != nil {
		return verified, err
	}
	expected := map[string]bool{manifestName: true, manifestChecksumName: true}
	files := make(map[string][]byte, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Name] = true
		artifact, err := readRootFile(root, entry.Name, maxArtifactBytes)
		if err != nil {
			return verified, fmt.Errorf("artifact %s: %w", entry.Name, err)
		}
		actual := sha256Bytes(artifact)
		if actual != entry.SHA256 || int64(len(artifact)) != entry.SizeBytes {
			return verified, fmt.Errorf("artifact %s checksum or size mismatch", entry.Name)
		}
		files[entry.Name] = artifact
	}
	entries, err := readDirectoryBounded(root, maxDistributionFiles)
	if err != nil {
		return verified, err
	}
	for _, entry := range entries {
		if !expected[entry.Name()] && entry.Name() != "manifest.json.sig" {
			return verified, fmt.Errorf("unexpected distribution member %q", entry.Name())
		}
	}
	signature, sigErr := readRootFile(root, "manifest.json.sig", ed25519.SignatureSize+1)
	if sigErr == nil {
		if err := verifyManifestSignature(data, signature, options.PublicKey); err != nil {
			return verified, err
		}
	} else if !errors.Is(sigErr, os.ErrNotExist) {
		return verified, sigErr
	} else if !options.AllowUnsigned {
		return verified, errors.New("distribution is unsigned; pass an explicit development-only unsigned allowance")
	}
	verified.Manifest = manifest
	verified.ManifestData = data
	verified.Files = files
	verified.Signature = signature
	return verified, nil
}

func validateManifest(manifest distributionManifest) error {
	if manifest.Schema != distributionSchema || manifest.SchemaVersion != distributionSchemaV1 || manifest.ContractVersion == "" || !validVersion(manifest.Version) || manifest.Platform == "" || manifest.Architecture == "" || !validCommit(manifest.SourceCommit) || !validDigest(manifest.SourceTreeSHA256) || !validDigest(manifest.SchemaRegistrySHA256) || !validDigest(manifest.ProtocolSHA256) || !validDigest(manifest.CompatibilityBundleSHA256) || !manifest.SignatureRequired || manifest.ContainerBase != "scratch" || manifest.ContainerEntrypoint != "/agent-eval" || manifest.ActionVersion != manifest.Version || len(manifest.Files) == 0 || len(manifest.Files) > maxDistributionFiles {
		return errors.New("distribution manifest metadata is invalid")
	}
	seen := make(map[string]bool, len(manifest.Files))
	requiredNames := map[string]bool{
		binaryName: true, compatibilityName: true, containerfileName: true,
		actionName: true, sbomName: true, provenanceName: true,
	}
	previousName := ""
	for _, entry := range manifest.Files {
		if !safeName(entry.Name) || seen[entry.Name] || entry.Name <= previousName || entry.Name == manifestName || entry.Name == manifestChecksumName || entry.Name == "manifest.json.sig" || entry.Name == distributionBuildMark || !validDigest(entry.SHA256) || entry.SizeBytes <= 0 || entry.SizeBytes > maxArtifactBytes {
			return errors.New("distribution manifest file entry is invalid")
		}
		if !requiredNames[entry.Name] {
			return fmt.Errorf("distribution manifest contains unsupported file %q", entry.Name)
		}
		seen[entry.Name] = true
		previousName = entry.Name
	}
	for _, required := range []string{binaryName, compatibilityName, containerfileName, actionName, sbomName, provenanceName} {
		if !seen[required] {
			return fmt.Errorf("distribution manifest omits %s", required)
		}
	}
	return nil
}

func installDistribution(options verifyOptions, prefix string) error {
	verified, err := loadVerifiedDistribution(options)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix {
		return errors.New("install prefix must be an absolute clean path")
	}
	if _, err := os.Lstat(prefix); err == nil {
		return errors.New("install prefix already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(prefix, 0o755); err != nil {
		return err
	}
	root, err := openDirectory(prefix)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := writeRootFile(root, distributionInstallMark, []byte("incomplete\n"), 0o600); err != nil {
		return err
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	if err := syncParentDirectory(prefix); err != nil {
		return err
	}
	if err := root.Mkdir("bin", 0o755); err != nil {
		return err
	}
	if err := root.Mkdir("share", 0o755); err != nil {
		return err
	}
	if err := root.Mkdir(filepath.Join("share", installedSupportDir), 0o755); err != nil {
		return err
	}
	share, err := root.OpenRoot(filepath.Join("share", installedSupportDir))
	if err != nil {
		return err
	}
	defer func() { _ = share.Close() }()
	if err := writeVerifiedSnapshot(root, share, verified); err != nil {
		return err
	}
	if err := validateInstalledDistribution(root, share, verified.Manifest, verified.ManifestData, true, options.PublicKey); err != nil {
		return fmt.Errorf("installation result is not exact: %w", err)
	}
	if err := syncDirectory(share); err != nil {
		return err
	}
	if err := syncChildDirectory(root, filepath.Join("share", installedSupportDir)); err != nil {
		return err
	}
	if err := syncChildDirectory(root, "bin"); err != nil {
		return err
	}
	if err := syncChildDirectory(root, "share"); err != nil {
		return err
	}
	if err := root.Remove(distributionInstallMark); err != nil {
		return err
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	return syncParentDirectory(prefix)
}

func syncChildDirectory(root *os.Root, name string) error {
	child, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer func() { _ = child.Close() }()
	return syncDirectory(child)
}

func writeVerifiedSnapshot(root, share *os.Root, verified verifiedDistribution) error {
	for _, name := range []string{binaryName, compatibilityName, containerfileName, actionName, sbomName, provenanceName, manifestName, manifestChecksumName, "manifest.json.sig"} {
		data, exists := verified.Files[name]
		if name == manifestName {
			data = verified.ManifestData
			exists = true
		}
		if name == manifestChecksumName {
			data = []byte(sha256Bytes(verified.ManifestData) + "\n")
			exists = true
		}
		if name == "manifest.json.sig" {
			if len(verified.Signature) == 0 {
				return errors.New("verified distribution has no signature")
			}
			data = verified.Signature
			exists = true
		}
		if !exists {
			return fmt.Errorf("verified distribution omitted %s", name)
		}
		if name == binaryName {
			if err := writeRootFile(root, filepath.Join("bin", binaryName), data, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := writeRootFile(share, name, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func removeInstalledSnapshot(root, share *os.Root, manifest *distributionManifest) error {
	if err := removeRegular(root, filepath.Join("bin", binaryName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	names := []string{installedCompatibility, containerfileName, actionName, sbomName, provenanceName, installedManifestName, installedChecksumName, installedSignatureName}
	if manifest != nil {
		for _, entry := range manifest.Files {
			if entry.Name != binaryName && entry.Name != manifestName {
				names = append(names, entry.Name)
			}
		}
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if err := removeRegular(share, name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func rollbackDistribution(options verifyOptions, prefix string) error {
	verified, err := loadVerifiedDistribution(options)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix {
		return errors.New("rollback prefix must be an absolute clean path")
	}
	root, err := openDirectory(prefix)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	share, err := root.OpenRoot(filepath.Join("share", installedSupportDir))
	if err != nil {
		return err
	}
	defer func() { _ = share.Close() }()
	marker := []byte("rollback:" + sha256Bytes(verified.ManifestData) + "\n")
	currentMarker, markerErr := readRootFile(root, rollbackInstallMark, 256)
	if markerErr == nil {
		if !bytes.Equal(currentMarker, marker) {
			return errors.New("rollback marker targets a different distribution")
		}
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	} else {
		currentData, readErr := readRootFile(share, installedManifestName, maxManifestBytes)
		if readErr != nil {
			return readErr
		}
		current, decodeErr := decodeManifest(currentData)
		if decodeErr != nil || current.SourceCommit == verified.Manifest.SourceCommit {
			return errors.New("rollback requires a different valid installed candidate")
		}
		if err := validateInstalledDistribution(root, share, current, currentData, false, options.PublicKey); err != nil {
			return fmt.Errorf("current installation is not exact: %w", err)
		}
		if err := writeRootFile(root, rollbackInstallMark, marker, 0o600); err != nil {
			return err
		}
		if err := syncDirectory(root); err != nil {
			return err
		}
		if err := syncParentDirectory(prefix); err != nil {
			return err
		}
	}
	var current *distributionManifest
	if currentData, readErr := readRootFile(share, installedManifestName, maxManifestBytes); readErr == nil {
		decoded, decodeErr := decodeManifest(currentData)
		if decodeErr == nil {
			current = &decoded
		}
	}
	if err := removeInstalledSnapshot(root, share, current); err != nil {
		return err
	}
	if err := writeVerifiedSnapshot(root, share, verified); err != nil {
		return err
	}
	if err := validateInstalledDistribution(root, share, verified.Manifest, verified.ManifestData, true, options.PublicKey); err != nil {
		return fmt.Errorf("rollback result is not exact: %w", err)
	}
	if err := syncDirectory(share); err != nil {
		return err
	}
	if err := syncChildDirectory(root, filepath.Join("share", installedSupportDir)); err != nil {
		return err
	}
	if err := syncChildDirectory(root, "bin"); err != nil {
		return err
	}
	if err := syncChildDirectory(root, "share"); err != nil {
		return err
	}
	if err := root.Remove(rollbackInstallMark); err != nil {
		return err
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	return syncParentDirectory(prefix)
}

func uninstallDistribution(prefix, confirmation, publicKey string) error {
	if confirmation != installerConfirmation {
		return errors.New("uninstall requires the exact confirmation token")
	}
	if !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix {
		return errors.New("uninstall prefix must be an absolute clean path")
	}
	root, err := openDirectory(prefix)
	if err != nil {
		return err
	}
	rootClosed := false
	defer func() {
		if !rootClosed {
			_ = root.Close()
		}
	}()
	share, err := root.OpenRoot(filepath.Join("share", installedSupportDir))
	if err != nil {
		return err
	}
	shareClosed := false
	defer func() {
		if !shareClosed {
			_ = share.Close()
		}
	}()
	if _, err := root.Lstat(distributionInstallMark); err == nil {
		return errors.New("incomplete installation must be recovered manually")
	}
	if _, err := root.Lstat(rollbackInstallMark); err == nil {
		return errors.New("incomplete rollback must be recovered manually")
	}
	if _, err := share.Lstat(distributionInstallMark); err == nil {
		return errors.New("incomplete installation must be recovered manually")
	}
	manifestData, err := readRootFile(share, installedManifestName, maxManifestBytes)
	if err != nil {
		return err
	}
	manifest, err := decodeManifest(manifestData)
	if err != nil || !manifest.SignatureRequired {
		return errors.New("installed manifest is invalid")
	}
	if err := validateInstalledDistribution(root, share, manifest, manifestData, false, publicKey); err != nil {
		return err
	}
	if err := removeRegular(root, filepath.Join("bin", binaryName)); err != nil {
		return err
	}
	for _, name := range []string{installedCompatibility, containerfileName, actionName, sbomName, provenanceName, installedManifestName, installedChecksumName, installedSignatureName} {
		if err := removeRegular(share, name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := share.Close(); err != nil {
		return err
	}
	shareClosed = true
	if err := root.Remove(filepath.Join("share", installedSupportDir)); err != nil {
		return err
	}
	if err := root.Remove("share"); err != nil {
		return err
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	if err := root.Remove("bin"); err != nil {
		return err
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	if err := root.Close(); err != nil {
		return err
	}
	rootClosed = true
	if err := os.Remove(prefix); err != nil {
		return err
	}
	return syncParentDirectory(prefix)
}

func validateInstalledDistribution(root, share *os.Root, manifest distributionManifest, manifestData []byte, allowRollbackMarker bool, publicKey string) error {
	checksum, err := readRootFile(share, installedChecksumName, 256)
	if err != nil || strings.TrimSpace(string(checksum)) != sha256Bytes(manifestData) {
		return errors.New("installed manifest checksum mismatch")
	}
	entryByName := make(map[string]fileEntry, len(manifest.Files))
	for _, entry := range manifest.Files {
		entryByName[entry.Name] = entry
	}
	for _, entry := range manifest.Files {
		memberRoot := share
		memberName := entry.Name
		if entry.Name == binaryName {
			memberRoot = root
			memberName = filepath.Join("bin", binaryName)
		}
		data, err := readRootFile(memberRoot, memberName, maxArtifactBytes)
		if err != nil {
			return fmt.Errorf("installed artifact %s: %w", entry.Name, err)
		}
		if sha256Bytes(data) != entry.SHA256 || int64(len(data)) != entry.SizeBytes {
			return fmt.Errorf("installed artifact %s checksum or size mismatch", entry.Name)
		}
	}
	signature, err := readRootFile(share, installedSignatureName, ed25519.SignatureSize+1)
	if err != nil {
		return errors.New("installed signature is missing or invalid")
	}
	if err := verifyManifestSignature(manifestData, signature, publicKey); err != nil {
		return fmt.Errorf("installed signature is invalid: %w", err)
	}
	rootEntries, err := readDirectoryNames(root, 8)
	if err != nil {
		return err
	}
	if allowRollbackMarker {
		filtered := rootEntries[:0]
		for _, entry := range rootEntries {
			if entry.Name() != rollbackInstallMark && entry.Name() != distributionInstallMark {
				filtered = append(filtered, entry)
			}
		}
		rootEntries = filtered
	}
	if !hasExactNames(rootEntries, "bin", "share") {
		return errors.New("installed root contains unexpected members")
	}
	binRoot, err := root.OpenRoot("bin")
	if err != nil {
		return err
	}
	defer func() { _ = binRoot.Close() }()
	if entries, err := readDirectoryNames(binRoot, 4); err != nil || !hasExactNames(entries, binaryName) {
		if err != nil {
			return err
		}
		return errors.New("installed bin contains unexpected members")
	}
	shareParent, err := root.OpenRoot("share")
	if err != nil {
		return err
	}
	defer func() { _ = shareParent.Close() }()
	if entries, err := readDirectoryNames(shareParent, 4); err != nil || !hasExactNames(entries, installedSupportDir) {
		if err != nil {
			return err
		}
		return errors.New("installed share contains unexpected members")
	}
	expected := make(map[string]bool, len(entryByName)+2)
	for name := range entryByName {
		if name != binaryName {
			expected[name] = true
		}
	}
	expected[installedManifestName] = true
	expected[installedChecksumName] = true
	expected[installedSignatureName] = true
	entries, err := readDirectoryNames(share, maxDistributionFiles)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return fmt.Errorf("installed support directory contains unexpected member %q", entry.Name())
		}
	}
	if len(entries) != len(expected) {
		return errors.New("installed support directory is incomplete")
	}
	return nil
}

func hasExactNames(entries []fs.DirEntry, names ...string) bool {
	if len(entries) != len(names) {
		return false
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	for _, entry := range entries {
		if !want[entry.Name()] {
			return false
		}
	}
	return true
}

func createAbsentDirectory(name string) (*os.Root, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, errors.New("distribution output must be an absolute clean path")
	}
	if _, err := os.Lstat(name); err == nil {
		return nil, errors.New("distribution output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Mkdir(name, 0o700); err != nil {
		return nil, err
	}
	return openDirectory(name)
}

func openDirectory(name string) (*os.Root, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, errors.New("path must be an absolute clean path")
	}
	info, err := os.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		if err == nil {
			err = errors.New("path is not a plain directory")
		}
		return nil, err
	}
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, openedErr := root.Stat(".")
	ambient, ambientErr := os.Lstat(name)
	if openedErr != nil || ambientErr != nil || !opened.IsDir() || !os.SameFile(info, opened) || !os.SameFile(info, ambient) {
		_ = root.Close()
		return nil, errors.New("directory changed while opened")
	}
	return root, nil
}

func copyIntoRoot(root *os.Root, name, source string, mode fs.FileMode) error {
	data, err := readFileBounded(source, maxArtifactBytes)
	if err != nil {
		return err
	}
	return writeRootFile(root, name, data, mode)
}

func writeRootFile(root *os.Root, name string, data []byte, mode fs.FileMode) error {
	if !safeName(name) || len(data) > maxArtifactBytes {
		return errors.New("invalid bounded output")
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	if writeErr != nil || written != len(data) {
		_ = file.Close()
		_ = root.Remove(name)
		if writeErr != nil {
			return writeErr
		}
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil && runtime.GOOS != "windows" {
		_ = file.Close()
		_ = root.Remove(name)
		return err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(name)
		return err
	}
	return nil
}

func writeExclusive(name string, data []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	if writeErr != nil || written != len(data) {
		_ = file.Close()
		_ = os.Remove(name)
		if writeErr != nil {
			return writeErr
		}
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil && runtime.GOOS != "windows" {
		_ = file.Close()
		_ = os.Remove(name)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func readFileBounded(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("distribution input is not a regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(data) > int(limit) {
		return nil, errors.New("file exceeds bounded distribution limit")
	}
	return data, nil
}

func readRootFile(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("distribution member is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(data) > int(limit) {
		return nil, errors.New("file exceeds bounded distribution limit")
	}
	return data, nil
}

func hashRegularFile(name string, limit int64) (string, int64, error) {
	data, err := readFileBounded(name, limit)
	if err != nil {
		return "", 0, err
	}
	return sha256Bytes(data), int64(len(data)), nil
}

func hashRootFile(root *os.Root, name string, limit int64) (string, int64, error) {
	data, err := readRootFile(root, name, limit)
	if err != nil {
		return "", 0, err
	}
	return sha256Bytes(data), int64(len(data)), nil
}

func hashSelectedTree(root string, paths []string) (string, error) {
	files := map[string]fileEntry{}
	var total int64
	visitedEntries := 0
	for _, selected := range paths {
		if selected == "" || filepath.IsAbs(selected) || filepath.Clean(selected) != selected || strings.HasPrefix(selected, ".."+string(filepath.Separator)) || selected == ".." {
			return "", errors.New("source selection must be relative and contained")
		}
		absolute := filepath.Join(root, selected)
		info, err := os.Lstat(absolute)
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", errors.New("source selection contains a symlink")
		}
		if info.IsDir() {
			err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				visitedEntries++
				if visitedEntries > maxSourceFiles*2 {
					return errors.New("source tree exceeds bounded entry selection")
				}
				if entry.Type()&fs.ModeSymlink != 0 {
					return errors.New("source tree contains a symlink")
				}
				if entry.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				return addTreeFile(files, filepath.ToSlash(rel), path, &total)
			})
		} else {
			err = addTreeFile(files, filepath.ToSlash(selected), absolute, &total)
		}
		if err != nil {
			return "", err
		}
	}
	if len(files) == 0 || len(files) > maxSourceFiles || total > maxSourceTreeBytes {
		return "", errors.New("source tree exceeds bounded selection")
	}
	entries := make([]fileEntry, 0, len(files))
	for _, entry := range files {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	hash := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(hash, "%s\x00%d\x00%s\x00", entry.Name, entry.SizeBytes, entry.SHA256)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func addTreeFile(files map[string]fileEntry, relative, absolute string, total *int64) error {
	if !safeName(relative) {
		return errors.New("source tree path is not canonical")
	}
	if _, exists := files[relative]; exists {
		return nil
	}
	if len(files) >= maxSourceFiles {
		return errors.New("source tree exceeds bounded file selection")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("source tree member is not a regular file")
	}
	if info.Size() < 0 || info.Size() > maxArtifactBytes || *total > maxSourceTreeBytes-info.Size() {
		return errors.New("source tree exceeds bounded byte selection")
	}
	sha, size, err := hashRegularFile(absolute, maxArtifactBytes)
	if err != nil {
		return err
	}
	files[relative] = fileEntry{Name: relative, SizeBytes: size, SHA256: sha}
	*total += size
	return nil
}

func readDirectoryBounded(root *os.Root, limit int) ([]fs.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(limit + 1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(entries) > limit {
		return nil, errors.New("distribution directory exceeds bounded member limit")
	}
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("distribution member %q is not a regular file", entry.Name())
		}
	}
	return entries, nil
}

func readDirectoryNames(root *os.Root, limit int) ([]fs.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(limit + 1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(entries) > limit {
		return nil, errors.New("installed directory exceeds bounded member limit")
	}
	return entries, nil
}

func removeRegular(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("refusing to remove non-regular installed member")
	}
	return root.Remove(name)
}

func syncDirectory(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func syncParentDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := parent.Sync()
	closeErr := parent.Close()
	return errors.Join(syncErr, closeErr)
}

func decodeManifest(data []byte) (distributionManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest distributionManifest
	if err := decoder.Decode(&manifest); err != nil {
		return distributionManifest{}, errors.New("invalid distribution manifest")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return distributionManifest{}, errors.New("distribution manifest has trailing data")
	}
	canonical, err := canonicalJSON(manifest)
	if err != nil || !bytes.Equal(canonical, data) {
		return distributionManifest{}, errors.New("distribution manifest is not canonical")
	}
	if err := validateManifest(manifest); err != nil {
		return distributionManifest{}, err
	}
	return manifest, nil
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func sha256Bytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validCommit(value string) bool {
	return len(value) == 40 && validHex(value)
}

func validHex(value string) bool {
	if value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validVersion(value string) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "/\\ \t\r\n") {
		return false
	}
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case strings.ContainsRune(".-+", character):
		default:
			return false
		}
	}
	return value[0] != '.' && value[0] != '-' && value[0] != '+' && value[len(value)-1] != '.' && value[len(value)-1] != '-' && value[len(value)-1] != '+'
}

func safeName(name string) bool {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") || strings.Contains(name, "\x00") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	return clean == name && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
