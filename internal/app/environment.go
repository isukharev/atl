package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// EnvironmentTimeFact is one explicitly qualified time semantic. Evidence is
// one of observed, configured, default, assumed, or unknown. Unknown facts omit
// Value and carry only a closed, privacy-safe reason.
type EnvironmentTimeFact struct {
	Value    string `json:"value,omitempty"`
	Evidence string `json:"evidence"`
	Source   string `json:"source"`
	Reason   string `json:"reason,omitempty"`
}

type JiraTimeEnvironment struct {
	Configured      bool                `json:"configured"`
	Status          string              `json:"status"`
	ServerUTCOffset EnvironmentTimeFact `json:"server_utc_offset"`
	UserTimeZone    EnvironmentTimeFact `json:"user_time_zone"`
	JQLTimeZone     EnvironmentTimeFact `json:"jql_time_zone"`
}

type ConfluenceTimeEnvironment struct {
	Configured   bool                `json:"configured"`
	Status       string              `json:"status"`
	UserTimeZone EnvironmentTimeFact `json:"user_time_zone"`
	CQLTimeZone  EnvironmentTimeFact `json:"cql_time_zone"`
}

type IncrementalTimeEnvironment struct {
	QueryLiteralTimeZone      EnvironmentTimeFact `json:"query_literal_time_zone"`
	BackendQueryTimeZone      EnvironmentTimeFact `json:"backend_query_time_zone"`
	SafetyOverlapHours        int                 `json:"safety_overlap_hours"`
	ExactTimestampFilter      bool                `json:"exact_timestamp_filter"`
	HiddenCalibrationRequests bool                `json:"hidden_calibration_requests"`
}

// EnvironmentInspectResult is deliberately identity- and URL-free. Complete
// means every configured backend returned all metadata facts; an unconfigured
// backend is represented but does not make another backend's observation fail.
type EnvironmentInspectResult struct {
	Complete        bool                       `json:"complete"`
	DisplayTimeZone EnvironmentTimeFact        `json:"display_time_zone"`
	Jira            JiraTimeEnvironment        `json:"jira"`
	Confluence      ConfluenceTimeEnvironment  `json:"confluence"`
	Incremental     IncrementalTimeEnvironment `json:"confluence_incremental"`
}

// InspectEnvironment performs at most three explicit metadata GETs: Jira
// serverInfo, Jira myself, and Confluence current user. It never searches,
// calibrates a query parser, or reads page/issue content.
func (s *EnvironmentService) InspectEnvironment(ctx context.Context, local *config.LocalConfig) *EnvironmentInspectResult {
	effective, provenance := config.EffectiveRender(s.cfg, local)
	displaySource := provenance["render.display_time_zone"]
	displayEvidence := displaySource
	if displayEvidence != "default" {
		displayEvidence = "configured"
	}
	result := &EnvironmentInspectResult{
		Complete: true,
		DisplayTimeZone: EnvironmentTimeFact{
			Value: effective.DisplayTimeZone, Evidence: displayEvidence, Source: displaySource,
		},
		Incremental: IncrementalTimeEnvironment{
			QueryLiteralTimeZone: EnvironmentTimeFact{Value: "UTC", Evidence: "configured", Source: "incremental_protocol_v2"},
			BackendQueryTimeZone: EnvironmentTimeFact{Evidence: "unknown", Source: "confluence_cql", Reason: "not_exposed_by_backend_metadata"},
			SafetyOverlapHours:   48, ExactTimestampFilter: true, HiddenCalibrationRequests: false,
		},
	}
	result.Jira = s.inspectJiraTime(ctx)
	result.Confluence = s.inspectConfluenceTime(ctx)
	if result.Jira.Configured && result.Jira.Status != "available" {
		result.Complete = false
	}
	if result.Confluence.Configured && result.Confluence.Status != "available" {
		result.Complete = false
	}
	return result
}

