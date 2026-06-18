// Command sign-manifest signs a release manifest with the ed25519 private key
// supplied via the ATL_RELEASE_PRIVATE_KEY environment variable (base64 std of
// the 64-byte ed25519 private key, as produced by `make genkey`). It writes a
// detached signature next to the manifest:
//
//	ATL_RELEASE_PRIVATE_KEY=… go run ./scripts/sign-manifest --manifest dist/manifest.json
//	  -> dist/manifest.json.sig  (base64 of the signature over the exact bytes)
//
// The CLI verifies this signature with its compiled-in public key before
// trusting the manifest. The private key must come ONLY from a CI secret.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	manifest := flag.String("manifest", "dist/manifest.json", "path to manifest.json to sign")
	flag.Parse()

	keyB64 := strings.TrimSpace(os.Getenv("ATL_RELEASE_PRIVATE_KEY"))
	if keyB64 == "" {
		fail("ATL_RELEASE_PRIVATE_KEY is not set")
	}
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		fail("decode ATL_RELEASE_PRIVATE_KEY: " + err.Error())
	}
	if len(raw) != ed25519.PrivateKeySize {
		fail(fmt.Sprintf("private key must be %d bytes, got %d", ed25519.PrivateKeySize, len(raw)))
	}
	priv := ed25519.PrivateKey(raw)

	body, err := os.ReadFile(*manifest)
	if err != nil {
		fail(err.Error())
	}
	sig := ed25519.Sign(priv, body)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	out := *manifest + ".sig"
	if err := os.WriteFile(out, []byte(sigB64+"\n"), 0o644); err != nil {
		fail(err.Error())
	}
	fmt.Fprintln(os.Stderr, "wrote", out)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "sign-manifest:", msg)
	os.Exit(1)
}
