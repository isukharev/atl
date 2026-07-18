package agenteval

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	privateAgentBinaryMaxBytes   = 512 << 20
	privateAgentSnapshotBaseName = "agent"
)

// privateAgentBinaryContract is deliberately not serialized. The durable plan
// binds its opaque byte and provenance hashes through InputsSHA256 without
// retaining an installation path. RunHeadless receives only the deterministic
// content identity, never a value obtained by executing the binary.
type privateAgentBinaryContract struct {
	canonicalPath    string
	bytesSHA256      string
	provenanceSHA256 string
	identity         string
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
	provenance := privateAgentProvenance(canonical)
	if provenanceOverride != "" {
		if !validSHA256(provenanceOverride) {
			return privateAgentBinaryContract{}, nil, privatePlanError("agent_binary")
		}
		provenance = provenanceOverride
	}
	return privateAgentBinaryContract{
		canonicalPath: canonical, bytesSHA256: bytesDigest, provenanceSHA256: provenance,
		identity: "binary-sha256:" + bytesDigest,
	}, data, nil
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
