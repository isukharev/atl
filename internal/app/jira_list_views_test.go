package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func TestResolveListColumnsPrecedenceForEverySource(t *testing.T) {
	view := config.JiraListView{
		Search:          []string{"search_field"},
		EpicChildren:    []string{"epic_field"},
		Board:           []string{"board_field"},
		BoardSnapshot:   []string{"board_snapshot_field"},
		Sprint:          []string{"sprint_field"},
		Structure:       []string{"structure_field"},
		ConfluenceMacro: []string{"macro_field"},
	}
	service := &JiraService{cfg: &config.Config{JiraListViews: map[string]config.JiraListView{"planning": view}}}
	tests := []struct {
		source string
		want   []string
	}{
		{config.JiraListSourceSearch, view.Search},
		{config.JiraListSourceEpicChildren, view.EpicChildren},
		{config.JiraListSourceBoard, view.Board},
		{config.JiraListSourceBoardSnapshot, view.BoardSnapshot},
		{config.JiraListSourceSprint, view.Sprint},
		{config.JiraListSourceStructure, view.Structure},
		{config.JiraListSourceConfluenceMacro, view.ConfluenceMacro},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			got, preset, err := service.resolveListColumns(tt.source, "planning", nil)
			if err != nil || preset != "planning" || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("resolve named view = (%v, %q, %v), want (%v, planning, nil)", got, preset, err, tt.want)
			}
			got, preset, err = service.resolveListColumns(tt.source, "missing", []string{"explicit_field"})
			if err != nil || preset != "explicit" || !reflect.DeepEqual(got, []string{"explicit_field"}) {
				t.Fatalf("explicit projection = (%v, %q, %v)", got, preset, err)
			}
		})
	}
}

func TestResolveListColumnsRejectsUnknownNamedViewBeforeUse(t *testing.T) {
	service := &JiraService{cfg: &config.Config{}}
	_, _, err := service.resolveListColumns(config.JiraListSourceSearch, "missing", nil)
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want ErrUsage", err)
	}
}
