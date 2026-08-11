//go:build windows

package agenteval

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/extension"
)

func TestAttemptLedgerWindowsFailsClosedBeforeExtensionProcessEntry(t *testing.T) {
	parent := t.TempDir()
	ledgerRoot := filepath.Join(parent, "attempt-ledger")
	if _, err := CreateAttemptLedgerStore(ledgerRoot, bytes.NewReader(bytes.Repeat([]byte{0x52}, 32))); !errors.Is(err, ErrAttemptLedgerUnsupported) {
		t.Fatalf("Windows ledger creation did not fail closed: %v", err)
	}

	executableDigest := stringsRepeatHex('a')
	manifest := extensionHostTestManifest(executableDigest)
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundleData, err := EncodeExtensionConformanceBundle(extensionHostTestBundle(manifestData, executableDigest, manifest))
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(parent, "manifest.json")
	bundlePath := filepath.Join(parent, "bundle.json")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, bundleData, 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := VerifyExtensionProtocolFiles(
		context.Background(), manifestPath, filepath.Join(parent, "must-not-start.exe"), bundlePath, ledgerRoot,
	)
	if !errors.Is(err, errExtensionOutcomeUnknown) || !reflect.DeepEqual(report, ExtensionConformanceReport{}) {
		t.Fatalf("Windows extension facade did not stop at durable-ledger admission: report=%+v err=%v", report, err)
	}
}
