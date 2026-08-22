package jira

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestJiraPaginationOwnerInventoryIsClosed(t *testing.T) {
	want := map[string][]string{
		"agile.go:Boards":                                     {"qualifiedAgileValuesPage", "requestStartAt"},
		"agile.go:SprintIssues":                               {"agileNext", "requestStartAt"},
		"agile.go:Sprints":                                    {"qualifiedAgileValuesPage", "requestStartAt"},
		"agile.go:agileNext":                                  {"advance", "matches", "requested", "requested", "requested", "requested"},
		"agile.go:boardIssuePage":                             {"agileNext", "requestStartAt"},
		"agile.go:qualifiedAgileValuesPage":                   {"advance", "matches", "requested", "requested", "requested"},
		"create_metadata.go:collectQualifiedCreateFields":     {"advance", "matches", "requestStartAt", "requested"},
		"create_metadata.go:collectQualifiedCreateIssueTypes": {"advance", "matches", "requestStartAt", "requested"},
		"create_metadata.go:readCreateFields":                 {"advance", "matches", "requestStartAt", "requested", "requested"},
		"create_metadata.go:readCreateIssueTypes":             {"advance", "matches", "requestStartAt", "requested", "requested"},
		"evidence.go:ListJiraCommentsQualified":               {"advance", "matches", "requestStartAt", "requested"},
		"inverse_reference.go:SelectInverseReferencePage":     {"requestStartAt"},
		"jira.go:CompleteChangelog":                           {"advance", "matches", "requestStartAt", "requested", "requested", "requested"},
		"jira.go:ListComments":                                {"advance", "matches", "requestStartAt", "requested", "requested", "requested", "requested", "requested", "requested", "requested", "requested", "requested", "requested"},
		"jira.go:searchPage":                                  {"advance", "matches", "requestStartAt"},
		"worklogs.go:ListIssueWorklogs":                       {"advance", "matches", "requestStartAt", "requested", "requested", "requested", "requested", "requested", "requested", "requested", "requested"},
	}
	trackedSelectors := map[string]bool{"advance": true, "matches": true, "requested": true}
	got := map[string][]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			key := entry.Name() + ":" + function.Name.Name
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
					value, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr == nil && strings.Contains(value, "startAt=%") {
						got[key] = append(got[key], "requestStartAt")
					}
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch called := call.Fun.(type) {
				case *ast.Ident:
					if called.Name == "agileNext" || called.Name == "qualifiedAgileValuesPage" {
						got[key] = append(got[key], called.Name)
					}
				case *ast.SelectorExpr:
					if trackedSelectors[called.Sel.Name] {
						got[key] = append(got[key], called.Sel.Name)
					}
					if called.Sel.Name == "Set" && len(call.Args) > 0 {
						if literal, literalOK := call.Args[0].(*ast.BasicLit); literalOK {
							value, unquoteErr := strconv.Unquote(literal.Value)
							if unquoteErr == nil && value == "startAt" {
								got[key] = append(got[key], "requestStartAt")
							}
						}
					}
				}
				return true
			})
		}
	}
	for key := range got {
		sort.Strings(got[key])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Jira checked-pagination owners = %#v, want exactly %#v", got, want)
	}
}

