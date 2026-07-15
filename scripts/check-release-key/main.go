// Command check-release-key verifies that the release private key is the key
// trusted by an already-published atl client. It reads private material only
// from ATL_RELEASE_PRIVATE_KEY and never prints it.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
)

const trustedKeyConstant = "trustedPublicKeyB64"

func main() {
	trustedSource := flag.String("trusted-source", "", "pubkey.go from the highest published stable release")
	currentSource := flag.String("current-source", "", "pubkey.go from the release being built")
	allowTrustReset := flag.Bool("allow-trust-reset", false, "intentionally start a new trust chain by matching the current source key")
	flag.Parse()
	if strings.TrimSpace(*trustedSource) == "" {
		fail("--trusted-source is required")
	}
	if strings.TrimSpace(*currentSource) == "" {
		fail("--current-source is required")
	}
	source, err := os.ReadFile(*trustedSource)
	if err != nil {
		fail("read trusted client source: " + err.Error())
	}
	current, err := os.ReadFile(*currentSource)
	if err != nil {
		fail("read current client source: " + err.Error())
	}
	if err := checkReleaseKey(os.Getenv("ATL_RELEASE_PRIVATE_KEY"), source, current, *allowTrustReset); err != nil {
		fail(err.Error())
	}
	if *allowTrustReset {
		fmt.Fprintln(os.Stderr, "release signing key matches the current source key; explicit trust reset accepted")
		return
	}
	fmt.Fprintln(os.Stderr, "release signing key matches the highest published stable client's trust key")
}

func checkReleaseKey(privateB64 string, trustedSource, currentSource []byte, allowTrustReset bool) error {
	privateRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privateB64))
	if err != nil {
		return fmt.Errorf("decode ATL_RELEASE_PRIVATE_KEY: %w", err)
	}
	if len(privateRaw) != ed25519.PrivateKeySize {
		return fmt.Errorf("ATL_RELEASE_PRIVATE_KEY must decode to %d bytes, got %d", ed25519.PrivateKeySize, len(privateRaw))
	}
	canonical := ed25519.NewKeyFromSeed(privateRaw[:ed25519.SeedSize])
	if !bytes.Equal(privateRaw, canonical) {
		return fmt.Errorf("ATL_RELEASE_PRIVATE_KEY is not a canonical Ed25519 private key")
	}

	trustedRaw, err := publicKeyFromSource("published", trustedSource)
	if err != nil {
		return err
	}
	currentRaw, err := publicKeyFromSource("current", currentSource)
	if err != nil {
		return err
	}
	derived := canonical[ed25519.SeedSize:]
	if allowTrustReset {
		if subtle.ConstantTimeCompare(trustedRaw, currentRaw) == 1 {
			return fmt.Errorf("trust reset is unnecessary because published and current clients trust the same key")
		}
		if subtle.ConstantTimeCompare(derived, currentRaw) != 1 {
			return fmt.Errorf("trust-reset signing key does not match the current source trust key")
		}
		return nil
	}
	if subtle.ConstantTimeCompare(derived, trustedRaw) != 1 {
		return fmt.Errorf("release signing key does not match the highest published stable client's trust key; configure the trusted key (a rotation bridge must still be signed by the old key)")
	}
	return nil
}

func publicKeyFromSource(label string, source []byte) ([]byte, error) {
	encoded, err := trustedPublicKeyFromSource(source)
	if err != nil {
		return nil, fmt.Errorf("%s client: %w", label, err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode %s %s: %w", label, trustedKeyConstant, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%s %s must decode to %d bytes, got %d", label, trustedKeyConstant, ed25519.PublicKeySize, len(raw))
	}
	return raw, nil
}

func trustedPublicKeyFromSource(source []byte) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "pubkey.go", source, 0)
	if err != nil {
		return "", fmt.Errorf("parse published trusted-key source: %w", err)
	}
	var value string
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, name := range decl.Names {
			if name.Name != trustedKeyConstant || index >= len(decl.Values) {
				continue
			}
			literal, ok := decl.Values[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			decoded, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr == nil {
				value = decoded
			}
		}
		return true
	})
	if value == "" {
		return "", fmt.Errorf("published trusted-key source does not define non-empty string constant %s", trustedKeyConstant)
	}
	return value, nil
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "check-release-key:", message)
	os.Exit(1)
}
