package confluence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"golang.org/x/net/html"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/domain"
)

const commentPreparationHTMLMaxBytes = 4 << 20

var _ domain.ConfluenceInlineCommentPreparer = (*CommentMutationProvider)(nil)

// PrepareConfluenceInlineComment derives every browser-dependent create field
// from one bounded server-rendered view. It never writes and never substitutes
// CSF, a local renderer, or the local clock for the server evidence.
func (provider *CommentMutationProvider) PrepareConfluenceInlineComment(ctx context.Context, request domain.ConfluenceInlineCommentPreparationRequest) (domain.ConfluenceInlineCommentPreparation, error) {
	if provider == nil || provider.confluence == nil || provider.confluence.c == nil {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: Confluence compatibility provider is unavailable", domain.ErrConfig)
	}
	if err := domain.ValidateConfluenceInlineCommentPreparationRequest(request); err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, err
	}
	if err := provider.activation.Validate(compatibility.ProductConfluence); err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, err
	}

	readContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	metadata, err := provider.confluence.ExactServerMetadata(readContext)
	if err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, sanitizedCommentMutationError("preparation_qualification", err, false)
	}
	if metadata.Product != domain.ServerProductConfluence ||
		metadata.Version != string(provider.activation.Version) ||
		metadata.BuildNumber != string(provider.activation.BuildNumber) {
		return domain.ConfluenceInlineCommentPreparation{}, sanitizedCommentMutationError(
			"preparation_qualification", fmt.Errorf("%w: activation does not match", domain.ErrCheckFailed), false,
		)
	}

	query := url.Values{}
	query.Set("pageId", request.PageID)
	body, err := provider.confluence.c.DoWithBodyLimit(readContext, http.MethodGet,
		"/pages/viewpage.action?"+query.Encode(), nil, map[string]string{"Accept": "text/html"}, commentPreparationHTMLMaxBytes)
	if err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, sanitizedCommentMutationError("preparation_read", err, false)
	}
	prepared, err := prepareInlineCommentFromHTML(body, request)
	if err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, sanitizedCommentMutationError("preparation_response", err, false)
	}
	return prepared, nil
}

type inlinePreparationTextSegment struct {
	node  *html.Node
	start int
	end   int
}

type inlinePreparationDOMRange struct {
	start int
	end   int
}

