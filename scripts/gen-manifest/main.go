// Command gen-manifest builds the release manifest.json from the cross-compiled
// binaries in a dist directory. It emits the exact selfupdate.Manifest shape the
// CLI verifies, so the published manifest can never drift from the consumer.
//
//	go run ./scripts/gen-manifest --dist dist --version 1.2.3 > dist/manifest.json
//
// It expects binaries named atl-<os>-<arch> in the dist dir.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/selfupdate"
)

func main() {
	dist := flag.String("dist", "dist", "directory with atl-<os>-<arch> binaries")
	version := flag.String("version", "", "release version (required)")
	flag.Parse()
	if *version == "" {
		fail("--version is required")
	}

	entries, err := os.ReadDir(*dist)
	if err != nil {
		fail(err.Error())
	}
	mf := selfupdate.Manifest{Version: strings.TrimPrefix(strings.TrimSpace(*version), "v")}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "atl-") || strings.ContainsAny(name, ".") {
			continue // skip .sha256, manifest.json, VERSION, dirs
		}
		parts := strings.SplitN(strings.TrimPrefix(name, "atl-"), "-", 2)
		if len(parts) != 2 {
			fail("unexpected binary name: " + name)
		}
		sum, err := sha256File(filepath.Join(*dist, name))
		if err != nil {
			fail(err.Error())
		}
		mf.Builds = append(mf.Builds, selfupdate.Build{
			OS:     parts[0],
			Arch:   parts[1],
			SHA256: sum,
			Path:   name,
		})
	}
	if len(mf.Builds) == 0 {
		fail("no atl-<os>-<arch> binaries found in " + *dist)
	}
	sort.Slice(mf.Builds, func(i, j int) bool { return mf.Builds[i].Path < mf.Builds[j].Path })

	out, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		fail(err.Error())
	}
	fmt.Println(string(out))
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "gen-manifest:", msg)
	os.Exit(1)
}
