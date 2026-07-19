package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/isukharev/atl/internal/safepath"
)

const codexAuthMaxBytes = 4 << 20

const (
	codexBenchmarkPluginID      = "atl@atl"
	codexBenchmarkPluginName    = "atl"
	codexPluginCommandOutputMax = 1 << 20
)

const codexIsolatedShell = "/bin/sh"

// codexAuthSession carries only validated provider authentication between
// otherwise fresh runtime capsules. It never writes back to the operator's
// CODEX_HOME; the benchmark must not mutate ambient provider state.
type codexAuthSession struct {
	mu         sync.Mutex
	auth       []byte
	connection map[string]string
	closed     bool
}

// providerRuntimeCapsule gives one provider invocation an isolated home while
// keeping its credential projection out of persistent run artifacts.
type providerRuntimeCapsule struct {
	root            string
	scratchRoot     string
	environment     map[string]string
	authSession     *codexAuthSession
	authPending     bool
	pluginSkillRoot string
}

func newCodexAuthSession(ambient []string) (*codexAuthSession, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("isolated codex provider execution requires POSIX owner-only filesystem permissions")
	}
	auth, err := loadCodexAuthProjection(ambient)
	if err != nil {
		return nil, err
	}
	return &codexAuthSession{auth: auth, connection: codexProviderConnectionEnvironment(ambient)}, nil
}

func (s *codexAuthSession) authentication() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.auth) == 0 {
		return nil, fmt.Errorf("codex provider authentication session is unavailable")
	}
	return bytes.Clone(s.auth), nil
}

func (s *codexAuthSession) connectionEnvironment() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.connection))
	for name, value := range s.connection {
		out[name] = value
	}
	return out
}

func (s *codexAuthSession) updateAuthentication(data []byte) error {
	if !validCodexAuthJSON(data) || len(data) > codexAuthMaxBytes {
		return fmt.Errorf("isolated codex provider authentication is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("codex provider authentication session is unavailable")
	}
	clear(s.auth)
	s.auth = bytes.Clone(data)
	return nil
}

func (s *codexAuthSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		clear(s.auth)
		s.auth = nil
		clear(s.connection)
		s.connection = nil
		s.closed = true
	}
	return nil
}

func newCodexProviderRuntime(scratchRoot string, session *codexAuthSession) (*providerRuntimeCapsule, error) {
	if scratchRoot == "" || session == nil {
		return nil, fmt.Errorf("prepare isolated codex provider runtime")
	}
	if err := requirePrivateDirectory("codex provider scratch root", scratchRoot); err != nil {
		return nil, fmt.Errorf("prepare isolated codex provider runtime")
	}
	root, err := os.MkdirTemp(scratchRoot, "atl-agent-eval-provider-runtime-")
	if err != nil {
		return nil, fmt.Errorf("prepare isolated codex provider runtime")
	}
	capsule := &providerRuntimeCapsule{root: root, scratchRoot: scratchRoot, authSession: session}
	failed := true
	defer func() {
		if failed {
			_ = capsule.Close()
		}
	}()
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("prepare isolated codex provider runtime")
	}
	directories := map[string]string{
		"HOME":            filepath.Join(root, "home"),
		"CODEX_HOME":      filepath.Join(root, "codex-home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME":   filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME":  filepath.Join(root, "xdg-cache"),
		"TMPDIR":          filepath.Join(root, "tmp"),
		"TMP":             filepath.Join(root, "tmp"),
		"TEMP":            filepath.Join(root, "tmp"),
		"USERPROFILE":     filepath.Join(root, "home"),
		"APPDATA":         filepath.Join(root, "xdg-config"),
		"LOCALAPPDATA":    filepath.Join(root, "xdg-data"),
	}
	created := map[string]struct{}{}
	for _, directory := range directories {
		if _, ok := created[directory]; ok {
			continue
		}
		if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
			return nil, fmt.Errorf("prepare isolated codex provider runtime")
		}
		if err := requirePrivateDirectory("codex provider runtime directory", directory); err != nil {
			return nil, fmt.Errorf("prepare isolated codex provider runtime")
		}
		created[directory] = struct{}{}
	}
	capsule.environment = session.connectionEnvironment()
	for name, value := range directories {
		capsule.environment[name] = value
	}
	capsule.environment["USER"] = "atl-agent-eval"
	capsule.environment["LOGNAME"] = "atl-agent-eval"
	// Do not inherit the operator's interactive shell or shell startup state.
	// Codex still needs one explicit POSIX shell to expose the reviewed local
	// execution tool used by private CLI benchmarks.
	capsule.environment["SHELL"] = codexIsolatedShell
	auth, err := session.authentication()
	if err != nil {
		return nil, err
	}
	destination := filepath.Join(directories["CODEX_HOME"], "auth.json")
	if err := safepath.WriteFileExclusiveWithin(root, destination, auth, 0o600); err != nil {
		clear(auth)
		return nil, fmt.Errorf("prepare isolated codex provider authentication")
	}
	clear(auth)
	capsule.authPending = true
	failed = false
	return capsule, nil
}

