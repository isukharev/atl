package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// signedManifestServer serves a manifest.json signed with priv, plus its
// detached signature, mimicking the release layout.
func signedManifestServer(t *testing.T, priv ed25519.PrivateKey, mf Manifest, tamper bool) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, body)
	if tamper {
		sig[0] ^= 0xFF // flip a bit so verification must fail
	}
	sigB64 := base64.StdEncoding.EncodeToString(sig)
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sigB64)) })
	return httptest.NewServer(mux)
}

func TestFetchSignedManifest(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mf := Manifest{Version: "1.2.3", Builds: []Build{{OS: "linux", Arch: "amd64", SHA256: "abc", Path: "atl-linux-amd64"}}}

	// A correctly signed manifest verifies and parses.
	srv := signedManifestServer(t, priv, mf, false)
	defer srv.Close()
	got, err := fetchSignedManifest(context.Background(), srv.URL, pub)
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if got.Version != "1.2.3" || len(got.Builds) != 1 {
		t.Fatalf("manifest not parsed: %+v", got)
	}

	// A tampered signature must be refused — no update may proceed.
	bad := signedManifestServer(t, priv, mf, true)
	defer bad.Close()
	if _, err := fetchSignedManifest(context.Background(), bad.URL, pub); err == nil {
		t.Error("tampered signature accepted")
	}

	// A signature made by a different key must be refused.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := fetchSignedManifest(context.Background(), srv.URL, otherPub); err == nil {
		t.Error("signature from wrong key accepted")
	}
}

func TestHighWaterVersionBlocksRollback(t *testing.T) {
	dir := t.TempDir()
	// No stamp yet: high-water is just the running version.
	if hw := highWaterVersion(dir, "1.0.0"); hw != "1.0.0" {
		t.Fatalf("highWaterVersion = %q, want 1.0.0", hw)
	}
	// After applying 1.5.0, a replayed 1.2.0 manifest must not be considered newer.
	recordVersion(dir, "1.5.0")
	hw := highWaterVersion(dir, "1.0.0")
	if hw != "1.5.0" {
		t.Fatalf("highWaterVersion = %q, want 1.5.0", hw)
	}
	if semverLess(hw, "1.2.0") {
		t.Error("replayed older signed manifest (1.2.0) must not pass the rollback gate")
	}
	if !semverLess(hw, "1.6.0") {
		t.Error("a genuinely newer version (1.6.0) should still pass")
	}
}

func TestDueForCheck(t *testing.T) {
	dir := t.TempDir()
	if !dueForCheck(dir) {
		t.Error("a never-checked dir should be due")
	}
	stampCheck(dir)
	if dueForCheck(dir) {
		t.Error("a freshly stamped dir should not be due")
	}
	// Once the cooldown has elapsed the dir is due again (the throttle expires).
	old := time.Now().Add(-checkInterval - time.Hour)
	if err := os.Chtimes(stampPath(dir), old, old); err != nil {
		t.Fatal(err)
	}
	if !dueForCheck(dir) {
		t.Error("a stamp older than the cooldown should be due")
	}
}

func TestHighWaterIgnoresCorruptStamp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(versionStampPath(dir), []byte("not-a-version"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A garbage stamp must be ignored, not adopted as the high-water mark — else
	// it would brick every future update via semverLess's string fallback.
	if hw := highWaterVersion(dir, "1.0.0"); hw != "1.0.0" {
		t.Fatalf("highWaterVersion with corrupt stamp = %q, want 1.0.0", hw)
	}
	// A valid higher stamp is still honored.
	recordVersion(dir, "2.0.0")
	if hw := highWaterVersion(dir, "1.0.0"); hw != "2.0.0" {
		t.Fatalf("highWaterVersion = %q, want 2.0.0", hw)
	}
}

func TestReplaceFilePreservesMode(t *testing.T) {
	cases := []struct {
		name        string
		start, want os.FileMode
	}{
		{"hardened install stays 0700", 0o700, 0o700},
		{"non-executable gets +x", 0o644, 0o755},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "atl")
			if err := os.WriteFile(path, []byte("OLD"), tc.start); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.start); err != nil { // defeat umask
				t.Fatal(err)
			}
			if err := replaceFile(path, []byte("NEW")); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "NEW" {
				t.Fatalf("content = %q err=%v, want NEW", got, err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != tc.want {
				t.Errorf("mode = %o, want %o", fi.Mode().Perm(), tc.want)
			}
		})
	}
}

