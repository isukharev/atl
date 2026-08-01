package mirror

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func qualifiedCommentsSidecarFixture() ConfluenceCommentsSidecarV2 {
	rootID := "comment-2"
	parentID := rootID
	return ConfluenceCommentsSidecarV2{
		SchemaVersion:    ConfluenceCommentsSidecarSchemaVersion,
		PageID:           "page-1",
		PageVersion:      7,
		Complete:         false,
		CommentsComplete: true,
		ThreadsComplete:  true,
		AnchorsComplete:  false,
		Count:            2,
		RootCount:        1,
		PartialReasons:   []string{domain.ConfluenceCommentPartialAnchorMissing},
		Capabilities: domain.ConfluenceCommentCapabilities{
			Footer: domain.ConfluenceCapabilityObserved, Inline: domain.ConfluenceCapabilityObserved,
			Resolved: domain.ConfluenceCapabilityDocumented, DepthAll: domain.ConfluenceCapabilityObserved,
			ThreadAncestry: domain.ConfluenceCapabilityObserved, InlineProperties: domain.ConfluenceCapabilityObserved,
			Resolution: domain.ConfluenceCapabilityObserved,
		},
		Comments: []ConfluenceCommentsSidecarComment{
			{
				ID: "comment-2", PageID: "page-1", RootID: &rootID,
				Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationFooter,
				Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 2,
				Author:    ConfluenceCommentsSidecarAuthor{ID: "user-2", DisplayName: "Reviewer"},
				CreatedAt: "2026-01-02T00:00:00Z", UpdatedAt: "2026-01-03T00:00:00Z",
				Body: "footer", BodyStorage: "<p>footer</p>", Anchor: nil,
			},
			{
				ID: "comment-1", PageID: "page-1", ParentID: &parentID, RootID: &rootID,
				Relation: domain.ConfluenceCommentRelationReply, Location: domain.ConfluenceCommentLocationInline,
				Resolution: domain.ConfluenceCommentResolutionOpen, Version: 1,
				Author:    ConfluenceCommentsSidecarAuthor{ID: "user-1", DisplayName: "Author"},
				CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
				Body: "inline", BodyStorage: "<p>  exact\n<ac:link/>  </p>",
				Anchor: &ConfluenceCommentsSidecarAnchor{
					MarkerRef: "marker-1", OriginalSelection: "before", ObservedSelection: "after",
					Status: domain.ConfluenceAnchorMissing,
				},
			},
		},
		Diagnostics: []ConfluenceCommentsSidecarDiagnostic{{
			Code: domain.ConfluenceCommentPartialAnchorMissing, CommentID: "comment-1",
			MarkerRef: "marker-1", Selector: domain.ConfluenceCommentSelectorInline,
			Location: domain.ConfluenceCommentLocationInline,
		}},
	}
}

