package capability

import "testing"

func TestDefinitionsReturnsDefensiveCopy(t *testing.T) {
	first := Definitions()
	if len(first) != 48 {
		t.Fatalf("definitions=%d want=48", len(first))
	}
	want := first[0]
	first[0] = Definition{ID: "changed"}

	second := Definitions()
	if second[0] != want {
		t.Fatalf("Definitions shared backing storage: got=%+v want=%+v", second[0], want)
	}
}

func TestDefinitionsTransportMappings(t *testing.T) {
	mapped, cliOnly, mappedMutating := 0, 0, 0
	for _, definition := range Definitions() {
		if definition.MCPTool == "" {
			cliOnly++
			if definition.MCPScope != "" {
				t.Errorf("%s has MCP scope without tool", definition.ID)
			}
			continue
		}
		mapped++
		if definition.MCPScope == "" {
			t.Errorf("%s has MCP tool without scope", definition.ID)
		}
		if definition.Role == "write" {
			mappedMutating++
		}
	}
	if mapped != 29 || cliOnly != 19 {
		t.Fatalf("mapped=%d cli_only=%d want=29/19", mapped, cliOnly)
	}
	if mappedMutating != 0 {
		t.Fatalf("mapped mutating definitions=%d want=0", mappedMutating)
	}
}
