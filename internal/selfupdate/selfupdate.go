// Package selfupdate implements opt-out, signature-verified, throttled CLI
// self-replacement from GitHub Releases. It is invoked on every command start
// but must NEVER block a command or surface an error to the user: every failure
// path is swallowed. Dev builds, an empty update URL, and a missing signing key
// disable it entirely (fail-closed).
//
// The update is applied by atomically swapping the on-disk binary; the running
// command keeps the already-loaded image and the new version takes effect on the
// next invocation. The check is cancellable via the caller's context, so Ctrl-C
// aborts an in-flight download instead of hanging.
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
	"crypto/tls"
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

	// Right-sized read caps: the manifest and its signature are a few KiB, so a
	// hostile source cannot force a large allocation before the signature check.
	maxManifestBytes = 1 << 20   // 1 MiB
	maxSigBytes      = 4 << 10   // 4 KiB
	maxBinaryBytes   = 200 << 20 // 200 MiB
)

// httpClient is dedicated to self-update so a global http.DefaultTransport
// mutation elsewhere cannot affect the most security-sensitive fetch. It pins a
// minimum TLS version and refuses any redirect hop that is not itself secure
// (e.g. an https→http downgrade), independent of the signature check.
var httpClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if !secureBase(req.URL.String()) {
			return fmt.Errorf("refusing insecure redirect to %s", req.URL.Redacted())
		}
		return nil
	},
}

// Run performs a best-effort self-update. baseURL is the release download base
// (e.g. https://github.com/<owner>/<repo>/releases/latest/download); current is
// the running version; configDir holds the throttle and version stamps. The
// context lets the caller cancel (Ctrl-C) an in-flight check/download. On a
// successful update the new binary is swapped in for the NEXT invocation; the
// current process is never re-execed.
func Run(ctx context.Context, baseURL, current, configDir string) {
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

	mf, err := fetchSignedManifest(ctx, baseURL, pub)
	if err != nil || mf.Version == "" {
		debugf("manifest: %v", err)
		return
	}
	// Anti-rollback: never apply a version at or below the highest one we have
	// ever run, so a replayed (validly-signed but superseded) manifest cannot
	// force a downgrade to a known-bad release.
	if !semverLess(highWaterVersion(configDir, current), mf.Version) {
		return // already current/newer, or a replay of an older signed manifest
	}
	build, ok := pickBuild(mf, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return
	}
	data, err := download(ctx, baseURL+"/"+strings.TrimPrefix(build.Path, "/"), maxBinaryBytes, downloadTimeout)
	if err != nil {
		debugf("download failed: %v", err)
		return
	}
	if !checksumOK(data, build.SHA256) {
		debugf("checksum mismatch for %s", build.Path)
		return
	}
	if err := replaceBinary(data); err != nil {
		debugf("self-replace failed: %v", err)
		return
	}
	recordVersion(configDir, mf.Version)
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
		// Only a genuinely-absent stamp means "never checked". Any other stat
		// error (e.g. an unreadable config dir) means we cannot throttle reliably,
		// so we skip rather than re-checking on every single command.
		return os.IsNotExist(err)
	}
	return time.Since(info.ModTime()) > checkInterval
}

func stampCheck(configDir string) {
	_ = os.MkdirAll(configDir, 0o700)
	// 0600 for consistency with the rest of the config dir (config/credentials).
	_ = os.WriteFile(stampPath(configDir), []byte(time.Now().Format(time.RFC3339)), 0o600)
}

func stampPath(configDir string) string { return filepath.Join(configDir, ".update-check") }

func versionStampPath(configDir string) string { return filepath.Join(configDir, ".update-version") }

// highWaterVersion returns the higher of the running version and the highest
// version ever applied (persisted across runs), so anti-rollback survives a
// downgrade of the on-disk binary by some other means.
func highWaterVersion(configDir, current string) string {
	hw := current
	if b, err := os.ReadFile(versionStampPath(configDir)); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			// Ignore a non-semver stamp (corruption or a foreign write). Otherwise a
			// garbage value that string-sorts above the manifest would, via
			// semverLess's string fallback, permanently block every future update.
			if _, ok := parseSemver(v); ok && semverLess(hw, v) {
				hw = v
			}
		}
	}
	return hw
}

func recordVersion(configDir, v string) {
	_ = os.MkdirAll(configDir, 0o700)
	_ = os.WriteFile(versionStampPath(configDir), []byte(v), 0o600)
}

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
// bytes) concurrently, verifies the signature against pub, and only then parses
// it. Any failure — fetch, decode, or a signature that does not verify —
// returns an error and no update happens.
func fetchSignedManifest(ctx context.Context, baseURL string, pub ed25519.PublicKey) (*Manifest, error) {
	type result struct {
		data []byte
		err  error
	}
	dataCh := make(chan result, 1)
	sigCh := make(chan result, 1)
	go func() {
		d, e := download(ctx, baseURL+"/manifest.json", maxManifestBytes, checkTimeout)
		dataCh <- result{d, e}
	}()
	go func() {
		d, e := download(ctx, baseURL+"/manifest.json.sig", maxSigBytes, checkTimeout)
		sigCh <- result{d, e}
	}()
	dr, sr := <-dataCh, <-sigCh
	if dr.err != nil {
		return nil, dr.err
	}
	if sr.err != nil {
		return nil, sr.err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sr.data)))
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, dr.data, sig) {
		return nil, fmt.Errorf("manifest signature verification failed")
	}
	var mf Manifest
	if err := json.Unmarshal(dr.data, &mf); err != nil {
		return nil, err
	}
	return &mf, nil
}

func download(ctx context.Context, url string, maxBytes int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
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

// replaceBinary writes the new binary over the current executable atomically,
// preserving the original file's permission bits (so a hardened 0700/0750
// install is not silently widened to 0755) while guaranteeing it stays
// executable. Replacing a running executable via rename is safe on Linux/macOS
// (the running image keeps the old inode); the new bytes take effect on the
// next invocation.
func replaceBinary(data []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Resolve a symlinked launcher (e.g. ~/.local/bin/atl → …/atl) and replace the
	// real target so the swap takes effect however atl was invoked. Note this
	// overwrites the file behind a symlinked/versioned install rather than the
	// link itself.
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	return replaceFile(exe, data)
}

// replaceFile atomically swaps the file at path for data, preserving the
// original permission bits (a hardened 0700/0750 install is not silently
// widened to 0755) while guaranteeing the result stays executable. Replacing a
// running executable via rename is safe on Linux/macOS (the running image keeps
// the old inode); the new bytes take effect on the next invocation.
func replaceFile(path string, data []byte) error {
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		if mode&0o111 == 0 { // never produce a non-executable binary
			mode |= 0o755
		}
	}
	dir := filepath.Dir(path)
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
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