func (c *providerRuntimeCapsule) Environment() map[string]string {
	out := make(map[string]string, len(c.environment))
	for name, value := range c.environment {
		out[name] = value
	}
	return out
}

func (c *providerRuntimeCapsule) PluginSkillRoot() string {
	if c == nil {
		return ""
	}
	return c.pluginSkillRoot
}

// provisionCodexBenchmarkPlugin uses Codex's public local-marketplace flow so
// private CLI evaluations exercise the shipped installed-plugin namespace.
// The provider capsule is fresh for every run, so the resulting config and
// cache cannot inherit or persist ambient plugins.
func provisionCodexBenchmarkPlugin(ctx context.Context, agentBinary, pluginRoot string, capsule *providerRuntimeCapsule) error {
	if ctx == nil || agentBinary == "" || pluginRoot == "" || capsule == nil || capsule.root == "" {
		return fmt.Errorf("provision isolated codex benchmark plugin")
	}
	manifestPath, _, err := providerPluginLayout(pluginRoot, "codex")
	if err != nil {
		return fmt.Errorf("provision isolated codex benchmark plugin")
	}
	manifestData, err := readBoundedFile(manifestPath, 1<<20)
	if err != nil {
		return fmt.Errorf("provision isolated codex benchmark plugin")
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(manifestData, &manifest) != nil || manifest.Name != codexBenchmarkPluginName || manifest.Version == "" {
		return fmt.Errorf("provision isolated codex benchmark plugin")
	}
	sourcePackage := filepath.Join(pluginRoot, "plugins", codexBenchmarkPluginName)
	sourcePackageDigest, err := digestTree(sourcePackage)
	if err != nil {
		return fmt.Errorf("provision isolated codex benchmark plugin")
	}
	if _, err := digestTree(filepath.Join(pluginRoot, ".agents", "plugins")); err != nil {
		return fmt.Errorf("provision isolated codex benchmark plugin")
	}
	environment := flattenEnvironment(capsule.Environment())
	if _, err := runCodexPluginCommand(ctx, agentBinary, environment, "plugin", "marketplace", "add", pluginRoot); err != nil {
		return err
	}
	installedData, err := runCodexPluginCommand(ctx, agentBinary, environment, "plugin", "add", codexBenchmarkPluginID, "--json")
	if err != nil {
		return err
	}
	var installed struct {
		PluginID        string `json:"pluginId"`
		Name            string `json:"name"`
		MarketplaceName string `json:"marketplaceName"`
		Version         string `json:"version"`
		InstalledPath   string `json:"installedPath"`
	}
	if json.Unmarshal(installedData, &installed) != nil || installed.PluginID != codexBenchmarkPluginID || installed.Name != manifest.Name || installed.MarketplaceName != codexBenchmarkPluginName || installed.Version != manifest.Version || !filepath.IsAbs(installed.InstalledPath) {
		return fmt.Errorf("verify isolated codex benchmark plugin")
	}
	cleanInstalledPath := filepath.Clean(installed.InstalledPath)
	codexHome := capsule.Environment()["CODEX_HOME"]
	canonicalCodexHome, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		return fmt.Errorf("verify isolated codex benchmark plugin")
	}
	installedPath, err := filepath.EvalSymlinks(cleanInstalledPath)
	if err != nil {
		return fmt.Errorf("verify isolated codex benchmark plugin")
	}
	inside, err := pathWithin(canonicalCodexHome, installedPath)
	if err != nil || !inside {
		return fmt.Errorf("verify isolated codex benchmark plugin")
	}
	installedPackageDigest, err := digestTree(installedPath)
	if err != nil || installedPackageDigest != sourcePackageDigest {
		return fmt.Errorf("verify isolated codex benchmark plugin")
	}
	pluginSkillRoot := filepath.Join(installedPath, "skills")
	if info, err := os.Lstat(pluginSkillRoot); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("verify isolated codex benchmark plugin")
	}
	listData, err := runCodexPluginCommand(ctx, agentBinary, environment, "plugin", "list", "--json")
	if err != nil {
		return err
	}
	var inventory struct {
		Installed []struct {
			PluginID        string `json:"pluginId"`
			Name            string `json:"name"`
			MarketplaceName string `json:"marketplaceName"`
			Version         string `json:"version"`
			Installed       bool   `json:"installed"`
			Enabled         bool   `json:"enabled"`
			Source          struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
		} `json:"installed"`
	}
	if json.Unmarshal(listData, &inventory) != nil || len(inventory.Installed) != 1 {
		return fmt.Errorf("verify isolated codex benchmark plugin")
	}
	entry := inventory.Installed[0]
	canonicalExpectedSource, expectedSourceErr := filepath.EvalSymlinks(sourcePackage)
	canonicalInventorySource, inventorySourceErr := filepath.EvalSymlinks(entry.Source.Path)
	if entry.PluginID != codexBenchmarkPluginID || entry.Name != manifest.Name || entry.MarketplaceName != codexBenchmarkPluginName || entry.Version != manifest.Version || !entry.Installed || !entry.Enabled || entry.Source.Source != "local" || expectedSourceErr != nil || inventorySourceErr != nil || canonicalInventorySource != canonicalExpectedSource {
		return fmt.Errorf("verify isolated codex benchmark plugin")
	}
	if err := disableCodexBenchmarkPluginMCP(ctx, agentBinary, environment, capsule); err != nil {
		return err
	}
	capsule.pluginSkillRoot = pluginSkillRoot
	return nil
}

