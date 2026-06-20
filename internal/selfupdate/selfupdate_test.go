package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
	got, err := fetchSignedManifest(srv.URL, pub)
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if got.Version != "1.2.3" || len(got.Builds) != 1 {
		t.Fatalf("manifest not parsed: %+v", got)
	}

	// A tampered signature must be refused — no update may proceed.
	bad := signedManifestServer(t, priv, mf, true)
	defer bad.Close()
	if _, err := fetchSignedManifest(bad.URL, pub); err == nil {
		t.Error("tampered signature accepted")
	}

	// A signature made by a different key must be refused.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := fetchSignedManifest(srv.URL, otherPub); err == nil {
		t.Error("signature from wrong key accepted")
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
