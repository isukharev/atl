package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var errProbeRead = errors.New("probe read failed")

type selfUpdateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f selfUpdateRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type selfUpdateProbeBody struct {
	total     int
	failAfter int
	read      int
	closed    bool
}

func (b *selfUpdateProbeBody) Read(p []byte) (int, error) {
	if b.failAfter >= 0 && b.read >= b.failAfter {
		return 0, errProbeRead
	}
	remaining := b.total - b.read
	if remaining <= 0 {
		return 0, io.EOF
	}
	n := min(len(p), remaining)
	if b.failAfter >= 0 && b.read+n > b.failAfter {
		n = b.failAfter - b.read
	}
	for index := 0; index < n; index++ {
		p[index] = 'x'
	}
	b.read += n
	if b.failAfter >= 0 && b.read == b.failAfter {
		return n, errProbeRead
	}
	return n, nil
}

func (b *selfUpdateProbeBody) Close() error {
	b.closed = true
	return nil
}

func useSelfUpdateProbeClient(t *testing.T, body io.ReadCloser, contentLength int64) {
	t.Helper()
	previous := httpClient
	httpClient = &http.Client{Transport: selfUpdateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          body,
			ContentLength: contentLength,
			Request:       request,
		}, nil
	})}
	t.Cleanup(func() { httpClient = previous })
}

// rawManifestServer serves arbitrary manifest + signature bytes, so a test can
// feed malformed/truncated/missing payloads through the trust boundary.
func rawManifestServer(t *testing.T, manifest, sig []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(manifest) })
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sig) })
	return httptest.NewServer(mux)
}

// TestFetchSignedManifestRejections asserts the trust boundary fails closed for
// every flavor of bad input: wrong key, tampered body, malformed/missing/short
// signature, and an unparseable (but unsigned) manifest.
func TestFetchSignedManifestRejections(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mf := Manifest{Version: "1.2.3", Builds: []Build{{OS: "linux", Arch: "amd64", SHA256: "abc", Path: "atl-linux-amd64"}}}
	body, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	goodSig := ed25519.Sign(priv, body)

	t.Run("wrong key", func(t *testing.T) {
		srv := rawManifestServer(t, body, []byte(base64.StdEncoding.EncodeToString(goodSig)))
		defer srv.Close()
		otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
		got, err := fetchSignedManifest(context.Background(), srv.URL, otherPub)
		if err == nil {
			t.Error("manifest signed by a different key was accepted")
		}
		if got != nil {
			t.Error("a usable manifest was returned despite a wrong-key signature")
		}
	})

	t.Run("tampered body after signing", func(t *testing.T) {
		// Change a byte inside a string VALUE so the body stays VALID JSON: the
		// rejection must come from the signature check (the bytes differ from what
		// was signed), not from a parse failure — otherwise this subtest would pass
		// even if ed25519.Verify were removed, since json.Unmarshal would still
		// reject malformed JSON. Verifying over the exact bytes is the guarantee.
		tampered := bytes.Replace(body, []byte(`"1.2.3"`), []byte(`"9.9.9"`), 1)
		if bytes.Equal(tampered, body) {
			t.Fatal("tamper did not modify the body")
		}
		srv := rawManifestServer(t, tampered, []byte(base64.StdEncoding.EncodeToString(goodSig)))
		defer srv.Close()
		got, err := fetchSignedManifest(context.Background(), srv.URL, pub)
		if err == nil {
			t.Error("a body tampered after signing was accepted (signature not over exact bytes)")
		}
		if got != nil {
			t.Error("a usable manifest was returned despite a tampered body")
		}
	})

	t.Run("signature not valid base64", func(t *testing.T) {
		srv := rawManifestServer(t, body, []byte("@@@ not base64 @@@"))
		defer srv.Close()
		got, err := fetchSignedManifest(context.Background(), srv.URL, pub)
		if err == nil {
			t.Error("a non-base64 signature was accepted")
		}
		if got != nil {
			t.Error("a usable manifest was returned despite an undecodable signature")
		}
	})

	t.Run("short/truncated signature", func(t *testing.T) {
		short := base64.StdEncoding.EncodeToString(goodSig[:10]) // valid base64, wrong length
		srv := rawManifestServer(t, body, []byte(short))
		defer srv.Close()
		got, err := fetchSignedManifest(context.Background(), srv.URL, pub)
		if err == nil {
			t.Error("a truncated signature was accepted")
		}
		if got != nil {
			t.Error("a usable manifest was returned despite a truncated signature")
		}
	})

	t.Run("empty signature", func(t *testing.T) {
		srv := rawManifestServer(t, body, nil)
		defer srv.Close()
		got, err := fetchSignedManifest(context.Background(), srv.URL, pub)
		if err == nil {
			t.Error("an empty signature was accepted")
		}
		if got != nil {
			t.Error("a usable manifest was returned despite an empty signature")
		}
	})

	t.Run("malformed manifest with valid signature over it", func(t *testing.T) {
		// The bytes verify (signed) but are not valid JSON: parsing must fail
		// AFTER the signature check, so this still returns an error and no manifest.
		garbage := []byte("{not valid json")
		sig := ed25519.Sign(priv, garbage)
		srv := rawManifestServer(t, garbage, []byte(base64.StdEncoding.EncodeToString(sig)))
		defer srv.Close()
		got, err := fetchSignedManifest(context.Background(), srv.URL, pub)
		if err == nil {
			t.Error("a malformed (but signed) manifest was accepted")
		}
		if got != nil {
			t.Error("a usable manifest was returned despite unparseable JSON")
		}
	})
}

