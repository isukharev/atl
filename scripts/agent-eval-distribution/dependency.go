package main

import (
	"errors"
	"path"
	"sort"
)

// dependencyIdentity is a content-addressed description of the selected
// evaluator module dependency closure. It intentionally contains only the
// canonical go.mod/go.sum bytes; source-tree and compatibility identities are
// bound independently on the distribution surfaces.
func dependencyIdentity(snapshot map[string][]byte) (string, error) {
	files := make([]map[string]string, 0, 2)
	seen := make(map[string]bool, 2)
	for name, data := range snapshot {
		base := path.Base(name)
		if base != "go.mod" && base != "go.sum" {
			continue
		}
		seen[base] = true
		files = append(files, map[string]string{"path": name, "sha256": sha256Bytes(data)})
	}
	if !seen["go.mod"] || !seen["go.sum"] || len(files) != 2 {
		return "", errors.New("selected source must include both go.mod and go.sum")
	}
	sort.Slice(files, func(i, j int) bool { return files[i]["path"] < files[j]["path"] })
	data, _ := canonicalJSON(map[string]any{
		"schema":         "agent-eval/dependency-identity",
		"schema_version": 1,
		"files":          files,
	})
	return sha256Bytes(data), nil
}