func prepareInlineCommentFromHTML(data []byte, request domain.ConfluenceInlineCommentPreparationRequest) (domain.ConfluenceInlineCommentPreparation, error) {
	document, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered page HTML is invalid", domain.ErrCheckFailed)
	}
	head := exactlyOneElement(document, func(node *html.Node) bool { return node.Data == "head" })
	if head == nil {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered page head is ambiguous", domain.ErrCheckFailed)
	}
	metas, ok := exactPreparationMeta(head)
	if !ok || metas["ajs-page-id"] != request.PageID || metas["ajs-latest-page-id"] != request.PageID {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered page identity is unavailable", domain.ErrCheckFailed)
	}
	pageVersion64, versionErr := strconv.ParseInt(metas["ajs-page-version"], 10, 32)
	lastFetchTime, timeErr := strconv.ParseInt(metas["confluence-request-time"], 10, 64)
	if versionErr != nil || pageVersion64 <= 0 || int(pageVersion64) != request.ExpectedPageVersion || timeErr != nil || lastFetchTime <= 0 {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered page version evidence is unavailable", domain.ErrCheckFailed)
	}

	content := exactlyOneElement(document, func(node *html.Node) bool { return htmlAttribute(node, "id") == "content" })
	if content == nil {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered page content root is ambiguous", domain.ErrCheckFailed)
	}
	wikiContent := exactlyOneElementBelow(content, func(node *html.Node) bool { return htmlClass(node, "wiki-content") })
	if wikiContent == nil {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered wiki-content root is ambiguous", domain.ErrCheckFailed)
	}

	markerRefs, markerErr := inlineMarkerInventory(wikiContent)
	if markerErr != nil {
		return domain.ConfluenceInlineCommentPreparation{}, markerErr
	}
	viewSHA256 := canonicalWikiContentSHA256(wikiContent)
	searchSelection := pinnedRangeHelperSelection(request.OriginalSelection)
	if searchSelection == "" {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered selection is empty after browser normalization", domain.ErrCheckFailed)
	}
	originalSelection := strings.ReplaceAll(searchSelection, "\n", "")
	text, rawText, segments, elementRanges := preparationText(wikiContent)
	selection := utf16.Encode([]rune(searchSelection))
	_, selectedStart := countUTF16Matches(text, selection, request.MatchIndex)
	if selectedStart < 0 {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered selection occurrence is unavailable", domain.ErrCheckFailed)
	}
	selectedEnd := selectedStart + len(selection)
	masked, selectedMasks, maskErr := browserMaskedPreparationText(wikiContent, text, rawText, elementRanges, selection, selectedStart, selectedEnd)
	if maskErr != nil {
		return domain.ConfluenceInlineCommentPreparation{}, maskErr
	}
	matchCount, browserMatchIndex := browserSelectionMatch(masked, selection, selectedEnd)
	if matchCount == 0 || browserMatchIndex < 0 {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered selection is not eligible for an inline comment", domain.ErrCheckFailed)
	}
	if err := rejectUnsupportedPreparationRange(wikiContent, elementRanges, selectedMasks, selectedStart, selectedEnd); err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, err
	}
	wrappers := make([]*html.Node, 0, 4)
	for _, segment := range segments {
		start := max(selectedStart, segment.start)
		end := min(selectedEnd, segment.end)
		if start >= end {
			continue
		}
		rawSelected := utf16.Encode([]rune(segment.node.Data))[start-segment.start : end-segment.start]
		if !containsPinnedJSNonWhitespace(rawSelected) {
			continue
		}
		wrapper, replaceErr := wrapPreparationText(segment.node, start-segment.start, end-segment.start)
		if replaceErr != nil {
			return domain.ConfluenceInlineCommentPreparation{}, replaceErr
		}
		wrappers = append(wrappers, wrapper)
	}
	if len(wrappers) == 0 {
		return domain.ConfluenceInlineCommentPreparation{}, fmt.Errorf("%w: rendered selection geometry is unavailable", domain.ErrCheckFailed)
	}
	highlights := make([]domain.ConfluenceInlineHighlightGeometry, 0, len(wrappers))
	var highlightedSelection strings.Builder
	for _, wrapper := range wrappers {
		geometry, geometryErr := preparationHighlightGeometry(wikiContent, wrapper)
		if geometryErr != nil {
			return domain.ConfluenceInlineCommentPreparation{}, geometryErr
		}
		highlights = append(highlights, geometry)
		highlightedSelection.WriteString(geometry.Text)
	}
	return domain.ConfluenceInlineCommentPreparation{
		PageID: request.PageID, PageVersion: int(pageVersion64), LastFetchTime: lastFetchTime,
		SearchSelection: searchSelection, OriginalSelection: originalSelection, HighlightedSelection: highlightedSelection.String(),
		NumMatches: matchCount, MatchIndex: browserMatchIndex, SerializedHighlights: highlights,
		ViewSHA256: viewSHA256, MarkerRefs: markerRefs,
	}, nil
}

