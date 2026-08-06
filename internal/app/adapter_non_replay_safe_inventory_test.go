package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type adapterRequestClassification struct {
	Disposition string
	Marker      string
}

func TestAdapterNonReplaySafeRequestInventory(t *testing.T) {
	const (
		mutating   = "mutating"
		readIntent = "read_intent"
		noMarker   = "none"
	)
	mutatingNoMarker := adapterRequestClassification{Disposition: mutating, Marker: noMarker}
	want := map[string]adapterRequestClassification{
		"confluence/blogposts.go:CreateBlogPost:SendJSON:POST":                      mutatingNoMarker,
		"confluence/comment_mutation_provider.go:createInline:DoWithBodyLimit:POST": mutatingNoMarker,
		"confluence/comment_mutation_provider.go:reply:DoWithBodyLimit:POST":        mutatingNoMarker,
		"confluence/comment_mutation_provider.go:setResolved:DoWithBodyLimit:PUT":   mutatingNoMarker,
		"confluence/confluence.go:CreatePage:SendJSON:POST":                         mutatingNoMarker,
		"confluence/confluence.go:DeletePage:Do:DELETE":                             mutatingNoMarker,
		"confluence/confluence.go:MovePage:SendJSON:PUT":                            mutatingNoMarker,
		"confluence/confluence.go:UpdatePage:SendJSON:PUT":                          mutatingNoMarker,
		"confluence/extras.go:AddComment:SendJSON:POST":                             mutatingNoMarker,
		"confluence/extras.go:DeleteAttachment:Do:DELETE":                           mutatingNoMarker,
		"confluence/extras.go:UploadAttachment:DoStreamSized:POST":                  mutatingNoMarker,
		"confluence/labels.go:AddContentLabels:Do:POST":                             mutatingNoMarker,
		"confluence/labels.go:RemoveContentLabel:Do:DELETE":                         mutatingNoMarker,
		"jira/agile.go:MoveIssuesToBacklog:SendJSON:POST":                           mutatingNoMarker,
		"jira/agile.go:MoveIssuesToSprint:SendJSON:POST":                            mutatingNoMarker,
		"jira/jira.go:AddComment:SendJSON:POST":                                     mutatingNoMarker,
		"jira/jira.go:Assign:SendJSON:PUT":                                          mutatingNoMarker,
		"jira/jira.go:Create:SendJSON:POST":                                         mutatingNoMarker,
		"jira/jira.go:DeleteComment:SendJSON:DELETE":                                mutatingNoMarker,
		"jira/jira.go:DeleteIssue:SendJSON:DELETE":                                  mutatingNoMarker,
		"jira/jira.go:DeleteLink:SendJSON:DELETE":                                   mutatingNoMarker,
		"jira/jira.go:Link:SendJSON:POST":                                           mutatingNoMarker,
		"jira/jira.go:LinkEpic:SendJSON:PUT":                                        mutatingNoMarker,
		"jira/jira.go:SetFields:SendJSON:PUT":                                       mutatingNoMarker,
		"jira/jira.go:TransitionByID:SendJSON:POST":                                 mutatingNoMarker,
		"jira/jira.go:Update:SendJSON:PUT":                                          mutatingNoMarker,
		"jira/jira.go:UpdateLabels:SendJSON:PUT":                                    mutatingNoMarker,
		"jira/meta.go:UploadAttachment:DoStreamSized:POST":                          mutatingNoMarker,
		"jira/structure.go:StructureValues:SendJSON:POST": {
			Disposition: readIntent,
			Marker:      noMarker,
		},
		"jira/watchers.go:AddIssueWatcher:Do:POST":       mutatingNoMarker,
		"jira/watchers.go:RemoveIssueWatcher:Do:DELETE":  mutatingNoMarker,
		"jira/worklogs.go:AddIssueWorklog:SendJSON:POST": mutatingNoMarker,
	}

	got, lookalikes := collectAdapterNonReplaySafeRequests(t)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("non-replay-safe adapter request inventory = %#v, want exactly %#v", got, want)
	}
	wantLookalikes := map[string]bool{"confluence/extras.go:Close:r.once:Do": true}
	if !reflect.DeepEqual(lookalikes, wantLookalikes) {
		t.Fatalf("transport-method lookalike inventory = %#v, want exactly %#v", lookalikes, wantLookalikes)
	}

	dispositions := map[string]int{}
	for _, classification := range got {
		dispositions[classification.Disposition]++
	}
	if len(got) != 32 || dispositions[mutating] != 31 || dispositions[readIntent] != 1 {
		t.Fatalf("inventory counts = total %d, mutating %d, read-intent %d; want 32/31/1", len(got), dispositions[mutating], dispositions[readIntent])
	}
}

