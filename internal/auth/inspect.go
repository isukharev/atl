package auth

import (
	"encoding/json"
	"os"

	"github.com/isukharev/atl/internal/config"
)

// CredentialInspection reports only whether and where a credential resolves.
// Source belongs to the closed set environment, credentials_file, or missing.
type CredentialInspection struct {
	Present bool   `json:"present"`
	Source  string `json:"source"`
	Status  string `json:"status"`
}

// StoreInspection is a path- and value-free view of credentials.json.
type StoreInspection struct {
	Present         bool   `json:"present"`
	Status          string `json:"status"`
	OwnerOnly       bool   `json:"owner_only"`
	PermissionKnown bool   `json:"permission_known"`
}

// Inspection contains privacy-safe credential facts for both backends.
type Inspection struct {
	Store      StoreInspection      `json:"store"`
	Confluence CredentialInspection `json:"confluence"`
	Jira       CredentialInspection `json:"jira"`
}

// Inspect never returns token values, environment variable names, or the
// credentials path. A valid environment credential remains available even if
// an unrelated credentials file is malformed.
func Inspect() Inspection {
	store, values := inspectStore()
	return Inspection{
		Store:      store,
		Confluence: inspectCredential(Confluence, values, store.Status),
		Jira:       inspectCredential(Jira, values, store.Status),
	}
}

func inspectStore() (StoreInspection, map[string]string) {
	info, err := os.Stat(credPath())
	switch {
	case os.IsNotExist(err):
		return StoreInspection{Status: "missing"}, nil
	case err != nil:
		return StoreInspection{Present: true, Status: "unavailable"}, nil
	case !info.Mode().IsRegular():
		return StoreInspection{Present: true, Status: "unsupported_type", PermissionKnown: true}, nil
	}
	ownerOnly, permissionKnown := config.OwnerOnlyPermission(info.Mode())
	out := StoreInspection{
		Present:         true,
		Status:          "available",
		OwnerOnly:       ownerOnly,
		PermissionKnown: permissionKnown,
	}
	body, err := os.ReadFile(credPath())
	if err != nil {
		out.Status = "unavailable"
		return out, nil
	}
	values := map[string]string{}
	if err := json.Unmarshal(body, &values); err != nil {
		out.Status = "invalid"
		return out, nil
	}
	return out, values
}

func inspectCredential(service Service, values map[string]string, storeStatus string) CredentialInspection {
	for _, key := range envKeysFor(service) {
		if os.Getenv(key) != "" {
			return CredentialInspection{Present: true, Source: "environment", Status: "available"}
		}
	}
	if values != nil && values[string(service)] != "" {
		return CredentialInspection{Present: true, Source: "credentials_file", Status: "available"}
	}
	switch storeStatus {
	case "invalid", "unavailable", "unsupported_type":
		return CredentialInspection{Source: "missing", Status: "credentials_unavailable"}
	default:
		return CredentialInspection{Source: "missing", Status: "missing"}
	}
}
