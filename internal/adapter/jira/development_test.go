package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const (
	developmentTestProject = "https://scm.example.test/group/subgroup/project"
	developmentTestSHA     = "0123456789abcdef0123456789abcdef01234567"
)

func TestReadIssueDevelopmentExactSequenceAndBothDetailTopologies(t *testing.T) {
	var requests []string
	var trace bytes.Buffer
	httpx.SetTrace(&trace)
	t.Cleanup(func() { httpx.SetTrace(nil) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		switch r.URL.Path {
		case "/rest/dev-status/1.0/issue/summary":
			_, _ = w.Write(developmentSummary(t, map[string]map[string]int{
				"repository": {"GitLab": 1}, "branch": {"GitLab": 1}, "pullrequest": {"GitLab": 1},
				"build": {"Synthetic": 9}, "review": {}, "deployment": {}, "deployment-environment": {}, "featureflag": {},
			}))
		case "/rest/dev-status/1.0/issue/detail":
			switch r.URL.Query().Get("dataType") {
			case "repository":
				_, _ = w.Write(developmentDetail(t, []any{map[string]any{
					"repositories": []any{map[string]any{
						"url":     developmentTestProject,
						"commits": []any{developmentCommitFixture(developmentTestProject, developmentTestSHA)},
						"message": "PRIVATE NARRATIVE", "author": map[string]any{"email": "person@example.test"},
					}},
				}}))
			case "branch":
				_, _ = w.Write(developmentDetailWithoutConfig(t, []any{map[string]any{
					"branches": []any{developmentBranchFixture(developmentTestProject, "feature/EX-1")},
				}}))
			case "pullrequest":
				_, _ = w.Write(developmentDetail(t, []any{map[string]any{
					"pullRequests": []any{developmentMRFixture(developmentTestProject, "7", "OPEN")},
				}}))
			default:
				http.Error(w, "unexpected selector", http.StatusBadRequest)
			}
		default:
			http.Error(w, "artifact fetch attempted", http.StatusTeapot)
		}
	}))
	defer server.Close()

	got, err := New(server.URL, "token", "test").ReadIssueDevelopment(context.Background(), "12345")
	if err != nil {
		t.Fatal(err)
	}
	wantRequests := []string{
		"/rest/dev-status/1.0/issue/summary?issueId=12345",
		"/rest/dev-status/1.0/issue/detail?applicationType=GitLab&dataType=repository&issueId=12345",
		"/rest/dev-status/1.0/issue/detail?applicationType=GitLab&dataType=branch&issueId=12345",
		"/rest/dev-status/1.0/issue/detail?applicationType=GitLab&dataType=pullrequest&issueId=12345",
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("requests = %#v, want %#v", requests, wantRequests)
	}
	if len(got.Projects) != 1 || len(got.Commits) != 1 || len(got.Branches) != 1 || len(got.MergeRequests) != 1 {
		t.Fatalf("inventory = %#v", got)
	}
	if got.Commits[0].SHA != developmentTestSHA || got.Branches[0].Name != "feature/EX-1" ||
		got.MergeRequests[0].IID != "7" || got.MergeRequests[0].State != "open" {
		t.Fatalf("inventory identities = %#v", got)
	}
	for _, secret := range []string{"12345", "GitLab", "PRIVATE NARRATIVE", "person@example.test"} {
		if strings.Contains(trace.String(), secret) {
			t.Fatalf("trace leaked %q: %s", secret, trace.String())
		}
	}
}

func TestReadIssueDevelopmentProvesEmptyWithoutDetailRequests(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/rest/dev-status/1.0/issue/summary" {
			t.Fatalf("unexpected request %s", r.URL.RequestURI())
		}
		_, _ = w.Write(developmentSummary(t, map[string]map[string]int{
			"repository": {}, "branch": {}, "pullrequest": {},
		}))
	}))
	defer server.Close()

	got, err := New(server.URL, "token", "test").ReadIssueDevelopment(context.Background(), "1")
	if err != nil || hits != 1 || len(got.Projects)+len(got.Commits)+len(got.Branches)+len(got.MergeRequests) != 0 {
		t.Fatalf("inventory=%#v error=%v hits=%d", got, err, hits)
	}
}

