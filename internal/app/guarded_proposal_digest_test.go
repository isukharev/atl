package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestGuardedProposalDigestExactVectors(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{name: "empty", body: nil, want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "text", body: []byte("reviewed proposal"), want: "43e48f9d5708d77148b4ae1f84c6550393fd372a2fb7a1b52e4edec3e3eac717"},
		{name: "binary", body: []byte{0x00, 0xff, 0x10, 0x80}, want: "a33bb2aed757bc839807d7a9deab0688c3cf06d36e53cb428f2e539c8dc76c5b"},
		{name: "one byte difference a", body: []byte("proposal-a"), want: "ce8e7cd227d83b8feeeabe47ca28d84523deaedcf3c0ced1a3c10575e9082b92"},
		{name: "one byte difference b", body: []byte("proposal-b"), want: "4a2022488c3542950b77e63ab92a5a04c930cdbd9626d18b02f861e7fec53db2"},
	}
	lowerHex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := guardedProposalDigest(test.body)
			if got != test.want {
				t.Fatalf("guardedProposalDigest(%x) = %q, want %q", test.body, got, test.want)
			}
			if !lowerHex.MatchString(got) {
				t.Fatalf("digest = %q, want exactly 64 lowercase hexadecimal characters", got)
			}
		})
	}
	if guardedProposalDigest([]byte("proposal-a")) == guardedProposalDigest([]byte("proposal-b")) {
		t.Fatal("one-byte input difference did not change the digest")
	}
}

