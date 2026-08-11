package agenteval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const capabilityCatalogProcessTimeout = 5 * time.Second

// VerifyATLCapabilityCatalog binds an evaluator run to the schema-v1 catalog
// exposed by the exact ATL executable selected for that run. The command is
// offline, bounded, single-attempt, and receives no ambient ATL configuration
// or backend credentials.
func VerifyATLCapabilityCatalog(ctx context.Context, binary, ledgerRoot string) (returnErr error) {
	executable, err := resolveCapabilityCatalogExecutable(binary)
	if err != nil {
		return err
	}
	digest, err := digestSyntheticExecutable(executable, privateAgentBinaryMaxBytes)
	if err != nil {
		return err
	}
	contract, err := contentMinimizedAttemptDigest("atl-capability-catalog-contract", struct {
		ExecutableSHA256 string `json:"executable_sha256"`
		CatalogSHA256    string `json:"catalog_sha256"`
	}{digest, sha256HexBytes(pinnedCapabilityCatalogJSON)})
	if err != nil {
		return err
	}
	session, err := prepareQualificationAttemptSession(ledgerRoot, "atl-capability-catalog", "binary-sha256:"+digest,
		contract, "not_applicable", int(capabilityCatalogProcessTimeout/time.Second))
	if err != nil {
		return err
	}
	terminationOK, receipt, verifyErr := verifyATLCapabilityCatalogWithSession(ctx, executable, session)
	return errors.Join(verifyErr, finalizeQualificationAttempt(session, struct {
		Compatible bool `json:"compatible"`
	}{verifyErr == nil}, terminationOK, receipt,
		errors.Is(verifyErr, context.DeadlineExceeded), errors.Is(verifyErr, context.Canceled), verifyErr))
}

func verifyATLCapabilityCatalogWithSession(ctx context.Context, binary string, session *DurableAttemptSession) (bool, string, error) {
	executable, err := resolveCapabilityCatalogExecutable(binary)
	if err != nil {
		return false, "", err
	}

	commandCtx, cancel := context.WithTimeout(ctx, capabilityCatalogProcessTimeout)
	defer cancel()
	stdout := &cappedCommandOutput{limit: maxCapabilityCatalogBytes}
	stderr := &cappedCommandOutput{limit: 64 << 10}
	command := exec.CommandContext(commandCtx, executable, "capabilities", "-o", "json")
	command.WaitDelay = 250 * time.Millisecond
	command.Env = capabilityCatalogProcessEnvironment()
	command.Stdout = stdout
	command.Stderr = stderr
	_, terminationOK, receipt, runErr := executeProviderAttemptWithSession(command, nil, nil, session)
	if runErr != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) || errors.Is(commandCtx.Err(), context.Canceled) {
			return terminationOK, receipt, fmt.Errorf("ATL capability catalog preflight did not complete: %w", commandCtx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			detail := strings.TrimSpace(stderr.data.String())
			if len(detail) > 512 {
				detail = detail[:512] + "…"
			}
			if detail != "" {
				return terminationOK, receipt, fmt.Errorf("ATL capability catalog preflight failed with exit %d: %s", exitErr.ExitCode(), detail)
			}
			return terminationOK, receipt, fmt.Errorf("ATL capability catalog preflight failed with exit %d", exitErr.ExitCode())
		}
		return terminationOK, receipt, fmt.Errorf("ATL capability catalog preflight failed: %w", runErr)
	}
	if stdout.overflow || stderr.overflow {
		return terminationOK, receipt, fmt.Errorf("ATL capability catalog preflight exceeded its output bound")
	}
	catalog, err := DecodeCapabilityCatalog(&stdout.data)
	if err != nil {
		return terminationOK, receipt, fmt.Errorf("ATL capability catalog preflight: %w", err)
	}
	if err := VerifyPinnedCapabilityCatalog(catalog); err != nil {
		return terminationOK, receipt, fmt.Errorf("ATL capability catalog preflight: %w", err)
	}
	return terminationOK, receipt, nil
}

func resolveCapabilityCatalogExecutable(binary string) (string, error) {
	if binary == "" {
		return "", fmt.Errorf("ATL capability catalog preflight requires an executable")
	}
	absolute, err := filepath.Abs(binary)
	if err != nil {
		return "", fmt.Errorf("resolve ATL capability catalog executable")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve ATL capability catalog executable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
		return "", fmt.Errorf("ATL capability catalog preflight requires an executable regular file")
	}
	return resolved, nil
}

func capabilityCatalogProcessEnvironment() []string {
	values := map[string]string{
		"ATL_NO_UPDATE": "1",
		"ATL_READ_ONLY": "1",
	}
	// Windows needs its system root to start some child executables. Temporary
	// directory variables are harmless process plumbing; all ATL/backend and
	// provider configuration remains deliberately absent.
	for _, name := range []string{"SYSTEMROOT", "WINDIR", "TMPDIR", "TMP", "TEMP"} {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			values[name] = value
		}
	}
	return flattenEnvironment(values)
}
