package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

const doctorSchemaVersion = 1

var safeServerVersion = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,4})?(-[0-9A-Za-z]{1,16})?$`)

type DoctorOptions struct {
	Remote         bool
	ReadOnlyPolicy bool
}

type DoctorResult struct {
	SchemaVersion int               `json:"schema_version"`
	Mode          string            `json:"mode"`
	Complete      bool              `json:"complete"`
	Healthy       bool              `json:"healthy"`
	Status        string            `json:"status"`
	CLI           version.BuildInfo `json:"cli"`
	Runtime       DoctorRuntime     `json:"runtime"`
	Config        DoctorConfig      `json:"config"`
	Credentials   DoctorCredentials `json:"credentials"`
	Safety        DoctorSafety      `json:"safety"`
	Services      DoctorServices    `json:"services"`
	Mirror        DoctorMirror      `json:"mirror"`
	Plugin        DoctorPlugin      `json:"plugin"`
	Problems      []DoctorProblem   `json:"problems"`
}

type DoctorRuntime struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type DoctorConfig struct {
	Status              string                `json:"status"`
	Reason              string                `json:"reason,omitempty"`
	DirectorySource     string                `json:"directory_source"`
	File                config.FileInspection `json:"file"`
	ConfluenceURLSource string                `json:"confluence_url_source"`
	JiraURLSource       string                `json:"jira_url_source"`
}

type DoctorCredentials struct {
	Store      auth.StoreInspection      `json:"store"`
	Confluence auth.CredentialInspection `json:"confluence"`
	Jira       auth.CredentialInspection `json:"jira"`
}

type DoctorSafety struct {
	ReadOnly bool   `json:"read_only"`
	Status   string `json:"status"`
}

type DoctorServices struct {
	Confluence DoctorService `json:"confluence"`
	Jira       DoctorService `json:"jira"`
}

type DoctorService struct {
	URLStatus        string              `json:"url_status"`
	URLSource        string              `json:"url_source"`
	CredentialStatus string              `json:"credential_status"`
	CredentialSource string              `json:"credential_source"`
	Status           string              `json:"status"`
	Remote           DoctorRemote        `json:"remote"`
	Compatibility    DoctorCompatibility `json:"compatibility"`
}

type DoctorRemote struct {
	Requested      bool   `json:"requested"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Product        string `json:"product,omitempty"`
	Version        string `json:"version,omitempty"`
	DeploymentType string `json:"deployment_type,omitempty"`
}

type DoctorCompatibility struct {
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
	Reason   string `json:"reason,omitempty"`
}

type DoctorMirror struct {
	Source     string              `json:"source"`
	Status     string              `json:"status"`
	Confluence DoctorMirrorService `json:"confluence"`
	Jira       DoctorMirrorService `json:"jira"`
}

type DoctorMirrorService struct {
	Status     string `json:"status"`
	Items      int    `json:"items"`
	Complete   bool   `json:"complete"`
	Reconciled bool   `json:"reconciled"`
}

type DoctorPlugin struct {
	Status          string `json:"status"`
	ExpectedVersion string `json:"expected_version,omitempty"`
	Reason          string `json:"reason"`
}

type DoctorProblem struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Reason      string `json:"reason"`
	Remediation string `json:"remediation"`
}