func canonicalWikiContentSHA256(root *html.Node) string {
	digest := sha256.New()
	var writeNode func(*html.Node)
	writeNode = func(node *html.Node) {
		switch node.Type {
		case html.ElementNode:
			switch node.Data {
			case "script", "style", "noscript", "template":
				return
			}
			writeCanonicalHTMLValue(digest, "element", node.Namespace+"\x00"+node.Data)
			attributes := append([]html.Attribute(nil), node.Attr...)
			sort.Slice(attributes, func(i, j int) bool {
				left := attributes[i].Namespace + "\x00" + attributes[i].Key + "\x00" + attributes[i].Val
				right := attributes[j].Namespace + "\x00" + attributes[j].Key + "\x00" + attributes[j].Val
				return left < right
			})
			for _, attribute := range attributes {
				writeCanonicalHTMLValue(digest, "attribute", attribute.Namespace+"\x00"+attribute.Key+"\x00"+attribute.Val)
			}
		case html.TextNode:
			writeCanonicalHTMLValue(digest, "text", node.Data)
		default:
			// Comments and parser bookkeeping are not part of the browser text or
			// highlight geometry and must not destabilize the view fingerprint.
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			writeNode(child)
		}
		if node.Type == html.ElementNode {
			writeCanonicalHTMLValue(digest, "end", node.Namespace+"\x00"+node.Data)
		}
	}
	writeNode(root)
	return hex.EncodeToString(digest.Sum(nil))
}

func writeCanonicalHTMLValue(writer io.Writer, kind, value string) {
	_, _ = io.WriteString(writer, kind)
	_, _ = io.WriteString(writer, ":")
	_, _ = io.WriteString(writer, strconv.Itoa(len(value)))
	_, _ = io.WriteString(writer, ":")
	_, _ = io.WriteString(writer, value)
	_, _ = io.WriteString(writer, ";")
}

func exactPreparationMeta(head *html.Node) (map[string]string, bool) {
	required := map[string]bool{
		"confluence-request-time": true,
		"ajs-page-id":             true,
		"ajs-latest-page-id":      true,
		"ajs-page-version":        true,
	}
	values := map[string]string{}
	valid := true
	walkHTML(head, func(node *html.Node) {
		if !valid || node.Type != html.ElementNode || node.Data != "meta" {
			return
		}
		name := strings.ToLower(htmlAttribute(node, "name"))
		if !required[name] {
			return
		}
		content := htmlAttribute(node, "content")
		if content == "" {
			valid = false
			return
		}
		if _, duplicate := values[name]; duplicate {
			valid = false
			return
		}
		values[name] = content
	})
	return values, valid && len(values) == len(required)
}

func exactlyOneElement(root *html.Node, match func(*html.Node) bool) *html.Node {
	var found *html.Node
	ambiguous := false
	walkHTML(root, func(node *html.Node) {
		if node.Type != html.ElementNode || !match(node) {
			return
		}
		if found != nil {
			ambiguous = true
			return
		}
		found = node
	})
	if ambiguous {
		return nil
	}
	return found
}

func exactlyOneElementBelow(root *html.Node, match func(*html.Node) bool) *html.Node {
	var found *html.Node
	ambiguous := false
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, func(node *html.Node) {
			if node.Type != html.ElementNode || !match(node) {
				return
			}
			if found != nil {
				ambiguous = true
				return
			}
			found = node
		})
	}
	if ambiguous {
		return nil
	}
	return found
}

