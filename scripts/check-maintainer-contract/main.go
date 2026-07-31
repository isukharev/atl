// Command check-maintainer-contract verifies the repository's exact Go toolchain contract.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	goFeatureID             = "ghcr.io/devcontainers/features/go:1"
	goFeaturePackageVersion = "1.3.4"
	goFeatureDigest         = "sha256:d85e921f91b41340055bb12b325d9d551170ed04b3b832e33530bf42f167c032"
	// verifiedBaseImage pins the OCI digest that
	// mcr.microsoft.com/devcontainers/base:bookworm resolved to on 2026-07-31.
	verifiedBaseImage = "mcr.microsoft.com/devcontainers/base:bookworm@sha256:73d85a96694a2cadca1ba3fcb5721f2312a64f1d571dd86f6c77e10a708931dc"
)

var exactGoVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

type devcontainerConfig struct {
	Image        string                       `json:"image"`
	RemoteUser   string                       `json:"remoteUser"`
	Features     map[string]map[string]string `json:"features"`
	ContainerEnv map[string]string            `json:"containerEnv"`
}

type featureLock struct {
	Features map[string]lockedFeature `json:"features"`
}

type lockedFeature struct {
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
}

type report struct {
	Status         string `json:"status"`
	GoVersion      string `json:"go_version"`
	RuntimeVersion string `json:"runtime_version"`
}

func main() {
	if err := run(".", runtime.Version(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "check-maintainer-contract:", err)
		os.Exit(1)
	}
}

func run(root, runtimeVersion string, output io.Writer) error {
	goVersion, err := validateRepository(root, runtimeVersion)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report{Status: "ok", GoVersion: goVersion, RuntimeVersion: runtimeVersion})
}

func validateRepository(root, runtimeVersion string) (string, error) {
	goVersion, err := readGoVersion(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	if err := validateRuntime(goVersion, runtimeVersion); err != nil {
		return "", err
	}
	if err := validateDevcontainer(root, goVersion); err != nil {
		return "", err
	}
	if err := validateBootstrap(root); err != nil {
		return "", err
	}
	for _, workflow := range []string{"ci.yml", "codeql.yml", "release.yml"} {
		path := filepath.Join(root, ".github", "workflows", workflow)
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		if err := validateSetupGoWorkflow(contents); err != nil {
			return "", fmt.Errorf("%s: %w", workflow, err)
		}
	}
	return goVersion, nil
}

func readGoVersion(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var versions []string
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" {
			versions = append(versions, fields[1])
		}
	}
	if len(versions) != 1 || !exactGoVersion.MatchString(versions[0]) {
		return "", fmt.Errorf("go.mod must contain exactly one exact go MAJOR.MINOR.PATCH directive")
	}
	return versions[0], nil
}

func validateRuntime(goVersion, runtimeVersion string) error {
	want := "go" + goVersion
	if runtimeVersion != want {
		return fmt.Errorf("runtime.Version() = %q, want %q from go.mod", runtimeVersion, want)
	}
	return nil
}

func validateDevcontainer(root, goVersion string) error {
	configPath := filepath.Join(root, ".devcontainer", "devcontainer.json")
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	var config devcontainerConfig
	if err := decodeJSONC(contents, &config); err != nil {
		return fmt.Errorf("decode %s: %w", configPath, err)
	}
	if config.Image != verifiedBaseImage {
		return fmt.Errorf("devcontainer image = %q, want verified base image %q", config.Image, verifiedBaseImage)
	}
	if config.RemoteUser != "vscode" {
		return fmt.Errorf("devcontainer remoteUser = %q, want %q", config.RemoteUser, "vscode")
	}
	if config.ContainerEnv["GOTOOLCHAIN"] != "local" {
		return fmt.Errorf("devcontainer GOTOOLCHAIN = %q, want %q", config.ContainerEnv["GOTOOLCHAIN"], "local")
	}
	feature, ok := config.Features[goFeatureID]
	if !ok {
		return fmt.Errorf("devcontainer is missing feature %q", goFeatureID)
	}
	if feature["version"] != goVersion {
		return fmt.Errorf("devcontainer Go feature version = %q, want %q from go.mod", feature["version"], goVersion)
	}

	lockPath := filepath.Join(root, ".devcontainer", "devcontainer-lock.json")
	lockContents, err := os.ReadFile(lockPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", lockPath, err)
	}
	var lock featureLock
	if err := json.Unmarshal(lockContents, &lock); err != nil {
		return fmt.Errorf("decode %s: %w", lockPath, err)
	}
	locked, ok := lock.Features[goFeatureID]
	if !ok {
		return fmt.Errorf("devcontainer lock is missing feature %q", goFeatureID)
	}
	if locked.Version != goFeaturePackageVersion {
		return fmt.Errorf("devcontainer Go feature package version = %q, want reviewed %q", locked.Version, goFeaturePackageVersion)
	}
	const resolvedPrefix = "ghcr.io/devcontainers/features/go@"
	if locked.Resolved != resolvedPrefix+goFeatureDigest || locked.Integrity != goFeatureDigest {
		return errors.New("devcontainer Go feature lock does not match the reviewed package digest")
	}
	return nil
}

