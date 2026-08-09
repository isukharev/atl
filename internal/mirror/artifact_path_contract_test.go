package mirror

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestArtifactPathPublicationAPIAndConversionInventory(t *testing.T) {
	artifactPathType := reflect.TypeOf(ArtifactPath{})
	for _, apiType := range []reflect.Type{reflect.TypeOf(CompletePullArtifact{}), reflect.TypeOf(RegistrationArtifact{})} {
		field, ok := apiType.FieldByName("Path")
		if !ok || field.Type != artifactPathType {
			t.Fatalf("%s.Path type=%v present=%t, want mirror.ArtifactPath object identity", apiType, field.Type, ok)
		}
	}
	for i := range artifactPathType.NumField() {
		field := artifactPathType.Field(i)
		if field.PkgPath == "" {
			t.Fatalf("ArtifactPath field %q is exported", field.Name)
		}
	}
	for _, forbidden := range []string{"String", "Value", "Path", "MarshalText", "MarshalJSON"} {
		if _, ok := artifactPathType.MethodByName(forbidden); ok {
			t.Fatalf("ArtifactPath exposes forbidden raw accessor %s", forbidden)
		}
	}

	type parsedFile struct {
		path string
		file *ast.File
	}
	fset := token.NewFileSet()
	var files []parsedFile
	for _, dir := range []string{".", filepath.Join("..", "app")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			parsed, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			files = append(files, parsedFile{path: filepath.ToSlash(path), file: parsed})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	artifactComposites := map[string]int{"CompletePullArtifact": 0, "RegistrationArtifact": 0}
	productionBridgeCalls := []string{}
	productionDurableParsers := []string{}
	for _, parsed := range files {
		production := !strings.HasSuffix(parsed.path, "_test.go")
		ast.Inspect(parsed.file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CompositeLit:
				literals := []struct {
					name string
					lit  *ast.CompositeLit
				}{{name: artifactCompositeTypeName(n.Type), lit: n}}
				if array, ok := n.Type.(*ast.ArrayType); ok {
					name := artifactCompositeTypeName(array.Elt)
					if _, tracked := artifactComposites[name]; tracked {
						for _, element := range n.Elts {
							if literal, ok := element.(*ast.CompositeLit); ok {
								literals = append(literals, struct {
									name string
									lit  *ast.CompositeLit
								}{name: name, lit: literal})
							}
						}
					}
				}
				for _, candidate := range literals {
					if _, tracked := artifactComposites[candidate.name]; !tracked {
						continue
					}
					artifactComposites[candidate.name]++
					hasPath := false
					for _, element := range candidate.lit.Elts {
						keyed, ok := element.(*ast.KeyValueExpr)
						if !ok {
							t.Errorf("%s:%d uses unkeyed %s literal", parsed.path, fset.Position(element.Pos()).Line, candidate.name)
							continue
						}
						if key, ok := keyed.Key.(*ast.Ident); ok && key.Name == "Path" {
							hasPath = true
						}
					}
					if len(candidate.lit.Elts) > 0 && !hasPath {
						t.Errorf("%s:%d constructs %s without Path", parsed.path, fset.Position(candidate.lit.Pos()).Line, candidate.name)
					}
				}
				name := artifactCompositeTypeName(n.Type)
				if name == "ArtifactPath" && len(n.Elts) > 0 && filepath.Base(parsed.path) != "artifact_path.go" {
					t.Errorf("%s:%d constructs ArtifactPath representation directly", parsed.path, fset.Position(n.Pos()).Line)
				}
			case *ast.SelectorExpr:
				if production && (n.Sel.Name == "value" || n.Sel.Name == "class") && filepath.Base(parsed.path) != "artifact_path.go" {
					t.Errorf("%s:%d reaches into ArtifactPath representation", parsed.path, fset.Position(n.Pos()).Line)
				}
			case *ast.CallExpr:
				identifier, ok := n.Fun.(*ast.Ident)
				if !ok || !production {
					break
				}
				position := parsed.path + ":" + fset.Position(n.Pos()).String()
				switch identifier.Name {
				case "artifactPathDurableString":
					productionBridgeCalls = append(productionBridgeCalls, position)
				case "artifactPathFromDurable":
					productionDurableParsers = append(productionDurableParsers, position)
				}
			}
			return true
		})
	}
	for name, count := range artifactComposites {
		if count == 0 {
			t.Errorf("artifact composite inventory did not find %s", name)
		}
	}
	if len(productionBridgeCalls) != 1 || !strings.HasPrefix(productionBridgeCalls[0], "complete_pull_artifact.go:") {
		t.Fatalf("transient-to-durable bridge calls=%v, want sole stagePublicationArtifact call", productionBridgeCalls)
	}
	if len(productionDurableParsers) != 4 {
		t.Fatalf("durable publication parser calls=%v, want validation, publication, and two committed verification sites", productionDurableParsers)
	}
}

func artifactCompositeTypeName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	}
	return ""
}