func walkHTML(node *html.Node, visit func(*html.Node)) {
	if node == nil {
		return
	}
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func htmlAttribute(node *html.Node, key string) string {
	if node == nil || node.Type != html.ElementNode {
		return ""
	}
	value := ""
	seen := false
	for _, attribute := range node.Attr {
		if asciiEqualFold(attribute.Key, key) {
			if seen {
				return ""
			}
			seen = true
			value = attribute.Val
		}
	}
	return value
}

func asciiEqualFold(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range len(left) {
		leftByte, rightByte := left[index], right[index]
		if leftByte >= 'A' && leftByte <= 'Z' {
			leftByte += 'a' - 'A'
		}
		if rightByte >= 'A' && rightByte <= 'Z' {
			rightByte += 'a' - 'A'
		}
		if leftByte != rightByte {
			return false
		}
	}
	return true
}

func htmlClass(node *html.Node, want string) bool {
	classes := htmlAttribute(node, "class")
	for start := 0; start < len(classes); {
		for start < len(classes) && cssASCIIWhitespace(classes[start]) {
			start++
		}
		end := start
		for end < len(classes) && !cssASCIIWhitespace(classes[end]) {
			end++
		}
		if classes[start:end] == want {
			return true
		}
		start = end
	}
	return false
}

func cssASCIIWhitespace(value byte) bool {
	return value == '\t' || value == '\n' || value == '\f' || value == '\r' || value == ' '
}

func inlineMarkerInventory(root *html.Node) ([]string, error) {
	refs := map[string]struct{}{}
	valid := true
	walkHTML(root, func(node *html.Node) {
		if !valid || node.Type != html.ElementNode || !htmlClass(node, "inline-comment-marker") {
			return
		}
		ref := htmlAttribute(node, "data-ref")
		if !validInlineMarkerRef(ref) {
			valid = false
			return
		}
		refs[ref] = struct{}{}
	})
	if !valid {
		return nil, fmt.Errorf("%w: rendered marker inventory is malformed", domain.ErrCheckFailed)
	}
	result := make([]string, 0, len(refs))
	for ref := range refs {
		result = append(result, ref)
	}
	sort.Strings(result)
	return result, nil
}

func pinnedRangeHelperSelection(value string) string {
	value = strings.ReplaceAll(value, "\u00a0", " ")
	runes := []rune(value)
	start, end := 0, len(runes)
	for start < end && pinnedJQueryWhitespace(runes[start]) {
		start++
	}
	for end > start && pinnedJQueryWhitespace(runes[end-1]) {
		end--
	}
	return string(runes[start:end])
}

// pinnedJQueryWhitespace is the exact jQuery 2.2.4 $.trim edge set. In
// particular it includes FEFF and excludes U+0085, unlike strings.TrimSpace.
func pinnedJQueryWhitespace(value rune) bool {
	switch {
	case value >= '\u0009' && value <= '\u000d':
		return true
	case value == '\u0020' || value == '\u00a0' || value == '\u1680' || value == '\u2028' ||
		value == '\u2029' || value == '\u202f' || value == '\u205f' || value == '\u3000' || value == '\ufeff':
		return true
	case value >= '\u2000' && value <= '\u200a':
		return true
	default:
		return false
	}
}

func preparationText(root *html.Node) ([]uint16, []uint16, []inlinePreparationTextSegment, map[*html.Node]inlinePreparationDOMRange) {
	text := []uint16{}
	rawText := []uint16{}
	segments := []inlinePreparationTextSegment{}
	ranges := map[*html.Node]inlinePreparationDOMRange{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		start := len(text)
		if node.Type == html.TextNode && node.Data != "" {
			rawUnits := utf16.Encode([]rune(node.Data))
			units := append([]uint16(nil), rawUnits...)
			for index := range units {
				if units[index] == '\u00a0' {
					units[index] = ' '
				}
			}
			segments = append(segments, inlinePreparationTextSegment{node: node, start: len(text), end: len(text) + len(units)})
			text = append(text, units...)
			rawText = append(rawText, rawUnits...)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if node.Type == html.ElementNode {
			ranges[node] = inlinePreparationDOMRange{start: start, end: len(text)}
		}
	}
	walk(root)
	return text, rawText, segments, ranges
}

func countUTF16Matches(text, pattern []uint16, selectedIndex int) (count, selectedStart int) {
	selectedStart = -1
	walkUTF16Matches(text, pattern, func(start int) {
		if count == selectedIndex {
			selectedStart = start
		}
		count++
	})
	return count, selectedStart
}

func walkUTF16Matches(text, pattern []uint16, visit func(start int)) {
	if len(pattern) == 0 || len(pattern) > len(text) {
		return
	}
	prefix := make([]int, len(pattern))
	for index, matched := 1, 0; index < len(pattern); {
		if pattern[index] == pattern[matched] {
			matched++
			prefix[index] = matched
			index++
		} else if matched > 0 {
			matched = prefix[matched-1]
		} else {
			index++
		}
	}
	for index, matched := 0, 0; index < len(text); {
		if text[index] == pattern[matched] {
			index++
			matched++
			if matched == len(pattern) {
				visit(index - matched)
				matched = prefix[matched-1]
			}
		} else if matched > 0 {
			matched = prefix[matched-1]
		} else {
			index++
		}
	}
}

func browserMaskedPreparationText(root *html.Node, text, rawText []uint16, ranges map[*html.Node]inlinePreparationDOMRange, selection []uint16, selectedStart, selectedEnd int) ([]uint16, map[*html.Node]bool, error) {
	floatingHeader := false
	walkHTML(root, func(node *html.Node) {
		floatingHeader = floatingHeader || node.Type == html.ElementNode && node.Data == "thead" && htmlClass(node, "tableFloatingHeader")
	})
	if floatingHeader {
		// The pinned selector is the layout-dependent
		// thead:hidden.tableFloatingHeader. Static HTML cannot prove :hidden,
		// so any floating-header clone makes preparation ambiguous.
		return nil, nil, fmt.Errorf("%w: rendered floating table header visibility is unavailable", domain.ErrCheckFailed)
	}
	selectedMasks := browserSelectedMaskElements(root, rawText, ranges, selection)
	maskedPositions := make([]bool, len(text))
	var applyMasks func(*html.Node, bool)
	applyMasks = func(node *html.Node, selectedAncestor bool) {
		selected := node.Type == html.ElementNode && selectedMasks[node]
		if selected && !selectedAncestor {
			// jQuery applies .text(replacedText) in document order. The first
			// selected ancestor flattens and detaches any selected descendants in
			// the search clone, so only topmost mask elements contribute.
			span := ranges[node]
			walkUTF16NonOverlappingMatches(text[span.start:span.end], selection, func(start int) {
				for offset, value := range selection {
					// The pinned replacement is selection.replace(/\S/g, " "):
					// non-whitespace becomes ASCII space while tab/LF and the exact
					// ECMAScript whitespace set remain byte-for-code-unit stable.
					if !pinnedJQueryWhitespace(rune(value)) {
						maskedPositions[span.start+start+offset] = true
					}
				}
			})
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			applyMasks(child, selectedAncestor || selected)
		}
	}
	applyMasks(root, false)
	for index := selectedStart; index < selectedEnd; index++ {
		if maskedPositions[index] {
			return nil, nil, fmt.Errorf("%w: rendered selection is masked by the pinned browser client", domain.ErrCheckFailed)
		}
	}
	masked := append([]uint16(nil), text...)
	for index, replace := range maskedPositions {
		if replace {
			masked[index] = ' '
		}
	}
	return masked, selectedMasks, nil
}

func walkUTF16NonOverlappingMatches(text, pattern []uint16, visit func(start int)) {
	if len(pattern) == 0 || len(pattern) > len(text) {
		return
	}
	prefix := make([]int, len(pattern))
	for index, matched := 1, 0; index < len(pattern); {
		if pattern[index] == pattern[matched] {
			matched++
			prefix[index] = matched
			index++
		} else if matched > 0 {
			matched = prefix[matched-1]
		} else {
			index++
		}
	}
	for index, matched := 0, 0; index < len(text); {
		if text[index] == pattern[matched] {
			index++
			matched++
			if matched == len(pattern) {
				visit(index - matched)
				matched = 0
			}
		} else if matched > 0 {
			matched = prefix[matched-1]
		} else {
			index++
		}
	}
}

func browserSelectedMaskElements(root *html.Node, rawText []uint16, ranges map[*html.Node]inlinePreparationDOMRange, selection []uint16) map[*html.Node]bool {
	rawMatchStarts := []int{}
	walkUTF16Matches(rawText, selection, func(start int) { rawMatchStarts = append(rawMatchStarts, start) })
	selected := map[*html.Node]bool{}
	walkHTML(root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}
		if unconditionalBrowserMaskElement(node) {
			selected[node] = true
			return
		}
		conditional := htmlClass(node, "conf-macro") && htmlAttribute(node, "data-hasbody") == "false" ||
			htmlClass(node, "jira-issue") || htmlClass(node, "jira-issues")
		if !conditional {
			return
		}
		span := ranges[node]
		matchIndex := sort.SearchInts(rawMatchStarts, span.start)
		selected[node] = matchIndex < len(rawMatchStarts) && rawMatchStarts[matchIndex]+len(selection) <= span.end
	})
	return selected
}

