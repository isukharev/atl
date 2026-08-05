package agenteval

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestBoundedJSONLineReaderDistinguishesMessageAndTotalBounds(t *testing.T) {
	for name, reader := range map[string]*boundedJSONLineReader{
		"message": newTestBoundedJSONLineReader(strings.Repeat("x", 65)+"\n", 128, 64),
		"total":   newTestBoundedJSONLineReader("1234\n56789\n", 10, 10),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := reader.readLine()
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("error=%v, want %s bound", err, name)
			}
		})
	}
}

func TestSyntheticMCPDecodersRejectDuplicatesAndInvalidInitialization(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate envelope": `{"jsonrpc":"2.0","jsonrpc":"2.0","id":2,"result":{}}`,
		"duplicate result":   `{"content":[],"content":[],"structuredContent":{},"isError":false}`,
		"duplicate structured": `{"content":[{"type":"text","text":"x"}],"structuredContent":` +
			`{"schema_version":1,"schema_version":1},"isError":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			switch name {
			case "duplicate envelope":
				if _, _, err := decodeSyntheticMCPResponse([]byte(data), 2); err == nil {
					t.Fatal("duplicate protocol member passed")
				}
			default:
				if _, err := decodeSyntheticMCPToolResult([]byte(data), 4096); err == nil {
					t.Fatal("duplicate tool-result member passed")
				}
			}
		})
	}
	for name, result := range map[string]string{
		"empty":              `{}`,
		"wrong protocol":     `{"protocolVersion":"old","capabilities":{},"serverInfo":{"name":"atl","version":"1"}}`,
		"missing server":     `{"protocolVersion":"2025-11-25","capabilities":{}}`,
		"duplicate protocol": `{"protocolVersion":"2025-11-25","protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"atl","version":"1"}}`,
	} {
		t.Run("initialize "+name, func(t *testing.T) {
			if err := validateSyntheticMCPInitializeResult([]byte(result)); err == nil {
				t.Fatal("invalid initialize result passed")
			}
		})
	}
}

func TestSyntheticMCPDecoderPreservesApplicationErrorEvidence(t *testing.T) {
	result, err := decodeSyntheticMCPToolResult([]byte(
		`{"content":[{"type":"text","text":"bounded failure"}],"isError":true}`,
	), 4096)
	if err != nil || !result.IsError || result.StructuredContent != nil ||
		len(result.TextContent) != 1 || result.TextContent[0] != "bounded failure" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSyntheticMCPToolInventoryRejectsDrift(t *testing.T) {
	expected := map[string]bool{"jira_fields": true, "jira_issue_search": true}
	if err := validateSyntheticMCPToolInventory([]byte(`{"tools":[{"name":"jira_fields"},{"name":"jira_issue_search"}]}`), expected); err != nil {
		t.Fatalf("released inventory rejected: %v", err)
	}
	for name, result := range map[string]string{
		"missing":      `{"tools":[{"name":"jira_fields"}]}`,
		"unexpected":   `{"tools":[{"name":"jira_fields"},{"name":"jira_board_view"}]}`,
		"duplicate":    `{"tools":[{"name":"jira_fields"},{"name":"jira_fields"}]}`,
		"cursor":       `{"tools":[{"name":"jira_fields"},{"name":"jira_issue_search"}],"nextCursor":"more"}`,
		"bad entry":    `{"tools":[{}, {"name":"jira_issue_search"}]}`,
		"duplicates":   `{"tools":[{"name":"jira_fields","name":"jira_fields"},{"name":"jira_issue_search"}]}`,
		"not an array": `{"tools":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSyntheticMCPToolInventory([]byte(result), expected); err == nil {
				t.Fatal("tool inventory drift passed")
			}
		})
	}
}

func newTestBoundedJSONLineReader(data string, total, message int64) *boundedJSONLineReader {
	limited := &io.LimitedReader{R: strings.NewReader(data), N: total + 1}
	return &boundedJSONLineReader{
		limited: limited, reader: bufio.NewReaderSize(limited, 4), maxMessage: message,
	}
}