func collectAdapterNonReplaySafeRequests(t *testing.T) (map[string]adapterRequestClassification, map[string]bool) {
	t.Helper()
	methodArgRequests := map[string]bool{
		"Do": true, "DoStream": true, "DoStreamSized": true,
		"DoWithBodyLimit": true, "SendJSON": true,
	}
	clientMethods := map[string]bool{
		"Do": true, "DoStream": true, "DoStreamSized": true,
		"DoWithBodyLimit": true, "GetJSON": true, "GetJSONUseNumber": true,
		"GetStream": true, "ResolveGET": true, "SendJSON": true,
	}
	packages := []struct {
		name, directory string
	}{
		{name: "confluence", directory: filepath.Join("..", "adapter", "confluence")},
		{name: "jira", directory: filepath.Join("..", "adapter", "jira")},
	}
	got := make(map[string]adapterRequestClassification)
	lookalikes := make(map[string]bool)
	for _, packageRoot := range packages {
		entries, err := os.ReadDir(packageRoot.directory)
		if err != nil {
			t.Fatalf("read adapter package %s: %v", packageRoot.name, err)
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				t.Fatalf("adapter package %s contains symbolic link %s", packageRoot.name, entry.Name())
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(packageRoot.directory, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok {
					if selectors := adapterTransportClientSelectors(declaration); len(selectors) != 0 {
						t.Fatalf("%s uses transport client selectors outside function declarations: %v", path, selectors)
					}
					if markers := adapterDetachedRequestMarkers(declaration, clientMethods); len(markers) != 0 {
						t.Fatalf("%s constructs request markers outside function declarations: %v", path, markers)
					}
					continue
				}
				if function.Body == nil {
					continue
				}
				if unknown := adapterUnknownTransportSelectors(function.Body, clientMethods); len(unknown) != 0 {
					t.Fatalf("%s uses unknown transport client selectors: %v", path, unknown)
				}
				if values := adapterTransportMethodValues(function.Body, clientMethods); len(values) != 0 {
					t.Fatalf("%s uses transport method values instead of direct calls: %v", path, values)
				}
				if markers := adapterDetachedRequestMarkers(function.Body, clientMethods); len(markers) != 0 {
					t.Fatalf("%s constructs request markers outside direct request context arguments: %v", path, markers)
				}
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					selector, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || !methodArgRequests[selector.Sel.Name] {
						return true
					}
					receiver := guardedWriteReceiver(selector.X)
					if !strings.HasSuffix(receiver, ".c") {
						key := packageRoot.name + "/" + entry.Name() + ":" + function.Name.Name + ":" + receiver + ":" + selector.Sel.Name
						if lookalikes[key] {
							t.Fatalf("duplicate transport-method lookalike %s", key)
						}
						lookalikes[key] = true
						return true
					}
					if len(call.Args) < 2 {
						t.Fatalf("%s.%s in %s has no static HTTP method argument", receiver, selector.Sel.Name, path)
					}
					method, ok := staticHTTPMethod(call.Args[1])
					if !ok {
						t.Fatalf("%s.%s in %s uses a dynamic or unsupported HTTP method", receiver, selector.Sel.Name, path)
					}
					if method == "GET" || method == "HEAD" {
						return true
					}
					key := packageRoot.name + "/" + entry.Name() + ":" + function.Name.Name + ":" + selector.Sel.Name + ":" + method
					if _, exists := got[key]; exists {
						t.Fatalf("duplicate non-replay-safe adapter request %s", key)
					}
					disposition := "mutating"
					if key == "jira/structure.go:StructureValues:SendJSON:POST" {
						disposition = "read_intent"
					}
					got[key] = adapterRequestClassification{
						Disposition: disposition,
						Marker:      adapterRequestMarker(call.Args[0]),
					}
					return true
				})
			}
		}
	}
	return got, lookalikes
}

func adapterTransportClientSelectors(node ast.Node) []string {
	var selectors []string
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver := guardedWriteReceiver(selector.X)
		if strings.HasSuffix(receiver, ".c") {
			selectors = append(selectors, receiver+"."+selector.Sel.Name)
		}
		return true
	})
	return selectors
}

func adapterUnknownTransportSelectors(node ast.Node, methods map[string]bool) []string {
	var unknown []string
	for _, selector := range adapterTransportClientSelectors(node) {
		method := selector[strings.LastIndex(selector, ".")+1:]
		if !methods[method] {
			unknown = append(unknown, selector)
		}
	}
	return unknown
}

