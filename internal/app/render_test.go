package app

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
)

func enabledSections(rs RenderSettings) []string {
	var out []string
	for k, v := range rs.Sections {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestProfileBases(t *testing.T) {
	cases := []struct {
		backend, profile string
		want             []string
	}{
		{"jira", "minimal", nil},
		{"jira", "default", []string{"assignee", "attachments", "comments", "labels", "links", "parent", "priority", "project", "status", "type"}},
		{"jira", "full", append([]string(nil), jiraFullSections...)},
		{"confluence", "minimal", nil},
		{"confluence", "default", nil},
		{"confluence", "full", []string{"comments", "frontmatter"}},
	}
	for _, c := range cases {
		rs, warns := computeSettings(c.backend, config.RenderService{Profile: c.profile})
		if len(warns) != 0 {
			t.Errorf("%s/%s: unexpected warnings %v", c.backend, c.profile, warns)
		}
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		if got := enabledSections(rs); !reflect.DeepEqual(got, want) {
			t.Errorf("%s/%s sections = %v, want %v", c.backend, c.profile, got, want)
		}
	}
}

func TestComputeSettingsIncludeExclude(t *testing.T) {
	// default + include sprint - comments.
	rs, warns := computeSettings("jira", config.RenderService{
		Profile: "default",
		Include: []string{"sprint"},
		Exclude: []string{"comments"},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if !rs.On("sprint") {
		t.Error("sprint should be enabled via include")
	}
	if rs.On("comments") {
		t.Error("comments should be removed via exclude")
	}
	// A field default keeps working.
	if !rs.On("priority") {
		t.Error("priority should remain from default profile")
	}
}

func TestComputeSettingsUnknownSectionWarns(t *testing.T) {
	rs, warns := computeSettings("jira", config.RenderService{
		Profile: "default",
		Include: []string{"bogus"},
		Exclude: []string{"alsobogus"},
	})
	if len(warns) != 2 {
		t.Fatalf("want 2 warnings, got %d: %v", len(warns), warns)
	}
	// The bad names never become sections.
	if rs.On("bogus") || rs.On("alsobogus") {
		t.Error("unknown section names must not enable anything")
	}
}

func TestComputeSettingsCustomFields(t *testing.T) {
	rs, _ := computeSettings("jira", config.RenderService{
		Profile:      "full",
		CustomFields: []string{"customfield_10001", "customfield_10002"},
	})
	if !reflect.DeepEqual(rs.CustomFields, []string{"customfield_10001", "customfield_10002"}) {
		t.Errorf("custom fields not carried: %v", rs.CustomFields)
	}
}

func TestComputeSettingsFieldViewsAndEpicChildren(t *testing.T) {
	rs, warns := computeSettings("jira", config.RenderService{
		Profile:   "full",
		Include:   []string{SecEpicChildren},
		EpicField: "customfield_10010",
		FieldViews: []config.JiraFieldView{
			{ID: "customfield_1", Key: "risk", Label: "Risk", Placement: "section", Format: "jira_wiki"},
			{ID: "customfield_2", Key: "score"},
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if !rs.On(SecEpicChildren) || rs.EpicField != "customfield_10010" {
		t.Fatalf("epic settings not carried: %+v", rs)
	}
	if len(rs.FieldViews) != 2 || rs.FieldViews[1].Placement != "frontmatter" || rs.FieldViews[1].Format != "auto" {
		t.Fatalf("field views not normalized: %+v", rs.FieldViews)
	}
	vs := viewStateOf(rs)
	restored := settingsFromViewState(vs)
	if !reflect.DeepEqual(restored.FieldViews, rs.FieldViews) || restored.EpicField != rs.EpicField || !restored.On(SecEpicChildren) {
		t.Fatalf("view state lost settings: restored=%+v original=%+v", restored, rs)
	}
}

func TestComputeSettingsRejectsDuplicateFieldViewKey(t *testing.T) {
	rs, warns := computeSettings("jira", config.RenderService{FieldViews: []config.JiraFieldView{
		{ID: "customfield_1", Key: "same"},
		{ID: "customfield_2", Key: "same"},
	}})
	if len(warns) != 1 || !strings.Contains(warns[0], "duplicate") {
		t.Fatalf("warnings = %v, want duplicate warning", warns)
	}
	if len(rs.FieldViews) != 1 || rs.FieldViews[0].ID != "customfield_1" {
		t.Fatalf("kept views = %+v", rs.FieldViews)
	}
}

func TestResolveRenderOverride(t *testing.T) {
	root := t.TempDir()
	// Write a local config selecting the full profile for jira.
	if err := config.SaveLocal(root, &config.LocalConfig{Render: &config.RenderConfig{
		Jira: &config.RenderService{Profile: "full"},
	}}); err != nil {
		t.Fatal(err)
	}
	// No override: local config wins -> full.
	rs, _ := ResolveRender(&config.Config{}, root, config.RenderService{}, "jira")
	if !rs.On("reporter") {
		t.Error("local full profile should enable reporter")
	}
	// Override to minimal: flag wins over local config.
	rs, _ = ResolveRender(&config.Config{}, root, config.RenderService{Profile: "minimal"}, "jira")
	if rs.On("reporter") || rs.On("status") {
		t.Error("minimal override should disable all optional sections")
	}
	// Exclude override on default.
	rs, _ = ResolveRender(&config.Config{}, root, config.RenderService{Profile: "default", Exclude: []string{"comments"}}, "jira")
	if rs.On("comments") {
		t.Error("exclude override should drop comments")
	}
	if !rs.On("priority") {
		t.Error("default keeps priority")
	}
}

func TestResolveRenderMissingLocalIsDefault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-mirror-here")
	rs, warns := ResolveRender(&config.Config{}, root, config.RenderService{}, "confluence")
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	// Confluence default is body-only: no sections enabled.
	if len(enabledSections(rs)) != 0 {
		t.Errorf("confluence default should enable no sections, got %v", enabledSections(rs))
	}
}
