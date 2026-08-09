package mirror

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const mirrorImportPath = "github.com/isukharev/atl/internal/mirror"

type contractGoListPackage struct {
	Dir          string
	ImportPath   string
	Name         string
	Export       string
	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
}

type contractSourcePackage struct {
	importPath string
	name       string
	files      map[string][]byte
}

type artifactContractWorkspace struct {
	root       string
	exports    map[string]string
	packages   map[string]contractSourcePackage
	allGoFiles []string
}

func TestArtifactPathPublicationAPIAndConversionInventory(t *testing.T) {
	artifactPathType := reflect.TypeOf(ArtifactPath{})
	for _, apiType := range []reflect.Type{reflect.TypeOf(CompletePullArtifact{}), reflect.TypeOf(RegistrationArtifact{})} {
		field, ok := apiType.FieldByName("Path")
		if !ok || field.Type != artifactPathType {
			t.Fatalf("%s.Path type=%v present=%t, want mirror.ArtifactPath object identity", apiType, field.Type, ok)
		}
	}
	for i := range artifactPathType.NumField() {
		if field := artifactPathType.Field(i); field.PkgPath == "" {
			t.Fatalf("ArtifactPath field %q is exported", field.Name)
		}
	}
	for _, forbidden := range []string{"String", "Value", "Path", "MarshalText", "MarshalJSON"} {
		if _, ok := artifactPathType.MethodByName(forbidden); ok {
			t.Fatalf("ArtifactPath exposes forbidden raw accessor %s", forbidden)
		}
	}

	workspace := loadArtifactContractWorkspace(t)
	if violations := workspace.analyze(nil); len(violations) != 0 {
		t.Fatalf("artifact-path contract violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestArtifactPathContractOracleRejectsRealSourceMutations(t *testing.T) {
	workspace := loadArtifactContractWorkspace(t)
	mutations := []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "type alias", path: "internal/mirror/contract_alias.go", body: `package mirror; type EscapedArtifactPath = ArtifactPath`},
		{name: "defined shadow type", path: "internal/mirror/contract_shadow.go", body: `package mirror; type escapedArtifactPath ArtifactPath; var escapedShadow = escapedArtifactPath{value: "x", class: artifactPathPublic}`},
		{name: "anonymous ArtifactPath conversion", path: "internal/mirror/contract_anonymous_path.go", body: `package mirror; var escapedAnonymousArtifact = ArtifactPath(struct { value string; class artifactPathClass }{value: "x", class: artifactPathPublic})`},
		{name: "anonymous DTO conversion", path: "internal/mirror/contract_anonymous_dto.go", body: `package mirror; import "os"; var escapedAnonymousDTO = CompletePullArtifact(struct { Path ArtifactPath; Data []byte; Mode os.FileMode; Remove bool; BestEffort bool }{})`},
		{name: "function alias", path: "internal/mirror/contract_function_alias.go", body: `package mirror; var escapedArtifactConstructor = NewPublicArtifactPath`},
		{name: "constructor wrapper", path: "internal/mirror/contract_wrapper.go", body: `package mirror; func escapedArtifactConstructor(v string) (ArtifactPath, error) { return NewPublicArtifactPath(v) }`},
		{name: "raw field", path: "internal/mirror/contract_field.go", body: `package mirror; func escapedArtifactValue(p ArtifactPath) string { return p.value }`},
		{name: "exported accessor", path: "internal/mirror/contract_accessor.go", body: `package mirror; func ArtifactPathValue(p ArtifactPath) string { return p.value }`},
		{name: "forged composite", path: "internal/mirror/contract_composite.go", body: `package mirror; var escapedArtifact = ArtifactPath{value: "x", class: artifactPathPublic}`},
		{name: "selector write", path: "internal/mirror/contract_selector_write.go", body: `package mirror; func escapedArtifactWrite(a *CompletePullArtifact) { a.Path, _ = NewPublicArtifactPath("x") }`},
		{name: "alternate parser", path: "internal/mirror/contract_parser.go", body: `package mirror; func escapedArtifactParser(v string) (ArtifactPath, error) { return newArtifactPath(v, artifactPathPublic) }`},
		{name: "alternate bridge", path: "internal/mirror/contract_bridge.go", body: `package mirror; func escapedArtifactBridge(p ArtifactPath) (string, error) { return artifactPathDurableString(p) }`},
		{name: "unexpected consumer package", path: "internal/contractconsumer/contract_consumer.go", body: `package contractconsumer; import "github.com/isukharev/atl/internal/mirror"; func escapedArtifactConsumer() { _, _ = mirror.NewPublicArtifactPath("x") }`},
		{name: "inactive raw access", path: "internal/mirror/contract_inactive.go", body: "//go:build ignore\n\npackage mirror\nfunc escapedInactiveValue(p ArtifactPath) string { return p.value }", want: "inactive artifact-path syntax"},
		{name: "inactive forged composite", path: "internal/mirror/contract_inactive_forge.go", body: "//go:build ignore\n\npackage mirror\nvar escapedInactiveArtifact = ArtifactPath{value: \"x\", class: artifactPathPublic}", want: "inactive artifact-path syntax"},
		{name: "inactive parse failure", path: "internal/mirror/contract_inactive_invalid.go", body: "//go:build ignore\n\npackage mirror\nfunc broken(", want: "parse"},
		{name: "exported struct wrapper", path: "internal/mirror/contract_exported_struct.go", body: `package mirror; type ArtifactEnvelope struct { Artifact ArtifactPath }`},
		{name: "exported slice result", path: "internal/mirror/contract_exported_slice.go", body: `package mirror; func ArtifactPaths() []ArtifactPath { return nil }`},
		{name: "exported map", path: "internal/mirror/contract_exported_map.go", body: `package mirror; var ArtifactByID map[string]ArtifactPath`},
		{name: "exported anonymous result", path: "internal/mirror/contract_exported_anon.go", body: `package mirror; func ArtifactResult() struct { Artifact ArtifactPath } { return struct { Artifact ArtifactPath }{} }`},
		{name: "exported generic constraint", path: "internal/mirror/contract_exported_generic.go", body: `package mirror; func ArtifactGeneric[T interface { ArtifactPath }]() {}`},
		{name: "exported named generic constraint", path: "internal/mirror/contract_exported_generic_type.go", body: `package mirror; type ArtifactGenericType[T interface { ArtifactPath }] struct{}`},
		{name: "cross package same name", path: "internal/contractconsumer/artifact_path_test.go", body: `package contractconsumer; import "github.com/isukharev/atl/internal/mirror"; func mustPublicArtifactPath(value string) (mirror.ArtifactPath, error) { return mirror.NewPublicArtifactPath(value) }`, want: "internal/contractconsumer/artifact_path_test.go"},
		{name: "parse failure", path: "internal/mirror/contract_parse.go", body: `package mirror; func broken(`},
		{name: "type failure", path: "internal/mirror/contract_type.go", body: `package mirror; var broken ArtifactPath = "raw"`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			violations := workspace.analyze(map[string][]byte{mutation.path: []byte(mutation.body)})
			if len(violations) == 0 {
				t.Fatal("contract oracle accepted synthetic source mutation")
			}
			if mutation.want != "" && !strings.Contains(strings.Join(violations, "\n"), mutation.want) {
				t.Fatalf("contract oracle violations omit %q:\n%s", mutation.want, strings.Join(violations, "\n"))
			}
		})
	}
	t.Run("allowed method exposure removed", func(t *testing.T) {
		observed := make(map[string]int, len(expectedArtifactContractExports))
		for key, count := range expectedArtifactContractExports {
			observed[key] = count
		}
		removed := mirrorImportPath + ".Mirror.PrepareCompletePullView"
		delete(observed, removed)
		violations := reconcileArtifactExportInventory(observed, expectedArtifactContractExports)
		if !strings.Contains(strings.Join(violations, "\n"), "missing exported artifact-path surface: "+removed) {
			t.Fatalf("export inventory accepted removed allowed method:\n%s", strings.Join(violations, "\n"))
		}
	})
}