// TestFetchSignedManifestFetchError covers the download-failure branch (server
// returns non-200 for the manifest, so fetchSignedManifest returns that error).
func TestFetchSignedManifestFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if got, err := fetchSignedManifest(context.Background(), srv.URL, pub); err == nil || got != nil {
		t.Errorf("expected fetch error, got manifest=%v err=%v", got, err)
	}
}

func TestSecureBaseRejectsPlainHTTP(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/releases": true,
		"https://127.0.0.1":            true, // https loopback still fine
		"http://example.com/releases":  false,
		"http://10.0.0.5/releases":     false, // private but non-loopback http: refused
		"ftp://example.com":            false,
		"":                             false,
		"http://127.0.0.1:8080/dl":     true,
		"http://localhost":             true,
		"http://localhost/dl":          true,
		"http://localhost.evil.com":    false,
		"http://127.0.0.1.evil.com":    false,
		"http://127.0.0.1evil":         false, // char after prefix is not ':' or '/'
	}
	for in, want := range cases {
		if got := secureBase(in); got != want {
			t.Errorf("secureBase(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestDownloadNon200 covers download's status!=200 branch.
func TestDownloadNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := download(context.Background(), srv.URL+"/x", maxBinaryBytes, downloadTimeout); err == nil {
		t.Error("download of a 500 response should error")
	}
}

func TestDownloadBadURL(t *testing.T) {
	// A malformed URL fails at request construction / dial.
	if _, err := download(context.Background(), "http://%zz", maxBinaryBytes, downloadTimeout); err == nil {
		t.Error("download of an invalid URL should error")
	}
}

func TestDownloadSizeBoundary(t *testing.T) {
	const limit = int64(8)
	for _, test := range []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "N-1", size: int(limit - 1)},
		{name: "N", size: int(limit)},
		{name: "N+1", size: int(limit + 1), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), test.size)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			got, err := download(context.Background(), srv.URL, limit, downloadTimeout)
			if test.wantErr {
				if err == nil || got != nil {
					t.Fatalf("oversized download returned bytes=%d err=%v", len(got), err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("download bytes=%d err=%v, want %d bytes", len(got), err, len(body))
			}
		})
	}

	const privateBody = "PRIVATE-UPDATE-CANARY"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(privateBody))
	}))
	defer srv.Close()
	if data, err := download(context.Background(), srv.URL, 1, downloadTimeout); err == nil || data != nil {
		t.Fatalf("oversized private body returned bytes=%d err=%v", len(data), err)
	} else if bytes.Contains([]byte(err.Error()), []byte(privateBody)) {
		t.Fatalf("oversize error exposed response content: %v", err)
	}

	t.Run("reads only one sentinel and closes", func(t *testing.T) {
		body := &selfUpdateProbeBody{total: 1024, failAfter: -1}
		useSelfUpdateProbeClient(t, body, -1)
		data, err := download(context.Background(), "https://example.invalid/update", limit, downloadTimeout)
		if err == nil || data != nil || body.read != int(limit+1) || !body.closed {
			t.Fatalf("data=%d err=%v read=%d closed=%t", len(data), err, body.read, body.closed)
		}
	})

	t.Run("partial read error returns no bytes and closes", func(t *testing.T) {
		body := &selfUpdateProbeBody{total: 1024, failAfter: 3}
		useSelfUpdateProbeClient(t, body, -1)
		data, err := download(context.Background(), "https://example.invalid/update", limit, downloadTimeout)
		if !errors.Is(err, errProbeRead) || data != nil || body.read != body.failAfter || !body.closed {
			t.Fatalf("data=%d err=%v read=%d closed=%t", len(data), err, body.read, body.closed)
		}
	})

	t.Run("negative limit is rejected before transport", func(t *testing.T) {
		called := false
		previous := httpClient
		httpClient = &http.Client{Transport: selfUpdateRoundTripFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, nil
		})}
		t.Cleanup(func() { httpClient = previous })
		if data, err := download(context.Background(), "https://example.invalid/update", -1, downloadTimeout); err == nil || data != nil || called {
			t.Fatalf("data=%d err=%v transport called=%t", len(data), err, called)
		}
	})
}

