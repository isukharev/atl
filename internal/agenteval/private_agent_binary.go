package agenteval

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	privateAgentBinaryMaxBytes           = 512 << 20
	privateAgentResourceMaxBytes         = 64 << 20
	privateAgentSnapshotBaseName         = "agent"
	privateAgentLinuxSandboxMarker       = "codex-resources/bwrap"
	privateAgentLinuxSandboxRelativePath = "codex-resources/bwrap"
)

// privateAgentBinaryContract is deliberately not serialized. The durable plan
// binds its opaque byte and provenance hashes through InputsSHA256 without
// retaining an installation path. RunHeadless receives only the deterministic
// content identity, never a value obtained by executing the binary.
type privateAgentBinaryContract struct {
	canonicalPath         string
	bytesSHA256           string
	provenanceSHA256      string
	identity              string
	resourceCanonicalPath string
	resourceRelativePath  string
	resourceBytesSHA256   string
}

func inspectPrivateAgentBinary(path, provenanceOverride string) (privateAgentBinaryContract, []byte, error) {
	if provenanceOverride != "" {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return privateAgentBinaryContract{}, nil, privatePlanError("agent_binary_snapshot")
		}
	}
	canonical, err := canonicalPrivateAgentExecutable(path)
	if err != nil {
		return privateAgentBinaryContract{}, nil, privatePlanError("agent_binary")
	}
	data, err := readBoundedFile(canonical, privateAgentBinaryMaxBytes)
	if err != nil {
		return privateAgentBinaryContract{}, nil, privatePlanError("agent_binary")
	}
	if err := validatePrivateAgentNativeFormat(data); err != nil {
		return privateAgentBinaryContract{}, nil, err
	}
	bytesDigest := sha256HexBytes(data)
	resourcePath, resourceRelativePath, resourceDigest, err := inspectPrivateAgentResource(canonical, data)
	if err != nil {
		return privateAgentBinaryContract{}, nil, err
	}
	provenance := privateAgentProvenance(canonical)
	if provenanceOverride != "" {
		if !validSHA256(provenanceOverride) {
			return privateAgentBinaryContract{}, nil, privatePlanError("agent_binary")
		}
		provenance = provenanceOverride
	}
	return privateAgentBinaryContract{
		canonicalPath: canonical, bytesSHA256: bytesDigest, provenanceSHA256: provenance,
		identity: "binary-sha256:" + bytesDigest, resourceCanonicalPath: resourcePath,
		resourceRelativePath: resourceRelativePath, resourceBytesSHA256: resourceDigest,
	}, data, nil
}

func inspectPrivateAgentResource(agentPath string, agentData []byte) (string, string, string, error) {
	if runtime.GOOS != "linux" || filepath.Base(agentPath) != "codex" || !bytes.Contains(agentData, []byte(privateAgentLinuxSandboxMarker)) {
		return "", "", "", nil
	}
	candidate := filepath.Join(filepath.Dir(agentPath), "..", filepath.FromSlash(privateAgentLinuxSandboxRelativePath))
	canonical, err := canonicalPrivateAgentExecutable(candidate)
	if err != nil {
		return "", "", "", privatePlanError("agent_binary_resource")
	}
	data, err := readBoundedFile(canonical, privateAgentResourceMaxBytes)
	if err != nil {
		return "", "", "", privatePlanError("agent_binary_resource")
	}
	if err := validatePrivateAgentNativeFormat(data); err != nil {
		return "", "", "", privatePlanError("agent_binary_resource_format")
	}
	return canonical, privateAgentLinuxSandboxRelativePath, sha256HexBytes(data), nil
}

func verifyPrivateAgentResourceSnapshot(agentPath string, reviewed privateAgentBinaryContract) error {
	if reviewed.resourceRelativePath == "" {
		return nil
	}
	if reviewed.resourceRelativePath != privateAgentLinuxSandboxRelativePath || !validSHA256(reviewed.resourceBytesSHA256) {
		return privatePlanError("agent_binary_resource_snapshot")
	}
	candidate := filepath.Join(filepath.Dir(agentPath), "..", filepath.FromSlash(reviewed.resourceRelativePath))
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return privatePlanError("agent_binary_resource_snapshot")
	}
	data, err := readBoundedFile(candidate, privateAgentResourceMaxBytes)
	if err != nil || sha256HexBytes(data) != reviewed.resourceBytesSHA256 {
		return privatePlanError("agent_binary_resource_snapshot")
	}
	if err := validatePrivateAgentNativeFormat(data); err != nil {
		return privatePlanError("agent_binary_resource_format")
	}
	return nil
}

func canonicalPrivateAgentExecutable(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty executable")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
		return "", fmt.Errorf("not an executable regular file")
	}
	return filepath.Clean(canonical), nil
}

func privateAgentProvenance(path string) string {
	normalized := filepath.ToSlash(filepath.Clean(path))
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	return sha256HexBytes([]byte("atl-agent-executable-v1\x00" + normalized))
}

func privateAgentSnapshotName(source string) string {
	if runtime.GOOS == "linux" && filepath.Base(source) == "codex" {
		return "codex"
	}
	extension := filepath.Ext(source)
	if runtime.GOOS == "windows" && !strings.EqualFold(extension, ".exe") {
		extension = ".exe"
	}
	return privateAgentSnapshotBaseName + extension
}

func writePrivateAgentCopy(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(path, 0o700)
}