func loadArtifactContractWorkspace(t *testing.T) artifactContractWorkspace {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "list", "-json", "-export", "-deps", "-test", "./...")
	cmd.Dir = root
	cmd.Env = append(withoutEnvironment(os.Environ(), "GOROOT", "GOWORK"), "GOTOOLCHAIN=auto", "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list artifact contract packages: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	exports := map[string]string{}
	listed := map[string]contractGoListPackage{}
	for {
		var item contractGoListPackage
		if err := decoder.Decode(&item); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode go list artifact contract: %v", err)
		}
		if item.Export != "" && !strings.Contains(item.ImportPath, " [") {
			exports[item.ImportPath] = item.Export
		}
		if strings.HasPrefix(item.ImportPath, "github.com/isukharev/atl/") && !strings.Contains(item.ImportPath, " [") && !strings.HasSuffix(item.ImportPath, ".test") {
			listed[item.ImportPath] = item
		}
	}
	packages := map[string]contractSourcePackage{}
	for importPath, item := range listed {
		files := map[string][]byte{}
		for _, name := range append(append([]string{}, item.GoFiles...), item.TestGoFiles...) {
			absolute := filepath.Join(item.Dir, name)
			body, err := os.ReadFile(absolute)
			if err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(root, absolute)
			if err != nil {
				t.Fatal(err)
			}
			files[filepath.ToSlash(relative)] = body
		}
		packages[importPath] = contractSourcePackage{importPath: importPath, name: item.Name, files: files}
		if len(item.XTestGoFiles) != 0 {
			externalFiles := map[string][]byte{}
			for _, name := range item.XTestGoFiles {
				absolute := filepath.Join(item.Dir, name)
				body, err := os.ReadFile(absolute)
				if err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(root, absolute)
				if err != nil {
					t.Fatal(err)
				}
				externalFiles[filepath.ToSlash(relative)] = body
			}
			packages[importPath+"_test"] = contractSourcePackage{importPath: importPath + "_test", name: item.Name + "_test", files: externalFiles}
		}
	}
	if _, ok := packages[mirrorImportPath]; !ok {
		t.Fatalf("go list omitted %s", mirrorImportPath)
	}
	var allGoFiles []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "tmp" || entry.Name() == "vendor") && path != root {
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			allGoFiles = append(allGoFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifactContractWorkspace{root: root, exports: exports, packages: packages, allGoFiles: allGoFiles}
}

func withoutEnvironment(environment []string, names ...string) []string {
	blocked := map[string]struct{}{}
	for _, name := range names {
		blocked[name] = struct{}{}
	}
	out := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if _, skip := blocked[name]; !skip {
			out = append(out, item)
		}
	}
	return out
}

