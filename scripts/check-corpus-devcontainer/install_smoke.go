package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runInstallSmoke(repositoryRoot, temporary string) error {
	fakeBin := filepath.Join(temporary, "install-tools")
	installRoot := filepath.Join(temporary, "installed")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		return err
	}
	release := filepath.Join(temporary, "release-atl")
	if err := writeExecutable(release, "#!/bin/sh\n[ \"$1\" = version ]\nprintf '%s\\n' '{\"version\":\"1.2.3\",\"commit\":\"synthetic\",\"build_state\":\"clean\"}'\n"); err != nil {
		return err
	}
	if err := writeExecutable(filepath.Join(fakeBin, "curl"), fakeCurlScript); err != nil {
		return err
	}
	if err := writeExecutable(filepath.Join(fakeBin, "gh"), fakeGHScript); err != nil {
		return err
	}
	data, err := os.ReadFile(release)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	environmentFor := func(binary, checksum, installDir, curlLog string) []string {
		return []string{
			"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"HOME=" + temporary,
			"ATL_VERSION=v1.2.3",
			"ATL_ASSET_SHA256=" + checksum,
			"ATL_INSTALL_DIR=" + installDir,
			"ATL_FAKE_RELEASE_BINARY=" + binary,
			"ATL_FAKE_CURL_LOG=" + curlLog,
			"GH_TOKEN=synthetic-must-not-reach-offline-verifier",
			"GITHUB_TOKEN=synthetic-must-not-reach-offline-verifier",
			"GH_ENTERPRISE_TOKEN=synthetic-must-not-reach-offline-verifier",
			"GH_HOST=synthetic.example.test",
			"GH_CONFIG_DIR=" + filepath.Join(temporary, "poisoned-gh-config"),
		}
	}
	ghLog := filepath.Join(fakeBin, "gh.log")
	ghFailMarker := filepath.Join(fakeBin, "fail-gh")
	curlLog := filepath.Join(temporary, "curl.log")
	environment := environmentFor(release, hex.EncodeToString(sum[:]), installRoot, curlLog)
	stdout, stderr, runErr := runCommand(filepath.Join(repositoryRoot, exampleRelative, "install-atl.sh"), environment)
	if runErr != nil {
		return fmt.Errorf("install smoke: %w: %s", runErr, stderr)
	}
	if strings.Contains(stdout+stderr, temporary) || !strings.Contains(stdout, "verified pinned ATL release") {
		return errors.New("installer output is not content-free")
	}
	installed, err := os.ReadFile(filepath.Join(installRoot, "atl"))
	if err != nil || !bytes.Equal(installed, data) {
		return errors.New("installer did not preserve the attested binary")
	}
	logBytes, err := os.ReadFile(ghLog)
	if err != nil || !bytes.Contains(logBytes, []byte("attestation verify")) ||
		!bytes.Contains(logBytes, []byte("--bundle")) ||
		!bytes.Contains(logBytes, []byte("--hostname github.com")) ||
		!bytes.Contains(logBytes, []byte(".github/workflows/release.yml")) ||
		!bytes.Contains(logBytes, []byte("--source-ref refs/tags/v1.2.3")) {
		return errors.New("installer did not invoke the pinned offline provenance check")
	}
	curlBytes, err := os.ReadFile(curlLog)
	if err != nil || !bytes.Contains(curlBytes, []byte("releases/download/v1.2.3/atl-linux-")) ||
		!bytes.Contains(curlBytes, []byte("api.github.com/repos/isukharev/atl/attestations/sha256:"+hex.EncodeToString(sum[:]))) ||
		bytes.Contains(bytes.ToLower(curlBytes), []byte("authorization")) {
		return errors.New("installer did not use the anonymous digest-bound attestation route")
	}

	rejectedRoot := filepath.Join(temporary, "rejected-install")
	if err := os.WriteFile(ghFailMarker, []byte("fail\n"), 0o600); err != nil {
		return err
	}
	rejectedEnvironment := environmentFor(release, hex.EncodeToString(sum[:]), rejectedRoot,
		filepath.Join(temporary, "rejected-curl.log"))
	stdout, stderr, runErr = runCommand(filepath.Join(repositoryRoot, exampleRelative, "install-atl.sh"), rejectedEnvironment)
	if err := os.Remove(ghFailMarker); err != nil {
		return err
	}
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "release provenance verification failed") {
		return errors.New("installer accepted a failed provenance verification")
	}
	if _, err := os.Lstat(filepath.Join(rejectedRoot, "atl")); !os.IsNotExist(err) {
		return errors.New("installer published a binary after failed provenance verification")
	}

	for _, failure := range []struct {
		name    string
		setting string
		message string
	}{
		{name: "unavailable", setting: "ATL_FAKE_ATTESTATION_FETCH_FAIL=1", message: "release attestation download failed"},
		{name: "malformed", setting: "ATL_FAKE_ATTESTATION_MALFORMED=1", message: "release attestation response was invalid"},
	} {
		failedRoot := filepath.Join(temporary, failure.name+"-attestation-install")
		ghLogBefore, err := os.ReadFile(ghLog)
		if err != nil {
			return err
		}
		failedEnvironment := environmentFor(release, hex.EncodeToString(sum[:]), failedRoot,
			filepath.Join(temporary, failure.name+"-attestation-curl.log"))
		failedEnvironment = append(failedEnvironment, failure.setting)
		stdout, stderr, runErr = runCommand(filepath.Join(repositoryRoot, exampleRelative, "install-atl.sh"), failedEnvironment)
		if runErr == nil || stdout != "" || !strings.Contains(stderr, failure.message) {
			return fmt.Errorf("installer accepted %s attestation evidence", failure.name)
		}
		ghLogAfter, err := os.ReadFile(ghLog)
		if err != nil || !bytes.Equal(ghLogAfter, ghLogBefore) {
			return fmt.Errorf("installer invoked the verifier after %s attestation evidence", failure.name)
		}
		if _, err := os.Lstat(filepath.Join(failedRoot, "atl")); !os.IsNotExist(err) {
			return fmt.Errorf("installer published a binary after %s attestation evidence", failure.name)
		}
	}

	mismatchedRelease := filepath.Join(temporary, "mismatched-release-atl")
	if err := writeExecutable(mismatchedRelease, "#!/bin/sh\n[ \"$1\" = version ]\nprintf '%s\\n' '{\"version\":\"9.9.9\",\"commit\":\"synthetic\",\"build_state\":\"clean\"}'\n"); err != nil {
		return err
	}
	mismatchedBytes, err := os.ReadFile(mismatchedRelease)
	if err != nil {
		return err
	}
	mismatchedSum := sha256.Sum256(mismatchedBytes)
	mismatchedRoot := filepath.Join(temporary, "mismatched-install")
	mismatchedEnvironment := environmentFor(mismatchedRelease, hex.EncodeToString(mismatchedSum[:]), mismatchedRoot,
		filepath.Join(temporary, "mismatched-curl.log"))
	stdout, stderr, runErr = runCommand(filepath.Join(repositoryRoot, exampleRelative, "install-atl.sh"), mismatchedEnvironment)
	if runErr == nil || stdout != "" || !strings.Contains(stderr, "verified binary version does not match ATL_VERSION") {
		return errors.New("installer accepted an attested binary from a different release version")
	}
	if _, err := os.Lstat(filepath.Join(mismatchedRoot, "atl")); !os.IsNotExist(err) {
		return errors.New("installer published a version-mismatched binary")
	}
	return nil
}

