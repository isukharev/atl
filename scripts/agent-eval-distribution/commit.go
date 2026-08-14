package main

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
)

type commitOptions struct {
	Output          string
	Compatibility   string
	SourceRoot      string
	SourceFiles     []string
	SchemaRegistry  string
	Protocol        string
	SourceCommit    string
	ContractVersion string
}

func runDistributionMode(mode string, build buildOptions, commit commitOptions, verify verifyOptions, privateKey, prefix, confirmation string) error {
	switch mode {
	case "build":
		return buildDistribution(build)
	case "commit":
		return commitBuildDistribution(commit)
	case "verify":
		return verifyDistribution(verify)
	case "sign":
		return signDistribution(verify.Distribution, privateKey)
	case "install":
		return installDistribution(verify, prefix)
	case "rollback":
		return rollbackDistribution(verify, prefix)
	case "uninstall":
		return uninstallDistribution(prefix, confirmation, verify.PublicKey)
	default:
		return errors.New("agent-eval-distribution: --mode must be build, commit, verify, sign, install, rollback, or uninstall")
	}
}

func commitBuildDistribution(options commitOptions) error {
	if options.Output == "" || options.SourceRoot == "" || len(options.SourceFiles) == 0 || options.SourceCommit == "" || options.SchemaRegistry == "" || options.Protocol == "" || options.Compatibility == "" {
		return errors.New("commit requires output, compatibility, source-root, source-files, source-commit, schema-registry, and protocol")
	}
	if !filepath.IsAbs(options.Output) || filepath.Clean(options.Output) != options.Output {
		return errors.New("distribution output must be an absolute clean path")
	}
	if !validCommit(options.SourceCommit) || !validDistributionContractVersion(options.ContractVersion) {
		return errors.New("commit metadata is not canonical")
	}
	root, err := openDirectory(options.Output)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	marker, err := readRootFile(root, distributionBuildMark, 64)
	if err != nil || !bytes.Equal(marker, []byte("incomplete\n")) {
		return errors.New("distribution is not awaiting the final build commit")
	}
	manifestData, err := readRootFile(root, manifestName, maxManifestBytes)
	if err != nil {
		return err
	}
	manifest, err := decodeManifest(manifestData)
	if err != nil {
		return err
	}
	checksum, err := readRootFile(root, manifestChecksumName, 256)
	if err != nil || !canonicalChecksumMatches(checksum, sha256Bytes(manifestData)) {
		return errors.New("manifest checksum mismatch")
	}
	if manifest.SourceCommit != options.SourceCommit || manifest.ContractVersion != options.ContractVersion {
		return errors.New("distribution source identity changed before commit")
	}
	sourceTree, sourceSnapshot, err := hashSelectedTree(options.SourceRoot, options.SourceFiles)
	if err != nil {
		return fmt.Errorf("source tree: %w", err)
	}
	if sourceTree != manifest.SourceTreeSHA256 {
		return errors.New("distribution source tree changed before commit")
	}
	compatibilityData, err := selectedSourceData(options.SourceRoot, options.SourceFiles, sourceSnapshot, options.Compatibility)
	if err != nil {
		return fmt.Errorf("compatibility bundle: %w", err)
	}
	if sha256Bytes(compatibilityData) != manifest.CompatibilityBundleSHA256 {
		return errors.New("compatibility bundle changed before commit")
	}
	if err := validateCompatibilityBundle(compatibilityData, options.ContractVersion); err != nil {
		return fmt.Errorf("compatibility bundle: %w", err)
	}
	if err := validateCompatibilityBundleInSnapshot(compatibilityData, options.ContractVersion, options.SourceRoot, options.Compatibility, sourceSnapshot); err != nil {
		return fmt.Errorf("compatibility source binding: %w", err)
	}
	schemaData, err := selectedSourceData(options.SourceRoot, options.SourceFiles, sourceSnapshot, options.SchemaRegistry)
	if err != nil {
		return fmt.Errorf("schema registry: %w", err)
	}
	protocolData, err := selectedSourceData(options.SourceRoot, options.SourceFiles, sourceSnapshot, options.Protocol)
	if err != nil {
		return fmt.Errorf("process protocol: %w", err)
	}
	if sha256Bytes(schemaData) != manifest.SchemaRegistrySHA256 || sha256Bytes(protocolData) != manifest.ProtocolSHA256 {
		return errors.New("source schema or protocol changed before commit")
	}
	entries, err := readDirectoryBounded(root, maxDistributionFiles+3)
	if err != nil {
		return err
	}
	expected := map[string]bool{manifestName: true, manifestChecksumName: true, distributionBuildMark: true}
	for _, entry := range manifest.Files {
		expected[entry.Name] = true
		data, err := readRootFile(root, entry.Name, maxArtifactBytes)
		if err != nil {
			return fmt.Errorf("artifact %s: %w", entry.Name, err)
		}
		if sha256Bytes(data) != entry.SHA256 || int64(len(data)) != entry.SizeBytes {
			return fmt.Errorf("artifact %s checksum or size mismatch", entry.Name)
		}
	}
	if len(entries) != len(expected) {
		return errors.New("incomplete build contains unexpected members")
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return fmt.Errorf("unexpected build member %q", entry.Name())
		}
	}
	if err := removeMarkerDurably(root, distributionBuildMark, []byte("incomplete\n")); err != nil {
		return fmt.Errorf("build marker commit: %w", err)
	}
	return nil
}
