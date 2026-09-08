package brokercontract

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBrokerContractProductionImportsStayPure(t *testing.T) {
	allowedATL := map[string]bool{
		"github.com/isukharev/atl/internal/domain":     true,
		"github.com/isukharev/atl/internal/strictjson": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			name, _ := strconv.Unquote(imported.Path.Value)
			if strings.HasPrefix(name, "github.com/isukharev/atl/") && !allowedATL[name] {
				t.Errorf("%s imports disallowed ATL package %s", path, name)
			}
		}
	}
}

func TestPublishedSchemaMatchesEmbeddedContract(t *testing.T) {
	published, err := os.ReadFile(filepath.Join("..", "..", "docs", "schemas", "broker-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, SchemaV1()) {
		t.Fatal("published Broker schema differs from embedded strict contract")
	}
}