func validateBootstrap(root string) error {
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}
	const makeContract = "check-maintainer-contract:\n\tGOTOOLCHAIN=local go run ./scripts/check-maintainer-contract\n"
	if !bytes.Contains(makefile, []byte(makeContract)) {
		return errors.New("makefile maintainer check must start with GOTOOLCHAIN=local")
	}
	postCreate, err := os.ReadFile(filepath.Join(root, ".devcontainer", "post-create.sh"))
	if err != nil {
		return fmt.Errorf("read devcontainer post-create: %w", err)
	}
	if !bytes.Contains(postCreate, []byte("go run ./scripts/check-maintainer-contract")) ||
		bytes.Contains(postCreate, []byte("GOTOOLCHAIN=auto")) {
		return errors.New("devcontainer post-create must run the local maintainer contract")
	}
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		return fmt.Errorf("read ci workflow: %w", err)
	}
	if !bytes.Contains(ci, []byte("run: make check-maintainer-contract")) {
		return errors.New("ci must run make check-maintainer-contract")
	}
	return nil
}

func validateSetupGoWorkflow(contents []byte) error {
	lines := strings.Split(string(contents), "\n")
	setupCount := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, "- uses: actions/setup-go@") {
			continue
		}
		setupCount++
		stepIndent := indentation(line)
		withIndent := -1
		hasVersionFile := false
		for next := index + 1; next < len(lines); next++ {
			nextLine := lines[next]
			nextTrimmed := strings.TrimSpace(nextLine)
			nextIndent := indentation(nextLine)
			if nextTrimmed != "" && nextIndent <= stepIndent {
				break
			}
			if nextTrimmed == "" || strings.HasPrefix(nextTrimmed, "#") {
				continue
			}
			if strings.HasPrefix(nextTrimmed, "go-version:") {
				return errors.New("actions/setup-go must not use a literal go-version")
			}
			if withIndent >= 0 && nextIndent <= withIndent {
				withIndent = -1
			}
			if nextTrimmed == "with:" {
				withIndent = nextIndent
				continue
			}
			if withIndent >= 0 && nextIndent == withIndent+2 && nextTrimmed == "go-version-file: go.mod" {
				hasVersionFile = true
			}
		}
		if !hasVersionFile {
			return errors.New("actions/setup-go must use go-version-file: go.mod")
		}
	}
	if setupCount == 0 {
		return errors.New("workflow has no actions/setup-go step")
	}
	return nil
}

func indentation(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

func decodeJSONC(contents []byte, destination any) error {
	stripped, err := stripJSONComments(contents)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(stripped))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing content")
	}
	return nil
}

func stripJSONComments(contents []byte) ([]byte, error) {
	result := make([]byte, 0, len(contents))
	inString := false
	escaped := false
	for index := 0; index < len(contents); index++ {
		current := contents[index]
		if inString {
			result = append(result, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			result = append(result, current)
			continue
		}
		if current != '/' || index+1 >= len(contents) {
			result = append(result, current)
			continue
		}
		switch contents[index+1] {
		case '/':
			index += 2
			for index < len(contents) && contents[index] != '\n' {
				index++
			}
			if index < len(contents) {
				result = append(result, '\n')
			}
		case '*':
			index += 2
			for index+1 < len(contents) && (contents[index] != '*' || contents[index+1] != '/') {
				if contents[index] == '\n' {
					result = append(result, '\n')
				}
				index++
			}
			if index+1 >= len(contents) {
				return nil, errors.New("unterminated JSON block comment")
			}
			index++
		default:
			result = append(result, current)
		}
	}
	if inString {
		return nil, errors.New("unterminated JSON string")
	}
	return result, nil
}
