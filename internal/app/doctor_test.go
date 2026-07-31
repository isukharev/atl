package app

import (
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestDoctorMetadataNormalizationIsClosed(t *testing.T) {
	if got := normalizeServerVersion("9.12.7"); got != "9.12.7" {
		t.Fatalf("normalizeServerVersion(valid) = %q", got)
	}
	for _, unsafe := range []string{
		"", "private host", "9.0\nsecret", "https://private.example", "<script>",
		"10.0.0.1", "9.private.example", "9.12.secret", "9secret-token-value",
	} {
		if got := normalizeServerVersion(unsafe); got != "" {
			t.Errorf("normalizeServerVersion(%q) = %q, want empty", unsafe, got)
		}
	}
	cases := map[string]string{
		"Data Center": "data_center",
		"datacenter":  "data_center",
		"SERVER":      "server",
		"Cloud":       "cloud",
		"private":     "",
	}
	for input, want := range cases {
		if got := normalizeDeploymentType(input); got != want {
			t.Errorf("normalizeDeploymentType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDoctorRemoteFailureNeverReturnsBackendText(t *testing.T) {
	tests := []struct {
		err    error
		status string
		reason string
	}{
		{errors.Join(domain.ErrAuth, errors.New("private response body")), "authentication_failed", "authentication_failed"},
		{errors.Join(domain.ErrForbidden, errors.New("private response body")), "forbidden", "forbidden"},
		{errors.Join(domain.ErrNotFound, errors.New("private response body")), "endpoint_unavailable", "endpoint_unavailable"},
		{errors.New("private host: connection refused"), "request_failed", "request_failed"},
	}
	for _, test := range tests {
		status, reason := doctorRemoteFailure(test.err)
		if status != test.status || reason != test.reason {
			t.Errorf("doctorRemoteFailure(%v) = %q/%q, want %q/%q", test.err, status, reason, test.status, test.reason)
		}
	}
}

func TestFinalizeDoctorWarningsStayHealthy(t *testing.T) {
	result := &DoctorResult{}
	addDoctorProblem(result, "plugin.version", "advisory", "plugin_version_not_observable", "verify_manually")
	finalizeDoctor(result)
	if !result.Healthy || result.Status != "warning" {
		t.Fatalf("warning result = %+v", result)
	}
	addDoctorProblem(result, "config.invalid", "error", "invalid_configuration", "repair_configuration")
	finalizeDoctor(result)
	if result.Healthy || result.Status != "fail" {
		t.Fatalf("error result = %+v", result)
	}
}

func TestDoctorMirrorProjectionIsContentFree(t *testing.T) {
	conf := doctorConfluenceMirror(&ConfluenceMirrorSnapshot{
		Complete: true, Reconciled: true, Local: ConfluenceMirrorLocalSummary{Present: 3},
	}, nil)
	if conf.Status != "healthy" || conf.Items != 3 {
		t.Fatalf("confluence projection = %+v", conf)
	}
	jira := doctorJiraMirror(&JiraMirrorSnapshot{
		Complete: true, Reconciled: false, Local: JiraMirrorLocalSummary{Present: 2},
	}, domain.ErrCheckFailed)
	if jira.Status != "unhealthy" || jira.Items != 2 {
		t.Fatalf("jira projection = %+v", jira)
	}
}