func (s *EnvironmentService) inspectJiraTime(ctx context.Context) JiraTimeEnvironment {
	unknownServer := unknownTimeFact("jira_server_info", setupReason(s.jiraSetup))
	unknownUser := unknownTimeFact("jira_current_user", setupReason(s.jiraSetup))
	out := JiraTimeEnvironment{
		Configured:      s.jiraSetup != "not_configured",
		Status:          s.jiraSetup,
		ServerUTCOffset: unknownServer,
		UserTimeZone:    unknownUser,
		JQLTimeZone:     unknownTimeFact("jira_current_user_time_zone", setupReason(s.jiraSetup)),
	}
	if s.jiraTime == nil {
		return out
	}

	serverTime, serverErr := s.jiraTime.ServerTime(ctx)
	if serverErr != nil {
		out.ServerUTCOffset.Reason = safeTimeReadReason(serverErr)
	} else if offset, ok := jiraServerUTCOffset(serverTime); ok {
		out.ServerUTCOffset = EnvironmentTimeFact{Value: offset, Evidence: "observed", Source: "jira_server_time"}
	} else {
		out.ServerUTCOffset.Reason = "server_time_missing_or_unparseable"
	}

	userTimeZone, userErr := s.jiraTime.CurrentUserTimeZone(ctx)
	if userErr != nil {
		out.UserTimeZone.Reason = safeTimeReadReason(userErr)
		out.JQLTimeZone.Reason = safeTimeReadReason(userErr)
	} else if value := strings.TrimSpace(userTimeZone); value != "" {
		out.UserTimeZone = EnvironmentTimeFact{Value: value, Evidence: "observed", Source: "jira_current_user"}
		out.JQLTimeZone = EnvironmentTimeFact{Value: value, Evidence: "assumed", Source: "jira_current_user_time_zone"}
	} else {
		out.UserTimeZone.Reason = "field_not_returned"
		out.JQLTimeZone.Reason = "user_time_zone_unknown"
	}

	out.Status = observedStatus(out.ServerUTCOffset, out.UserTimeZone)
	return out
}

func (s *EnvironmentService) inspectConfluenceTime(ctx context.Context) ConfluenceTimeEnvironment {
	unknownUser := unknownTimeFact("confluence_current_user", setupReason(s.confluenceSetup))
	out := ConfluenceTimeEnvironment{
		Configured:   s.confluenceSetup != "not_configured",
		Status:       s.confluenceSetup,
		UserTimeZone: unknownUser,
		CQLTimeZone:  EnvironmentTimeFact{Evidence: "unknown", Source: "confluence_cql", Reason: "not_exposed_by_backend_metadata"},
	}
	if s.confluenceTime == nil {
		return out
	}
	userTimeZone, err := s.confluenceTime.CurrentUserTimeZone(ctx)
	if err != nil {
		out.UserTimeZone.Reason = safeTimeReadReason(err)
		out.Status = "unavailable"
		return out
	}
	if value := strings.TrimSpace(userTimeZone); value != "" {
		out.UserTimeZone = EnvironmentTimeFact{Value: value, Evidence: "observed", Source: "confluence_current_user"}
		out.Status = "available"
	} else {
		out.UserTimeZone.Reason = "field_not_returned"
		out.Status = "partial"
	}
	return out
}

func unknownTimeFact(source, reason string) EnvironmentTimeFact {
	if reason == "" {
		reason = "not_observed"
	}
	return EnvironmentTimeFact{Evidence: "unknown", Source: source, Reason: reason}
}

func setupReason(status string) string {
	if status == "" {
		return "not_observed"
	}
	return status
}

func observedStatus(facts ...EnvironmentTimeFact) string {
	observed := 0
	for _, fact := range facts {
		if fact.Evidence == "observed" {
			observed++
		}
	}
	switch {
	case observed == len(facts):
		return "available"
	case observed > 0:
		return "partial"
	default:
		return "unavailable"
	}
}

func safeTimeReadReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrAuth):
		return "authentication_failed"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden"
	case errors.Is(err, domain.ErrNotFound):
		return "endpoint_unavailable"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "request_canceled"
	default:
		return "request_failed"
	}
}

func jiraServerUTCOffset(value string) (string, bool) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05-0700",
	} {
		parsed, err := time.Parse(layout, strings.TrimSpace(value))
		if err != nil {
			continue
		}
		_, seconds := parsed.Zone()
		sign := "+"
		if seconds < 0 {
			sign = "-"
			seconds = -seconds
		}
		return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, seconds%3600/60), true
	}
	return "", false
}