type codexMCPInventoryEntry struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
}

func disableCodexBenchmarkPluginMCP(ctx context.Context, agentBinary string, environment []string, capsule *providerRuntimeCapsule) error {
	beforeData, err := runCodexPluginCommand(ctx, agentBinary, environment, "mcp", "list", "--json")
	if err != nil {
		return err
	}
	var before []codexMCPInventoryEntry
	if json.Unmarshal(beforeData, &before) != nil || len(before) > 32 {
		return fmt.Errorf("verify isolated codex benchmark plugin MCP policy")
	}
	seen := make(map[string]struct{}, len(before))
	names := make([]string, 0, len(before))
	for _, server := range before {
		if !mcpToolNameRE.MatchString(server.Name) || server.Enabled == nil {
			return fmt.Errorf("verify isolated codex benchmark plugin MCP policy")
		}
		if _, exists := seen[server.Name]; exists {
			return fmt.Errorf("verify isolated codex benchmark plugin MCP policy")
		}
		seen[server.Name] = struct{}{}
		names = append(names, server.Name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		var policy strings.Builder
		for _, name := range names {
			policy.WriteString("\n[plugins.\"atl@atl\".mcp_servers.")
			policy.WriteString(strconv.Quote(name))
			policy.WriteString("]\nenabled = false\n")
		}
		if err := appendCodexCapsuleConfig(capsule, []byte(policy.String())); err != nil {
			return err
		}
	}
	afterData, err := runCodexPluginCommand(ctx, agentBinary, environment, "mcp", "list", "--json")
	if err != nil {
		return err
	}
	var after []codexMCPInventoryEntry
	if json.Unmarshal(afterData, &after) != nil || len(after) != len(names) {
		return fmt.Errorf("verify isolated codex benchmark plugin MCP policy")
	}
	afterSeen := make(map[string]struct{}, len(after))
	for _, server := range after {
		_, expected := seen[server.Name]
		_, duplicate := afterSeen[server.Name]
		if !expected || duplicate || server.Enabled == nil || *server.Enabled {
			return fmt.Errorf("verify isolated codex benchmark plugin MCP policy")
		}
		afterSeen[server.Name] = struct{}{}
	}
	return nil
}

func appendCodexCapsuleConfig(capsule *providerRuntimeCapsule, data []byte) error {
	if capsule == nil || capsule.root == "" || len(data) == 0 || len(data) > 1<<20 {
		return fmt.Errorf("configure isolated codex benchmark plugin")
	}
	root, err := os.OpenRoot(capsule.root)
	if err != nil {
		return fmt.Errorf("configure isolated codex benchmark plugin")
	}
	defer func() { _ = root.Close() }()
	const relative = "codex-home/config.toml"
	before, err := root.Lstat(relative)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > 1<<20 {
		return fmt.Errorf("configure isolated codex benchmark plugin")
	}
	file, err := root.OpenFile(relative, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("configure isolated codex benchmark plugin")
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return fmt.Errorf("configure isolated codex benchmark plugin")
	}
	written, writeErr := file.Write(data)
	chmodErr := file.Chmod(0o600)
	syncErr := file.Sync()
	closeErr := file.Close()
	after, afterErr := root.Lstat(relative)
	if writeErr != nil || written != len(data) || chmodErr != nil || syncErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 || after.Size() != before.Size()+int64(len(data)) || after.Size() > 2<<20 {
		return fmt.Errorf("configure isolated codex benchmark plugin")
	}
	return nil
}

type cappedCommandOutput struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (w *cappedCommandOutput) Write(p []byte) (int, error) {
	original := len(p)
	remaining := w.limit - w.data.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.data.Write(p)
	}
	if original > remaining {
		w.overflow = true
	}
	return original, nil
}