func TestGuardedProposalHashDigestInventory(t *testing.T) {
	type contract struct {
		file     string
		argument string
	}
	want := map[string]contract{
		"confluenceAttachmentDeleteProposalHash": {file: "confluence_attachment_delete.go", argument: "canonical"},
		"confluenceCommentMutationProposalHash":  {file: "confluence_comment_mutations_guarded.go", argument: "canonical"},
		"confluenceFooterCommentProposalHash":    {file: "confluence_comments_guarded.go", argument: "canonical"},
		"confluenceInlineCreateProposalHash":     {file: "confluence_inline_comment_create_guarded.go", argument: "canonical"},
		"confluenceLabelProposalHash":            {file: "confluence_labels.go", argument: "canonical"},
		"confluenceMoveProposalHash":             {file: "confluence_move.go", argument: "canonical"},
		"confluencePageCopyProposalHash":         {file: "confluence_page_copy.go", argument: "canonical"},
		"confluencePageTrashProposalHash":        {file: "confluence_page_trash.go", argument: "canonical"},
		"confluencePlanHash":                     {file: "confluence_plan.go", argument: "canonical"},
		"confluenceTitleProposalHash":            {file: "confluence_title.go", argument: "canonical"},
		"jiraCommentProposalHash":                {file: "jira_comments_guarded.go", argument: "canonical"},
		"jiraDescriptionEditProposalHash":        {file: "jira_description_edit.go", argument: "canonical"},
		"guardedLinkProposalHash":                {file: "jira_links_guarded.go", argument: "canonical"},
		"jiraFieldProposalHash":                  {file: "jira_field_set.go", argument: "encoded"},
		"jiraIssueDeleteProposalHash":            {file: "jira_issue_delete.go", argument: "canonical"},
		"jiraTransitionProposalHash":             {file: "jira_transition_guarded.go", argument: "encoded"},
		"jiraWatcherProposalHash":                {file: "jira_watchers.go", argument: "canonical"},
		"jiraWorklogProposalHash":                {file: "jira_worklogs.go", argument: "canonical"},
	}
	wantOwners := make(map[string]string, len(want))
	for name, expected := range want {
		wantOwners[name] = expected.file
	}

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob production Go files: %v", err)
	}
	discoveredOwners := make(map[string]string)
	digestCalls := make(map[string]contract)
	allDigestCalls := make([]string, 0, len(want))
	marshalCalls := make(map[string]int)
	operationDiscriminators := make(map[string]string)
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee, ok := call.Fun.(*ast.Ident)
			if ok && callee.Name == "guardedProposalDigest" {
				allDigestCalls = append(allDigestCalls, fset.Position(call.Pos()).String())
			}
			return true
		})
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := function.Name.Name
			// nativeReconcileProposal is intentionally excluded: it owns distinct
			// generic marshal and error-return semantics rather than a guarded hash tail.
			isOwner := strings.HasSuffix(name, "ProposalHash") || name == "confluencePlanHash"
			if isOwner {
				if previous, exists := discoveredOwners[name]; exists {
					t.Fatalf("duplicate production function %s in %s and %s", name, previous, path)
				}
				discoveredOwners[name] = filepath.Base(path)
			}

			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					packageName, packageOK := selector.X.(*ast.Ident)
					if isOwner && packageOK && packageName.Name == "json" && selector.Sel.Name == "Marshal" {
						marshalCalls[name]++
						if name == "jiraDescriptionEditProposalHash" && len(call.Args) == 1 {
							operationDiscriminators[name] = guardedProposalOperationLiteral(t, call.Args[0])
						}
					}
					return true
				}
				callee, ok := call.Fun.(*ast.Ident)
				if !ok || callee.Name != "guardedProposalDigest" {
					return true
				}
				if previous, exists := digestCalls[name]; exists {
					t.Fatalf("%s: multiple guardedProposalDigest calls in %s (first in %s)", name, path, previous.file)
				}
				argument := "<non-identifier>"
				if len(call.Args) == 1 {
					if identifier, ok := call.Args[0].(*ast.Ident); ok {
						argument = identifier.Name
					}
				}
				digestCalls[name] = contract{file: filepath.Base(path), argument: argument}
				return true
			})
		}
	}
	if !reflect.DeepEqual(discoveredOwners, wantOwners) {
		t.Fatalf("production guarded proposal owner inventory = %#v, want exactly %#v", discoveredOwners, wantOwners)
	}
	if !reflect.DeepEqual(digestCalls, want) {
		t.Fatalf("all production guardedProposalDigest calls = %#v, want exactly %#v", digestCalls, want)
	}
	if len(allDigestCalls) != len(want) {
		t.Fatalf("all production guardedProposalDigest call sites = %v, want exactly %d classified function calls", allDigestCalls, len(want))
	}
	for name := range want {
		if marshalCalls[name] != 1 {
			t.Errorf("%s: json.Marshal calls = %d, want exactly 1", name, marshalCalls[name])
		}
	}
	if !reflect.DeepEqual(operationDiscriminators, map[string]string{"jiraDescriptionEditProposalHash": "edit_description"}) {
		t.Fatalf("guarded proposal operation discriminators = %#v, want exact Jira edit member", operationDiscriminators)
	}
}

func guardedProposalOperationLiteral(t *testing.T, expression ast.Expr) string {
	t.Helper()
	composite, ok := expression.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("guarded proposal canonical value is %T, want anonymous struct composite literal", expression)
	}
	structure, ok := composite.Type.(*ast.StructType)
	if !ok || structure.Fields == nil || len(structure.Fields.List) != len(composite.Elts) {
		t.Fatalf("guarded proposal canonical value is not a positional anonymous struct literal")
	}
	for index, field := range structure.Fields.List {
		if len(field.Names) != 1 || field.Names[0].Name != "Operation" {
			continue
		}
		if field.Tag == nil {
			t.Fatal("guarded proposal Operation member has no JSON tag")
		}
		tag, err := strconv.Unquote(field.Tag.Value)
		if err != nil || tag != `json:"operation"` {
			t.Fatalf("guarded proposal Operation tag=%q err=%v, want exact json operation member", field.Tag.Value, err)
		}
		literal, ok := composite.Elts[index].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			t.Fatalf("guarded proposal Operation value is %T, want string literal", composite.Elts[index])
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("decode guarded proposal Operation literal: %v", err)
		}
		return value
	}
	t.Fatal("guarded proposal canonical value has no Operation JSON member")
	return ""
}