func (workspace artifactContractWorkspace) analyze(overlays map[string][]byte) []string {
	var violations []string
	syntaxFiles := map[string]*ast.File{}
	syntaxSet := token.NewFileSet()
	for _, absolute := range workspace.allGoFiles {
		body, err := os.ReadFile(absolute)
		if err != nil {
			violations = append(violations, fmt.Sprintf("read %s: %v", absolute, err))
			continue
		}
		relative, err := filepath.Rel(workspace.root, absolute)
		if err != nil {
			violations = append(violations, fmt.Sprintf("rel %s: %v", absolute, err))
			continue
		}
		relative = filepath.ToSlash(relative)
		if !artifactContractFileRelevant(relative, body) {
			continue
		}
		file, err := parser.ParseFile(syntaxSet, relative, body, parser.AllErrors)
		if err != nil {
			violations = append(violations, fmt.Sprintf("parse %s: %v", relative, err))
			continue
		}
		syntaxFiles[relative] = file
	}
	for name, body := range overlays {
		file, err := parser.ParseFile(syntaxSet, name, body, parser.AllErrors)
		if err != nil {
			violations = append(violations, fmt.Sprintf("parse %s: %v", name, err))
			continue
		}
		syntaxFiles[name] = file
	}
	packages := map[string]contractSourcePackage{}
	for importPath, sourcePackage := range workspace.packages {
		files := map[string][]byte{}
		for name, body := range sourcePackage.files {
			files[name] = body
		}
		packages[importPath] = contractSourcePackage{importPath: importPath, name: sourcePackage.name, files: files}
	}
	for name, body := range overlays {
		if artifactSourceHasIgnoreBuildTag(body) {
			continue
		}
		directory := filepath.ToSlash(filepath.Dir(name))
		importPath := "github.com/isukharev/atl/" + directory
		if directory == "." {
			importPath = "github.com/isukharev/atl"
		}
		pkg := packages[importPath]
		if pkg.files == nil {
			pkg = contractSourcePackage{importPath: importPath, name: filepath.Base(directory), files: map[string][]byte{}}
		}
		pkg.files[name] = body
		packages[importPath] = pkg
	}
	activeFiles := map[string]struct{}{}
	for _, sourcePackage := range packages {
		for name := range sourcePackage.files {
			activeFiles[name] = struct{}{}
		}
	}
	for name, file := range syntaxFiles {
		if _, active := activeFiles[name]; !active && artifactContractASTRelevant(file) {
			violations = append(violations, name+": inactive artifact-path syntax is outside the typed operation inventory")
		}
	}

	fset := token.NewFileSet()
	lookup := func(importPath string) (io.ReadCloser, error) {
		file := workspace.exports[importPath]
		if file == "" {
			return nil, fmt.Errorf("missing export data for %s", importPath)
		}
		return os.Open(file)
	}
	baseImporter := importer.ForCompiler(fset, "gc", lookup)
	local := map[string]*types.Package{}
	contractImporter := artifactPathImporter{local: local, fallback: baseImporter}
	checked := map[string]artifactCheckedPackage{}
	var ordered []string
	for importPath, sourcePackage := range packages {
		if importPath == mirrorImportPath || artifactContractSourcesRelevant(sourcePackage.files) {
			ordered = append(ordered, importPath)
		}
	}
	sort.Strings(ordered)
	for index, importPath := range ordered {
		if importPath == mirrorImportPath {
			ordered[0], ordered[index] = ordered[index], ordered[0]
			break
		}
	}
	for _, importPath := range ordered {
		sourcePackage := packages[importPath]
		var names []string
		for name := range sourcePackage.files {
			names = append(names, name)
		}
		sort.Strings(names)
		parsed := make([]*ast.File, 0, len(names))
		for _, name := range names {
			file, err := parser.ParseFile(fset, name, sourcePackage.files[name], parser.AllErrors)
			if err != nil {
				violations = append(violations, fmt.Sprintf("parse %s: %v", name, err))
				continue
			}
			parsed = append(parsed, file)
		}
		info := &types.Info{
			Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
			Types: map[ast.Expr]types.TypeAndValue{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
		config := types.Config{Importer: contractImporter, GoVersion: "go1.26", Error: func(err error) {
			violations = append(violations, fmt.Sprintf("type %s: %v", importPath, err))
		}}
		pkg, err := config.Check(importPath, fset, parsed, info)
		if err != nil {
			violations = append(violations, fmt.Sprintf("type-check %s failed", importPath))
			continue
		}
		local[importPath] = pkg
		checked[importPath] = artifactCheckedPackage{pkg: pkg, files: parsed, info: info}
	}
	if len(violations) != 0 {
		return sortedUnique(violations)
	}
	mirrorPackage := checked[mirrorImportPath]
	artifactObject, _ := mirrorPackage.pkg.Scope().Lookup("ArtifactPath").(*types.TypeName)
	if artifactObject == nil {
		return []string{"mirror.ArtifactPath definition missing"}
	}
	artifactType := types.Unalias(artifactObject.Type())
	artifactNamed, ok := artifactType.(*types.Named)
	if !ok {
		return []string{"mirror.ArtifactPath is not a named type"}
	}
	artifactStruct, ok := artifactNamed.Underlying().(*types.Struct)
	if !ok || artifactStruct.NumFields() != 2 {
		return []string{"mirror.ArtifactPath representation changed"}
	}
	for index := range artifactNamed.NumMethods() {
		method := artifactNamed.Method(index)
		if method.Exported() {
			violations = append(violations, "ArtifactPath exposes exported method "+method.Name())
		}
	}
	fieldObjects := map[types.Object]string{}
	for index := range artifactStruct.NumFields() {
		fieldObjects[artifactStruct.Field(index)] = artifactStruct.Field(index).Name()
	}
	sensitive := map[types.Object]string{}
	for _, name := range []string{"NewPublicArtifactPath", "newPrivateArtifactPath", "newArtifactPath", "artifactPathFromDurable", "artifactPathDurableString"} {
		object := mirrorPackage.pkg.Scope().Lookup(name)
		if object == nil {
			violations = append(violations, "missing contract function "+name)
			continue
		}
		sensitive[object] = name
	}
	dtoTypes := map[types.Type]string{}
	dtoPathFields := map[types.Object]string{}
	protectedObjects := map[types.Object]string{artifactObject: "ArtifactPath"}
	protectedTypes := map[types.Type]string{artifactType: "ArtifactPath"}
	for _, name := range []string{"CompletePullArtifact", "RegistrationArtifact"} {
		object, _ := mirrorPackage.pkg.Scope().Lookup(name).(*types.TypeName)
		if object == nil {
			violations = append(violations, "missing publication DTO "+name)
			continue
		}
		named := types.Unalias(object.Type())
		dtoTypes[named] = name
		protectedObjects[object] = name
		protectedTypes[named] = name
		structure, _ := named.Underlying().(*types.Struct)
		for index := range structure.NumFields() {
			if field := structure.Field(index); field.Name() == "Path" {
				if !types.Identical(field.Type(), artifactType) {
					violations = append(violations, name+".Path does not have ArtifactPath object identity")
				}
				dtoPathFields[field] = name + ".Path"
			}
		}
	}

	operations := map[string]int{}
	for _, checkedPackage := range checked {
		for _, file := range checkedPackage.files {
			fileName := filepath.ToSlash(fset.Position(file.Pos()).Filename)
			ast.Inspect(file, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.TypeSpec:
					object, _ := checkedPackage.info.Defs[value.Name].(*types.TypeName)
					if object != nil {
						if _, owner := protectedObjects[object]; !owner {
							if name, matched := artifactProtectedRepresentation(object.Type(), protectedTypes); matched {
								violations = append(violations, fileName+":"+enclosingArtifactFunction(file, value.Pos())+": defines alias or shadow of "+name)
							}
						}
					}
				case *ast.CallExpr:
					if checkedPackage.info.Types[value.Fun].IsType() {
						if name, matched := artifactProtectedRepresentation(checkedPackage.info.Types[value].Type, protectedTypes); matched {
							kind := "shadow of "
							if artifactExactProtectedType(checkedPackage.info.Types[value].Type, protectedTypes) {
								kind = "protected type "
							}
							violations = append(violations, fileName+":"+enclosingArtifactFunction(file, value.Pos())+": converts to "+kind+name)
						}
					}
					if object := artifactCalledObject(checkedPackage.info, value.Fun); object != nil {
						if name, tracked := sensitive[object]; tracked {
							key := artifactOperationKey(checkedPackage.pkg.Path(), fileName, enclosingArtifactFunction(file, value.Pos()), "call", name)
							operations[key]++
						}
					}
				case *ast.Ident:
					object := checkedPackage.info.Uses[value]
					if name, tracked := sensitive[object]; tracked && !artifactIdentIsCallTarget(file, value) {
						key := artifactOperationKey(checkedPackage.pkg.Path(), fileName, enclosingArtifactFunction(file, value.Pos()), "reference", name)
						operations[key]++
					}
				case *ast.SelectorExpr:
					selection := checkedPackage.info.Selections[value]
					if selection != nil {
						if name, tracked := fieldObjects[selection.Obj()]; tracked {
							key := artifactOperationKey(checkedPackage.pkg.Path(), fileName, enclosingArtifactFunction(file, value.Pos()), "field", name)
							operations[key]++
						}
					}
				case *ast.CompositeLit:
					literalType := types.Unalias(checkedPackage.info.Types[value].Type)
					if name, matched := artifactProtectedRepresentation(literalType, protectedTypes); matched && !artifactExactProtectedType(literalType, protectedTypes) {
						violations = append(violations, fileName+":"+enclosingArtifactFunction(file, value.Pos())+": composites shadow of "+name)
					}
					if len(value.Elts) > 0 && types.Identical(literalType, artifactType) {
						key := artifactOperationKey(checkedPackage.pkg.Path(), fileName, enclosingArtifactFunction(file, value.Pos()), "composite", "ArtifactPath")
						operations[key]++
					}
					if dtoName, tracked := dtoTypes[literalType]; tracked {
						hasPath := false
						for _, element := range value.Elts {
							keyed, ok := element.(*ast.KeyValueExpr)
							if !ok {
								violations = append(violations, fileName+":"+enclosingArtifactFunction(file, value.Pos())+": unkeyed "+dtoName+" composite")
								continue
							}
							identifier, _ := keyed.Key.(*ast.Ident)
							hasPath = hasPath || (identifier != nil && identifier.Name == "Path")
						}
						if len(value.Elts) > 0 && !hasPath {
							violations = append(violations, fileName+":"+enclosingArtifactFunction(file, value.Pos())+": "+dtoName+" composite omits Path")
						}
						key := artifactOperationKey(checkedPackage.pkg.Path(), fileName, enclosingArtifactFunction(file, value.Pos()), "dto-composite", dtoName)
						operations[key]++
					}
				case *ast.AssignStmt:
					for _, left := range value.Lhs {
						selector, _ := left.(*ast.SelectorExpr)
						if selector == nil {
							continue
						}
						selection := checkedPackage.info.Selections[selector]
						if selection == nil {
							continue
						}
						if name, tracked := dtoPathFields[selection.Obj()]; tracked {
							key := artifactOperationKey(checkedPackage.pkg.Path(), fileName, enclosingArtifactFunction(file, value.Pos()), "dto-write", name)
							operations[key]++
						}
					}
				}
				return true
			})
		}
	}
	for key, count := range operations {
		if want, ok := allowedArtifactContractOperations[key]; !ok || want != count {
			violations = append(violations, fmt.Sprintf("unexpected operation %s count=%d", key, count))
		}
	}
	for key, want := range allowedArtifactContractOperations {
		if got := operations[key]; got != want {
			violations = append(violations, fmt.Sprintf("missing operation %s count=%d want=%d", key, got, want))
		}
	}
	observedExports := map[string]int{}
	for importPath, checkedPackage := range checked {
		for _, name := range checkedPackage.pkg.Scope().Names() {
			if !ast.IsExported(name) {
				continue
			}
			object := checkedPackage.pkg.Scope().Lookup(name)
			key := importPath + "." + name
			if artifactTypeExposed(object.Type(), artifactType) {
				observedExports[key]++
			}
			typeName, _ := object.(*types.TypeName)
			named, _ := types.Unalias(object.Type()).(*types.Named)
			if typeName == nil || named == nil {
				continue
			}
			for index := range named.NumMethods() {
				method := named.Method(index)
				if !method.Exported() || !artifactTypeExposed(method.Type(), artifactType) {
					continue
				}
				methodKey := importPath + "." + name + "." + method.Name()
				observedExports[methodKey]++
			}
		}
	}
	violations = append(violations, reconcileArtifactExportInventory(observedExports, expectedArtifactContractExports)...)
	constructor, _ := mirrorPackage.pkg.Scope().Lookup("NewPublicArtifactPath").(*types.Func)
	if constructor == nil || !validPublicArtifactConstructorSignature(constructor.Type(), artifactType) {
		violations = append(violations, "NewPublicArtifactPath signature changed or became class-selectable")
	}
	return sortedUnique(violations)
}