func TestReadIssueDevelopmentRejectsInvalidIDBeforeNetwork(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	for _, value := range []string{"", "0", "-1", "1.0", strings.Repeat("9", 21)} {
		_, err := New(server.URL, "token", "test").ReadIssueDevelopment(context.Background(), value)
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("id %q error = %v", value, err)
		}
	}
	if hits != 0 {
		t.Fatalf("invalid ids reached network %d times", hits)
	}
}

func TestDevelopmentSummaryClosedSemantics(t *testing.T) {
	valid := developmentSummary(t, map[string]map[string]int{"repository": {}, "branch": {}, "pullrequest": {}})
	if selectors, err := decodeDevelopmentSummary(valid); err != nil || len(selectors) != 0 {
		t.Fatalf("valid empty summary: selectors=%v error=%v", selectors, err)
	}
	selfManaged := developmentSummary(t, map[string]map[string]int{
		"repository": {"GitLabSelfManaged": 1}, "branch": {}, "pullrequest": {},
	})
	if selectors, err := decodeDevelopmentSummary(selfManaged); err != nil || len(selectors) != 1 {
		t.Fatalf("self-managed GitLab selector: selectors=%v error=%v", selectors, err)
	}
	tests := map[string]string{
		"missing relevant":    `{"errors":[],"configErrors":[],"summary":{"repository":{"overall":{"count":0},"byInstanceType":{}},"branch":{"overall":{"count":0},"byInstanceType":{}}}}`,
		"plugin error":        `{"errors":[{"message":"PRIVATE"}],"configErrors":[],"summary":{}}`,
		"fractional":          `{"errors":[],"configErrors":[],"summary":{"repository":{"overall":{"count":1.5},"byInstanceType":{}},"branch":{"overall":{"count":0},"byInstanceType":{}},"pullrequest":{"overall":{"count":0},"byInstanceType":{}}}}`,
		"mismatch":            `{"errors":[],"configErrors":[],"summary":{"repository":{"overall":{"count":2},"byInstanceType":{"scm":{"count":1}}},"branch":{"overall":{"count":0},"byInstanceType":{}},"pullrequest":{"overall":{"count":0},"byInstanceType":{}}}}`,
		"case collision":      `{"errors":[],"configErrors":[],"summary":{"repository":{"overall":{"count":2},"byInstanceType":{"SCM":{"count":1},"scm":{"count":1}}},"branch":{"overall":{"count":0},"byInstanceType":{}},"pullrequest":{"overall":{"count":0},"byInstanceType":{}}}}`,
		"unknown nonzero":     `{"errors":[],"configErrors":[],"summary":{"repository":{"overall":{"count":0},"byInstanceType":{}},"branch":{"overall":{"count":0},"byInstanceType":{}},"pullrequest":{"overall":{"count":0},"byInstanceType":{}},"future":{"overall":{"count":1},"byInstanceType":{"scm":{"count":1}}}}}`,
		"non GitLab provider": `{"errors":[],"configErrors":[],"summary":{"repository":{"overall":{"count":1},"byInstanceType":{"GitHub":{"count":1}}},"branch":{"overall":{"count":0},"byInstanceType":{}},"pullrequest":{"overall":{"count":0},"byInstanceType":{}}}}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeDevelopmentSummary([]byte(raw))
			if !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(fmt.Sprint(err), "PRIVATE") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	unknownZero := []byte(`{"errors":[],"configErrors":[],"summary":{"repository":{"overall":{"count":0},"byInstanceType":{}},"branch":{"overall":{"count":0},"byInstanceType":{}},"pullrequest":{"overall":{"count":0},"byInstanceType":{}},"future":{"overall":{"count":0},"byInstanceType":{}}}}`)
	if _, err := decodeDevelopmentSummary(unknownZero); err != nil {
		t.Fatalf("unknown zero category rejected: %v", err)
	}

	applications := map[string]int{}
	for index := 0; index < developmentMaxApplications; index++ {
		applications[fmt.Sprintf("gitlab-%d", index)] = 1
	}
	atLimit := developmentSummary(t, map[string]map[string]int{
		"repository": applications, "branch": applications, "pullrequest": applications,
	})
	if selectors, err := decodeDevelopmentSummary(atLimit); err != nil || len(selectors) != developmentMaxSelectors {
		t.Fatalf("selectors at cap = %d, error=%v", len(selectors), err)
	}
	applications["gitlab-over"] = 1
	overLimit := developmentSummary(t, map[string]map[string]int{
		"repository": applications, "branch": applications, "pullrequest": applications,
	})
	if _, err := decodeDevelopmentSummary(overLimit); !errors.Is(err, domain.ErrOutputLimit) {
		t.Fatalf("application/selector cap error=%v", err)
	}
}

func TestDevelopmentDetailRejectsNullPluginErrorsAndSelectorMismatch(t *testing.T) {
	inv := newDevelopmentInventory()
	for _, raw := range []string{
		`{"errors":[],"detail":null}`,
		`{"errors":null,"detail":[]}`,
		`{"errors":[],"configErrors":[{"message":"PRIVATE"}],"detail":[]}`,
	} {
		if _, err := inv.addDetail([]byte(raw), "repository"); !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(fmt.Sprint(err), "PRIVATE") {
			t.Fatalf("raw=%s error=%v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"errors":[],"detail":[{"branches":null}]}`,
		`{"errors":[],"detail":[{"repositories":[{"url":"https://scm.example.test/g/p","commits":null}]}]}`,
	} {
		if _, err := newDevelopmentInventory().addDetail([]byte(raw), "repository"); err != nil {
			t.Fatalf("optional null array rejected: raw=%s error=%v", raw, err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/summary") {
			_, _ = w.Write(developmentSummary(t, map[string]map[string]int{
				"repository": {"GitLab": 1}, "branch": {}, "pullrequest": {},
			}))
			return
		}
		_, _ = w.Write(developmentDetail(t, []any{}))
	}))
	defer server.Close()
	got, err := New(server.URL, "token", "test").ReadIssueDevelopment(context.Background(), "7")
	if !errors.Is(err, domain.ErrCheckFailed) || len(got.Commits) != 0 {
		t.Fatalf("inventory=%#v error=%v", got, err)
	}
}

