// Command genkey generates an ed25519 keypair for signing release manifests.
//
// Run it OUTSIDE CI (e.g. `make genkey`). It prints the public key to embed in
// internal/selfupdate/pubkey.go and writes the private key to a gitignored file.
// Store the private key as the ATL_RELEASE_PRIVATE_KEY GitHub Actions secret,
// then delete the local file. NEVER commit the private key.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

const privateKeyFile = "atl-release-key.b64" // gitignored

func main() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "genkey:", err)
		os.Exit(1)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	privB64 := base64.StdEncoding.EncodeToString(priv)

	// 0600: the private key must never be world-readable.
	if err := os.WriteFile(privateKeyFile, []byte(privB64+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "genkey: write private key:", err)
		os.Exit(1)
	}

	fmt.Printf(`ed25519 release signing key generated.

1. Embed this PUBLIC key in internal/selfupdate/pubkey.go (trustedPublicKeyB64)
   and commit it:

       const trustedPublicKeyB64 = %q

2. Add this PRIVATE key as the GitHub Actions secret ATL_RELEASE_PRIVATE_KEY
   (Settings -> Secrets and variables -> Actions). It was written to:

       %s

   Copy it into the secret, then DELETE the local file:

       gh secret set ATL_RELEASE_PRIVATE_KEY < %s
       rm %s

NEVER commit the private key.
`, pubB64, privateKeyFile, privateKeyFile, privateKeyFile)
}