func artifactContractFileRelevant(name string, body []byte) bool {
	if strings.HasPrefix(name, "internal/mirror/") || strings.HasPrefix(name, "internal/app/") {
		return true
	}
	return artifactContractSourcesRelevant(map[string][]byte{name: body})
}

func artifactSourceHasIgnoreBuildTag(body []byte) bool {
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "//go:build ignore" {
			return true
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
			break
		}
	}
	return false
}

func artifactContractASTRelevant(file *ast.File) bool {
	relevant := false
	ast.Inspect(file, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok {
			switch identifier.Name {
			case "ArtifactPath", "CompletePullArtifact", "RegistrationArtifact", "NewPublicArtifactPath", "newPrivateArtifactPath", "newArtifactPath", "artifactPathFromDurable", "artifactPathDurableString":
				relevant = true
			}
		}
		return !relevant
	})
	return relevant
}

func validPublicArtifactConstructorSignature(value, artifact types.Type) bool {
	signature, _ := value.(*types.Signature)
	if signature == nil || signature.Params().Len() != 1 || signature.Results().Len() != 2 {
		return false
	}
	errorObject := types.Universe.Lookup("error")
	return types.Identical(signature.Params().At(0).Type(), types.Typ[types.String]) &&
		types.Identical(signature.Results().At(0).Type(), artifact) &&
		types.Identical(signature.Results().At(1).Type(), errorObject.Type())
}

