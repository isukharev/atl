package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func testPrivate(seedByte byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = seedByte
	}
	return ed25519.NewKeyFromSeed(seed)
}

func trustedSource(public ed25519.PublicKey) []byte {
	return []byte("package selfupdate\nconst trustedPublicKeyB64 = " +
		"\"" + base64.StdEncoding.EncodeToString(public) + "\"\n")
}

func TestCheckReleaseKeyAcceptsMatchingCanonicalKey(t *testing.T) {
	private := testPrivate(1)
	source := trustedSource(private.Public().(ed25519.PublicKey))
	err := checkReleaseKey(base64.StdEncoding.EncodeToString(private), source, source)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckReleaseKeyAcceptsValidRotatedCurrentKey(t *testing.T) {
	private := testPrivate(1)
	trusted := trustedSource(private.Public().(ed25519.PublicKey))
	current := trustedSource(testPrivate(2).Public().(ed25519.PublicKey))
	if err := checkReleaseKey(base64.StdEncoding.EncodeToString(private), trusted, current); err != nil {
		t.Fatal(err)
	}
}

func TestCheckReleaseKeyRejectsMismatch(t *testing.T) {
	private := testPrivate(1)
	other := testPrivate(2)
	err := checkReleaseKey(base64.StdEncoding.EncodeToString(private), trustedSource(other.Public().(ed25519.PublicKey)), trustedSource(private.Public().(ed25519.PublicKey)))
	if err == nil || !strings.Contains(err.Error(), "rotation bridge") {
		t.Fatalf("error=%v, want rotation-bridge mismatch", err)
	}
}

func TestCheckReleaseKeyRejectsNonCanonicalPrivateKey(t *testing.T) {
	private := testPrivate(1)
	private[len(private)-1] ^= 1
	source := trustedSource(testPrivate(1).Public().(ed25519.PublicKey))
	err := checkReleaseKey(base64.StdEncoding.EncodeToString(private), source, source)
	if err == nil || !strings.Contains(err.Error(), "not a canonical") {
		t.Fatalf("error=%v, want canonical-key refusal", err)
	}
}

func TestCheckReleaseKeyRejectsMalformedInputs(t *testing.T) {
	private := testPrivate(1)
	cases := []struct {
		name, private string
		source        []byte
	}{
		{name: "private base64", private: "not-base64", source: trustedSource(private.Public().(ed25519.PublicKey))},
		{name: "private size", private: base64.StdEncoding.EncodeToString([]byte("short")), source: trustedSource(private.Public().(ed25519.PublicKey))},
		{name: "source syntax", private: base64.StdEncoding.EncodeToString(private), source: []byte("package")},
		{name: "missing constant", private: base64.StdEncoding.EncodeToString(private), source: []byte("package selfupdate")},
		{name: "public size", private: base64.StdEncoding.EncodeToString(private), source: []byte("package selfupdate\nconst trustedPublicKeyB64 = \"c2hvcnQ=\"")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkReleaseKey(tc.private, tc.source, trustedSource(private.Public().(ed25519.PublicKey))); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}

func TestCheckReleaseKeyRejectsMalformedCurrentSource(t *testing.T) {
	private := testPrivate(1)
	trusted := trustedSource(private.Public().(ed25519.PublicKey))
	for name, current := range map[string][]byte{
		"syntax":  []byte("package"),
		"missing": []byte("package selfupdate"),
		"size":    []byte("package selfupdate\nconst trustedPublicKeyB64 = \"c2hvcnQ=\""),
	} {
		t.Run(name, func(t *testing.T) {
			err := checkReleaseKey(base64.StdEncoding.EncodeToString(private), trusted, current)
			if err == nil || !strings.Contains(err.Error(), "current client") && !strings.Contains(err.Error(), "current trustedPublicKeyB64") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
