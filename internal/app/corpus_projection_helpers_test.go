package app

import (
	"encoding/json"
	"testing"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/mirror"
)

func TestCorpusInt64ValueAcceptsOnlyNonNegativeIntegralValues(t *testing.T) {
	for name, test := range map[string]struct {
		value any
		want  int64
	}{
		"float":          {float64(7), 7},
		"fraction":       {float64(7.5), 0},
		"negative float": {float64(-1), 0},
		"int":            {int(8), 8},
		"negative int":   {int(-1), 0},
		"int64":          {int64(9), 9},
		"negative int64": {int64(-1), 0},
		"json number":    {json.Number("10"), 10},
		"bad number":     {json.Number("10.5"), 0},
		"other":          {"11", 0},
	} {
		t.Run(name, func(t *testing.T) {
			if got := corpusInt64Value(test.value); got != test.want {
				t.Fatalf("corpusInt64Value(%T(%v))=%d, want %d", test.value, test.value, got, test.want)
			}
		})
	}
}

func TestCorpusConfluenceVisibilityKeepsUnknownDistinct(t *testing.T) {
	restricted, unrestricted := true, false
	for name, test := range map[string]struct {
		value      *bool
		visibility corpus.Visibility
		status     corpus.EvidenceStatus
	}{
		"unknown":      {nil, corpus.VisibilityUnknown, corpus.EvidenceNotRequested},
		"restricted":   {&restricted, corpus.VisibilityRestricted, corpus.EvidenceComplete},
		"unrestricted": {&unrestricted, corpus.VisibilityUnrestricted, corpus.EvidenceComplete},
	} {
		t.Run(name, func(t *testing.T) {
			visibility, evidence := corpusConfluenceVisibility(test.value)
			if visibility != test.visibility || evidence.Status != test.status || evidence.Kind != corpus.EvidenceVisibility {
				t.Fatalf("visibility=%q evidence=%+v", visibility, evidence)
			}
		})
	}
}

func TestCorpusLinkResolverUsesStableTargetsAndRejectsAmbiguity(t *testing.T) {
	builder := &corpusProjectionBuilder{
		jiraByKey: map[string]string{"EX-1": "jira-stable"},
		confluenceByTitle: map[string]string{
			corpusConfluenceTitleKey("DOC", "Page title"): "page-stable",
			corpusConfluenceTitleKey("ALT", "Other"):      "other-stable",
		},
		confluenceAmbiguous: map[string]bool{corpusConfluenceTitleKey("DOC", "Duplicate"): true},
	}
	resolve := builder.linkResolver(corpusIndexedItem{markdownPath: "markdown/confluence/source.md", container: "DOC"})
	for name, target := range map[string]string{
		"jira":                      "jira:ex-1",
		"confluence fallback space": "confluence-page:Page%20title",
		"confluence explicit space": "confluence-page:ALT/Other",
	} {
		t.Run(name, func(t *testing.T) {
			if path, ok := resolve(target); !ok || path == "" {
				t.Fatalf("resolve(%q)=(%q,%t)", target, path, ok)
			}
		})
	}
	for name, target := range map[string]string{
		"missing jira":       "jira:missing",
		"invalid escape":     "confluence-page:%zz",
		"ambiguous page":     "confluence-page:Duplicate",
		"unsupported scheme": "https://example.invalid",
	} {
		t.Run(name, func(t *testing.T) {
			if path, ok := resolve(target); ok || path != "" {
				t.Fatalf("resolve(%q)=(%q,%t)", target, path, ok)
			}
		})
	}
}

func TestCorpusProjectionAuxiliaryHelpersRejectUnknownStates(t *testing.T) {
	files := []mirror.CorpusSnapshotFile{{Path: "one.json", Data: []byte("one")}}
	if value, ok := corpusAuxiliaryAtPath(files, "one.json"); !ok || string(value.Data) != "one" {
		t.Fatalf("value=%+v ok=%t", value, ok)
	}
	if _, ok := corpusAuxiliaryAtPath(files, "missing.json"); ok {
		t.Fatal("missing auxiliary was found")
	}
	if _, _, err := corpusArtifactBodyState(mirror.AttachmentSidecarBody{State: "future"}); err == nil {
		t.Fatal("future attachment body state was accepted")
	}
	if corpusCaptureDimensionNotRequested(corpusExportSource{}, corpus.CaptureComments) {
		t.Fatal("missing capture claimed a not-requested dimension")
	}
	source := corpusExportSource{capture: &corpus.CaptureReceipt{Dimensions: []corpus.CaptureDimensionEvidence{
		{Dimension: corpus.CaptureComments, State: corpus.CaptureNotRequested},
	}}}
	if !corpusCaptureDimensionNotRequested(source, corpus.CaptureComments) ||
		corpusCaptureDimensionNotRequested(source, corpus.CaptureAttachments) {
		t.Fatalf("capture=%+v", source.capture)
	}
}
