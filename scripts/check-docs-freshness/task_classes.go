package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/capability"
)

const (
	taskClassDocumentPath = "docs/reference/cli/agent-interfaces.md"
	taskClassFence        = "```json capability-task-classes"
)

func validatePublishedTaskClasses(root string) (int, error) {
	body, err := readRegular(filepath.Join(root, filepath.FromSlash(taskClassDocumentPath)), maxTrackedFileBytes)
	if err != nil {
		return 0, errors.New("published capability task classes are unavailable")
	}
	got, err := parsePublishedTaskClasses(body)
	if err != nil {
		return 0, err
	}
	want := capability.TaskClasses()
	if !reflect.DeepEqual(got, want) {
		return 0, fmt.Errorf("published capability task classes drift from capability.TaskClasses()")
	}
	return len(got), nil
}

func parsePublishedTaskClasses(body []byte) ([]string, error) {
	lines := strings.Split(string(body), "\n")
	var blocks [][]byte
	for index := 0; index < len(lines); index++ {
		if lines[index] != taskClassFence {
			continue
		}
		var content strings.Builder
		closed := false
		for index++; index < len(lines); index++ {
			if lines[index] == "```" {
				closed = true
				break
			}
			content.WriteString(lines[index])
			content.WriteByte('\n')
		}
		if !closed {
			return nil, errors.New("capability task-class fence is unterminated")
		}
		blocks = append(blocks, []byte(content.String()))
	}
	if len(blocks) != 1 {
		return nil, fmt.Errorf("capability task-class document requires one exact named JSON fence, found %d", len(blocks))
	}
	var classes []string
	decoder := json.NewDecoder(bytes.NewReader(blocks[0]))
	if err := decoder.Decode(&classes); err != nil || classes == nil {
		return nil, errors.New("capability task-class fence must contain one JSON string array")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("capability task-class fence contains trailing JSON data")
	}
	if !sort.StringsAreSorted(classes) {
		return nil, errors.New("published capability task classes must be sorted")
	}
	for index, class := range classes {
		if strings.TrimSpace(class) != class || class == "" || index > 0 && classes[index-1] == class {
			return nil, errors.New("published capability task classes must be non-empty, normalized, and unique")
		}
	}
	return classes, nil
}
