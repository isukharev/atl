package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/config"
)

type jiraTimeStub struct {
	serverTime string
	userZone   string
	serverErr  error
	userErr    error
	serverGets int
	userGets   int
}

func (s *jiraTimeStub) ServerTime(context.Context) (string, error) {
	s.serverGets++
	return s.serverTime, s.serverErr
}

func (s *jiraTimeStub) CurrentUserTimeZone(context.Context) (string, error) {
	s.userGets++
	return s.userZone, s.userErr
}

type confluenceTimeStub struct {
	userZone string
	err      error
	gets     int
}

func (s *confluenceTimeStub) CurrentUserTimeZone(context.Context) (string, error) {
	s.gets++
	return s.userZone, s.err
}

func TestInspectEnvironmentQualifiesIndependentTimeSemantics(t *testing.T) {
	j := &jiraTimeStub{serverTime: "2026-07-14T12:00:00.000+0300", userZone: "Europe/Moscow"}
	c := &confluenceTimeStub{userZone: "Europe/Berlin"}
	svc := &EnvironmentService{
		cfg:      &config.Config{Render: &config.RenderConfig{DisplayTimeZone: "UTC"}},
		jiraTime: j, confluenceTime: c,
	}
	local := &config.LocalConfig{Render: &config.RenderConfig{DisplayTimeZone: "Asia/Tokyo"}}
	got := svc.InspectEnvironment(context.Background(), local)
	if !got.Complete || got.DisplayTimeZone.Value != "Asia/Tokyo" || got.DisplayTimeZone.Evidence != "configured" || got.DisplayTimeZone.Source != "local" {
		t.Fatalf("display/complete=%+v complete=%t", got.DisplayTimeZone, got.Complete)
	}
	if got.Jira.ServerUTCOffset.Value != "+03:00" || got.Jira.ServerUTCOffset.Evidence != "observed" || got.Jira.UserTimeZone.Value != "Europe/Moscow" {
		t.Fatalf("jira=%+v", got.Jira)
	}
	if got.Jira.JQLTimeZone.Value != "Europe/Moscow" || got.Jira.JQLTimeZone.Evidence != "assumed" {
		t.Fatalf("jql=%+v", got.Jira.JQLTimeZone)
	}
	if got.Confluence.UserTimeZone.Value != "Europe/Berlin" || got.Confluence.CQLTimeZone.Evidence != "unknown" {
		t.Fatalf("confluence=%+v", got.Confluence)
	}
	if got.Incremental.QueryLiteralTimeZone.Value != "UTC" || got.Incremental.BackendQueryTimeZone.Evidence != "unknown" || got.Incremental.SafetyOverlapHours != 48 || !got.Incremental.ExactTimestampFilter || got.Incremental.HiddenCalibrationRequests {
		t.Fatalf("incremental=%+v", got.Incremental)
	}
	if j.serverGets != 1 || j.userGets != 1 || c.gets != 1 {
		t.Fatalf("request counts jira server=%d user=%d conf=%d", j.serverGets, j.userGets, c.gets)
	}
}

func TestInspectEnvironmentDegradesWithoutLeakingReadErrors(t *testing.T) {
	j := &jiraTimeStub{serverErr: errors.New("https://private.invalid/serverInfo failed"), userZone: "UTC"}
	c := &confluenceTimeStub{err: errors.New("private user response")}
	svc := &EnvironmentService{cfg: &config.Config{}, jiraTime: j, confluenceTime: c}
	got := svc.InspectEnvironment(context.Background(), nil)
	if got.Complete || got.Jira.Status != "partial" || got.Jira.ServerUTCOffset.Reason != "request_failed" || got.Confluence.Status != "unavailable" || got.Confluence.UserTimeZone.Reason != "request_failed" {
		t.Fatalf("result=%+v", got)
	}
	if got.DisplayTimeZone.Value != "UTC" || got.DisplayTimeZone.Evidence != "default" {
		t.Fatalf("display=%+v", got.DisplayTimeZone)
	}
}

func TestJiraServerUTCOffsetDoesNotInventIANAZone(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
		ok    bool
	}{
		{"2026-07-14T12:00:00.000+0000", "+00:00", true},
		{"2026-07-14T12:00:00-04:30", "-04:30", true},
		{"not-a-time", "", false},
	} {
		got, ok := jiraServerUTCOffset(tc.value)
		if got != tc.want || ok != tc.ok {
			t.Errorf("offset(%q)=(%q,%t), want (%q,%t)", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}