const jiraProductionLoopKeys = `
agile.go:Boards:range:1
agile.go:SprintIssues:range:1
agile.go:Sprints:range:1
agile.go:boardIssuePage:range:1
agile.go:toDomain:range:1
agile.go:toDomain:range:2
authorization.go:addWriteVerb:range:1
authorization.go:evictReference:range:1
authorization.go:fail:for:1
authorization.go:issueTargets:range:1
authorization.go:put:for:1
authorization.go:removeCanonical:range:1
create_metadata.go:ReadCreateIssueTypes:range:1
create_metadata.go:ReadQualifiedCreateMetadata:range:1
create_metadata.go:ReadQualifiedCreateMetadata:range:2
create_metadata.go:collectQualifiedCreateFields:for:1
create_metadata.go:collectQualifiedCreateIssueTypes:for:1
create_metadata.go:readCreateFields:for:1
create_metadata.go:readCreateIssueTypes:for:1
create_metadata.go:readCreateMetadataFields:range:1
development.go:ReadIssueDevelopment:range:1
development.go:ReadIssueDevelopment:range:2
development.go:addDetail:range:1
development.go:addDetail:range:2
development.go:addDetail:range:3
development.go:addDetail:range:4
development.go:addDetail:range:5
development.go:addDetail:range:6
development.go:addDetail:range:7
development.go:addDetail:range:8
development.go:decodeDevelopmentSummary:range:1
development.go:decodeDevelopmentSummary:range:2
development.go:decodeDevelopmentSummary:range:3
development.go:developmentCategoryCounts:range:1
development.go:developmentMapCount:range:1
development.go:normalized:range:1
development.go:normalized:range:2
development.go:normalized:range:3
development.go:normalized:range:4
development.go:parseDevelopmentArtifact:for:1
development.go:parseDevelopmentArtifact:range:1
development.go:parseDevelopmentArtifact:range:2
development.go:validDevelopmentBranch:range:1
evidence.go:ListJiraAttachmentsQualified:range:1
evidence.go:ListJiraCommentsQualified:for:1
evidence.go:ListJiraCommentsQualified:range:1
evidence.go:RevalidateJiraAttachmentDownload:range:1
evidence.go:jiraCommentConservativeEncodedBytes:range:1
fields.go:coerceCreateFields:range:1
fields.go:coerceFields:range:1
fields.go:reservedCreateField:range:1
graph.go:ReadIssueRemoteLinks:range:1
graph.go:validJiraRemoteLinkMetadata:range:1
guarded_links.go:ReadStrictLinkEndpoint:range:1
guarded_links.go:ReadStrictLinkTypes:range:1
inverse_reference.go:ReadInverseReferenceSnapshot:range:1
inverse_reference.go:ReadInverseReferenceSnapshot:range:2
inverse_reference.go:ReadInverseReferenceSnapshot:range:3
inverse_reference.go:ReadInverseReferenceSnapshot:range:4
inverse_reference.go:SelectInverseReferencePage:range:1
inverse_reference.go:validInverseReferenceIdentifier:range:1
jira.go:CompleteChangelog:for:1
jira.go:Create:range:1
jira.go:LinkEpic:range:1
jira.go:ListComments:for:1
jira.go:ListComments:range:1
jira.go:SearchUsers:range:1
jira.go:Transition:range:1
jira.go:Transition:range:2
jira.go:UpdateLabels:range:1
jira.go:UpdateLabels:range:2
jira.go:mapChangelogHistories:range:1
jira.go:mapChangelogHistories:range:2
jira.go:resolveAdapterOptions:range:1
jira.go:searchPage:range:1
jira.go:updateWithTypedFields:range:1
meta.go:DownloadAttachment:range:1
meta.go:FieldOptions:range:1
meta.go:FieldOptions:range:2
meta.go:FieldOptions:range:3
meta.go:FieldOptions:range:4
meta.go:LinkTypes:range:1
meta.go:ListAttachments:range:1
meta.go:ReadFieldCatalog:range:1
meta.go:Transitions:range:1
mirror_metadata.go:PlanIssueMetadataBatches:for:1
mirror_metadata.go:PlanIssueMetadataBatches:for:2
mirror_metadata.go:ReadIssueMetadataBatch:range:1
mirror_metadata.go:quoteJiraJQLString:range:1
projects.go:ReadProjects:range:1
server_metadata.go:decimalBuildNumber:range:1
structure.go:StructureValues:range:1
structure.go:extractInt64s:range:1
structure.go:mapStructureValues:range:1
structure.go:stringMap:range:1
watchers.go:ListIssueWatchers:range:1
worklogs.go:ListIssueWorklogs:for:1
worklogs.go:ListIssueWorklogs:range:1
`

func TestJiraProductionLoopInventoryIsClosed(t *testing.T) {
	pagination := map[string]bool{
		"create_metadata.go:collectQualifiedCreateFields:for:1":     true,
		"create_metadata.go:collectQualifiedCreateIssueTypes:for:1": true,
		"create_metadata.go:readCreateFields:for:1":                 true,
		"create_metadata.go:readCreateIssueTypes:for:1":             true,
		"evidence.go:ListJiraCommentsQualified:for:1":               true,
		"jira.go:CompleteChangelog:for:1":                           true,
		"jira.go:ListComments:for:1":                                true,
		"worklogs.go:ListIssueWorklogs:for:1":                       true,
	}
	want := classifiedJiraLoops(strings.Fields(jiraProductionLoopKeys), pagination)
	got := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for key, reason := range collectJiraLoops(entry.Name(), file, pagination) {
			got[key] = reason
		}
	}
	if err := validateJiraLoopInventory(got, want); err != nil {
		t.Fatal(err)
	}
}

func TestJiraProductionLoopInventoryRejectsRawOffsetLoop(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "raw_offset.go", `package jira
func rawPage() {
	for skip := 0; ; skip += 9 {
		query := "?page-skip="
		_ = query
		if skip > 27 { break }
	}
}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJiraLoopInventory(collectJiraLoops("raw_offset.go", file, nil), map[string]string{}); err == nil {
		t.Fatal("raw offset loop passed the closed production inventory")
	}
}

func classifiedJiraLoops(keys []string, pagination map[string]bool) map[string]string {
	classified := make(map[string]string, len(keys))
	for _, key := range keys {
		reason := "non-pagination: bounded local scan, cache, or batch loop"
		if strings.Contains(key, ":range:") {
			reason = "non-pagination: finite in-memory collection traversal"
		}
		if pagination[key] {
			reason = "pagination: checked provider-local cursor loop"
		}
		classified[key] = reason
	}
	return classified
}

func validateJiraLoopInventory(got, want map[string]string) error {
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("Jira production loop classifications = %#v, want exactly %#v", got, want)
	}
	return nil
}

func collectJiraLoops(fileName string, file *ast.File, pagination map[string]bool) map[string]string {
	functionAt := func(position token.Pos) string {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Body != nil && function.Body.Pos() <= position && position <= function.Body.End() {
				return function.Name.Name
			}
		}
		return "<package>"
	}
	counts := map[string]int{}
	keys := []string{}
	ast.Inspect(file, func(node ast.Node) bool {
		kind := ""
		switch node.(type) {
		case *ast.ForStmt:
			kind = "for"
		case *ast.RangeStmt:
			kind = "range"
		}
		if kind == "" {
			return true
		}
		function := functionAt(node.Pos())
		counter := function + "\x00" + kind
		counts[counter]++
		keys = append(keys, fileName+":"+function+":"+kind+":"+strconv.Itoa(counts[counter]))
		return true
	})
	return classifiedJiraLoops(keys, pagination)
}