func TestTrustedPublicKeyEmbedded(t *testing.T) {
	// A release signing key is embedded; verify it decodes to a valid ed25519
	// public key (guards against a typo in the committed base64).
	pub, ok := trustedPublicKey()
	if !ok {
		t.Fatal("expected an embedded signing key")
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("embedded key has wrong size %d, want %d", len(pub), ed25519.PublicKeySize)
	}
}

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.2.0", "1.10.0", true},
		{"2.0.0", "1.9.9", false},
		{"1.0.0", "1.0.0", false},
		{"v1.0.0", "1.0.1", true},
		{"1.0.0-rc1", "1.0.0", false}, // pre-release suffix ignored → equal
		{"0.0.1", "0.0.2", true},
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestChecksumOK(t *testing.T) {
	data := []byte("hello")
	// sha256("hello")
	const sum = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if !checksumOK(data, sum) {
		t.Error("valid checksum rejected")
	}
	if checksumOK(data, "deadbeef") {
		t.Error("invalid checksum accepted")
	}
	if !checksumOK(data, "  "+sum+"  ") { // tolerate whitespace
		t.Error("whitespace-padded checksum rejected")
	}
}

func TestSecureBase(t *testing.T) {
	if !secureBase("https://atl.example.com") {
		t.Error("https should be allowed")
	}
	if secureBase("http://atl.example.com") {
		t.Error("plaintext non-loopback must be refused")
	}
	if !secureBase("http://127.0.0.1:8080") {
		t.Error("loopback http allowed for tests")
	}
	if !secureBase("http://localhost:18090") {
		t.Error("loopback localhost allowed for tests")
	}
	// Look-alike host must NOT pass the loopback prefix check.
	if secureBase("http://localhost.evil.com") {
		t.Error("localhost.evil.com must be refused")
	}
	if secureBase("http://127.0.0.1.evil.com") {
		t.Error("127.0.0.1.evil.com must be refused")
	}
}

func TestPickBuild(t *testing.T) {
	mf := &Manifest{Builds: []Build{
		{OS: "linux", Arch: "amd64", Path: "dl/linux/amd64/atl"},
		{OS: "darwin", Arch: "arm64", Path: "dl/darwin/arm64/atl"},
	}}
	b, ok := pickBuild(mf, "darwin", "arm64")
	if !ok || b.Path != "dl/darwin/arm64/atl" {
		t.Errorf("pickBuild = %+v ok=%v", b, ok)
	}
	if _, ok := pickBuild(mf, "windows", "amd64"); ok {
		t.Error("unexpected windows build")
	}
}

func TestRunWithBudgetStalledManifest(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requestStarted := make(chan struct{}, 2)
	requestCanceled := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requestStarted <- struct{}{}
		<-r.Context().Done()
		requestCanceled <- struct{}{}
	}))
	defer srv.Close()

	replaced := make(chan struct{}, 1)
	returned := make(chan struct{})
	dir := t.TempDir()
	go func() {
		runWithBudget(
			context.Background(), srv.URL, "1.0.0", dir,
			time.Second, pub,
			func(context.Context, []byte) error {
				replaced <- struct{}{}
				return nil
			},
		)
		close(returned)
	}()

	waitForSignal(t, requestStarted, time.Second, "manifest request to start")
	waitForSignal(t, returned, 3*time.Second, "stalled manifest update to respect its startup budget")
	waitForSignal(t, requestCanceled, time.Second, "stalled manifest request to be canceled")
	select {
	case <-replaced:
		t.Fatal("replacer called after the manifest request exhausted the budget")
	default:
	}
}