func TestConfluenceCommentsSidecarV2DeterministicRoundTrip(t *testing.T) {
	input := qualifiedCommentsSidecarFixture()
	originalFirstID := input.Comments[0].ID

	encoded, err := EncodeConfluenceCommentsSidecarV2(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(encoded, []byte("\n")) {
		t.Fatal("encoded sidecar lacks one trailing newline")
	}
	if input.Comments[0].ID != originalFirstID {
		t.Fatal("encoder mutated caller comment order")
	}

	decoded, err := DecodeConfluenceCommentsSidecar(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Format != ConfluenceCommentsSidecarFormatV2 || decoded.V2 == nil || decoded.Legacy == nil || len(decoded.Legacy) != 0 {
		t.Fatalf("decoded representation = %+v", decoded)
	}
	if decoded.V2.Comments == nil || decoded.V2.PartialReasons == nil || decoded.V2.Diagnostics == nil {
		t.Fatal("decoded v2 contains a nil array")
	}
	if decoded.V2.Comments[0].ID != "comment-1" || decoded.V2.Comments[1].ID != "comment-2" {
		t.Fatalf("comments were not canonicalized: %+v", decoded.V2.Comments)
	}
	comment := decoded.V2.Comments[0]
	if comment.ParentID == nil || *comment.ParentID != "comment-2" || comment.RootID == nil || *comment.RootID != "comment-2" || comment.Anchor == nil {
		t.Fatalf("nullable qualified fields were not preserved: %+v", comment)
	}
	if comment.Anchor.Status != domain.ConfluenceAnchorMissing || comment.Anchor.ObservedSelection != "after" {
		t.Fatalf("enriched anchor was not preserved: %+v", comment.Anchor)
	}
	if comment.BodyStorage != "<p>  exact\n<ac:link/>  </p>" {
		t.Fatalf("native body changed: %q", comment.BodyStorage)
	}

	reencoded, err := EncodeConfluenceCommentsSidecarV2(*decoded.V2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("re-encoding is not deterministic\nfirst:\n%s\nsecond:\n%s", encoded, reencoded)
	}
}

func TestDecodeConfluenceCommentsSidecarLegacyStrictAndExplicit(t *testing.T) {
	data := []byte(`[
  {"id":"2","author":"Reviewer","created":"later","body":"plain","body_storage":"<p>  exact </p>"},
  {"id":"1","author":"Author","created":"earlier","body":"other"}
]`)
	decoded, err := DecodeConfluenceCommentsSidecar(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Format != ConfluenceCommentsSidecarFormatLegacy || decoded.V2 != nil || decoded.Legacy == nil || len(decoded.Legacy) != 2 {
		t.Fatalf("decoded representation = %+v", decoded)
	}
	if decoded.Legacy[0].ID != "2" || decoded.Legacy[0].BodyStorage != "<p>  exact </p>" || decoded.Legacy[1].ID != "1" {
		t.Fatalf("legacy shape/order changed: %+v", decoded.Legacy)
	}

	empty, err := DecodeConfluenceCommentsSidecar([]byte(`[]`))
	if err != nil || empty.Legacy == nil || len(empty.Legacy) != 0 {
		t.Fatalf("empty legacy array: decoded=%+v err=%v", empty, err)
	}
}

func TestDecodeConfluenceCommentsSidecarRejectsMalformedContracts(t *testing.T) {
	valid, err := EncodeConfluenceCommentsSidecarV2(qualifiedCommentsSidecarFixture())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"future schema":        bytes.Replace(valid, []byte(`"schema_version": 2`), []byte(`"schema_version": 3`), 1),
		"duplicate top key":    bytes.Replace(valid, []byte(`"schema_version": 2`), []byte(`"schema_version": 99, "schema_version": 2`), 1),
		"unknown top key":      bytes.Replace(valid, []byte(`"page_id":`), []byte(`"extra":true,"page_id":`), 1),
		"unknown nested key":   bytes.Replace(valid, []byte(`"display_name": "Author"`), []byte(`"display_name":"Author","secret":"x"`), 1),
		"duplicate nested key": bytes.Replace(valid, []byte(`"display_name": "Author"`), []byte(`"display_name":"Wrong","display_name":"Author"`), 1),
		"null comments": bytes.Replace(valid, bytesFromCommentsThroughDiagnostics(valid), []byte(`"comments": null,
  "diagnostics":`), 1),
		"trailing value":       append(append([]byte{}, valid...), []byte(`{}`)...),
		"legacy unknown key":   []byte(`[{"id":"1","author":"A","created":"now","body":"x","extra":true}]`),
		"legacy duplicate key": []byte(`[{"id":"wrong","id":"1","author":"A","created":"now","body":"x"}]`),
		"legacy duplicate id":  []byte(`[{"id":"1","author":"A","created":"now","body":"x"},{"id":"1","author":"B","created":"later","body":"y"}]`),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeConfluenceCommentsSidecar(data)
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
}

func TestConfluenceCommentsSidecarRejectsInvalidQualifiedInventoryAndAssertions(t *testing.T) {
	tests := map[string]func(*ConfluenceCommentsSidecarV2){
		"duplicate id": func(value *ConfluenceCommentsSidecarV2) {
			value.Comments[1].ID = value.Comments[0].ID
		},
		"invalid enum": func(value *ConfluenceCommentsSidecarV2) {
			value.Comments[0].Location = domain.ConfluenceCommentLocation("resolved")
		},
		"invalid anchor": func(value *ConfluenceCommentsSidecarV2) {
			value.Comments[1].Anchor.Status = domain.ConfluenceAnchorStatus("backend-value")
		},
		"inline without anchor": func(value *ConfluenceCommentsSidecarV2) {
			value.Comments[1].Anchor = nil
		},
		"matched anchor without marker": func(value *ConfluenceCommentsSidecarV2) {
			value.Comments[1].Anchor.Status = domain.ConfluenceAnchorMatched
			value.Comments[1].Anchor.MarkerRef = ""
		},
		"missing anchor without marker": func(value *ConfluenceCommentsSidecarV2) {
			value.Comments[1].Anchor.MarkerRef = ""
		},
		"complete anchors with unmatched projection": func(value *ConfluenceCommentsSidecarV2) {
			value.AnchorsComplete = true
			value.Complete = true
		},
		"page mismatch": func(value *ConfluenceCommentsSidecarV2) {
			value.Comments[0].PageID = "other-page"
		},
		"empty page id": func(value *ConfluenceCommentsSidecarV2) {
			value.PageID = ""
		},
		"zero page version": func(value *ConfluenceCommentsSidecarV2) {
			value.PageVersion = 0
		},
		"future schema": func(value *ConfluenceCommentsSidecarV2) {
			value.SchemaVersion++
		},
		"count mismatch": func(value *ConfluenceCommentsSidecarV2) {
			value.Count++
		},
		"root count mismatch": func(value *ConfluenceCommentsSidecarV2) {
			value.RootCount++
		},
		"complete mismatch": func(value *ConfluenceCommentsSidecarV2) {
			value.Complete = true
		},
		"complete with partial reason": func(value *ConfluenceCommentsSidecarV2) {
			value.AnchorsComplete = true
			value.Complete = true
			value.Comments[1].Anchor.Status = domain.ConfluenceAnchorMatched
		},
		"partial diagnostic without reason": func(value *ConfluenceCommentsSidecarV2) {
			value.Diagnostics = append(value.Diagnostics, ConfluenceCommentsSidecarDiagnostic{Code: domain.ConfluenceCommentPartialPageLimit})
		},
		"nil diagnostics": func(value *ConfluenceCommentsSidecarV2) {
			value.Diagnostics = nil
		},
		"unsorted reasons": func(value *ConfluenceCommentsSidecarV2) {
			value.PartialReasons = []string{domain.ConfluenceCommentPartialPageLimit, domain.ConfluenceCommentPartialAnchorMissing}
		},
		"duplicate reasons": func(value *ConfluenceCommentsSidecarV2) {
			value.PartialReasons = []string{domain.ConfluenceCommentPartialAnchorMissing, domain.ConfluenceCommentPartialAnchorMissing}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := qualifiedCommentsSidecarFixture()
			mutate(&value)
			if _, err := EncodeConfluenceCommentsSidecarV2(value); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
}

// bytesFromCommentsThroughDiagnostics returns the exact field span replaced by
// the null-array corruption case without depending on comment body formatting.
func bytesFromCommentsThroughDiagnostics(encoded []byte) []byte {
	start := bytes.Index(encoded, []byte(`"comments": [`))
	end := bytes.Index(encoded[start:], []byte(`"diagnostics":`))
	if start < 0 || end < 0 {
		return nil
	}
	return encoded[start : start+end+len(`"diagnostics":`)]
}

func TestConfluenceCommentsSidecarLegacyRejectsTrailingData(t *testing.T) {
	_, err := DecodeConfluenceCommentsSidecar([]byte(`[] trailing`))
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteConfluenceCommentsKeepsPageHashAndBaseIndependent(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	page := &domain.Resource{ID: "page-1", Title: "Page", SpaceKey: "DOC", Version: 7, Body: []byte("<p>body</p>")}
	dir, slug, err := m.ClaimPageDir(page.SpaceKey, nil, page.Title, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := qualifiedCommentsSidecarFixture()
	display := []domain.Comment{{ID: "comment-2", Author: "Reviewer", BodyStorage: "<p>footer</p>"}}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.WriteConfluenceComments(dir, slug, page, nil, sidecar, display, false, MDViewOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
	state, ok, err := m.SyncStateOf(page.ID)
	if err != nil || !ok || state.Hash != Hash(page.Body) {
		t.Fatalf("sync state=%+v ok=%v error=%v", state, ok, err)
	}
	base, ok := m.BaseBody(page.ID)
	if !ok || !bytes.Equal(base, page.Body) {
		t.Fatalf("base=%q ok=%v", base, ok)
	}
	metaBytes, err := os.ReadFile(filepath.Join(dir, slug+".meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Hash != Hash(page.Body) || meta.CommentSidecarVersion != 2 || meta.CommentCount != 2 ||
		meta.CommentsComplete == nil || !*meta.CommentsComplete || meta.CommentAnchorsComplete == nil || *meta.CommentAnchorsComplete {
		t.Fatalf("meta=%+v", meta)
	}
}
