package config

import (
	"errors"
	"os"
	"runtime"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// FileInspection is a path-free view of one local configuration file. Mode is
// deliberately reduced to owner_only instead of exposing platform-specific
// permission bits or a filesystem location.
type FileInspection struct {
	Present         bool   `json:"present"`
	Status          string `json:"status"`
	OwnerOnly       bool   `json:"owner_only"`
	PermissionKnown bool   `json:"permission_known"`
}

// Inspection is the privacy boundary used by setup diagnostics. Effective is
// retained for in-process orchestration only and is never serialized.
type Inspection struct {
	DirectorySource     string         `json:"directory_source"`
	File                FileInspection `json:"file"`
	Status              string         `json:"status"`
	Reason              string         `json:"reason,omitempty"`
	ConfluenceURLSource string         `json:"confluence_url_source"`
	JiraURLSource       string         `json:"jira_url_source"`
	UpdateURLSource     string         `json:"update_url_source"`
	Effective           *Config        `json:"-"`
}

// Inspect reports config health without returning a path, URL, hostname, or
// parser text. Environment URL overrides remain usable in Effective even when
// the on-disk file is malformed, so a diagnostic can still inspect an
// independently configured backend.
func Inspect() Inspection {
	out := Inspection{
		DirectorySource: configDirectorySource(),
		File:            inspectFile(path()),
		Status:          "valid",
		Effective:       &Config{},
	}

	editable, editableErr := LoadForEdit()
	if editableErr == nil {
		out.Effective = editable
	}
	strict, err := Load()
	if err == nil {
		out.Effective = strict
	} else {
		switch {
		case errors.Is(err, domain.ErrConfig):
			out.Status = "invalid"
			out.Reason = "invalid_configuration"
		default:
			out.Status = "unavailable"
			out.Reason = "configuration_unreadable"
		}
		if editableErr != nil {
			overlayEnvironmentURLs(out.Effective)
		}
	}
	if !out.File.Present && out.File.Status == "missing" && err == nil &&
		out.Effective.ConfluenceURL == "" && out.Effective.JiraURL == "" &&
		out.Effective.UpdateBaseURL == "" {
		out.Status = "missing"
	}

	out.ConfluenceURLSource = valueSource(
		firstEnv("ATL_CONFLUENCE_URL", "CONFLUENCE_URL"),
		out.Effective.ConfluenceURL,
	)
	out.JiraURLSource = valueSource(
		firstEnv("ATL_JIRA_URL", "JIRA_URL"),
		out.Effective.JiraURL,
	)
	out.UpdateURLSource = valueSource(
		os.Getenv("ATL_UPDATE_URL"),
		out.Effective.UpdateBaseURL,
	)
	return out
}

func inspectFile(filename string) FileInspection {
	info, err := os.Stat(filename)
	switch {
	case os.IsNotExist(err):
		return FileInspection{Status: "missing"}
	case err != nil:
		return FileInspection{Present: true, Status: "unavailable"}
	case !info.Mode().IsRegular():
		return FileInspection{Present: true, Status: "unsupported_type", PermissionKnown: true}
	default:
		ownerOnly, permissionKnown := OwnerOnlyPermission(info.Mode())
		return FileInspection{
			Present:         true,
			Status:          "available",
			OwnerOnly:       ownerOnly,
			PermissionKnown: permissionKnown,
		}
	}
}

// OwnerOnlyPermission projects POSIX file-mode evidence. Windows ACLs are not
// represented by os.FileMode, so that platform is explicitly unknown rather
// than falsely unhealthy.
func OwnerOnlyPermission(mode os.FileMode) (ownerOnly, known bool) {
	return ownerOnlyPermission(mode, runtime.GOOS)
}

func ownerOnlyPermission(mode os.FileMode, platform string) (ownerOnly, known bool) {
	if platform == "windows" {
		return false, false
	}
	return mode.Perm()&0o077 == 0, true
}

func configDirectorySource() string {
	switch {
	case strings.TrimSpace(os.Getenv("ATL_CONFIG_DIR")) != "":
		return "environment_override"
	case strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")) != "":
		return "xdg"
	default:
		return "user_default"
	}
}

func overlayEnvironmentURLs(cfg *Config) {
	cfg.ConfluenceURL = strings.TrimRight(firstEnv("ATL_CONFLUENCE_URL", "CONFLUENCE_URL"), "/")
	cfg.JiraURL = strings.TrimRight(firstEnv("ATL_JIRA_URL", "JIRA_URL"), "/")
	cfg.UpdateBaseURL = strings.TrimRight(os.Getenv("ATL_UPDATE_URL"), "/")
}

func valueSource(envValue, effectiveValue string) string {
	switch {
	case strings.TrimSpace(envValue) != "":
		return "environment"
	case strings.TrimSpace(effectiveValue) != "":
		return "config_file"
	default:
		return "missing"
	}
}
