package compose

import (
	"strings"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
)

func brokerDoctorDependencies(service string, inspection config.Inspection, validateCABundle func(string) error) app.DoctorDependencies {
	cfg := inspection.Effective
	brokerURL := ""
	brokerCA := ""
	if cfg != nil && cfg.Broker != nil {
		brokerURL = cfg.Broker.BaseURL
		brokerCA = cfg.Broker.CAFile
	}
	credentials := app.DoctorCredentialInspection{Store: app.DoctorCredentialStore{Status: "not_used"}}
	confluenceURL, confluenceSource := "", "not_configured"
	jiraURL, jiraSource := "", "not_configured"
	if cfg != nil && cfg.Broker != nil && cfg.Broker.ConfluenceSessionFile != "" {
		confluenceURL, confluenceSource = brokerURL, "broker_configuration"
		credentials.Confluence = app.DoctorCredential{Present: true, Source: "broker_session_file", Status: "available"}
	} else {
		credentials.Confluence = app.DoctorCredential{Source: "missing", Status: "missing"}
	}
	if cfg != nil && cfg.Broker != nil && cfg.Broker.JiraSessionFile != "" {
		jiraURL, jiraSource = brokerURL, "broker_configuration"
		credentials.Jira = app.DoctorCredential{Present: true, Source: "broker_session_file", Status: "available"}
	} else {
		credentials.Jira = app.DoctorCredential{Source: "missing", Status: "missing"}
	}
	transport := brokerDoctorTransport(service, brokerCA, validateCABundle)
	return app.DoctorDependencies{
		Config: app.DoctorConfigInspection{
			ConnectionMode: config.ConnectionModeBroker,
			Status:         inspection.Status, Reason: inspection.Reason, DirectorySource: inspection.DirectorySource,
			File:          app.DoctorFileInspection{Present: inspection.File.Present, Status: inspection.File.Status, OwnerOnly: inspection.File.OwnerOnly, PermissionKnown: inspection.File.PermissionKnown},
			ConfluenceURL: confluenceURL, ConfluenceURLSource: confluenceSource, ConfluenceURLStatus: doctorURLStatus(confluenceURL),
			JiraURL: jiraURL, JiraURLSource: jiraSource, JiraURLStatus: doctorURLStatus(jiraURL), ReadOnly: cfg != nil && cfg.ReadOnly,
			Transport: transport,
		},
		Credentials: credentials, RemoteSkipReason: "broker_operation_unsupported",
	}
}

func brokerDoctorTransport(service, caFile string, validate func(string) error) app.DoctorTransport {
	selection := strings.TrimSpace(service)
	if selection == "" {
		selection = app.DoctorServiceAll
	}
	projection := func(selected bool) app.DoctorCABundle {
		out := app.DoctorCABundle{Configured: caFile != "", Source: "broker_configuration", Status: "not_selected"}
		if !selected {
			return out
		}
		if caFile == "" {
			out.Status = "not_configured"
		} else if validate(caFile) != nil {
			out.Status, out.Reason = "invalid", "ca_bundle_invalid"
		} else {
			out.Status = "available"
		}
		return out
	}
	return app.DoctorTransport{
		Confluence: projection(selection == app.DoctorServiceAll || selection == app.DoctorServiceConfluence),
		Jira:       projection(selection == app.DoctorServiceAll || selection == app.DoctorServiceJira),
	}
}