func TestDevelopmentReconcilesPerSelectorAndRejectsMRConflict(t *testing.T) {
	all := []any{map[string]any{
		"repositories": []any{map[string]any{
			"url":          developmentTestProject,
			"commits":      []any{developmentCommitFixture(developmentTestProject, developmentTestSHA)},
			"branches":     []any{developmentBranchFixture(developmentTestProject, "feature/EX-1")},
			"pullRequests": []any{developmentMRFixture(developmentTestProject, "7", "OPEN")},
		}},
	}}
	inv := newDevelopmentInventory()
	for _, selected := range []string{"repository", "branch", "pullrequest"} {
		count, err := inv.addDetail(developmentDetail(t, all), selected)
		if err != nil || count != 1 {
			t.Fatalf("selected=%s count=%d error=%v", selected, count, err)
		}
	}
	got := inv.normalized()
	if len(got.Projects) != 1 || len(got.Commits) != 1 || len(got.Branches) != 1 || len(got.MergeRequests) != 1 {
		t.Fatalf("duplicates not deduped: %#v", got)
	}
	conflict := developmentDetail(t, []any{map[string]any{
		"pullRequests": []any{developmentMRFixture(developmentTestProject, "7", "CLOSED")},
	}})
	if _, err := inv.addDetail(conflict, "pullrequest"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("conflicting MR state error = %v", err)
	}
}

func TestDevelopmentMergeRequestUsesURLIIDWhenRawIDIsOpaque(t *testing.T) {
	fixture := developmentMRFixture(developmentTestProject, "7", "OPEN")
	fixture["id"] = "provider-global-id"
	inv := newDevelopmentInventory()
	count, err := inv.addDetail(developmentDetail(t, []any{map[string]any{
		"pullRequests": []any{fixture},
	}}), "pullrequest")
	if err != nil || count != 1 {
		t.Fatalf("count=%d error=%v", count, err)
	}
	got := inv.normalized()
	if len(got.MergeRequests) != 1 || got.MergeRequests[0].IID != "7" {
		t.Fatalf("merge requests = %#v", got.MergeRequests)
	}
}