func artifactContractSourcesRelevant(files map[string][]byte) bool {
	for _, body := range files {
		for _, name := range []string{"ArtifactPath", "CompletePullArtifact", "RegistrationArtifact", "artifactPathFromDurable", "artifactPathDurableString", "newArtifactPath"} {
			if strings.Contains(string(body), name) {
				return true
			}
		}
	}
	return false
}

type artifactPathImporter struct {
	local    map[string]*types.Package
	fallback types.Importer
}

func (importer artifactPathImporter) Import(path string) (*types.Package, error) {
	if pkg := importer.local[path]; pkg != nil {
		return pkg, nil
	}
	return importer.fallback.Import(path)
}

type artifactCheckedPackage struct {
	pkg   *types.Package
	files []*ast.File
	info  *types.Info
}

func artifactCalledObject(info *types.Info, expression ast.Expr) types.Object {
	switch value := expression.(type) {
	case *ast.Ident:
		return info.Uses[value]
	case *ast.SelectorExpr:
		return info.Uses[value.Sel]
	}
	return nil
}

func artifactProtectedRepresentation(candidate types.Type, protected map[types.Type]string) (string, bool) {
	if candidate == nil {
		return "", false
	}
	candidate = types.Unalias(candidate)
	if _, named := candidate.(*types.Named); !named {
		return "", false
	}
	for expected, name := range protected {
		if types.Identical(candidate, expected) || types.Identical(candidate.Underlying(), expected.Underlying()) {
			return name, true
		}
	}
	return "", false
}

