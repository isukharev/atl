package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestProjectReadOnlyPrecedenceMatrix(t *testing.T) {
	tests := []struct {
		name                          string
		configured, flag, environment bool
		want                          ReadOnlyProjection
	}{
		{name: "none", want: ReadOnlyProjection{ReadOnlySource: "none"}},
		{name: "configuration", configured: true, want: ReadOnlyProjection{ConfiguredReadOnly: true, EffectiveReadOnly: true, ReadOnlySource: "configuration"}},
		{name: "environment", environment: true, want: ReadOnlyProjection{EffectiveReadOnly: true, ReadOnlySource: "environment"}},
		{name: "environment over configuration", configured: true, environment: true, want: ReadOnlyProjection{ConfiguredReadOnly: true, EffectiveReadOnly: true, ReadOnlySource: "environment"}},
		{name: "flag", flag: true, want: ReadOnlyProjection{EffectiveReadOnly: true, ReadOnlySource: "flag"}},
		{name: "flag over configuration", configured: true, flag: true, want: ReadOnlyProjection{ConfiguredReadOnly: true, EffectiveReadOnly: true, ReadOnlySource: "flag"}},
		{name: "flag over environment", flag: true, environment: true, want: ReadOnlyProjection{EffectiveReadOnly: true, ReadOnlySource: "flag"}},
		{name: "flag over all", configured: true, flag: true, environment: true, want: ReadOnlyProjection{ConfiguredReadOnly: true, EffectiveReadOnly: true, ReadOnlySource: "flag"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ProjectReadOnly(test.configured, test.flag, test.environment)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ProjectReadOnly(%t,%t,%t)=%+v want=%+v", test.configured, test.flag, test.environment, got, test.want)
			}
		})
	}
}

func TestNormalizeDoctorServiceIsClosed(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: DoctorServiceAll},
		{input: " jira ", want: DoctorServiceJira},
		{input: DoctorServiceConfluence, want: DoctorServiceConfluence},
	}
	for _, test := range tests {
		got, err := normalizeDoctorService(test.input)
		if err != nil || got != test.want {
			t.Fatalf("normalizeDoctorService(%q)=%q,%v want %q,nil", test.input, got, err, test.want)
		}
	}
	if got, err := normalizeDoctorService("unknown"); !errors.Is(err, domain.ErrUsage) || got != "" {
		t.Fatalf("normalizeDoctorService(unknown)=%q,%v want empty,ErrUsage", got, err)
	}
}

func TestDoctorInsecureOverrideExcludesHTTPSAndLoopback(t *testing.T) {
	t.Setenv("ATL_ALLOW_INSECURE", "")
	if insecureOverrideActive("http://backend.example") {
		t.Fatal("disabled insecure override reported active")
	}

	t.Setenv("ATL_ALLOW_INSECURE", "1")
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "https", url: "https://backend.example"},
		{name: "localhost", url: "http://localhost:8080"},
		{name: "ipv4 loopback", url: "http://127.0.0.1:8080"},
		{name: "ipv6 loopback", url: "http://[::1]:8080"},
		{name: "invalid", url: "://"},
		{name: "non-loopback http", url: "http://backend.example", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := insecureOverrideActive(test.url); got != test.want {
				t.Fatalf("insecureOverrideActive(%q)=%t want %t", test.url, got, test.want)
			}
		})
	}
}

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
		{context.Canceled, "request_failed", "request_canceled_or_timed_out"},
		{errors.New("private host: connection refused"), "request_failed", "request_failed"},
	}
	for _, test := range tests {
		status, reason := doctorRemoteFailure(test.err)
		if status != test.status || reason != test.reason {
			t.Errorf("doctorRemoteFailure(%v) = %q/%q, want %q/%q", test.err, status, reason, test.status, test.reason)
		}
	}
}

func TestDoctorCompatibilityRequiresObservedVersion(t *testing.T) {
	got := doctorCompatibility(domain.ServerProductConfluence, DoctorRemote{
		Status:  "available",
		Product: domain.ServerProductConfluence,
	})
	want := (DoctorCompatibility{Status: "unverified", Evidence: "metadata_only", Reason: "version_unavailable"})
	if got != want {
		t.Fatalf("doctorCompatibility(version unavailable) = %+v, want %+v", got, want)
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