func TestReadIssueDevelopmentRejectsGlobalDedupeCountMismatchAcrossApplications(t *testing.T) {
	detailHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/summary") {
			_, _ = w.Write(developmentSummary(t, map[string]map[string]int{
				"repository": {"GitLab-A": 1, "GitLab-B": 1}, "branch": {}, "pullrequest": {},
			}))
			return
		}
		detailHits++
		_, _ = w.Write(developmentDetail(t, []any{map[string]any{
			"repositories": []any{map[string]any{
				"url":     developmentTestProject,
				"commits": []any{developmentCommitFixture(developmentTestProject, developmentTestSHA)},
			}},
		}}))
	}))
	defer server.Close()
	got, err := New(server.URL, "token", "test").ReadIssueDevelopment(context.Background(), "9")
	if !errors.Is(err, domain.ErrCheckFailed) || detailHits != 2 || len(got.Commits) != 0 {
		t.Fatalf("inventory=%#v error=%v detail_hits=%d", got, err, detailHits)
	}
}

func TestDevelopmentIdentityGrammar(t *testing.T) {
	project, ok := parseDevelopmentProject("https://SCM.Example.Test:443/group/sub/project.git")
	if !ok || project.host != "scm.example.test" || project.path != "group/sub/project" {
		t.Fatalf("project=%#v ok=%v", project, ok)
	}
	project, ok = parseDevelopmentProject("https://SCM.Example.Test:0443/group/sub/project.git")
	if !ok || project.host != "scm.example.test" || project.path != "group/sub/project" {
		t.Fatalf("zero-padded default port project=%#v ok=%v", project, ok)
	}
	for _, raw := range []string{
		"http://scm.example.test/g/p", "https://user@scm.example.test/g/p",
		"https://scm.example.test/g/p?token=x", "https://scm.example.test/g/%2F/p",
		"https://scm.example.test:70000/g/p",
	} {
		if _, ok := parseDevelopmentProject(raw); ok {
			t.Fatalf("unsafe project accepted: %s", raw)
		}
	}
	for _, sha := range []string{developmentTestSHA, strings.Repeat("a", 64)} {
		project, value, ok := parseDevelopmentArtifact(developmentTestProject+"/-/commit/"+sha, "commit", sha)
		if !ok || value != sha || project.path != "group/subgroup/project" {
			t.Fatalf("commit %s rejected", sha)
		}
	}
	if _, _, ok := parseDevelopmentArtifact(developmentTestProject+"/commit/"+developmentTestSHA, "commit", developmentTestSHA); !ok {
		t.Fatal("legacy commit path rejected")
	}
	branch := "feature/ветка/EX-1"
	escapedBranch := urlPathEscapeBranch(branch)
	if _, value, ok := parseDevelopmentArtifact(developmentTestProject+"/-/tree/"+escapedBranch, "tree", branch); !ok || value != branch {
		t.Fatalf("branch value=%q ok=%v", value, ok)
	}
	if validDevelopmentBranch("bad\nbranch") || validDevelopmentBranch("") {
		t.Fatal("invalid branch accepted")
	}

	inv := newDevelopmentInventory()
	badCommit := developmentCommitFixture(developmentTestProject, developmentTestSHA)
	badCommit["url"] = "https://other.example.test/group/project/-/commit/" + developmentTestSHA
	badCommit["repository"] = map[string]any{"url": developmentTestProject}
	badMR := developmentMRFixture(developmentTestProject, "7", "OPEN")
	badMR["id"] = "8"
	for _, item := range []struct {
		kind string
		body map[string]any
	}{
		{"commits", badCommit}, {"pullRequests", badMR},
	} {
		raw := developmentDetail(t, []any{map[string]any{item.kind: []any{item.body}}})
		if _, err := inv.addDetail(raw, map[string]string{"commits": "repository", "pullRequests": "pullrequest"}[item.kind]); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("%s conflict error=%v", item.kind, err)
		}
	}
}