// RunDoctor returns a complete, content-free aggregate. The returned error is
// static and classified only after the result has been emitted by the CLI.
func RunDoctor(ctx context.Context, opts DoctorOptions) (*DoctorResult, error) {
	cfgInspection := config.Inspect()
	authInspection := auth.Inspect()
	build := version.Current()
	result := &DoctorResult{
		SchemaVersion: doctorSchemaVersion,
		Mode:          "offline",
		Complete:      true,
		Healthy:       true,
		Status:        "pass",
		CLI:           build,
		Runtime:       DoctorRuntime{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Config: DoctorConfig{
			Status:              cfgInspection.Status,
			Reason:              cfgInspection.Reason,
			DirectorySource:     cfgInspection.DirectorySource,
			File:                cfgInspection.File,
			ConfluenceURLSource: cfgInspection.ConfluenceURLSource,
			JiraURLSource:       cfgInspection.JiraURLSource,
		},
		Credentials: DoctorCredentials{
			Store: authInspection.Store, Confluence: authInspection.Confluence, Jira: authInspection.Jira,
		},
		Safety: DoctorSafety{ReadOnly: opts.ReadOnlyPolicy || cfgInspection.Effective.ReadOnly, Status: "available"},
		Plugin: DoctorPlugin{Status: "not_observable", Reason: "host_does_not_expose_plugin_version"},
	}
	if build.Version != "" && build.Version != "dev" {
		result.Plugin.ExpectedVersion = build.Version
	}
	if opts.Remote {
		result.Mode = "remote"
	}

	evaluateLocalDoctor(result, cfgInspection, authInspection)
	result.Mirror = inspectDoctorMirror(result)
	if opts.Remote {
		runDoctorRemote(ctx, result, cfgInspection, authInspection)
	}
	finalizeDoctor(result)
	if !result.Healthy {
		return result, fmt.Errorf("%w: doctor found blocking setup problems", domain.ErrCheckFailed)
	}
	return result, nil
}

func evaluateLocalDoctor(result *DoctorResult, cfg config.Inspection, credentials auth.Inspection) {
	switch cfg.Status {
	case "invalid", "unavailable":
		addDoctorProblem(result, "config.invalid", "error", cfg.Reason, "repair_configuration")
	case "missing":
		addDoctorProblem(result, "config.missing", "advisory", "configuration_file_missing", "configure_backend")
	}
	if cfg.File.Present && cfg.File.PermissionKnown && !cfg.File.OwnerOnly {
		addDoctorProblem(result, "config.permissions", "error", "configuration_not_owner_only", "restrict_file_permissions")
	}
	switch credentials.Store.Status {
	case "invalid", "unavailable", "unsupported_type":
		addDoctorProblem(result, "credentials.store", "error", "credentials_store_unavailable", "repair_credentials")
	}
	if credentials.Store.Present && credentials.Store.PermissionKnown && !credentials.Store.OwnerOnly {
		addDoctorProblem(result, "credentials.permissions", "error", "credentials_not_owner_only", "restrict_file_permissions")
	}

	result.Services.Confluence = localDoctorService(
		cfg.Effective.ConfluenceURL, cfg.ConfluenceURLSource, credentials.Confluence,
	)
	result.Services.Jira = localDoctorService(
		cfg.Effective.JiraURL, cfg.JiraURLSource, credentials.Jira,
	)
	evaluateDoctorService(result, "confluence", cfg.Effective.ConfluenceURL, result.Services.Confluence)
	evaluateDoctorService(result, "jira", cfg.Effective.JiraURL, result.Services.Jira)
	if result.Services.Confluence.Status == "not_configured" && result.Services.Jira.Status == "not_configured" {
		addDoctorProblem(result, "services.none_configured", "error", "no_backend_configured", "configure_backend")
	}
	if build := result.CLI; build.Version == "dev" || build.Commit == "unknown" || build.BuildState == "unknown" {
		addDoctorProblem(result, "cli.provenance", "advisory", "build_provenance_incomplete", "use_release_build")
	}
}

func localDoctorService(rawURL, source string, credential auth.CredentialInspection) DoctorService {
	out := DoctorService{
		URLStatus:        "not_configured",
		URLSource:        source,
		CredentialStatus: credentialStatus(credential),
		CredentialSource: credentialSource(credential),
		Status:           "not_configured",
		Remote:           DoctorRemote{Status: "not_requested"},
		Compatibility:    DoctorCompatibility{Status: "unknown", Evidence: "not_observed", Reason: "remote_metadata_not_requested"},
	}
	if rawURL != "" {
		if err := config.CheckSecureURL(rawURL); err != nil {
			out.URLStatus = "invalid"
		} else {
			out.URLStatus = "valid"
		}
	}
	switch {
	case out.URLStatus == "invalid":
		out.Status = "invalid_configuration"
	case out.URLStatus == "valid" && credential.Status == "available":
		out.Status = "ready"
	case out.URLStatus == "valid":
		out.Status = "credentials_missing"
	case out.URLStatus == "not_configured" && credential.Present:
		out.Status = "url_missing"
	}
	return out
}

func credentialStatus(value auth.CredentialInspection) string {
	switch value.Status {
	case "available":
		return "present"
	case "credentials_unavailable":
		return "unavailable"
	default:
		return "missing"
	}
}

func credentialSource(value auth.CredentialInspection) string {
	switch value.Source {
	case "environment":
		return "environment"
	case "credentials_file":
		return "credential_store"
	default:
		return "none"
	}
}

func evaluateDoctorService(result *DoctorResult, name, rawURL string, service DoctorService) {
	switch service.Status {
	case "not_configured":
		addDoctorProblem(result, "service."+name+".not_configured", "advisory", "service_not_configured", "configure_service")
	case "ready":
		if insecureOverrideActive(rawURL) {
			addDoctorProblem(result, "service."+name+".transport_override", "advisory", "insecure_transport_override", "use_https")
		}
	default:
		addDoctorProblem(result, "service."+name+".setup", "error", service.Status, "repair_service_setup")
	}
}

func insecureOverrideActive(raw string) bool {
	if os.Getenv("ATL_ALLOW_INSECURE") == "" {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme != "https" && !isLoopbackDoctorHost(u.Hostname())
}

func isLoopbackDoctorHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func inspectDoctorMirror(result *DoctorResult) DoctorMirror {
	out := DoctorMirror{
		Source: "not_found", Status: "not_found",
		Confluence: DoctorMirrorService{Status: "not_found"},
		Jira:       DoctorMirrorService{Status: "not_found"},
	}
	root := strings.TrimSpace(os.Getenv("ATL_MIRROR_ROOT"))
	if root != "" {
		out.Source = "environment"
		if info, err := os.Stat(filepath.Join(root, ".atl")); err != nil || !info.IsDir() {
			addDoctorProblem(result, "mirror.not_found", "advisory", "configured_mirror_not_found", "pull_or_select_mirror")
			return out
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			out.Status = "unavailable"
			addDoctorProblem(result, "mirror.discovery", "error", "working_directory_unavailable", "select_mirror_root")
			return out
		}
		var ok bool
		root, ok = MirrorRootOf(cwd)
		if !ok {
			addDoctorProblem(result, "mirror.not_found", "advisory", "mirror_not_found", "pull_or_select_mirror")
			return out
		}
		out.Source = "ancestor"
	}

	confSnapshot, confErr := SnapshotConfluenceMirror(root)
	out.Confluence = doctorConfluenceMirror(confSnapshot, confErr)
	jiraSnapshot, jiraErr := SnapshotJiraMirror(root)
	out.Jira = doctorJiraMirror(jiraSnapshot, jiraErr)
	if out.Confluence.Status == "unhealthy" || out.Jira.Status == "unhealthy" {
		out.Status = "unhealthy"
		addDoctorProblem(result, "mirror.integrity", "error", "mirror_integrity_failed", "inspect_mirror_snapshot")
	} else if out.Confluence.Status == "empty" && out.Jira.Status == "empty" {
		out.Status = "empty"
		addDoctorProblem(result, "mirror.empty", "advisory", "mirror_has_no_tracked_items", "pull_service_content")
	} else {
		out.Status = "healthy"
	}
	return out
}

func doctorConfluenceMirror(snapshot *ConfluenceMirrorSnapshot, err error) DoctorMirrorService {
	if snapshot == nil {
		return DoctorMirrorService{Status: "unhealthy"}
	}
	out := DoctorMirrorService{
		Items: snapshot.Local.Present, Complete: snapshot.Complete, Reconciled: snapshot.Reconciled,
	}
	switch {
	case err != nil || !snapshot.Complete || !snapshot.Reconciled:
		out.Status = "unhealthy"
	case out.Items == 0:
		out.Status = "empty"
	default:
		out.Status = "healthy"
	}
	return out
}

func doctorJiraMirror(snapshot *JiraMirrorSnapshot, err error) DoctorMirrorService {
	if snapshot == nil {
		return DoctorMirrorService{Status: "unhealthy"}
	}
	out := DoctorMirrorService{
		Items: snapshot.Local.Present, Complete: snapshot.Complete, Reconciled: snapshot.Reconciled,
	}
	switch {
	case err != nil || !snapshot.Complete || !snapshot.Reconciled:
		out.Status = "unhealthy"
	case out.Items == 0:
		out.Status = "empty"
	default:
		out.Status = "healthy"
	}
	return out
}

func runDoctorRemote(ctx context.Context, result *DoctorResult, cfg config.Inspection, credentials auth.Inspection) {
	if cfg.Status == "invalid" || cfg.Status == "unavailable" {
		skipDoctorRemote(&result.Services.Jira, "configuration_preflight_failed")
		skipDoctorRemote(&result.Services.Confluence, "configuration_preflight_failed")
		return
	}
	runOneDoctorRemote(ctx, result, "jira", cfg.Effective.JiraURL, cfg.File, credentials.Store, &result.Services.Jira)
	runOneDoctorRemote(ctx, result, "confluence", cfg.Effective.ConfluenceURL, cfg.File, credentials.Store, &result.Services.Confluence)
}

func runOneDoctorRemote(
	ctx context.Context,
	result *DoctorResult,
	service, rawURL string,
	configFile config.FileInspection,
	credentialStore auth.StoreInspection,
	out *DoctorService,
) {
	out.Remote.Requested = true
	if out.Status != "ready" {
		skipDoctorRemote(out, "service_not_ready")
		return
	}
	if out.URLSource == "config_file" && configFile.PermissionKnown && !configFile.OwnerOnly {
		skipDoctorRemote(out, "configuration_permissions_failed")
		return
	}
	if out.CredentialSource == "credential_store" &&
		(credentialStore.Status != "available" ||
			credentialStore.PermissionKnown && !credentialStore.OwnerOnly) {
		skipDoctorRemote(out, "credential_store_preflight_failed")
		return
	}
	token, err := auth.Token(auth.Service(service))
	if err != nil {
		out.Remote.Status = "skipped"
		out.Remote.Reason = "credentials_unavailable"
		addDoctorProblem(result, "remote."+service, "error", out.Remote.Reason, "repair_credentials")
		return
	}

	reader := newDoctorServerMetadataReader(service, rawURL, token, result.CLI.Version)
	probeCtx := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	probeCtx, cancel := context.WithTimeout(probeCtx, 5*time.Second)
	defer cancel()
	metadata, readErr := reader.ServerMetadata(probeCtx)
	if readErr != nil {
		out.Remote.Status, out.Remote.Reason = doctorRemoteFailure(readErr)
		out.Compatibility = DoctorCompatibility{Status: "unknown", Evidence: "remote_failed", Reason: out.Remote.Reason}
		addDoctorProblem(result, "remote."+service, "error", out.Remote.Reason, "check_backend_access")
		return
	}
	out.Remote.Product = metadata.Product
	out.Remote.Version = normalizeServerVersion(metadata.Version)
	out.Remote.DeploymentType = normalizeDeploymentType(metadata.DeploymentType)
	out.Remote.Status = "available"
	out.Compatibility = doctorCompatibility(service, out.Remote)
	if out.Remote.Version == "" {
		out.Remote.Reason = "version_not_returned_or_invalid"
		addDoctorProblem(result, "remote."+service+".version", "advisory", out.Remote.Reason, "report_compatibility_result")
	}
	if out.Compatibility.Status == "unsupported" {
		addDoctorProblem(result, "remote."+service+".compatibility", "error", out.Compatibility.Reason, "use_supported_data_center_backend")
	} else if out.Compatibility.Status == "unverified" {
		addDoctorProblem(result, "remote."+service+".compatibility", "advisory", out.Compatibility.Reason, "report_compatibility_result")
	}
}

func skipDoctorRemote(service *DoctorService, reason string) {
	service.Remote.Requested = true
	service.Remote.Status = "skipped"
	service.Remote.Reason = reason
	service.Compatibility = DoctorCompatibility{Status: "unknown", Evidence: "not_observed", Reason: reason}
}

func doctorRemoteFailure(err error) (string, string) {
	switch {
	case errors.Is(err, domain.ErrAuth):
		return "authentication_failed", "authentication_failed"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden", "forbidden"
	case errors.Is(err, domain.ErrNotFound):
		return "endpoint_unavailable", "endpoint_unavailable"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "request_failed", "request_canceled_or_timed_out"
	default:
		return "request_failed", "request_failed"
	}
}

func normalizeServerVersion(value string) string {
	value = strings.TrimSpace(value)
	if !safeServerVersion.MatchString(value) {
		return ""
	}
	return value
}

func normalizeDeploymentType(value string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", " ")) {
	case "data center", "datacenter":
		return "data_center"
	case "server":
		return "server"
	case "cloud":
		return "cloud"
	default:
		return ""
	}
}

func doctorCompatibility(service string, remote DoctorRemote) DoctorCompatibility {
	if remote.Version == "" {
		return DoctorCompatibility{Status: "unverified", Evidence: "metadata_only", Reason: "version_unavailable"}
	}
	if service == "jira" {
		switch remote.DeploymentType {
		case "data_center", "server":
			return DoctorCompatibility{Status: "supported", Evidence: "product_metadata"}
		case "cloud":
			return DoctorCompatibility{Status: "unsupported", Evidence: "product_metadata", Reason: "cloud_not_supported"}
		default:
			return DoctorCompatibility{Status: "unverified", Evidence: "metadata_only", Reason: "deployment_type_unavailable"}
		}
	}
	return DoctorCompatibility{Status: "supported", Evidence: "product_metadata"}
}

func addDoctorProblem(result *DoctorResult, id, severity, reason, remediation string) {
	result.Problems = append(result.Problems, DoctorProblem{
		ID: id, Severity: severity, Reason: reason, Remediation: remediation,
	})
}

func finalizeDoctor(result *DoctorResult) {
	hasAdvisory := false
	result.Healthy = true
	for _, problem := range result.Problems {
		if problem.Severity == "error" {
			result.Healthy = false
			result.Status = "fail"
			return
		}
		hasAdvisory = true
	}
	if hasAdvisory {
		result.Status = "warning"
	} else {
		result.Status = "pass"
	}
}