func TestFetchSignedManifestRejectsOversizedInputs(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(Manifest{Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	exactManifest := append([]byte(nil), manifest...)
	exactManifest = append(exactManifest, bytes.Repeat([]byte(" "), maxManifestBytes-len(exactManifest))...)
	exactManifestSignature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, exactManifest)))
	exactSignature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest)))
	exactSignature = append(exactSignature, bytes.Repeat([]byte(" "), maxSigBytes-len(exactSignature))...)

	for _, test := range []struct {
		name      string
		manifest  []byte
		signature []byte
	}{
		{
			name:      "manifest",
			manifest:  append(exactManifest, 'x'),
			signature: exactManifestSignature,
		},
		{
			name:      "signature",
			manifest:  manifest,
			signature: append(exactSignature, 'x'),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := rawManifestServer(t, test.manifest, test.signature)
			defer srv.Close()
			got, err := fetchSignedManifest(context.Background(), srv.URL, pub)
			if err == nil || got != nil {
				t.Fatalf("oversized %s returned manifest=%v err=%v", test.name, got, err)
			}
		})
	}
}

func TestDownloadBinaryUsesBinaryResponseCap(t *testing.T) {
	body := &selfUpdateProbeBody{total: 0, failAfter: -1}
	useSelfUpdateProbeClient(t, body, maxBinaryBytes+1)
	data, err := downloadBinary(context.Background(), "https://example.invalid/atl")
	if err == nil || data != nil || body.read != 0 || !body.closed {
		t.Fatalf("data=%d err=%v read=%d closed=%t", len(data), err, body.read, body.closed)
	}
}

// TestChecksumMismatch is the download-integrity gate: even a 200 body whose
// sha256 disagrees with the manifest must be rejected by checksumOK.
func TestChecksumMismatch(t *testing.T) {
	data := []byte("the actual binary bytes")
	sum := sha256.Sum256(data)
	correct := hex.EncodeToString(sum[:])
	if !checksumOK(data, correct) {
		t.Error("the correct checksum was rejected")
	}
	if checksumOK(data, hex.EncodeToString(make([]byte, 32))) {
		t.Error("a mismatched checksum was accepted (integrity gate bypassed)")
	}
	if checksumOK([]byte("tampered binary bytes"), correct) {
		t.Error("tampered bytes passed against the original checksum")
	}
}

func TestReplaceFileSourceContentSwapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atl")
	if err := os.WriteFile(path, []byte("OLD-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o750); err != nil { // defeat umask, hardened install
		t.Fatal(err)
	}
	if err := replaceFile(path, []byte("NEW-binary")); err != nil {
		t.Fatalf("replaceFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW-binary" {
		t.Errorf("content = %q, want NEW-binary", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o750 {
		t.Errorf("mode = %o, want 0750 (hardened perms must be preserved)", fi.Mode().Perm())
	}
	// No leftover temp file.
	ents, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range ents {
		if e.Name() != "atl" {
			t.Errorf("unexpected leftover file after replaceFile: %q", e.Name())
		}
	}
}

// TestReplaceFileNewTargetDefaultsExecutable covers the path where the target
// does not yet exist (os.Stat fails) → default 0755 executable mode.
func TestReplaceFileNewTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atl-new")
	if err := replaceFile(path, []byte("FRESH")); err != nil {
		t.Fatalf("replaceFile (new target): %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("a freshly written binary must be executable, got mode %o", fi.Mode().Perm())
	}
}

// TestReplaceFileDirGone covers the CreateTemp-failure branch (the destination
// directory does not exist).
func TestReplaceFileDirGone(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "gone")
	path := filepath.Join(missingDir, "atl")
	if err := replaceFile(path, []byte("x")); err == nil {
		t.Error("replaceFile into a non-existent directory should error")
	}
}

func TestReplaceBinaryFromResolvesSymlinkLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-update releases support Linux and macOS")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "atl-real")
	launcher := filepath.Join(dir, "atl")
	if err := os.WriteFile(target, []byte("OLD"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(target), launcher); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinaryFrom(context.Background(), []byte("NEW"), func() (string, error) {
		return launcher, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "NEW" {
		t.Fatalf("real target content=%q err=%v, want NEW", got, err)
	}
	info, err := os.Lstat(launcher)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("launcher symlink was replaced instead of its real target")
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if targetInfo.Mode().Perm() != 0o700 {
		t.Fatalf("real target mode=%o, want 0700", targetInfo.Mode().Perm())
	}
}

func TestReplaceFileCommitsBySameDirectoryRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atl")
	if err := os.WriteFile(path, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	err = replaceFileWithRename(path, []byte("NEW"), func(temporary, destination string) error {
		if destination != path || filepath.Dir(temporary) != dir ||
			!strings.HasPrefix(filepath.Base(temporary), ".atl-update-") {
			t.Fatalf("rename(%q, %q) is not a same-directory atomic commit", temporary, destination)
		}
		committed = true
		return os.Rename(temporary, destination)
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !committed || os.SameFile(before, after) {
		t.Fatal("replacement did not atomically install a new file at the target path")
	}
}

func TestReplaceFileRenameFailurePreservesTargetAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atl")
	if err := os.WriteFile(path, []byte("ORIGINAL"), 0o750); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected rename failure")
	err := replaceFileWithRename(path, []byte("REPLACEMENT"), func(_, _ string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want injected rename failure", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ORIGINAL" {
		t.Fatalf("target content=%q after failed commit, want ORIGINAL", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("target mode=%o after failed commit, want 0750", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "atl" {
		t.Fatalf("directory entries after failed commit=%v, want only atl", entries)
	}
}

func TestParseSemverEdges(t *testing.T) {
	cases := []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"1.2.3", [3]int{1, 2, 3}, true},
		{"v1.2.3", [3]int{1, 2, 3}, true},
		{"1.2", [3]int{1, 2, 0}, true},          // short component list is allowed
		{"1", [3]int{1, 0, 0}, true},            // single component
		{"1.2.3-rc1", [3]int{1, 2, 3}, true},    // pre-release dropped
		{"1.2.3+build5", [3]int{1, 2, 3}, true}, // build metadata dropped
		{"  1.2.3  ", [3]int{1, 2, 3}, true},    // surrounding whitespace tolerated
		{"1.2.3.4", [3]int{}, false},            // too many components
		{"1.x.3", [3]int{}, false},              // non-numeric component
		{"", [3]int{}, false},                   // empty
		{"abc", [3]int{}, false},                // garbage
		// "-" is the pre-release separator: "1.-2.3" truncates to "1." → "1","" →
		// Atoi("") fails → not ok.
		{"1.-2.3", [3]int{}, false},
		{"1.2-rc.3", [3]int{1, 2, 0}, true}, // truncated at '-' to "1.2"
	}
	for _, c := range cases {
		got, ok := parseSemver(c.in)
		if ok != c.ok {
			t.Errorf("parseSemver(%q) ok = %v, want %v (got %v)", c.in, ok, c.ok, got)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseSemver(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSemverLessEdges(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// Differing-length numeric components compare as expected.
		{"1.2", "1.2.1", true},
		{"1.2.1", "1.2", false},
		{"1", "1.0.1", true},
		// Unparseable on either side → string-comparison fallback.
		{"garbage", "1.0.0", "garbage" < "1.0.0"},
		{"1.0.0", "also-garbage", "1.0.0" < "also-garbage"},
		{"foo", "bar", false}, // "foo" < "bar" is false
		{"bar", "foo", true},
		// Equal numeric versions are not "less".
		{"1.2.3", "1.2.3", false},
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestDebugfEmitsWhenEnabled covers debugf's enabled branch (it writes to
// stderr only when ATL_UPDATE_DEBUG is set; both halves are exercised here).
func TestDebugf(t *testing.T) {
	// Disabled: no panic, no-op.
	t.Setenv("ATL_UPDATE_DEBUG", "")
	debugf("hidden %d", 1)
	// Enabled: writes to stderr without error.
	t.Setenv("ATL_UPDATE_DEBUG", "1")
	debugf("visible %s", "msg")
}

// TestRunBestEffortNoOps exercises Run's early-return guards (which never touch
// the real executable): dev/empty version, opt-out env, and the non-HTTPS source
// rejection. Run is best-effort by contract and must never panic or error out.
func TestRunBestEffortNoOps(t *testing.T) {
	ctx := context.Background()
	// Dev/empty current version: returns immediately.
	Run(ctx, "https://example.com", "dev", t.TempDir())
	Run(ctx, "https://example.com", "", t.TempDir())
	Run(ctx, "", "1.0.0", t.TempDir())

	// Opt-out via env.
	t.Setenv("ATL_NO_UPDATE", "1")
	Run(ctx, "https://example.com", "1.0.0", t.TempDir())
	t.Setenv("ATL_NO_UPDATE", "")

	// Non-HTTPS source is refused before any check stamp is written.
	dir := t.TempDir()
	Run(ctx, "http://example.com/releases", "1.0.0", dir)
	if _, err := os.Stat(stampPath(dir)); !os.IsNotExist(err) {
		t.Error("a refused (non-HTTPS) source should not have written a check stamp")
	}
}

// TestRunSignedManifestNoUpgrade drives Run far enough to fetch and verify a
// real signed manifest against the EMBEDDED public key path is not possible
// (the private key is a CI secret), so this exercises the throttle + early
// no-op guards only and confirms Run records its check stamp when due. It never
// reaches replaceBinary because the source URL is loopback and the version is
// not newer; this keeps the real test executable untouched.
//
// Note: Run uses the compiled-in trustedPublicKey(), which we cannot sign for
// in a test, so fetchSignedManifest will reject our locally-signed manifest.
// That is exactly the fail-closed behavior; we assert Run still no-ops safely
// and stamps the check (throttle) without replacing any binary.
func TestRunStampsCheckAndFailsClosed(t *testing.T) {
	if _, ok := trustedPublicKey(); !ok {
		t.Skip("no embedded key; Run disabled")
	}

	// Serve a manifest signed with a NON-embedded key. Run's fetchSignedManifest
	// uses the embedded public key, so verification fails → Run no-ops. It still
	// records the check stamp (throttle) before the manifest fetch.
	_, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mf := Manifest{Version: "999.0.0", Builds: []Build{{OS: "x", Arch: "y", SHA256: "z", Path: "atl"}}}
	srv := signedManifestServer(t, signer, mf, false)
	defer srv.Close()

	t.Setenv("ATL_UPDATE_DEBUG", "1") // exercise Run's debugf("manifest: …") branch
	dir := t.TempDir()
	// Use the loopback URL (secureBase accepts http loopback) so Run proceeds past
	// the source check.
	Run(context.Background(), srv.URL, "1.0.0", dir)

	// Run must have stamped the check (throttle recorded before manifest fetch).
	if _, err := os.Stat(stampPath(dir)); err != nil {
		t.Errorf("Run did not record a check stamp: %v", err)
	}
	// It must NOT have recorded a new applied version (verification failed → no
	// upgrade applied), so the version stamp must be absent.
	if _, err := os.Stat(versionStampPath(dir)); !os.IsNotExist(err) {
		t.Error("Run recorded an applied version despite a manifest it could not verify (fail-closed violated)")
	}
}