func TestRunWithBudgetStalledBinary(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte("new atl binary")
	sum := sha256.Sum256(binary)
	mf := Manifest{
		Version: "2.0.0",
		Builds: []Build{{
			OS: runtime.GOOS, Arch: runtime.GOARCH,
			SHA256: hex.EncodeToString(sum[:]), Path: "atl",
		}},
	}
	manifest, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
	binaryStarted := make(chan struct{}, 1)
	binaryCanceled := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(signature))
	})
	mux.HandleFunc("/atl", func(_ http.ResponseWriter, r *http.Request) {
		binaryStarted <- struct{}{}
		<-r.Context().Done()
		binaryCanceled <- struct{}{}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	replaced := make(chan struct{}, 1)
	returned := make(chan struct{})
	dir := t.TempDir()
	go func() {
		runWithBudget(
			context.Background(), srv.URL, "1.0.0", dir,
			time.Second, pub,
			func(context.Context, []byte) error {
				replaced <- struct{}{}
				return nil
			},
		)
		close(returned)
	}()

	waitForSignal(t, binaryStarted, time.Second, "binary request to start")
	waitForSignal(t, returned, 3*time.Second, "stalled binary update to respect the shared startup budget")
	waitForSignal(t, binaryCanceled, time.Second, "stalled binary request to be canceled")
	select {
	case <-replaced:
		t.Fatal("replacer called after the binary request exhausted the budget")
	default:
	}
	if _, err := os.Stat(versionStampPath(dir)); !os.IsNotExist(err) {
		t.Fatal("version recorded after the binary request exhausted the budget")
	}
}

func TestRunWithBudgetPreservesShorterCallerDeadline(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requestStarted := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requestStarted <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	returned := make(chan struct{})
	dir := t.TempDir()
	go func() {
		runWithBudget(
			ctx, srv.URL, "1.0.0", dir, 2*time.Second, pub,
			func(context.Context, []byte) error {
				t.Error("replacer called after caller deadline")
				return nil
			},
		)
		close(returned)
	}()

	waitForSignal(t, requestStarted, time.Second, "manifest request to start")
	waitForSignal(t, returned, 3*time.Second, "update to preserve the shorter caller deadline")
}

func TestRunWithBudgetSuccessfulUpdate(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte("verified replacement bytes")
	sum := sha256.Sum256(binary)
	mf := Manifest{
		Version: "2.0.0",
		Builds: []Build{{
			OS: runtime.GOOS, Arch: runtime.GOARCH,
			SHA256: hex.EncodeToString(sum[:]), Path: "atl",
		}},
	}
	manifest, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(signature))
	})
	mux.HandleFunc("/atl", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(binary)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	replaced := make(chan []byte, 1)
	dir := t.TempDir()
	runWithBudget(
		context.Background(), srv.URL, "1.0.0", dir, time.Second, pub,
		func(ctx context.Context, data []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			replaced <- append([]byte(nil), data...)
			return nil
		},
	)

	select {
	case got := <-replaced:
		if string(got) != string(binary) {
			t.Fatalf("replacement bytes = %q, want %q", got, binary)
		}
	default:
		t.Fatal("valid update did not invoke the safe test replacer")
	}
	gotVersion, err := os.ReadFile(versionStampPath(dir))
	if err != nil {
		t.Fatalf("read applied version stamp: %v", err)
	}
	if string(gotVersion) != mf.Version {
		t.Fatalf("applied version stamp = %q, want %q", gotVersion, mf.Version)
	}

	// Once the verified local commit is admitted before the deadline, it must
	// finish coherently with the anti-rollback stamp even if the deadline
	// expires during that commit.
	lateDir := t.TempDir()
	admitted := false
	runWithBudget(
		context.Background(), srv.URL, "1.0.0", lateDir, time.Second, pub,
		func(ctx context.Context, data []byte) error {
			admitted = true
			<-ctx.Done()
			if string(data) != string(binary) {
				t.Fatalf("late replacement bytes = %q, want %q", data, binary)
			}
			return nil
		},
	)
	if !admitted {
		t.Fatal("verified local commit was not admitted before the deadline")
	}
	lateVersion, err := os.ReadFile(versionStampPath(lateDir))
	if err != nil {
		t.Fatalf("read late applied version stamp: %v", err)
	}
	if string(lateVersion) != mf.Version {
		t.Fatalf("late applied version stamp = %q, want %q", lateVersion, mf.Version)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}
