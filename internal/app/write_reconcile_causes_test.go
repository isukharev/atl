package app

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/domain"
)

type operationCauseTestError struct{ marker string }

func (e *operationCauseTestError) Error() string { return e.marker }

type notAttemptedTestError struct{}

func (notAttemptedTestError) Error() string                  { return "write was not attempted" }
func (notAttemptedTestError) DiagnosticWriteAttempted() bool { return false }

func TestOperationErrorCausesExactVectors(t *testing.T) {
	typed := &operationCauseTestError{marker: "typed"}
	joined := errors.Join(domain.ErrForbidden, typed)
	tests := []struct {
		name   string
		cause  error
		closed bool
		want   []error
	}{
		{name: "open nil", want: []error{}},
		{name: "closed nil", closed: true, want: []error{domain.ErrCheckFailed}},
		{name: "open sentinel", cause: domain.ErrForbidden, want: []error{domain.ErrForbidden}},
		{name: "closed sentinel", cause: domain.ErrForbidden, closed: true, want: []error{domain.ErrCheckFailed, domain.ErrForbidden}},
		{name: "open typed", cause: typed, want: []error{typed}},
		{name: "closed typed", cause: typed, closed: true, want: []error{domain.ErrCheckFailed, typed}},
		{name: "open joined", cause: joined, want: []error{joined}},
		{name: "closed joined", cause: joined, closed: true, want: []error{domain.ErrCheckFailed, joined}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := operationErrorCauses(test.cause, test.closed)
			if got == nil {
				t.Fatal("operationErrorCauses returned a nil slice")
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("causes = %#v, want exact ordered slice %#v", got, test.want)
			}
			joinedResult := errors.Join(got...)
			if errors.Is(joinedResult, domain.ErrCheckFailed) != test.closed {
				t.Fatalf("ErrCheckFailed visibility = %t, want %t", errors.Is(joinedResult, domain.ErrCheckFailed), test.closed)
			}
			if test.cause != nil && !errors.Is(joinedResult, test.cause) {
				t.Fatalf("joined result lost original cause %v", test.cause)
			}
			var gotTyped *operationCauseTestError
			if errors.As(test.cause, &gotTyped) && !errors.As(joinedResult, &gotTyped) {
				t.Fatalf("joined result lost typed cause %T", test.cause)
			}
		})
	}
}

func TestPolicyDenialIsDefinitiveAndPreservesLocalMessage(t *testing.T) {
	denial := &contentpolicy.DenialError{Reason: contentpolicy.ReasonExplicitDeny, RuleID: "deny-ml"}
	if !definitiveWriteRejection(denial) {
		t.Fatal("policy denial was not classified as definitive")
	}
	if !writeDefinitelyNotAttempted(denial) {
		t.Fatal("policy denial lost not-attempted evidence")
	}
	if got := sanitizeRemoteWriteCause(denial); got != denial {
		t.Fatalf("sanitized policy denial = %#v, want original", got)
	}
	if got := classifyCreateWriteError("create", denial); got != denial {
		t.Fatalf("create classification = %#v, want original denial", got)
	}
	want := denial.Error()
	for name, err := range map[string]error{
		"field":      &jiraFieldWriteError{message: "remote", cause: denial},
		"worklog":    &jiraWorklogWriteError{message: "remote", cause: denial},
		"watcher":    &jiraWatcherWriteError{message: "remote", cause: denial},
		"comment":    &jiraCommentWriteError{message: "remote", cause: denial, closed: true},
		"delete":     &jiraIssueDeleteWriteError{message: "remote", cause: denial},
		"transition": &jiraTransitionWriteError{message: "remote", cause: denial, closed: true},
	} {
		if err.Error() != want || !errors.Is(err, domain.ErrCheckFailed) {
			t.Errorf("%s error=%q check_failed=%t, want %q/true", name, err, errors.Is(err, domain.ErrCheckFailed), want)
		}
	}
}

func TestGenericNotAttemptedErrorIsDefinitive(t *testing.T) {
	err := notAttemptedTestError{}
	if !writeDefinitelyNotAttempted(err) || !definitiveWriteRejection(err) {
		t.Fatal("generic not-attempted evidence was not classified as definitive")
	}
	if got := sanitizeRemoteWriteCause(err); got != err {
		t.Fatalf("sanitized not-attempted error = %#v, want original", got)
	}
}