func TestDevelopmentCapsAtBoundaries(t *testing.T) {
	for _, count := range []int{developmentMaxGroups - 1, developmentMaxGroups, developmentMaxGroups + 1} {
		t.Run(fmt.Sprintf("groups_%d", count), func(t *testing.T) {
			groups := make([]any, count)
			for index := range groups {
				groups[index] = map[string]any{}
			}
			_, err := newDevelopmentInventory().addDetail(developmentDetail(t, groups), "repository")
			if count <= developmentMaxGroups && err != nil {
				t.Fatal(err)
			}
			if count > developmentMaxGroups && !errors.Is(err, domain.ErrOutputLimit) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	aggregate := newDevelopmentInventory()
	first := make([]any, developmentMaxGroups/2)
	second := make([]any, developmentMaxGroups/2+1)
	for index := range first {
		first[index] = map[string]any{}
	}
	for index := range second {
		second[index] = map[string]any{}
	}
	if _, err := aggregate.addDetail(developmentDetail(t, first), "repository"); err != nil {
		t.Fatal(err)
	}
	if _, err := aggregate.addDetail(developmentDetail(t, second), "repository"); !errors.Is(err, domain.ErrOutputLimit) {
		t.Fatalf("aggregate group cap error=%v", err)
	}
	for _, count := range []int{developmentMaxCommits - 1, developmentMaxCommits, developmentMaxCommits + 1} {
		t.Run(fmt.Sprintf("commits_%d", count), func(t *testing.T) {
			commits := make([]any, count)
			for index := range commits {
				sha := fmt.Sprintf("%040x", index+1)
				commits[index] = developmentCommitFixture(developmentTestProject, sha)
			}
			body := developmentDetail(t, []any{map[string]any{"repositories": []any{map[string]any{"url": developmentTestProject, "commits": commits}}}})
			_, err := newDevelopmentInventory().addDetail(body, "repository")
			if count <= developmentMaxCommits && err != nil {
				t.Fatal(err)
			}
			if count > developmentMaxCommits && !errors.Is(err, domain.ErrOutputLimit) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for _, size := range []int{developmentMaxBranchBytes - 1, developmentMaxBranchBytes, developmentMaxBranchBytes + 1} {
		name := strings.Repeat("a", size)
		if got := validDevelopmentBranch(name); got != (size <= developmentMaxBranchBytes) {
			t.Fatalf("branch bytes=%d valid=%v", size, got)
		}
	}
	for _, count := range []int{developmentMaxArtifacts - 1, developmentMaxArtifacts, developmentMaxArtifacts + 1} {
		t.Run(fmt.Sprintf("artifact_sightings_%d", count), func(t *testing.T) {
			commits := make([]any, count)
			for index := range commits {
				commits[index] = developmentCommitFixture(developmentTestProject, developmentTestSHA)
			}
			body := developmentDetail(t, []any{map[string]any{"repositories": []any{map[string]any{"url": developmentTestProject, "commits": commits}}}})
			_, err := newDevelopmentInventory().addDetail(body, "repository")
			if count <= developmentMaxArtifacts && err != nil {
				t.Fatal(err)
			}
			if count > developmentMaxArtifacts && !errors.Is(err, domain.ErrOutputLimit) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestReadIssueDevelopmentStaticHTTPMappingsAndNoRetryOrRedirect(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, domain.ErrAuth}, {http.StatusForbidden, domain.ErrForbidden},
		{http.StatusNotFound, domain.ErrNotFound}, {http.StatusMethodNotAllowed, domain.ErrNotFound},
		{http.StatusTooManyRequests, nil}, {http.StatusInternalServerError, nil},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			hits := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits++
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte("PRIVATE BODY person@example.test"))
			}))
			defer server.Close()
			_, err := New(server.URL, "token", "test").ReadIssueDevelopment(context.Background(), "1")
			if err == nil || hits != 1 || strings.Contains(fmt.Sprint(err), "PRIVATE") || strings.Contains(fmt.Sprint(err), "example.test") {
				t.Fatalf("error=%v hits=%d", err, hits)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}

	var sourceHits, targetHits int
	var redirect *httptest.Server
	redirect = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			targetHits++
			return
		}
		sourceHits++
		w.Header().Set("Location", redirect.URL+"/target")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err := New(redirect.URL, "token", "test").ReadIssueDevelopment(context.Background(), "1")
	if err == nil || sourceHits != 1 || targetHits != 0 {
		t.Fatalf("error=%v source=%d target=%d", err, sourceHits, targetHits)
	}

	budget, _ := domain.NewReadBudget(0, 1024)
	_, err = New(redirect.URL, "token", "test").ReadIssueDevelopment(domain.WithReadBudget(context.Background(), budget), "1")
	if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
		t.Fatalf("budget error=%v", err)
	}
}

func developmentSummary(t *testing.T, categories map[string]map[string]int) []byte {
	t.Helper()
	summary := map[string]any{}
	for category, applications := range categories {
		by := map[string]any{}
		total := 0
		for application, count := range applications {
			by[application] = map[string]int{"count": count}
			total += count
		}
		summary[category] = map[string]any{"overall": map[string]int{"count": total}, "byInstanceType": by}
	}
	return developmentJSON(t, map[string]any{"errors": []any{}, "configErrors": []any{}, "summary": summary})
}

func developmentDetail(t *testing.T, groups []any) []byte {
	t.Helper()
	return developmentJSON(t, map[string]any{"errors": []any{}, "configErrors": []any{}, "detail": groups})
}

func developmentDetailWithoutConfig(t *testing.T, groups []any) []byte {
	t.Helper()
	return developmentJSON(t, map[string]any{"errors": []any{}, "detail": groups})
}

func developmentJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func developmentCommitFixture(project, sha string) map[string]any {
	return map[string]any{"id": sha, "url": project + "/-/commit/" + sha}
}

func developmentBranchFixture(project, name string) map[string]any {
	return map[string]any{
		"name": name, "url": project + "/-/tree/" + urlPathEscapeBranch(name),
		"repository": map[string]any{"url": project},
	}
}

func developmentMRFixture(project, iid, state string) map[string]any {
	return map[string]any{
		"id": iid, "url": project + "/-/merge_requests/" + iid, "status": state,
		"repository": map[string]any{"url": project},
	}
}

func urlPathEscapeBranch(value string) string {
	parts := strings.Split(value, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func FuzzDevelopmentResponseDecoders(f *testing.F) {
	f.Add(`{"errors":[],"configErrors":[],"summary":{"repository":{"overall":{"count":0},"byInstanceType":{}},"branch":{"overall":{"count":0},"byInstanceType":{}},"pullrequest":{"overall":{"count":0},"byInstanceType":{}}}}`)
	f.Add(`{"errors":[],"detail":[]}`)
	f.Add(`{"errors":null,"detail":[{"repositories":null}]}`)
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = decodeDevelopmentSummary([]byte(raw))
		for _, kind := range []string{"repository", "branch", "pullrequest"} {
			inv := newDevelopmentInventory()
			_, _ = inv.addDetail([]byte(raw), kind)
			got := inv.normalized()
			for _, project := range got.Projects {
				if !utf8.ValidString(project.Host) || !utf8.ValidString(project.ProjectPath) {
					t.Fatal("decoder emitted invalid UTF-8")
				}
			}
		}
	})
}

func FuzzDevelopmentIdentityNormalization(f *testing.F) {
	f.Add("https://scm.example.test/group/project", "feature/graph")
	f.Add("https://user@scm.example.test/group/project?token=x", "bad\nbranch")
	f.Add("https://scm.example.test/group/project.git", "release/v1")
	f.Fuzz(func(t *testing.T, rawURL, branch string) {
		project, ok := parseDevelopmentProject(rawURL)
		if ok {
			if project.host == "" || project.path == "" || !utf8.ValidString(project.host) || !utf8.ValidString(project.path) {
				t.Fatalf("invalid normalized project: %#v", project)
			}
		}
		if validDevelopmentBranch(branch) && (!utf8.ValidString(branch) || len(branch) > developmentMaxBranchBytes) {
			t.Fatalf("invalid branch accepted: %q", branch)
		}
	})
}
