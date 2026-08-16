package app

import (
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestValidateJiraPullOptionalArtifacts(t *testing.T) {
	valid := JiraPullOpts{
		Complete: true,
		Comments: true, MaxCommentPagesPerItem: 2, MaxCommentsPerItem: 10,
		Attachments: true, MaxAttachmentsPerItem: 10,
		AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"},
		MaxAttachmentBytes: 4, MaxTotalAttachmentBytes: 64,
	}
	tests := []struct {
		name string
		opts JiraPullOpts
		want bool
	}{
		{name: "ordinary has no evidence", opts: JiraPullOpts{}, want: true},
		{name: "complete evidence", opts: valid, want: true},
		{name: "complete aggregate maximum", opts: JiraPullOpts{Complete: true, Attachments: true, MaxAttachmentsPerItem: 1, AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"}, MaxAttachmentBytes: 1, MaxTotalAttachmentBytes: corpusBuildMaxAttachmentTotalBytes}, want: true},
		{name: "ordinary comments", opts: JiraPullOpts{Comments: true, MaxCommentPagesPerItem: 1, MaxCommentsPerItem: 1}},
		{name: "comments missing bounds", opts: JiraPullOpts{Complete: true, Comments: true}},
		{name: "attachments missing bound", opts: JiraPullOpts{Complete: true, Attachments: true}},
		{name: "body without inventory", opts: JiraPullOpts{Complete: true, AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"}, MaxAttachmentBytes: 1, MaxTotalAttachmentBytes: 1}},
		{name: "noncanonical media", opts: JiraPullOpts{Complete: true, Attachments: true, MaxAttachmentsPerItem: 1, AttachmentBodies: true, AttachmentMediaTypes: []string{"Image/*"}, MaxAttachmentBytes: 1, MaxTotalAttachmentBytes: 1}},
		{name: "body total exceeds complete cap", opts: JiraPullOpts{Complete: true, Attachments: true, MaxAttachmentsPerItem: 1, AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"}, MaxAttachmentBytes: 1, MaxTotalAttachmentBytes: corpusBuildMaxAttachmentTotalBytes + 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateJiraPullOptionalArtifacts(test.opts)
			if test.want {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPrepareJiraPullOptionalArtifactsBindsOnlyPublicCompleteEvidence(t *testing.T) {
	ordinary := JiraPullOpts{}
	if err := prepareJiraPullOptionalArtifacts(&ordinary); err != nil || ordinary.evidence != nil {
		t.Fatalf("ordinary evidence=%+v error=%v", ordinary.evidence, err)
	}
	opts := JiraPullOpts{
		Complete: true, Attachments: true, MaxAttachmentsPerItem: 10,
		AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"},
		MaxAttachmentBytes: 4, MaxTotalAttachmentBytes: 64,
	}
	if err := prepareJiraPullOptionalArtifacts(&opts); err != nil || opts.evidence == nil ||
		!opts.evidence.binding.Attachments || opts.evidence.binding.MaxAttachmentBodiesPerItem != jiraCompletePullMaxAttachmentBodiesPerIssue {
		t.Fatalf("evidence=%+v error=%v", opts.evidence, err)
	}
	if fields := jiraCompletePullFields(opts, nil, RenderSettings{}); len(fields) == 0 || fields[len(fields)-1] != "updated" {
		t.Fatalf("complete fields=%v", fields)
	}
}

func TestJiraCompleteOptionsHashKeepsLegacyNoEvidenceBinding(t *testing.T) {
	// The unfinished default complete-pull checkpoint predates optional Jira
	// evidence. A nil evidence pointer must remain omitted in its binding JSON,
	// otherwise a feature upgrade would strand an ordinary legacy resume before
	// it can fetch or recover its accepted prefix.
	view := mirror.ViewState{Sections: []string{"content"}}
	fields := []string{"summary", "description", "project"}
	options := JiraPullOpts{Complete: true, MaxIssues: 42}
	legacy, err := confluenceCompleteHashJSON(struct {
		Fields         []string         `json:"fields"`
		Render         mirror.ViewState `json:"render"`
		OverwriteLocal bool             `json:"overwrite_local"`
		StashLocal     bool             `json:"stash_local"`
		MaxIssues      int              `json:"max_issues"`
	}{
		Fields: fields, Render: view, MaxIssues: options.MaxIssues,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := jiraCompleteOptionsHash(options, fields, view)
	if err != nil || got != legacy {
		t.Fatalf("hash=%q legacy=%q error=%v", got, legacy, err)
	}
	withPublicEvidence := JiraPullOpts{
		Complete: true, MaxIssues: 42,
		Attachments: true, MaxAttachmentsPerItem: 1,
	}
	if err := prepareJiraPullOptionalArtifacts(&withPublicEvidence); err != nil {
		t.Fatal(err)
	}
	withEvidence, err := jiraCompleteOptionsHash(withPublicEvidence, fields, view)
	if err != nil || withEvidence == legacy {
		t.Fatalf("evidence hash=%q legacy=%q error=%v", withEvidence, legacy, err)
	}
}