func unconditionalBrowserMaskElement(node *html.Node) bool {
	if htmlClass(node, "user-mention") {
		return true
	}
	if node.Data == "a" {
		href := htmlAttribute(node, "href")
		if strings.HasPrefix(href, "/") || strings.HasPrefix(href, "#") {
			return true
		}
	}
	if htmlClass(node, "panelHeader") && hasHTMLAncestor(node, func(ancestor *html.Node) bool {
		return ancestor.Data == "div" && htmlAttribute(ancestor, "data-macro-name") == "panel"
	}) {
		return true
	}
	if htmlClass(node, "conf-macro-render") && hasHTMLAncestor(node, func(ancestor *html.Node) bool {
		return htmlAttributePresent(ancestor, "data-macro-name")
	}) {
		return true
	}
	return false
}

func hasHTMLAncestor(node *html.Node, predicate func(*html.Node) bool) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && predicate(ancestor) {
			return true
		}
	}
	return false
}

func htmlAttributePresent(node *html.Node, key string) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}
	for _, attribute := range node.Attr {
		if asciiEqualFold(attribute.Key, key) {
			return true
		}
	}
	return false
}

func browserSelectionMatch(masked, selection []uint16, selectedEnd int) (count, selectedIndex int) {
	selectedIndex = -1
	prefix := masked[:selectedEnd]
	for len(prefix) > 0 && pinnedJQueryWhitespace(rune(prefix[len(prefix)-1])) {
		prefix = prefix[:len(prefix)-1]
	}
	selectedStart := len(prefix) - len(selection)
	if selectedStart < 0 {
		return 0, -1
	}
	walkUTF16Matches(masked, selection, func(start int) {
		if start == selectedStart {
			selectedIndex = count
		}
		count++
	})
	return count, selectedIndex
}