func TestGuardedWriteErrorCauseAdapters(t *testing.T) {
	typed := &operationCauseTestError{marker: "typed"}
	tests := []struct {
		name   string
		err    error
		closed bool
	}{
		{name: "comment mutation", err: &confluenceCommentMutationWriteError{cause: typed, closed: true}, closed: true},
		{name: "footer comment", err: &confluenceFooterCommentWriteError{cause: typed, closed: true}, closed: true},
		{name: "page copy", err: &confluencePageCopyWriteError{cause: typed, closed: true}, closed: true},
		{name: "page trash", err: &confluencePageTrashWriteError{cause: typed, closed: true}, closed: true},
		{name: "jira comment", err: &jiraCommentWriteError{cause: typed, closed: true}, closed: true},
		{name: "jira transition", err: &jiraTransitionWriteError{cause: typed, closed: true}, closed: true},
		{name: "jira description edit", err: &jiraDescriptionEditError{cause: typed, closed: true}, closed: true},
		{name: "attachment delete", err: &confluenceAttachmentDeleteWriteError{cause: typed}, closed: true},
		{name: "jira issue delete", err: &jiraIssueDeleteWriteError{cause: typed}, closed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if errors.Is(test.err, domain.ErrCheckFailed) != test.closed {
				t.Fatalf("ErrCheckFailed visibility = %t, want %t", errors.Is(test.err, domain.ErrCheckFailed), test.closed)
			}
			var got *operationCauseTestError
			if !errors.As(test.err, &got) || got != typed {
				t.Fatalf("typed cause = %#v, want %#v", got, typed)
			}
		})
	}

	var commentMutation *confluenceCommentMutationWriteError
	var footerComment *confluenceFooterCommentWriteError
	var pageCopy *confluencePageCopyWriteError
	var pageTrash *confluencePageTrashWriteError
	var jiraComment *jiraCommentWriteError
	var jiraTransition *jiraTransitionWriteError
	var attachmentDelete *confluenceAttachmentDeleteWriteError
	var issueDelete *jiraIssueDeleteWriteError
	for name, causes := range map[string][]error{
		"comment mutation":  commentMutation.Unwrap(),
		"footer comment":    footerComment.Unwrap(),
		"page copy":         pageCopy.Unwrap(),
		"page trash":        pageTrash.Unwrap(),
		"jira comment":      jiraComment.Unwrap(),
		"jira transition":   jiraTransition.Unwrap(),
		"attachment delete": attachmentDelete.Unwrap(),
		"jira issue delete": issueDelete.Unwrap(),
	} {
		if causes != nil {
			t.Errorf("%s nil receiver causes = %#v, want nil", name, causes)
		}
	}
}

func TestGuardedWriteErrorCauseAdapterInventory(t *testing.T) {
	type contract struct {
		file      string
		causeArg  string
		closedArg string
	}
	want := map[string]contract{
		"confluenceCommentMutationWriteError":  {file: "confluence_comment_mutations_guarded.go", causeArg: "e.cause", closedArg: "e.closed"},
		"confluenceFooterCommentWriteError":    {file: "confluence_comments_guarded.go", causeArg: "e.cause", closedArg: "e.closed"},
		"confluencePageCopyWriteError":         {file: "confluence_page_copy.go", causeArg: "e.cause", closedArg: "e.closed"},
		"confluencePageTrashWriteError":        {file: "confluence_page_trash.go", causeArg: "e.cause", closedArg: "e.closed"},
		"jiraCommentWriteError":                {file: "jira_comments_guarded.go", causeArg: "e.cause", closedArg: "e.closed"},
		"jiraTransitionWriteError":             {file: "jira_transition_guarded.go", causeArg: "e.cause", closedArg: "e.closed"},
		"confluenceAttachmentDeleteWriteError": {file: "confluence_attachment_delete.go", causeArg: "e.cause", closedArg: "true"},
		"jiraIssueDeleteWriteError":            {file: "jira_issue_delete.go", causeArg: "e.cause", closedArg: "true"},
		"jiraDescriptionEditError":             {file: "jira_description_edit.go", causeArg: "e.cause", closedArg: "e.closed"},
	}

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob production Go files: %v", err)
	}
	got := make(map[string]contract)
	discoveredMultiUnwrap := make(map[string]string)
	allCalls := make([]string, 0, len(want))
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
			if ok && callee.Name == "operationErrorCauses" {
				allCalls = append(allCalls, fset.Position(call.Pos()).String())
			}
			return true
		})
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name.Name != "Unwrap" || function.Recv == nil || len(function.Recv.List) != 1 {
				continue
			}
			receiver := receiverTypeName(function.Recv.List[0].Type)
			if unwrapReturnsErrorSlice(function.Type.Results) {
				discoveredMultiUnwrap[receiver] = filepath.Base(path)
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee, ok := call.Fun.(*ast.Ident)
				if !ok || callee.Name != "operationErrorCauses" {
					return true
				}
				if len(call.Args) != 2 {
					t.Fatalf("%s: operationErrorCauses arguments = %d, want 2", path, len(call.Args))
				}
				if _, exists := got[receiver]; exists {
					t.Fatalf("%s: multiple operationErrorCauses calls", receiver)
				}
				got[receiver] = contract{
					file:      filepath.Base(path),
					causeArg:  operationCauseExpression(call.Args[0]),
					closedArg: operationCauseExpression(call.Args[1]),
				}
				return true
			})
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operation error cause adapter inventory = %#v, want exactly %#v", got, want)
	}
	wantMultiUnwrap := make(map[string]string, len(want))
	for receiver, contract := range want {
		wantMultiUnwrap[receiver] = contract.file
	}
	if !reflect.DeepEqual(discoveredMultiUnwrap, wantMultiUnwrap) {
		t.Fatalf("production Unwrap() []error inventory = %#v, want exactly %#v", discoveredMultiUnwrap, wantMultiUnwrap)
	}
	if len(allCalls) != len(want) {
		t.Fatalf("all production operationErrorCauses calls = %v, want exactly %d classified Unwrap calls", allCalls, len(want))
	}
}

func unwrapReturnsErrorSlice(results *ast.FieldList) bool {
	if results == nil || len(results.List) != 1 {
		return false
	}
	slice, ok := results.List[0].Type.(*ast.ArrayType)
	if !ok || slice.Len != nil {
		return false
	}
	element, ok := slice.Elt.(*ast.Ident)
	return ok && element.Name == "error"
}

func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if identifier, ok := expr.(*ast.Ident); ok {
		return identifier.Name
	}
	return "<unknown>"
}

func operationCauseExpression(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		identifier, ok := value.X.(*ast.Ident)
		if !ok {
			return "<non-identifier-selector>"
		}
		return identifier.Name + "." + value.Sel.Name
	default:
		return "<unsupported>"
	}
}