const fakeCurlScript = `#!/bin/sh
set -eu
output=
url=
printf '%s\n' "$*" >>"$ATL_FAKE_CURL_LOG"
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o|--output)
			shift
			output=$1
			;;
		https://*) url=$1 ;;
	esac
	shift
done
[ -n "$output" ]
case "$url" in
	https://api.github.com/repos/isukharev/atl/attestations/sha256:*)
		[ -z "${ATL_FAKE_ATTESTATION_FETCH_FAIL:-}" ] || exit 22
		if [ -n "${ATL_FAKE_ATTESTATION_MALFORMED:-}" ]; then
			printf '%s\n' '{"attestations":[]}' >"$output"
		else
			printf '%s\n' '{"attestations":[{"bundle":{"synthetic":true}}]}' >"$output"
		fi
		;;
	https://github.com/isukharev/atl/releases/download/*)
		cp "$ATL_FAKE_RELEASE_BINARY" "$output"
		chmod 0600 "$output"
		;;
	*) exit 23 ;;
esac
`

const fakeGHScript = `#!/bin/sh
set -eu
fake_root=${0%/*}
printf '%s\n' "$*" >>"$fake_root/gh.log"
[ "$1" = attestation ] && [ "$2" = verify ] && [ ! -x "$3" ] || exit 8
[ -z "${GH_TOKEN:-}${GITHUB_TOKEN:-}${GH_ENTERPRISE_TOKEN:-}${GH_HOST:-}" ] || exit 8
[ -n "${HOME:-}" ] && [ -n "${GH_CONFIG_DIR:-}" ] || exit 8
case "$GH_CONFIG_DIR" in *poisoned*) exit 8 ;; esac
bundle=
while [ "$#" -gt 0 ]; do
	if [ "$1" = --bundle ]; then
		shift
		bundle=$1
	fi
	shift
done
[ -n "$bundle" ] && [ -s "$bundle" ]
[ ! -e "$fake_root/fail-gh" ] || exit 9
`
