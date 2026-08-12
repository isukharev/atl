package confluence

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

func TestConfluencePaginationOwnerInventoryIsClosed(t *testing.T) {
	want := map[string][]string{
		"attachment_discovery.go:DiscoverAttachmentsQualified":             {"advance", "checkedEnd", "requestStart", "startAt", "startAt", "startAt", "startAt", "startAt", "startAt", "startAt", "startAt", "startAt", "startAt", "startAt", "startAt", "startAt"},
		"attachment_download_revalidation.go:RevalidateAttachmentDownload": {"requestStart"},
		"comments_qualified.go:ListConfluenceComments":                     {"advance", "requestStart", "startAt", "startAt"},
		"confluence.go:HistoryQualified":                                   {"advance", "requestStart", "startAt"},
		"corpus_metadata.go:ReadConfluenceCorpusMetadata":                  {"advance", "advance", "requestStart", "startAt", "startAt"},
		"corpus_metadata.go:qualifiedCorpusMetadataPage":                   {"checkedEnd"},
		"extras.go:listAttachmentsQualified":                               {"advance", "requestStart", "startAt"},
		"extras.go:ListComments":                                           {"advance", "requestStart", "startAt"},
		"labels.go:ListContentLabels":                                      {"advance", "requestStart", "startAt"},
		"pagination.go:advance":                                            {"checkedEnd"},
		"search.go:SearchComplete":                                         {"advance", "checkedEnd", "requestStart", "startAt"},
		"search.go:TreeQualified":                                          {"advance", "checkedEnd", "requestStart", "startAt", "startAt"},
	}
	tracked := map[string]bool{"advance": true, "checkedEnd": true, "startAt": true}
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
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && tracked[selector.Sel.Name] {
					got[key] = append(got[key], selector.Sel.Name)
				}
				if ok && selector.Sel.Name == "Set" && len(call.Args) > 0 {
					if literal, literalOK := call.Args[0].(*ast.BasicLit); literalOK {
						value, unquoteErr := strconv.Unquote(literal.Value)
						if unquoteErr == nil && value == "start" {
							got[key] = append(got[key], "requestStart")
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
		t.Fatalf("Confluence checked-pagination owners = %#v, want exactly %#v", got, want)
	}
}

const confluenceProductionLoopKeys = `
attachment_discovery.go:DiscoverAttachmentsQualified:for:1
attachment_discovery.go:DiscoverAttachmentsQualified:range:1
authorization.go:authorizeHierarchy:range:1
authorization.go:authorizeMoveHierarchy:range:1
authorization.go:containsID:range:1
authorization.go:evictSubtree:range:1
authorization.go:exactIdentityFromResource:range:1
authorization.go:fail:for:1
authorization.go:identityFromMeta:range:1
authorization.go:put:for:1
comment_mutation_highlights.go:serializeInlineHighlights:range:1
comment_mutation_highlights.go:serializeInlineHighlights:range:2
comment_mutation_preparation.go:asciiEqualFold:range:1
comment_mutation_preparation.go:browserMaskedPreparationText:for:1
comment_mutation_preparation.go:browserMaskedPreparationText:for:2
comment_mutation_preparation.go:browserMaskedPreparationText:range:1
comment_mutation_preparation.go:browserMaskedPreparationText:range:2
comment_mutation_preparation.go:browserSelectionMatch:for:1
comment_mutation_preparation.go:canonicalWikiContentSHA256:for:1
comment_mutation_preparation.go:canonicalWikiContentSHA256:range:1
comment_mutation_preparation.go:containsPinnedJSNonWhitespace:range:1
comment_mutation_preparation.go:exactlyOneElementBelow:for:1
comment_mutation_preparation.go:hasHTMLAncestor:for:1
comment_mutation_preparation.go:htmlAttribute:range:1
comment_mutation_preparation.go:htmlAttributePresent:range:1
comment_mutation_preparation.go:htmlClass:for:1
comment_mutation_preparation.go:htmlClass:for:2
comment_mutation_preparation.go:htmlClass:for:3
comment_mutation_preparation.go:inlineMarkerInventory:range:1
comment_mutation_preparation.go:pinnedRangeHelperSelection:for:1
comment_mutation_preparation.go:pinnedRangeHelperSelection:for:2
comment_mutation_preparation.go:preparationHighlightGeometry:for:1
comment_mutation_preparation.go:preparationHighlightGeometry:for:2
comment_mutation_preparation.go:preparationText:for:1
comment_mutation_preparation.go:preparationText:range:1
comment_mutation_preparation.go:prepareInlineCommentFromHTML:range:1
comment_mutation_preparation.go:prepareInlineCommentFromHTML:range:2
comment_mutation_preparation.go:walkHTML:for:1
comment_mutation_preparation.go:walkUTF16Matches:for:1
comment_mutation_preparation.go:walkUTF16Matches:for:2
comment_mutation_preparation.go:walkUTF16NonOverlappingMatches:for:1
comment_mutation_preparation.go:walkUTF16NonOverlappingMatches:for:2
comment_mutation_provider.go:sanitizedCommentMutationError:range:1
comments_qualified.go:ListConfluenceComments:for:1
comments_qualified.go:ListConfluenceComments:range:1
comments_qualified.go:ListConfluenceComments:range:2
comments_qualified.go:commentAncestryObjectsPresent:range:1
comments_qualified.go:commentRelationship:range:1
comments_qualified.go:consistentCommentAncestry:for:1
comments_qualified.go:finish:range:1
comments_qualified.go:finish:range:2
comments_qualified.go:selectedCommentSelectors:range:1
comments_qualified.go:selectedCommentSelectors:range:2
confluence.go:GetMeta:range:1
confluence.go:HistoryQualified:for:1
confluence.go:HistoryQualified:range:1
confluence.go:confluenceWebURL:range:1
confluence.go:resolveAdapterOptions:range:1
confluence.go:toResource:range:1
corpus_metadata.go:ReadConfluenceCorpusMetadata:for:1
corpus_metadata.go:ReadConfluenceCorpusMetadata:range:1
corpus_metadata.go:labelValues:range:1
corpus_metadata.go:qualifiedCorpusMetadataRow:range:1
corpus_metadata.go:qualifiedLabelValues:range:1
corpus_metadata.go:safePaginationSignal:range:1
corpus_metadata.go:validateConfluenceCorpusHierarchy:for:1
corpus_metadata.go:validateConfluenceCorpusHierarchy:range:1
corpus_metadata.go:validateConfluenceCorpusHierarchy:range:2
corpus_metadata.go:validateConfluenceCorpusHierarchy:range:3
extras.go:listAttachmentsQualified:for:1
extras.go:listAttachmentsQualified:range:1
extras.go:ListComments:for:1
extras.go:ListComments:range:1
graph_metadata.go:canonicalConfluenceGraphPageID:range:1
labels.go:ListContentLabels:for:1
mirror_metadata.go:PlanPageMetadataBatches:for:1
mirror_metadata.go:PlanPageMetadataBatches:for:2
mirror_metadata.go:ReadPageMetadataBatch:range:1
search.go:SearchComplete:range:1
search.go:TreeQualified:for:1
search.go:TreeQualified:range:1
search.go:stripHTML:range:1
server_metadata.go:confluenceBuildNumber:range:1
server_metadata.go:legacyConfluenceIdentity:range:1
server_metadata.go:legacyConfluenceIdentity:range:2
server_metadata.go:legacyConfluenceIdentity:range:3
`

func TestConfluenceProductionLoopInventoryIsClosed(t *testing.T) {
	pagination := map[string]bool{
		"attachment_discovery.go:DiscoverAttachmentsQualified:for:1": true,
		"comments_qualified.go:ListConfluenceComments:for:1":         true,
		"confluence.go:HistoryQualified:for:1":                       true,
		"corpus_metadata.go:ReadConfluenceCorpusMetadata:for:1":      true,
		"extras.go:listAttachmentsQualified:for:1":                   true,
		"extras.go:ListComments:for:1":                               true,
		"labels.go:ListContentLabels:for:1":                          true,
		"search.go:TreeQualified:for:1":                              true,
	}
	want := classifiedConfluenceLoops(strings.Fields(confluenceProductionLoopKeys), pagination)
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
		for key, reason := range collectConfluenceLoops(entry.Name(), file, pagination) {
			got[key] = reason
		}
	}
	if err := validateConfluenceLoopInventory(got, want); err != nil {
		t.Fatal(err)
	}
}

func TestConfluenceProductionLoopInventoryRejectsRawOffsetLoop(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "raw_offset.go", `package confluence
func rawPage() {
	for offset := 0; ; offset += 7 {
		query := "?skip="
		_ = query
		if offset > 21 { break }
	}
}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfluenceLoopInventory(collectConfluenceLoops("raw_offset.go", file, nil), map[string]string{}); err == nil {
		t.Fatal("raw offset loop passed the closed production inventory")
	}
}

func classifiedConfluenceLoops(keys []string, pagination map[string]bool) map[string]string {
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

func validateConfluenceLoopInventory(got, want map[string]string) error {
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("Confluence production loop classifications = %#v, want exactly %#v", got, want)
	}
	return nil
}

func collectConfluenceLoops(fileName string, file *ast.File, pagination map[string]bool) map[string]string {
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
	return classifiedConfluenceLoops(keys, pagination)
}