func rejectUnsupportedPreparationRange(root *html.Node, ranges map[*html.Node]inlinePreparationDOMRange, selectedMasks map[*html.Node]bool, selectedStart, selectedEnd int) error {
	var rejection string
	walkHTML(root, func(node *html.Node) {
		if rejection != "" || node.Type != html.ElementNode || !preparationRangeIntersects(ranges[node], selectedStart, selectedEnd) {
			return
		}
		switch {
		case htmlClass(node, "inline-comment-marker"):
			rejection = "rendered selection overlaps an existing marker"
		case selectedMasks[node]:
			rejection = "rendered selection intersects browser-masked content"
		case ignoredHighlightElement(node):
			rejection = "rendered selection intersects content skipped by the browser highlighter"
		case footerFallbackElement(node):
			rejection = "rendered selection falls back to a footer comment in the pinned browser client"
		}
	})
	if rejection != "" {
		return fmt.Errorf("%w: %s", domain.ErrCheckFailed, rejection)
	}
	return nil
}

func preparationRangeIntersects(span inlinePreparationDOMRange, selectedStart, selectedEnd int) bool {
	if span.start == span.end {
		return span.start > selectedStart && span.start < selectedEnd
	}
	return selectedStart < span.end && selectedEnd > span.start
}

func ignoredHighlightElement(node *html.Node) bool {
	switch strings.ToUpper(node.Data) {
	case "SCRIPT", "STYLE", "SELECT", "BUTTON", "OBJECT", "APPLET":
		return true
	default:
		return false
	}
}