func runCodexPluginCommand(ctx context.Context, binary string, environment []string, args ...string) ([]byte, error) {
	stdout := &cappedCommandOutput{limit: codexPluginCommandOutputMax}
	stderr := &cappedCommandOutput{limit: codexPluginCommandOutputMax}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = environment
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil || stdout.overflow || stderr.overflow {
		return nil, fmt.Errorf("provision isolated codex benchmark plugin")
	}
	return bytes.Clone(stdout.data.Bytes()), nil
}

func (c *providerRuntimeCapsule) Close() error {
	if c == nil || c.root == "" {
		return nil
	}
	var result error
	if c.authPending {
		updated, err := readCodexAuthFromCapsule(c.root)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("capture isolated codex provider authentication"))
		} else {
			if err := c.authSession.updateAuthentication(updated); err != nil {
				result = errors.Join(result, err)
			} else {
				c.authPending = false
			}
			clear(updated)
		}
	}
	if err := removePrivateTree(c.scratchRoot, c.root); err != nil && !os.IsNotExist(err) {
		return errors.Join(result, fmt.Errorf("clean isolated codex provider runtime"))
	}
	c.pluginSkillRoot = ""
	c.root = ""
	return result
}

func codexProviderConnectionEnvironment(ambient []string) map[string]string {
	all := environmentMap(ambient)
	allowed := []string{
		"LANG", "LC_ALL", "LC_CTYPE", "TERM", "COLORTERM", "TZ",
		"CODEX_CA_CERTIFICATE", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "no_proxy",
	}
	out := make(map[string]string, len(allowed))
	for _, name := range allowed {
		if value, ok := all[name]; ok {
			out[name] = value
		}
	}
	return out
}

