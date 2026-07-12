package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeJiraListViewsOverlaysEverySource(t *testing.T) {
	user := map[string]JiraListView{
		"planning": {Description: "Planning", Board: []string{"key", "priority"}},
	}
	views, err := NormalizeJiraListViews(user)
	if err != nil {
		t.Fatal(err)
	}
	if len(views["default"].Search) == 0 || len(views["full"].Structure) == 0 {
		t.Fatalf("built-ins missing: %+v", views)
	}
	planning := views["planning"]
	if !reflect.DeepEqual(planning.Board, []string{"key", "priority"}) {
		t.Fatalf("board override=%v", planning.Board)
	}
	for source, columns := range map[string][]string{
		JiraListSourceSearch: planning.Search, JiraListSourceEpicChildren: planning.EpicChildren,
		JiraListSourceBoardSnapshot: planning.BoardSnapshot, JiraListSourceSprint: planning.Sprint,
		JiraListSourceStructure: planning.Structure, JiraListSourceConfluenceMacro: planning.ConfluenceMacro,
	} {
		if len(columns) == 0 {
			t.Errorf("source %s did not inherit default", source)
		}
	}
}

func TestResolveJiraListViewEverySource(t *testing.T) {
	defaults := DefaultJiraListViews()["default"]
	for source, want := range map[string][]string{
		JiraListSourceSearch: defaults.Search, JiraListSourceEpicChildren: defaults.EpicChildren,
		JiraListSourceBoard: defaults.Board, JiraListSourceBoardSnapshot: defaults.BoardSnapshot,
		JiraListSourceSprint: defaults.Sprint, JiraListSourceStructure: defaults.Structure,
		JiraListSourceConfluenceMacro: defaults.ConfluenceMacro,
	} {
		got, name, err := ResolveJiraListView(nil, "", source)
		if err != nil || name != "default" || !reflect.DeepEqual(got, want) {
			t.Errorf("source=%s columns=%v name=%q err=%v", source, got, name, err)
		}
	}
}

func TestNormalizeJiraListViewsRejectsInvalidCatalog(t *testing.T) {
	tests := []struct {
		name     string
		viewName string
		view     JiraListView
		want     string
	}{
		{name: "name", viewName: "Bad Name", view: JiraListView{Search: []string{"key"}}, want: "must match"},
		{name: "duplicate", viewName: "bad", view: JiraListView{Search: []string{"key", "key"}}, want: "repeats column"},
		{name: "empty", viewName: "bad", view: JiraListView{Search: []string{" "}}, want: "empty column"},
		{name: "source", viewName: "bad", view: JiraListView{Search: []string{"board.column"}}, want: "not available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeJiraListViews(map[string]JiraListView{tt.viewName: tt.view})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestSetJiraListViewsJSONRoundTripAndDelete(t *testing.T) {
	views, err := SetJiraListViewsJSON(nil, "jira.list_views.planning", `{"board":["key","priority"]}`)
	if err != nil || len(views["planning"].Board) == 0 {
		t.Fatalf("set=%+v err=%v", views, err)
	}
	views, err = SetJiraListViewsJSON(views, "jira.list_views.planning", "null")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := views["planning"]; ok {
		t.Fatalf("planning was not deleted: %+v", views)
	}
	if len(views["default"].Search) == 0 || len(views["full"].Search) == 0 {
		t.Fatalf("built-ins disappeared: %+v", views)
	}
}