func footerFallbackElement(node *html.Node) bool {
	if htmlClass(node, "conf-macro") && (htmlAttribute(node, "data-hasbody") == "false" ||
		htmlAttribute(node, "data-macro-name") == "code" || htmlAttribute(node, "data-macro-name") == "include") {
		return true
	}
	if htmlClass(node, "user-mention") {
		return true
	}
	if node.Data == "a" && strings.HasPrefix(htmlAttribute(node, "href"), "/") && !htmlClass(node, "user-mention") {
		return true
	}
	return node.Data == "time" && strings.HasPrefix(htmlAttribute(node, "class"), "date-")
}

func containsPinnedJSNonWhitespace(units []uint16) bool {
	for _, value := range utf16.Decode(units) {
		if !pinnedJQueryWhitespace(value) {
			return true
		}
	}
	return false
}

func wrapPreparationText(node *html.Node, start, end int) (*html.Node, error) {
	if node == nil || node.Type != html.TextNode || node.Parent == nil {
		return nil, fmt.Errorf("%w: rendered selection geometry is unavailable", domain.ErrCheckFailed)
	}
	units := utf16.Encode([]rune(node.Data))
	if start < 0 || end <= start || end > len(units) || !validUTF16Boundary(units, start) || !validUTF16Boundary(units, end) {
		return nil, fmt.Errorf("%w: rendered selection splits invalid text geometry", domain.ErrCheckFailed)
	}
	before := string(utf16.Decode(units[:start]))
	selected := string(utf16.Decode(units[start:end]))
	after := string(utf16.Decode(units[end:]))
	parent := node.Parent
	if before != "" {
		parent.InsertBefore(&html.Node{Type: html.TextNode, Data: before}, node)
	}
	wrapper := &html.Node{Type: html.ElementNode, Data: "span", Attr: []html.Attribute{{Key: "class", Val: "ic-current-selection"}}}
	wrapper.AppendChild(&html.Node{Type: html.TextNode, Data: selected})
	parent.InsertBefore(wrapper, node)
	if after != "" {
		parent.InsertBefore(&html.Node{Type: html.TextNode, Data: after}, node)
	}
	parent.RemoveChild(node)
	return wrapper, nil
}

func validUTF16Boundary(units []uint16, index int) bool {
	return index <= 0 || index >= len(units) || units[index] < 0xdc00 || units[index] > 0xdfff
}

func preparationHighlightGeometry(root, wrapper *html.Node) (domain.ConfluenceInlineHighlightGeometry, error) {
	if wrapper == nil || wrapper.FirstChild == nil || wrapper.FirstChild.Type != html.TextNode || wrapper.FirstChild.NextSibling != nil {
		return domain.ConfluenceInlineHighlightGeometry{}, fmt.Errorf("%w: rendered highlight geometry is malformed", domain.ErrCheckFailed)
	}
	path := []int{}
	for node := wrapper; node != root; node = node.Parent {
		if node.Parent == nil {
			return domain.ConfluenceInlineHighlightGeometry{}, fmt.Errorf("%w: rendered highlight path escaped wiki-content", domain.ErrCheckFailed)
		}
		index := 0
		found := false
		for child := node.Parent.FirstChild; child != nil; child = child.NextSibling {
			if child == node {
				found = true
				break
			}
			index++
		}
		if !found {
			return domain.ConfluenceInlineHighlightGeometry{}, fmt.Errorf("%w: rendered highlight path is unavailable", domain.ErrCheckFailed)
		}
		path = append([]int{index}, path...)
	}
	offset := 0
	if previous := wrapper.PrevSibling; previous != nil && previous.Type == html.TextNode {
		offset = len(utf16.Encode([]rune(previous.Data)))
	}
	text := wrapper.FirstChild.Data
	return domain.ConfluenceInlineHighlightGeometry{
		Text: text, ChildIndexPath: path, PreviousTextSiblingOffset: offset,
		Length: len(utf16.Encode([]rune(text))),
	}, nil
}
