package compose

import (
	"strings"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
)

func doctorTransportProjection(service string, cfg *config.Config, transport config.TransportSummary, validateCABundle func(string) error) app.DoctorTransport {
	selection := strings.TrimSpace(service)
	if selection == "" {
		selection = app.DoctorServiceAll
	}
	out := app.DoctorTransport{
		Confluence: unselectedDoctorCABundle(transport.Confluence),
		Jira:       unselectedDoctorCABundle(transport.Jira),
	}
	if selection == app.DoctorServiceAll || selection == app.DoctorServiceConfluence {
		out.Confluence = doctorCABundle(cfg.CABundle(config.TransportServiceConfluence), transport.Confluence, validateCABundle)
	}
	if selection == app.DoctorServiceAll || selection == app.DoctorServiceJira {
		out.Jira = doctorCABundle(cfg.CABundle(config.TransportServiceJira), transport.Jira, validateCABundle)
	}
	return out
}

func unselectedDoctorCABundle(summary config.BackendTransportSummary) app.DoctorCABundle {
	return app.DoctorCABundle{
		Configured: summary.CABundleConfigured,
		Source:     summary.CABundleSource,
		Status:     "not_selected",
	}
}

func doctorURLStatus(rawURL string) string {
	if rawURL == "" {
		return "not_configured"
	}
	if err := config.CheckSecureURL(rawURL); err != nil {
		return "invalid"
	}
	return "valid"
}

func doctorCABundle(path string, summary config.BackendTransportSummary, validate func(string) error) app.DoctorCABundle {
	out := app.DoctorCABundle{
		Configured: summary.CABundleConfigured, Source: summary.CABundleSource, Status: "not_configured",
	}
	if !out.Configured {
		return out
	}
	if err := validate(path); err != nil {
		out.Status = "invalid"
		out.Reason = "ca_bundle_invalid"
		return out
	}
	out.Status = "available"
	return out
}
