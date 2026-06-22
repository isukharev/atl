// Package auth resolves per-user Personal Access Tokens. Tokens never live in
// the repo or mirror. Resolution order: explicit env, then a 0600 credentials
// file in the config dir. The file store is abstracted so an OS-keychain
// backend can replace it later without touching callers.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/safepath"
)

// ErrNoToken marks the specific "no PAT is configured" condition (nothing in the
// env and nothing in the credentials file) — as opposed to an unreadable or
// corrupt credentials file, which is a genuine error. Callers translate this to
// the "not configured" exit code; a read/parse failure stays a generic error.
var ErrNoToken = errors.New("no PAT configured")

// Service identifies which backend a token is for.
type Service string

const (
	Confluence Service = "confluence"
	Jira       Service = "jira"
)

// envKeys lists, in priority order, the canonical env vars consulted for each
// service. ATL_* are preferred; CONFLUENCE_PAT/JIRA_PAT are common aliases.
var envKeys = map[Service][]string{
	Confluence: {"ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT"},
	Jira:       {"ATL_JIRA_PAT", "JIRA_PAT"},
}

// testEnvKeys are the integration-only fallbacks matching the repo's .env.local.
// They are consulted ONLY when ATL_INTEGRATION is set, so a stray TEST_*_PAT in
// a CI/dev shell can never be silently used for real operations.
var testEnvKeys = map[Service]string{
	Confluence: "TEST_CONFLUENCE_PAT",
	Jira:       "TEST_JIRA_PAT",
}

// envKeysFor returns the env vars to consult for a service, appending the
// integration-only TEST_*_PAT fallback when ATL_INTEGRATION is set.
func envKeysFor(s Service) []string {
	keys := envKeys[s]
	if os.Getenv("ATL_INTEGRATION") != "" {
		if tk := testEnvKeys[s]; tk != "" {
			keys = append(append([]string{}, keys...), tk)
		}
	}
	return keys
}

// Token returns the PAT for a service, or an error if none is configured.
func Token(s Service) (string, error) {
	for _, k := range envKeysFor(s) {
		if v := os.Getenv(k); v != "" {
			return v, nil
		}
	}
	store, err := loadStore()
	if err != nil {
		return "", err
	}
	if t := store[string(s)]; t != "" {
		return t, nil
	}
	return "", fmt.Errorf("%w: no %s PAT found — set %s or run `atl auth login --service %s`",
		ErrNoToken, s, envKeys[s][0], s)
}

// Source describes where a token (if any) was found, without revealing it.
func Source(s Service) string {
	for _, k := range envKeysFor(s) {
		if os.Getenv(k) != "" {
			return "env:" + k
		}
	}
	if store, err := loadStore(); err == nil && store[string(s)] != "" {
		return "keychain-file:" + credPath()
	}
	return ""
}

// Login persists a PAT for a service to the 0600 credentials file.
func Login(s Service, token string) error {
	store, err := loadStore()
	if err != nil {
		return err
	}
	store[string(s)] = token
	return saveStore(store)
}

// Logout removes a stored PAT.
func Logout(s Service) error {
	store, err := loadStore()
	if err != nil {
		return err
	}
	delete(store, string(s))
	return saveStore(store)
}

func credPath() string { return filepath.Join(config.Dir(), "credentials.json") }

func loadStore() (map[string]string, error) {
	p := credPath()
	// We do not reject a symlinked credentials file. The write path
	// (WriteFileAtomic) replaces it by rename, so a planted symlink is overwritten
	// rather than followed; reading through one is harmless (tokens go only to the
	// configured host). The previous Lstat refusal was TOCTOU-racy and broke a
	// legitimate dotfiles-managed credentials.json for no real gain.
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("corrupt credentials file %s: %w", p, err)
	}
	return m, nil
}

func saveStore(m map[string]string) error {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// 0600 written atomically (temp + rename): readable only by the owner — this
	// file holds secrets — and a pre-existing looser-mode file is replaced, not
	// left with its old permissions.
	return safepath.WriteFileAtomic(credPath(), append(b, '\n'), 0o600)
}
