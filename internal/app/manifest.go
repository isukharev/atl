package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

// ManifestOpts controls a local mirror/snapshot manifest.
type ManifestOpts struct {
	Root        string
	Out         string
	Command     string
	Service     string
	Selectors   []string
	Fields      []string
	Include     []string
	Version     string
	BackendURLs map[string]string
}

// ManifestResult describes the written manifest.
type ManifestResult struct {
	Path     string         `json:"path"`
	Manifest MirrorManifest `json:"manifest"`
}

// MirrorManifest is a backend-identity-hashed local snapshot manifest. Caller
// metadata and paths may still be sensitive and are intentionally retained.
type MirrorManifest struct {
	CreatedAt  string            `json:"created_at"`
	Command    string            `json:"command"`
	Root       string            `json:"root"`
	Service    string            `json:"service,omitempty"`
	Selectors  []string          `json:"selectors,omitempty"`
	Fields     []string          `json:"fields,omitempty"`
	Include    []string          `json:"include,omitempty"`
	Counts     ManifestCounts    `json:"counts"`
	Backend    []ManifestBackend `json:"backend,omitempty"`
	ATLVersion string            `json:"atl_version,omitempty"`
	ElapsedMS  int64             `json:"elapsed_ms"`
}

type ManifestCounts struct {
	Files      int            `json:"files"`
	Bytes      int64          `json:"bytes"`
	Extensions map[string]int `json:"extensions,omitempty"`
}

type ManifestBackend struct {
	Service string `json:"service"`
	URLHash string `json:"url_hash"`
}

// CreateManifest writes a backend-identity-hashed local mirror/snapshot manifest.
func CreateManifest(opts ManifestOpts) (*ManifestResult, error) {
	start := time.Now()
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, fmt.Errorf("%w: --root is required", domain.ErrUsage)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: --root must be a directory", domain.ErrUsage)
	}
	out := strings.TrimSpace(opts.Out)
	if out == "" {
		out = filepath.Join(root, "manifest.json")
	}
	counts, err := manifestCounts(root, out)
	if err != nil {
		return nil, err
	}
	m := MirrorManifest{
		CreatedAt:  start.UTC().Format(time.RFC3339),
		Command:    strings.TrimSpace(opts.Command),
		Root:       root,
		Service:    strings.TrimSpace(opts.Service),
		Selectors:  cleanList(opts.Selectors),
		Fields:     cleanList(opts.Fields),
		Include:    cleanList(opts.Include),
		Counts:     counts,
		Backend:    manifestBackend(opts.Service, opts.BackendURLs),
		ATLVersion: opts.Version,
	}
	m.ElapsedMS = time.Since(start).Milliseconds()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeUserFile(out, append(data, '\n')); err != nil {
		return nil, err
	}
	return &ManifestResult{Path: out, Manifest: m}, nil
}

func manifestCounts(root, out string) (ManifestCounts, error) {
	counts := ManifestCounts{Extensions: map[string]int{}}
	cleanOut, _ := filepath.Abs(out)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		abs, _ := filepath.Abs(path)
		if abs == cleanOut {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		counts.Files++
		counts.Bytes += info.Size()
		ext := strings.ToLower(filepath.Ext(path))
		if ext == "" {
			ext = "(none)"
		}
		counts.Extensions[ext]++
		return nil
	})
	if err != nil {
		return counts, err
	}
	return counts, nil
}

func manifestBackend(service string, urls map[string]string) []ManifestBackend {
	var services []string
	if strings.TrimSpace(service) != "" {
		services = []string{strings.TrimSpace(service)}
	} else {
		for svc := range urls {
			services = append(services, svc)
		}
		sort.Strings(services)
	}
	var out []ManifestBackend
	for _, svc := range services {
		url := strings.TrimRight(strings.TrimSpace(urls[svc]), "/")
		if url == "" {
			continue
		}
		sum := sha256.Sum256([]byte(url))
		out = append(out, ManifestBackend{Service: svc, URLHash: "sha256:" + hex.EncodeToString(sum[:])})
	}
	return out
}