func artifactExactProtectedType(candidate types.Type, protected map[types.Type]string) bool {
	if candidate == nil {
		return false
	}
	candidate = types.Unalias(candidate)
	for expected := range protected {
		if types.Identical(candidate, expected) {
			return true
		}
	}
	return false
}

func artifactIdentIsCallTarget(file *ast.File, target *ast.Ident) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch function := call.Fun.(type) {
		case *ast.Ident:
			found = found || function == target
		case *ast.SelectorExpr:
			found = found || function.Sel == target
		}
		return !found
	})
	return found
}

func artifactTypeExposed(value, artifact types.Type) bool {
	return artifactTypeExposedWithSeen(value, artifact, map[types.Type]struct{}{})
}

func artifactTypeExposedWithSeen(value, artifact types.Type, seen map[types.Type]struct{}) bool {
	if value == nil {
		return false
	}
	value = types.Unalias(value)
	if types.Identical(value, artifact) {
		return true
	}
	if _, visited := seen[value]; visited {
		return false
	}
	seen[value] = struct{}{}
	switch typed := value.(type) {
	case *types.Named:
		if artifactTypeParamListExposed(typed.TypeParams(), artifact, seen) || artifactTypeListExposed(typed.TypeArgs(), artifact, seen) {
			return true
		}
		return artifactTypeExposedWithSeen(typed.Underlying(), artifact, seen)
	case *types.Pointer:
		return artifactTypeExposedWithSeen(typed.Elem(), artifact, seen)
	case *types.Array:
		return artifactTypeExposedWithSeen(typed.Elem(), artifact, seen)
	case *types.Slice:
		return artifactTypeExposedWithSeen(typed.Elem(), artifact, seen)
	case *types.Map:
		return artifactTypeExposedWithSeen(typed.Key(), artifact, seen) || artifactTypeExposedWithSeen(typed.Elem(), artifact, seen)
	case *types.Chan:
		return artifactTypeExposedWithSeen(typed.Elem(), artifact, seen)
	case *types.Struct:
		for index := range typed.NumFields() {
			if artifactTypeExposedWithSeen(typed.Field(index).Type(), artifact, seen) {
				return true
			}
		}
	case *types.Interface:
		for index := range typed.NumMethods() {
			if artifactTypeExposedWithSeen(typed.Method(index).Type(), artifact, seen) {
				return true
			}
		}
		for index := range typed.NumEmbeddeds() {
			if artifactTypeExposedWithSeen(typed.EmbeddedType(index), artifact, seen) {
				return true
			}
		}
	case *types.Tuple:
		for index := range typed.Len() {
			if artifactTypeExposedWithSeen(typed.At(index).Type(), artifact, seen) {
				return true
			}
		}
	case *types.Signature:
		return artifactTypeParamListExposed(typed.TypeParams(), artifact, seen) ||
			artifactTypeParamListExposed(typed.RecvTypeParams(), artifact, seen) ||
			artifactTypeExposedWithSeen(typed.Params(), artifact, seen) ||
			artifactTypeExposedWithSeen(typed.Results(), artifact, seen)
	case *types.TypeParam:
		return artifactTypeExposedWithSeen(typed.Constraint(), artifact, seen)
	case *types.Union:
		for index := range typed.Len() {
			if artifactTypeExposedWithSeen(typed.Term(index).Type(), artifact, seen) {
				return true
			}
		}
	}
	return false
}

func artifactTypeParamListExposed(parameters *types.TypeParamList, artifact types.Type, seen map[types.Type]struct{}) bool {
	if parameters == nil {
		return false
	}
	for index := range parameters.Len() {
		if artifactTypeExposedWithSeen(parameters.At(index).Constraint(), artifact, seen) {
			return true
		}
	}
	return false
}

func artifactTypeListExposed(arguments *types.TypeList, artifact types.Type, seen map[types.Type]struct{}) bool {
	if arguments == nil {
		return false
	}
	for index := range arguments.Len() {
		if artifactTypeExposedWithSeen(arguments.At(index), artifact, seen) {
			return true
		}
	}
	return false
}

func enclosingArtifactFunction(file *ast.File, position token.Pos) string {
	owner := "<package>"
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Pos() <= position && position <= function.End() {
			owner = function.Name.Name
			break
		}
	}
	return owner
}

