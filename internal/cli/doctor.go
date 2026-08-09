package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/compose"
)

func newDoctorCmd() *cobra.Command {
	var remote bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose setup safely (offline by default)",
		Long: "Report build, configuration, credential, safety, and mirror health without\n" +
			"printing URLs, hostnames, paths, identities, tokens, or mirrored content.\n" +
			"The default is fully offline. --remote adds one single-attempt version probe\n" +
			"per ready backend; legacy Confluence may add one bodyless reachability probe.\n" +
			"It never reads page/issue bodies, identities, or search results.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := invocationRuntimeFor(cmd).processPolicy.resolve()
			if err != nil {
				return classifyProcessPolicyLoadError(err)
			}
			policy := buildPolicyShowResult(invocationRuntimeFor(cmd), resolved)
			result, doctorErr := compose.RunDoctor(cmd.Context(), app.DoctorOptions{
				Remote: remote, ReadOnlyPolicy: invocationRuntimeFor(cmd).readOnly || envReadOnly(), ContentPolicyActive: policy.Active,
				ContentPolicyEnforcement: policy.Enforcement, ContentPolicyAdvisory: policy.AdvisoryBecause,
			})
			emitErr := emitSnapshot(cmd, result, func() string { return doctorText(result) })
			return snapshotResultErr(doctorErr, emitErr)
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "make bounded version/reachability probes for ready backends")
	return cmd
}

func doctorText(result *app.DoctorResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "schema_version: %d\nstatus: %s\nhealthy: %t\ncomplete: %t\nmode: %s\n",
		result.SchemaVersion, result.Status, result.Healthy, result.Complete, result.Mode)
	fmt.Fprintf(&b, "cli: version=%s commit=%s build_state=%s\n", result.CLI.Version, result.CLI.Commit, result.CLI.BuildState)
	fmt.Fprintf(&b, "runtime: os=%s arch=%s\n", result.Runtime.OS, result.Runtime.Arch)
	fmt.Fprintf(&b, "config: status=%s reason=%s source=%s confluence_url_source=%s jira_url_source=%s\n",
		result.Config.Status, orNone(result.Config.Reason), result.Config.DirectorySource,
		result.Config.ConfluenceURLSource, result.Config.JiraURLSource)
	fmt.Fprintf(&b, "config_file: present=%t status=%s owner_only=%t permission_known=%t\n",
		result.Config.File.Present, result.Config.File.Status,
		result.Config.File.OwnerOnly, result.Config.File.PermissionKnown)
	fmt.Fprintf(&b, "jira_ca_bundle: configured=%t source=%s status=%s reason=%s\n",
		result.Config.Transport.Jira.Configured, result.Config.Transport.Jira.Source,
		result.Config.Transport.Jira.Status, orNone(result.Config.Transport.Jira.Reason))
	fmt.Fprintf(&b, "confluence_ca_bundle: configured=%t source=%s status=%s reason=%s\n",
		result.Config.Transport.Confluence.Configured, result.Config.Transport.Confluence.Source,
		result.Config.Transport.Confluence.Status, orNone(result.Config.Transport.Confluence.Reason))
	fmt.Fprintf(&b, "credential_store: present=%t status=%s owner_only=%t permission_known=%t\n",
		result.Credentials.Store.Present, result.Credentials.Store.Status,
		result.Credentials.Store.OwnerOnly, result.Credentials.Store.PermissionKnown)
	fmt.Fprintf(&b, "credentials_jira: present=%t status=%s source=%s\n",
		result.Credentials.Jira.Present, result.Credentials.Jira.Status, result.Credentials.Jira.Source)
	fmt.Fprintf(&b, "credentials_confluence: present=%t status=%s source=%s\n",
		result.Credentials.Confluence.Present, result.Credentials.Confluence.Status, result.Credentials.Confluence.Source)
	fmt.Fprintf(&b, "safety: read_only=%t status=%s\n", result.Safety.ReadOnly, result.Safety.Status)
	fmt.Fprintf(&b, "content_policy: active=%t enforcement=%s advisory_because=%s\n", result.ContentPolicy.Active, result.ContentPolicy.Enforcement, strings.Join(result.ContentPolicy.AdvisoryBecause, ","))
	writeDoctorServiceText(&b, "jira", result.Services.Jira)
	writeDoctorServiceText(&b, "confluence", result.Services.Confluence)
	fmt.Fprintf(&b, "mirror: status=%s source=%s\n", result.Mirror.Status, result.Mirror.Source)
	writeDoctorMirrorText(&b, "jira", result.Mirror.Jira)
	writeDoctorMirrorText(&b, "confluence", result.Mirror.Confluence)
	fmt.Fprintf(&b, "plugin: status=%s expected_version=%s reason=%s",
		result.Plugin.Status, orNone(result.Plugin.ExpectedVersion), result.Plugin.Reason)
	for _, problem := range result.Problems {
		fmt.Fprintf(&b, "\nproblem: severity=%s id=%s reason=%s remediation=%s",
			problem.Severity, problem.ID, problem.Reason, problem.Remediation)
	}
	return b.String()
}

func writeDoctorServiceText(b *strings.Builder, name string, service app.DoctorService) {
	fmt.Fprintf(b, "%s: status=%s url_status=%s url_source=%s credential_status=%s credential_source=%s\n",
		name, service.Status, service.URLStatus, service.URLSource,
		service.CredentialStatus, service.CredentialSource)
	fmt.Fprintf(b, "%s_remote: requested=%t status=%s reason=%s product=%s version=%s deployment_type=%s\n",
		name, service.Remote.Requested, service.Remote.Status, orNone(service.Remote.Reason),
		orNone(service.Remote.Product), orNone(service.Remote.Version), orNone(service.Remote.DeploymentType))
	fmt.Fprintf(b, "%s_compatibility: status=%s evidence=%s reason=%s\n",
		name, service.Compatibility.Status, service.Compatibility.Evidence,
		orNone(service.Compatibility.Reason))
}

func writeDoctorMirrorText(b *strings.Builder, name string, service app.DoctorMirrorService) {
	fmt.Fprintf(b, "mirror_%s: status=%s items=%d complete=%t reconciled=%t\n",
		name, service.Status, service.Items, service.Complete, service.Reconciled)
}