func staticHTTPMethod(expression ast.Expr) (string, bool) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		decoded, err := strconv.Unquote(value.Value)
		if err != nil {
			return "", false
		}
		return decoded, true
	case *ast.SelectorExpr:
		packageName, ok := value.X.(*ast.Ident)
		if !ok || packageName.Name != "http" || !strings.HasPrefix(value.Sel.Name, "Method") {
			return "", false
		}
		method := strings.ToUpper(strings.TrimPrefix(value.Sel.Name, "Method"))
		if method == "" {
			return "", false
		}
		return method, true
	default:
		return "", false
	}
}

func adapterTransportMethodValues(body ast.Node, methods map[string]bool) []string {
	directCalls := make(map[token.Pos]bool)
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && methods[selector.Sel.Name] {
			directCalls[selector.Pos()] = true
		}
		return true
	})
	var values []string
	ast.Inspect(body, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || !methods[selector.Sel.Name] || directCalls[selector.Pos()] {
			return true
		}
		values = append(values, guardedWriteReceiver(selector.X)+"."+selector.Sel.Name)
		return true
	})
	return values
}

func TestAdapterInventoryPreservesLiteralHTTPMethodCase(t *testing.T) {
	expression, err := parser.ParseExpr(`"get"`)
	if err != nil {
		t.Fatal(err)
	}
	method, ok := staticHTTPMethod(expression)
	if !ok || method != "get" {
		t.Fatalf("static HTTP method = %q, %t; want lowercase literal preserved", method, ok)
	}
}

func TestAdapterInventoryRejectsTransportMethodValues(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package fixture
func f() {
	send := j.c.SendJSON
	_ = send
}
`, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	function := file.Decls[0].(*ast.FuncDecl)
	got := adapterTransportMethodValues(function.Body, map[string]bool{"SendJSON": true})
	want := []string{"j.c.SendJSON"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transport method values = %v, want %v", got, want)
	}
}

func TestAdapterInventoryRejectsUnknownTransportSelectors(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package fixture
func f() { _ = j.c.PostJSON(ctx, path, payload) }
`, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	function := file.Decls[0].(*ast.FuncDecl)
	got := adapterUnknownTransportSelectors(function.Body, map[string]bool{"SendJSON": true})
	want := []string{"j.c.PostJSON"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unknown transport selectors = %v, want %v", got, want)
	}
}

func TestAdapterInventoryFindsTransportSelectorsOutsideFunctions(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package fixture
var send = j.c.SendJSON
`, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := adapterTransportClientSelectors(file.Decls[0])
	want := []string{"j.c.SendJSON"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transport selectors outside functions = %v, want %v", got, want)
	}
}

func adapterRequestMarker(expression ast.Expr) string {
	marker := "none"
	ast.Inspect(expression, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if !ok || packageName.Name != "domain" {
			return true
		}
		switch selector.Sel.Name {
		case "WithReadIntent":
			marker = "read_intent"
		case "WithWriteClearance":
			marker = "write_clearance"
		}
		return true
	})
	return marker
}

func adapterDetachedRequestMarkers(node ast.Node, methods map[string]bool) []string {
	attached := make(map[token.Pos]bool)
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !methods[selector.Sel.Name] {
			return true
		}
		receiver := guardedWriteReceiver(selector.X)
		if !strings.HasSuffix(receiver, ".c") {
			return true
		}
		ast.Inspect(call.Args[0], func(node ast.Node) bool {
			if marker, ok := adapterRequestMarkerName(node); ok {
				attached[marker.Pos()] = true
			}
			return true
		})
		return true
	})
	var detached []string
	ast.Inspect(node, func(node ast.Node) bool {
		marker, ok := adapterRequestMarkerName(node)
		if ok && !attached[marker.Pos()] {
			detached = append(detached, marker.Sel.Name)
		}
		return true
	})
	return detached
}

func adapterRequestMarkerName(node ast.Node) (*ast.SelectorExpr, bool) {
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "domain" {
		return nil, false
	}
	return selector, selector.Sel.Name == "WithReadIntent" || selector.Sel.Name == "WithWriteClearance"
}

func TestAdapterInventoryRejectsDetachedRequestMarkers(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package fixture
func f() {
	marked := domain.WithWriteClearance(ctx)
	_ = j.c.SendJSON(marked, "POST", path, nil, nil)
}
`, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	function := file.Decls[0].(*ast.FuncDecl)
	got := adapterDetachedRequestMarkers(function.Body, map[string]bool{"SendJSON": true})
	want := []string{"WithWriteClearance"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detached request markers = %v, want %v", got, want)
	}
}