func artifactOperationKey(packagePath, file, function, operation, name string) string {
	return packagePath + "|" + filepath.ToSlash(file) + ":" + function + ":" + operation + ":" + name
}

func sortedUnique(values []string) []string {
	unique := map[string]struct{}{}
	for _, value := range values {
		unique[value] = struct{}{}
	}
	out := make([]string, 0, len(unique))
	for value := range unique {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func reconcileArtifactExportInventory(observed, expected map[string]int) []string {
	var violations []string
	for key, count := range observed {
		want, allowed := expected[key]
		if !allowed {
			violations = append(violations, "unexpected exported artifact-path surface: "+key)
		} else if count != want {
			violations = append(violations, fmt.Sprintf("exported artifact-path surface count changed: %s count=%d want=%d", key, count, want))
		}
	}
	for key, want := range expected {
		if _, present := observed[key]; !present {
			violations = append(violations, fmt.Sprintf("missing exported artifact-path surface: %s count=0 want=%d", key, want))
		}
	}
	return sortedUnique(violations)
}

var expectedArtifactContractExports = map[string]int{
	mirrorImportPath + ".ArtifactPath":                                 1,
	mirrorImportPath + ".CompletePullArtifact":                         1,
	mirrorImportPath + ".RegistrationArtifact":                         1,
	mirrorImportPath + ".NewPublicArtifactPath":                        1,
	mirrorImportPath + ".Mirror.RegisterNew":                           1,
	mirrorImportPath + ".Mirror.PrepareCompletePullPublication":        1,
	mirrorImportPath + ".Mirror.PrepareCompletePullView":               1,
	mirrorImportPath + ".Mirror.PrepareCompletePullConfluenceComments": 1,
}

var allowedArtifactContractOperations = qualifyArtifactContractOperations(map[string]int{
	"artifact_path.go:NewPublicArtifactPath:call:newArtifactPath":                                                                                       1,
	"artifact_path.go:artifactPathCollisionKey:field:value":                                                                                             1,
	"artifact_path.go:artifactPathDurableString:field:value":                                                                                            1,
	"artifact_path.go:artifactPathFromDurable:call:NewPublicArtifactPath":                                                                               1,
	"artifact_path.go:artifactPathFromDurable:call:newPrivateArtifactPath":                                                                              1,
	"artifact_path.go:artifactPathIsPrivateBase:field:class":                                                                                            1,
	"artifact_path.go:artifactPathIsPublic:field:class":                                                                                                 1,
	"artifact_path.go:artifactPathMatchesDurable:field:value":                                                                                           1,
	"artifact_path.go:artifactPathTarget:field:value":                                                                                                   1,
	"artifact_path.go:newArtifactPath:composite:ArtifactPath":                                                                                           1,
	"artifact_path.go:newPrivateArtifactPath:call:newArtifactPath":                                                                                      1,
	"artifact_path.go:validateArtifactPath:call:newArtifactPath":                                                                                        1,
	"artifact_path.go:validateArtifactPath:field:class":                                                                                                 1,
	"artifact_path.go:validateArtifactPath:field:value":                                                                                                 1,
	"artifact_path_test.go:FuzzArtifactPathConstructors:call:NewPublicArtifactPath":                                                                     1,
	"artifact_path_test.go:FuzzArtifactPathConstructors:call:artifactPathDurableString":                                                                 1,
	"artifact_path_test.go:FuzzArtifactPathConstructors:call:artifactPathFromDurable":                                                                   1,
	"artifact_path_test.go:FuzzArtifactPathConstructors:call:newPrivateArtifactPath":                                                                    1,
	"artifact_path_test.go:TestArtifactPathConstructorsEnforceClosedClasses:call:NewPublicArtifactPath":                                                 1,
	"artifact_path_test.go:TestArtifactPathConstructorsEnforceClosedClasses:call:newPrivateArtifactPath":                                                1,
	"artifact_path_test.go:TestArtifactPathConstructorsEnforceClosedClasses:field:class":                                                                2,
	"artifact_path_test.go:TestArtifactPathConstructorsEnforceClosedClasses:field:value":                                                                2,
	"artifact_path_test.go:TestArtifactPathDurableReparseInfersAndPreservesClass:call:artifactPathDurableString":                                        1,
	"artifact_path_test.go:TestArtifactPathDurableReparseInfersAndPreservesClass:call:artifactPathFromDurable":                                          2,
	"artifact_path_test.go:TestArtifactPathDurableReparseInfersAndPreservesClass:field:class":                                                           1,
	"artifact_path_test.go:TestArtifactPathZeroValueFailsEveryPublicationConsumer:call:artifactPathDurableString":                                       1,
	"artifact_path_test.go:artifactPathStringForTest:call:artifactPathDurableString":                                                                    1,
	"artifact_path_test.go:mustPrivateArtifactPath:call:newPrivateArtifactPath":                                                                         1,
	"artifact_path_test.go:mustPublicArtifactPath:call:NewPublicArtifactPath":                                                                           1,
	"complete_pull.go:validateCompletePullJournalEntry:call:NewPublicArtifactPath":                                                                      1,
	"complete_pull_artifact.go:stagePublicationArtifact:call:artifactPathDurableString":                                                                 1,
	"complete_pull_artifact.go:validatePublicationArtifact:call:artifactPathFromDurable":                                                                1,
	"complete_pull_publication.go:publishArtifact:call:artifactPathFromDurable":                                                                         1,
	"complete_pull_publication.go:relocationPublicationArtifacts:call:NewPublicArtifactPath":                                                            2,
	"complete_pull_publication.go:verifyCommittedPublication:call:artifactPathFromDurable":                                                              2,
	"complete_pull_test.go:appendCompletePullJournalForTest:call:NewPublicArtifactPath":                                                                 1,
	"confluence_complete_test.go:TestCompletePullRecoversStagedPublicationBeforeQualificationWithoutSearchOrRefetch:call:NewPublicArtifactPath":         1,
	"confluence_complete_test.go:seedCompletePullJournal:call:NewPublicArtifactPath":                                                                    1,
	"confluence_pull_phases.go:stagePage:call:NewPublicArtifactPath":                                                                                    2,
	"created_registration.go:createdRegistrationArtifactPath:call:NewPublicArtifactPath":                                                                1,
	"page_artifacts.go:preparePageCommentArtifacts:call:NewPublicArtifactPath":                                                                          2,
	"page_artifacts.go:preparePageFiles:call:NewPublicArtifactPath":                                                                                     3,
	"page_artifacts.go:preparePageFiles:call:newPrivateArtifactPath":                                                                                    1,
	"register.go:prepareRegistration:call:NewPublicArtifactPath":                                                                                        1,
	"register.go:prepareRegistration:call:newPrivateArtifactPath":                                                                                       1,
	"sidecar.go:loadSidecar:call:NewPublicArtifactPath":                                                                                                 1,
	"sidecar.go:validateStagedState:call:NewPublicArtifactPath":                                                                                         1,
	"artifact_path_contract_test.go:TestArtifactPathPublicationAPIAndConversionInventory:dto-composite:CompletePullArtifact":                            1,
	"artifact_path_contract_test.go:TestArtifactPathPublicationAPIAndConversionInventory:dto-composite:RegistrationArtifact":                            1,
	"artifact_path_test.go:TestArtifactPathZeroValueFailsEveryPublicationConsumer:dto-write:CompletePullArtifact.Path":                                  1,
	"complete_pull_publication.go:relocationPublicationArtifacts:dto-composite:CompletePullArtifact":                                                    2,
	"complete_pull_publication_test.go:TestCompletePullPublicationPrivateBoundedAndContentMinimized:dto-composite:CompletePullArtifact":                 1,
	"complete_pull_publication_test.go:TestCompletePullPublicationRelocationRecoversStateThenExactRetirement:dto-composite:CompletePullArtifact":        4,
	"complete_pull_publication_test.go:TestStagePublicationArtifactRejectsInvalidPrivateOptionsBeforeStaging:dto-composite:CompletePullArtifact":        3,
	"complete_pull_publication_test.go:completePullPublicationFixture:dto-composite:CompletePullArtifact":                                               9,
	"complete_pull_test.go:appendCompletePullJournalForTest:dto-composite:CompletePullArtifact":                                                         1,
	"confluence_complete_test.go:TestCompletePullRecoversStagedPublicationBeforeQualificationWithoutSearchOrRefetch:dto-composite:CompletePullArtifact": 1,
	"confluence_complete_test.go:seedCompletePullJournal:dto-composite:CompletePullArtifact":                                                            1,
	"confluence_pull_phases.go:stagePage:dto-composite:CompletePullArtifact":                                                                            3,
	"created_registration.go:createAndRegister:dto-composite:RegistrationArtifact":                                                                      3,
	"created_registration.go:registerConfluenceReadback:dto-composite:RegistrationArtifact":                                                             4,
	"page_artifacts.go:preparePageCommentArtifacts:dto-composite:CompletePullArtifact":                                                                  2,
	"page_artifacts.go:preparePageFiles:dto-composite:CompletePullArtifact":                                                                             4,
	"register_test.go:TestRegisterNewAcceptsExactEmptyNativeBody:dto-composite:RegistrationArtifact":                                                    2,
	"register_test.go:TestRegisterNewDurabilityBarriersAreChildToRootAndStateLast:dto-write:RegistrationArtifact.Path":                                  1,
	"register_test.go:TestRegisterNewRejectsInvalidPlansBeforeWriting:dto-write:RegistrationArtifact.Path":                                              3,
	"register_test.go:TestRegisterNewRollbackDurabilityIsChildToRoot:dto-write:RegistrationArtifact.Path":                                               1,
	"register_test.go:registrationFixture:dto-composite:RegistrationArtifact":                                                                           3,
})

func qualifyArtifactContractOperations(short map[string]int) map[string]int {
	appFiles := map[string]struct{}{
		"confluence_complete_test.go": {},
		"confluence_pull_phases.go":   {},
		"created_registration.go":     {},
	}
	qualified := make(map[string]int, len(short))
	for key, count := range short {
		file, rest, _ := strings.Cut(key, ":")
		packagePath := mirrorImportPath
		directory := "internal/mirror/"
		if _, app := appFiles[file]; app {
			packagePath = "github.com/isukharev/atl/internal/app"
			directory = "internal/app/"
		}
		qualified[packagePath+"|"+directory+file+":"+rest] = count
	}
	return qualified
}
