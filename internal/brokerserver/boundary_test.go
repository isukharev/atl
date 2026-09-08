package brokerserver

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBrokerServerAndAuthorityImportBoundaries(t *testing.T) {
	tests := []struct {
		directory string
		allowed   map[string]bool
	}{
		{directory: ".", allowed: map[string]bool{"app": true, "brokercontract": true, "brokertransport": true, "domain": true}},
		{directory: "../adapter/brokerauthority", allowed: map[string]bool{"backendid": true, "brokercontract": true, "brokertransport": true, "domain": true, "httpx": true}},
	}
	for _, test := range tests {
		entries, err := os.ReadDir(test.directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(test.directory, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imported := range file.Imports {
				name, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				const prefix = "github.com/isukharev/atl/internal/"
				if strings.HasPrefix(name, prefix) && !test.allowed[strings.TrimPrefix(name, prefix)] {
					t.Errorf("%s imports unreviewed ATL package %q", path, name)
				}
			}
		}
	}
}