func loadCodexAuthProjection(ambient []string) ([]byte, error) {
	values := environmentMap(ambient)
	root := values["CODEX_HOME"]
	if root == "" && values["HOME"] != "" {
		root = filepath.Join(values["HOME"], ".codex")
	}
	if root == "" {
		return nil, fmt.Errorf("codex provider authentication requires a safe file-backed auth.json")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !safeCodexAuthRootInfo(rootInfo) {
		return nil, fmt.Errorf("codex provider authentication is not a safe file-backed credential")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("codex provider authentication is not a safe file-backed credential")
	}
	defer func() { _ = rootHandle.Close() }()
	openedRoot, err := rootHandle.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedRoot) || !safeCodexAuthRootInfo(openedRoot) {
		return nil, fmt.Errorf("codex provider authentication is not a safe file-backed credential")
	}
	data, err := readSafeCodexAuthFromRoot(rootHandle)
	if err != nil {
		return nil, fmt.Errorf("codex provider authentication is not a safe file-backed credential")
	}
	finalRoot, rootErr := rootHandle.Stat(".")
	pathRoot, pathErr := os.Lstat(root)
	finalAuth, authErr := rootHandle.Lstat("auth.json")
	if rootErr != nil || pathErr != nil || authErr != nil || !os.SameFile(rootInfo, finalRoot) || !os.SameFile(rootInfo, pathRoot) || !safeCodexAuthRootInfo(finalRoot) || !safeCodexAuthRootInfo(pathRoot) || !finalAuth.Mode().IsRegular() || finalAuth.Mode()&os.ModeSymlink != 0 || finalAuth.Size() > codexAuthMaxBytes || (runtime.GOOS != "windows" && finalAuth.Mode().Perm()&0o077 != 0) {
		clear(data)
		return nil, fmt.Errorf("codex provider authentication is not a safe file-backed credential")
	}
	return data, nil
}

func safeCodexAuthRootInfo(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && (runtime.GOOS == "windows" || info.Mode().Perm()&0o022 == 0)
}

func validCodexAuthJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed)
}

func readCodexAuthFromCapsule(capsuleRoot string) ([]byte, error) {
	root, err := os.OpenRoot(capsuleRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat("codex-home")
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && before.Mode().Perm() != 0o700) {
		return nil, fmt.Errorf("unsafe credential directory")
	}
	parent, err := root.OpenRoot("codex-home")
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	opened, err := parent.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("unsafe credential directory")
	}
	return readSafeCodexAuthFromRoot(parent)
}

func readSafeCodexAuthFromRoot(root *os.Root) ([]byte, error) {
	before, err := root.Lstat("auth.json")
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > codexAuthMaxBytes || (runtime.GOOS != "windows" && before.Mode().Perm()&0o077 != 0) {
		return nil, fmt.Errorf("unsafe credential")
	}
	file, err := root.Open("auth.json")
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !stableCodexAuthInfo(before, opened) || (runtime.GOOS != "windows" && opened.Mode().Perm()&0o077 != 0) {
		_ = file.Close()
		return nil, fmt.Errorf("unsafe credential")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, codexAuthMaxBytes+1))
	postRead, postReadErr := file.Stat()
	_, seekErr := file.Seek(0, io.SeekStart)
	verification, verificationErr := io.ReadAll(io.LimitReader(file, codexAuthMaxBytes+1))
	postVerify, postVerifyErr := file.Stat()
	closeErr := file.Close()
	after, afterErr := root.Lstat("auth.json")
	stable := readErr == nil && postReadErr == nil && seekErr == nil && verificationErr == nil && postVerifyErr == nil && closeErr == nil && afterErr == nil &&
		len(data) <= codexAuthMaxBytes && len(verification) <= codexAuthMaxBytes && bytes.Equal(data, verification) &&
		stableCodexAuthInfo(opened, postRead) && stableCodexAuthInfo(opened, postVerify) && stableCodexAuthInfo(opened, after) && validCodexAuthJSON(data)
	clear(verification)
	if !stable {
		clear(data)
		return nil, fmt.Errorf("unsafe credential")
	}
	return data, nil
}

func stableCodexAuthInfo(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Size() == second.Size() && first.ModTime().Equal(second.ModTime()) && first.Mode() == second.Mode()
}
