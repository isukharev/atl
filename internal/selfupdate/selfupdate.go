// Package selfupdate implements opt-out, signature-verified, throttled CLI
// self-replacement from GitHub Releases. It is invoked on every command start
// but must NEVER block a command or surface an error to the user: every failure
// path is swallowed. Dev builds, an empty update URL, and a missing signing key
// disable it entirely (fail-closed).
//
// Trust model: the release manifest (manifest.json) is signed with an ed25519
// key held only as a CI secret; the matching public key is compiled in (see
// pubkey.go). The CLI verifies that signature BEFORE trusting the version or
// the per-binary sha256 it lists, so an attacker who swaps a release binary
// AND its hash still cannot forge an update without the private key.
package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Build describes one published binary.
type Build struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Path   string `json:"path"` // asset filename under the base URL, e.g. atl-linux-amd64
}

// Manifest is published as the release asset manifest.json and lists the latest
// build set. Its exact bytes are signed (ed25519) into manifest.json.sig; the
// CLI verifies that signature with the compiled-in public key before trusting
// any version or checksum it carries.
type Manifest struct {
	Version string  `json:"version"`
	Builds  []Build `json:"builds"`
}

const (
	checkInterval = 6 * time.Hour
	// The manifest check must be snappy so it never noticeably delays a command.
	// The binary download can take longer: once we've decided to update we'd
	// rather finish than abort a multi-megabyte transfer on a slow link.
	checkTimeout    = 4 * time.Second
	downloadTimeout = 90 * time.Second
)

// Run performs a best-effort self-update. baseURL is the release download base
// (e.g. https://github.com/<owner>/<repo>/releases/latest/download); current is
// the running version; configDir holds the throttle stamp. On a successful
// update it re-execs and does not return.
func Run(baseURL, current, configDir string) {
	if baseURL == "" || current == "" || current == "dev" {
		return // local/dev builds never auto-update
	}
	if os.Getenv("ATL_NO_UPDATE") != "" {
		return
	}
	// Fail-closed: without a compiled-in signing key we cannot verify any
	// update, so we never apply one. (Manual install via install.sh still works.)
	pub, ok := trustedPublicKey()
	if !ok {
		debugf("no trusted signing key embedded; auto-update disabled")
		return
	}
	// Supply-chain guard: a self-replacing binary must only fetch over TLS.
	// Plaintext is allowed solely against loopback for tests.
	if !secureBase(baseURL) {
		debugf("refusing non-HTTPS update source %q", baseURL)
		return
	}
	if !dueForCheck(configDir) {
		return
	}
	stampCheck(configDir) // record now, even if the rest fails, to throttle

	mf, err := fetchSignedManifest(baseURL, pub)
	if err != nil || mf.Version == "" {
		debugf("manifest: %v", err)
		return
	}
	if !semverLess(current, mf.Version) {
		return // already current or newer
	}
	build, ok := pickBuild(mf, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return
	}
	data, err := download(baseURL+"/"+strings.TrimPrefix(build.Path, "/"), downloadTimeout)
	if err != nil {
		debugf("download failed: %v", err)
		return
	}
	if !checksumOK(data, build.SHA256) {
		debugf("checksum mismatch for %s", build.Path)
		return
	}
	if err := replaceAndExec(data); err != nil {
		debugf("self-replace failed: %v", err)
		return
	}
}

// secureBase allows HTTPS anywhere, and HTTP only for genuine loopback (test
// servers). The host boundary check stops look-alikes like
// http://localhost.evil.com from slipping through a prefix match.
func secureBase(base string) bool {
	if strings.HasPrefix(base, "https://") {
		return true
	}
	for _, lo := range []string{"http://127.0.0.1", "http://localhost"} {
		if base == lo {
			return true
		}
		if strings.HasPrefix(base, lo) {
			switch base[len(lo)] { // next char must end the host (port or path)
			case ':', '/':
				return true
			}
		}
	}
	return false
}

func dueForCheck(configDir string) bool {
	info, err := os.Stat(stampPath(configDir))
	if err != nil {
		return true // never checked
	}
	return time.Since(info.ModTime()) > checkInterval
}

func stampCheck(configDir string) {
	_ = os.MkdirAll(configDir, 0o700)
	// 0600 for consistency with the rest of the config dir (config/credentials).
	_ = os.WriteFile(stampPath(configDir), []byte(time.Now().Format(time.RFC3339)), 0o600)
}

func stampPath(configDir string) string { return filepath.Join(configDir, ".update-check") }

// trustedPublicKey decodes the compiled-in ed25519 public key. ok is false when
// none is embedded (auto-update then stays disabled, fail-closed).
func trustedPublicKey() (ed25519.PublicKey, bool) {
	s := strings.TrimSpace(trustedPublicKeyB64)
	if s == "" {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, false
	}
	return ed25519.PublicKey(raw), true
}

// fetchSignedManifest downloads manifest.json and its detached signature
// (manifest.json.sig, base64 of the ed25519 signature over the exact manifest
// bytes), verifies the signature against pub, and only then parses it. Any
// failure — fetch, decode, or a signature that does not verify — returns an
// error and no update happens.
func fetchSignedManifest(baseURL string, pub ed25519.PublicKey) (*Manifest, error) {
	data, err := download(baseURL+"/manifest.json", checkTimeout)
	if err != nil {
		return nil, err
	}
	sigRaw, err := download(baseURL+"/manifest.json.sig", checkTimeout)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigRaw)))
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, data, sig) {
		return nil, fmt.Errorf("manifest signature verification failed")
	}
	var mf Manifest
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, err
	}
	return &mf, nil
}

func download(url string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func pickBuild(mf *Manifest, goos, goarch string) (Build, bool) {
	for _, b := range mf.Builds {
		if b.OS == goos && b.Arch == goarch {
			return b, true
		}
	}
	return Build{}, false
}

func checksumOK(data []byte, want string) bool {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == strings.ToLower(strings.TrimSpace(want))
}

// replaceAndExec writes the new binary over the current executable atomically
// and re-execs it with the original args. Replacing a running executable via
// rename is safe on Linux/macOS (the running image keeps the old inode).
func replaceAndExec(data []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".atl-update-*")
	if err != nil {
		return err // dir not writable → skip silently upstream
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return err
	}
	// Re-exec the freshly written binary. On success this replaces the process.
	return syscall.Exec(exe, os.Args, os.Environ())
}

// semverLess reports whether a < b for dotted numeric versions (pre-release
// suffixes ignored). Unparseable versions fall back to string comparison.
func semverLess(a, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return a < b
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	// drop pre-release / build metadata
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	var out [3]int
	if len(parts) == 0 || len(parts) > 3 {
		return out, false
	}
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func debugf(format string, a ...any) {
	if os.Getenv("ATL_UPDATE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[atl self-update] "+format+"\n", a...)
	}
}
