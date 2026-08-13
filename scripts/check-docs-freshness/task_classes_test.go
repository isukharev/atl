package main

import (
	"reflect"
	"testing"
)

func TestParsePublishedTaskClassesSelectsOnlyExactNamedFence(t *testing.T) {
	body := []byte("# Agent interfaces\n\n```json workflow-classes\n[\"triage\", \"report\"]\n```\n\n" + taskClassFence + "\n[\"jira/evidence\", \"knowledge/search\"]\n```\n")
	got, err := parsePublishedTaskClasses(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"jira/evidence", "knowledge/search"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("task classes=%v want=%v", got, want)
	}
}

func TestParsePublishedTaskClassesFailsClosed(t *testing.T) {
	tests := map[string]string{
		"missing":      "```json\n[]\n```\n",
		"duplicated":   taskClassFence + "\n[]\n```\n" + taskClassFence + "\n[]\n```\n",
		"unterminated": taskClassFence + "\n[]\n",
		"unsorted":     taskClassFence + "\n[\"z\",\"a\"]\n```\n",
		"duplicate":    taskClassFence + "\n[\"a\",\"a\"]\n```\n",
		"non-array":    taskClassFence + "\n{}\n```\n",
		"trailing":     taskClassFence + "\n[] {}\n```\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePublishedTaskClasses([]byte(body)); err == nil {
				t.Fatal("invalid published task class block passed")
			}
		})
	}
}
