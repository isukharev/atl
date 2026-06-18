// Package auth resolves per-user Personal Access Tokens. Tokens never live in
// the repo or mirror. Resolution order: explicit env, then a 0600 credentials
// file in the config dir. The file store is abstracted so an OS-keychain
// backend can replace it later without touching callers.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/config"
)

// Service identifies which backend a token is for.
type Service string

const (
	Confluence Service = "confluence"
	Jira       Service = "jira"
)

// envKeys lists, in priority order, the env vars consulted for each service.
// ATL_* are the canonical names; TEST_*_PAT match the repo's .env.local so
// integration runs work out of the box.
var envKeys = map[Service][]string{
	Confluence: {"ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT", "TEST_CONFLUENCE_PAT"},
	Jira:       {"ATL_JIRA_PAT", "JIRA_PAT", "TEST_JIRA_PAT"},
}

// Token returns the PAT for a service, or an error if none is configured.
func Token(s Service) (string, error) {
	for _, k := range envKeys[s] {
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
	return "", fmt.Errorf("no %s PAT configured: set %s or run `atl auth login --service %s`",
		s, envKeys[s][0], s)
}

// Source describes where a token (if any) was found, without revealing it.
func Source(s Service) string {
	for _, k := range envKeys[s] {
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
	b, err := os.ReadFile(credPath())
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("corrupt credentials file %s: %w", credPath(), err)
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
	// 0600: readable only by the owner — this file holds secrets.
	return os.WriteFile(credPath(), append(b, '\n'), 0o600)
}
