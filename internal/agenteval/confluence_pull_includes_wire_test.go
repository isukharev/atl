package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

type confluenceSelectionPullIncludeWire struct {
	Dimension     string `json:"dimension"`
	Requested     bool   `json:"requested"`
	Qualification string `json:"qualification"`
	Complete      *bool  `json:"complete,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

func decodeConfluenceSelectionPullWire(data []byte) (confluenceSelectionPullWire, error) {
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return confluenceSelectionPullWire{}, fmt.Errorf("decode Confluence pull members: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return confluenceSelectionPullWire{}, fmt.Errorf("decode Confluence pull members: %w", err)
	}
	if root == nil {
		return confluenceSelectionPullWire{}, fmt.Errorf("Confluence pull wire must be an object")
	}
	if err := requireConfluenceSelectionMembers(root, "Confluence pull",
		[]string{"root", "pages", "includes"}, []string{"truncated", "truncated_at"}); err != nil {
		return confluenceSelectionPullWire{}, err
	}
	if err := rejectNullConfluenceSelectionMembers(root, "Confluence pull",
		[]string{"root", "pages", "includes", "truncated", "truncated_at"}); err != nil {
		return confluenceSelectionPullWire{}, err
	}

	var includes []map[string]json.RawMessage
	if err := json.Unmarshal(root["includes"], &includes); err != nil {
		return confluenceSelectionPullWire{}, fmt.Errorf("decode Confluence pull includes: %w", err)
	}
	if len(includes) != 2 {
		return confluenceSelectionPullWire{}, fmt.Errorf("Confluence pull includes=%d, want exactly 2", len(includes))
	}
	for index, include := range includes {
		owner := fmt.Sprintf("Confluence pull include[%d]", index)
		if err := requireConfluenceSelectionMembers(include, owner,
			[]string{"dimension", "requested", "qualification"}, []string{"complete", "reason"}); err != nil {
			return confluenceSelectionPullWire{}, err
		}
		if err := rejectNullConfluenceSelectionMembers(include, owner,
			[]string{"dimension", "requested", "qualification", "complete", "reason"}); err != nil {
			return confluenceSelectionPullWire{}, err
		}
		if err := requireNonemptyOptionalConfluenceSelectionStrings(include, owner, []string{"reason"}); err != nil {
			return confluenceSelectionPullWire{}, err
		}
	}

	var wire confluenceSelectionPullWire
	if err := decodeStrict(bytes.NewReader(data), &wire); err != nil {
		return confluenceSelectionPullWire{}, err
	}
	if wire.Root == "" || wire.Pages == nil {
		return confluenceSelectionPullWire{}, fmt.Errorf("Confluence pull wire has an empty root or null pages")
	}
	wantDimensions := []string{"assets", "comments"}
	for index, include := range wire.Includes {
		if include.Dimension != wantDimensions[index] {
			return confluenceSelectionPullWire{}, fmt.Errorf("Confluence pull include[%d] dimension is unknown or out of order", index)
		}
		hasComplete := includes[index]["complete"] != nil
		hasReason := includes[index]["reason"] != nil
		if err := validateConfluenceSelectionPullInclude(include, hasComplete, hasReason); err != nil {
			return confluenceSelectionPullWire{}, fmt.Errorf("Confluence pull include[%d]: %w", index, err)
		}
	}
	hasTruncated := root["truncated"] != nil
	hasTruncatedAt := root["truncated_at"] != nil
	if wire.Truncated != hasTruncated || wire.Truncated != hasTruncatedAt || (wire.Truncated && wire.TruncatedAt < 1) {
		return confluenceSelectionPullWire{}, fmt.Errorf("Confluence pull truncation members are contradictory")
	}
	return wire, nil
}

func validateConfluenceSelectionPullInclude(
	include confluenceSelectionPullIncludeWire,
	hasComplete, hasReason bool,
) error {
	switch include.Qualification {
	case "not_requested":
		if include.Requested || hasComplete || hasReason {
			return fmt.Errorf("not_requested must be unrequested and omit complete and reason")
		}
	case "deferred":
		if !include.Requested || hasComplete || !hasReason ||
			(include.Reason != "preview_deferred" && include.Reason != "not_attempted") {
			return fmt.Errorf("deferred must be requested, omit complete, and carry a deferred reason")
		}
	case "qualified":
		if !include.Requested || !hasComplete || include.Complete == nil || !*include.Complete || hasReason {
			return fmt.Errorf("qualified must be requested, complete, and omit reason")
		}
	case "partial":
		if !include.Requested || !hasComplete || include.Complete == nil || *include.Complete || !hasReason ||
			(include.Reason != "resolution_incomplete" && include.Reason != "inventory_incomplete" && include.Reason != "not_attempted") {
			return fmt.Errorf("partial must be requested, incomplete, and carry a partial reason")
		}
	case "failed":
		if !include.Requested || !hasComplete || include.Complete == nil || *include.Complete || !hasReason ||
			(include.Reason != "read_failed" && include.Reason != "staging_failed") {
			return fmt.Errorf("failed must be requested, incomplete, and carry a failure reason")
		}
	default:
		return fmt.Errorf("qualification is outside the released vocabulary")
	}
	return nil
}

func TestConfluenceSelectionPullWireAcceptsReleasedIncludes(t *testing.T) {
	rows := map[string]map[string]any{
		"not requested": {"dimension": "assets", "requested": false, "qualification": "not_requested"},
		"preview deferred": {
			"dimension": "assets", "requested": true, "qualification": "deferred", "reason": "preview_deferred",
		},
		"actual deferred": {
			"dimension": "assets", "requested": true, "qualification": "deferred", "reason": "not_attempted",
		},
		"qualified": {
			"dimension": "assets", "requested": true, "qualification": "qualified", "complete": true,
		},
		"resolution partial": {
			"dimension": "assets", "requested": true, "qualification": "partial", "complete": false,
			"reason": "resolution_incomplete",
		},
		"inventory partial": {
			"dimension": "assets", "requested": true, "qualification": "partial", "complete": false,
			"reason": "inventory_incomplete",
		},
		"unattempted partial": {
			"dimension": "assets", "requested": true, "qualification": "partial", "complete": false,
			"reason": "not_attempted",
		},
		"read failed": {
			"dimension": "assets", "requested": true, "qualification": "failed", "complete": false,
			"reason": "read_failed",
		},
		"staging failed": {
			"dimension": "assets", "requested": true, "qualification": "failed", "complete": false,
			"reason": "staging_failed",
		},
	}
	for name, row := range rows {
		t.Run(name, func(t *testing.T) {
			data := marshalConfluenceSelectionPullWire(t, row)
			wire, err := decodeConfluenceSelectionPullWire(data)
			if err != nil || len(wire.Includes) != 2 || wire.Includes[0].Dimension != "assets" {
				t.Fatalf("released Confluence pull includes rejected: wire=%+v err=%v", wire, err)
			}
		})
	}
}

func TestConfluenceSelectionPullWireRejectsOpenOrContradictoryIncludes(t *testing.T) {
	valid := marshalConfluenceSelectionPullWire(t, map[string]any{
		"dimension": "assets", "requested": false, "qualification": "not_requested",
	})
	mutations := map[string]func(map[string]any){
		"missing includes": func(root map[string]any) { delete(root, "includes") },
		"null includes":    func(root map[string]any) { root["includes"] = nil },
		"unknown root member": func(root map[string]any) {
			root["unexpected"] = true
		},
		"missing include member": func(root map[string]any) {
			delete(confluenceSelectionPullIncludeMap(root, 0), "qualification")
		},
		"null include member": func(root map[string]any) {
			confluenceSelectionPullIncludeMap(root, 0)["requested"] = nil
		},
		"unknown include member": func(root map[string]any) {
			confluenceSelectionPullIncludeMap(root, 0)["unexpected"] = true
		},
		"reordered dimensions": func(root map[string]any) {
			includes := root["includes"].([]any)
			includes[0], includes[1] = includes[1], includes[0]
		},
		"unknown qualification": func(root map[string]any) {
			confluenceSelectionPullIncludeMap(root, 0)["qualification"] = "unknown"
		},
		"unrequested marked requested": func(root map[string]any) {
			confluenceSelectionPullIncludeMap(root, 0)["requested"] = true
		},
		"unrequested has completeness": func(root map[string]any) {
			confluenceSelectionPullIncludeMap(root, 0)["complete"] = false
		},
		"qualified missing completeness": func(root map[string]any) {
			include := confluenceSelectionPullIncludeMap(root, 0)
			include["requested"] = true
			include["qualification"] = "qualified"
		},
		"qualified incomplete": func(root map[string]any) {
			include := confluenceSelectionPullIncludeMap(root, 0)
			include["requested"] = true
			include["qualification"] = "qualified"
			include["complete"] = false
		},
		"partial missing reason": func(root map[string]any) {
			include := confluenceSelectionPullIncludeMap(root, 0)
			include["requested"] = true
			include["qualification"] = "partial"
			include["complete"] = false
		},
		"partial claims complete": func(root map[string]any) {
			include := confluenceSelectionPullIncludeMap(root, 0)
			include["requested"] = true
			include["qualification"] = "partial"
			include["complete"] = true
			include["reason"] = "inventory_incomplete"
		},
		"unknown reason": func(root map[string]any) {
			include := confluenceSelectionPullIncludeMap(root, 0)
			include["requested"] = true
			include["qualification"] = "failed"
			include["complete"] = false
			include["reason"] = "backend_detail"
		},
		"null optional reason": func(root map[string]any) {
			confluenceSelectionPullIncludeMap(root, 0)["reason"] = nil
		},
		"truncated without bound": func(root map[string]any) { root["truncated"] = true },
		"bound without truncated": func(root map[string]any) { root["truncated_at"] = 1 },
		"explicit false truncation": func(root map[string]any) {
			root["truncated"] = false
			root["truncated_at"] = 1
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceSelectionPullWire(t, valid, mutate)
			if _, err := decodeConfluenceSelectionPullWire(data); err == nil {
				t.Fatal("invalid Confluence pull wire passed")
			}
		})
	}

	duplicate := bytes.Replace(valid, []byte(`"root":`), []byte(`"root":"other","root":`), 1)
	if _, err := decodeConfluenceSelectionPullWire(duplicate); err == nil {
		t.Fatal("duplicate Confluence pull member passed")
	}
}

func marshalConfluenceSelectionPullWire(t *testing.T, assets map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"root":  "selection-mirror",
		"pages": []any{},
		"includes": []any{
			assets,
			map[string]any{"dimension": "comments", "requested": false, "qualification": "not_requested"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateConfluenceSelectionPullWire(
	t *testing.T,
	data []byte,
	mutate func(map[string]any),
) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	mutated, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}

func confluenceSelectionPullIncludeMap(root map[string]any, index int) map[string]any {
	return root["includes"].([]any)[index].(map[string]any)
}
