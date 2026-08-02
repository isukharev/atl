package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanPullDryRunSafetyUsesEmptyActionsArray(t *testing.T) {
	data, err := json.Marshal(newPullLocalSafety(true, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"actions":[]`) {
		t.Fatalf("local safety JSON = %s", data)
	}
}
