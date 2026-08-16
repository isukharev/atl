package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateJiraCommentReadOptions(t *testing.T) {
	if err := ValidateJiraCommentReadOptions(JiraCommentReadOptions{MaxPages: 1, MaxItems: 1}); err != nil {
		t.Fatal(err)
	}
	for _, options := range []JiraCommentReadOptions{
		{},
		{MaxPages: -1, MaxItems: 1},
		{MaxPages: JiraCommentReadMaxPages + 1, MaxItems: 1},
		{MaxPages: 1, MaxItems: JiraCommentReadMaxItems + 1},
		{MaxPages: 1, MaxItems: 1, MaxBytes: -1},
		{MaxPages: 1, MaxItems: 1, MaxBytes: JiraCommentReadMaxBytes + 1},
	} {
		if err := ValidateJiraCommentReadOptions(options); !errors.Is(err, ErrUsage) {
			t.Fatalf("options=%+v error=%v", options, err)
		}
	}
}

func TestValidateJiraCommentInventory(t *testing.T) {
	complete := JiraCommentInventory{Comments: []Comment{}, Complete: true, TotalKnown: true, PageCount: 1}
	if err := ValidateJiraCommentInventory(complete); err != nil {
		t.Fatal(err)
	}
	partial := JiraCommentInventory{
		Comments: []Comment{{ID: "1"}}, PartialReason: JiraCommentPartialItemLimit,
		Total: 2, TotalKnown: true, PageCount: 1,
	}
	if err := ValidateJiraCommentInventory(partial); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*JiraCommentInventory){
		"nil collection":   func(value *JiraCommentInventory) { value.Comments = nil },
		"missing page":     func(value *JiraCommentInventory) { value.PageCount = 0 },
		"unknown total":    func(value *JiraCommentInventory) { value.TotalKnown = false },
		"partial complete": func(value *JiraCommentInventory) { value.PartialReason = JiraCommentPartialPageLimit },
		"total mismatch":   func(value *JiraCommentInventory) { value.Total = 1 },
		"missing identity": func(value *JiraCommentInventory) { value.Comments = []Comment{{}} },
		"duplicate identity": func(value *JiraCommentInventory) {
			value.Comments = []Comment{{ID: "1"}, {ID: "1"}}
			value.Total = 2
		},
		"too many items": func(value *JiraCommentInventory) {
			value.Comments = make([]Comment, JiraCommentReadMaxItems+1)
			value.Total = len(value.Comments)
			for index := range value.Comments {
				value.Comments[index].ID = fmt.Sprintf("%d", index+1)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := complete
			mutate(&value)
			if err := ValidateJiraCommentInventory(value); !errors.Is(err, ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestValidateJiraAttachmentInventory(t *testing.T) {
	complete := JiraAttachmentInventory{Attachments: []Attachment{{ID: "1", Title: "a.bin", FileSize: 0}}, Complete: true}
	if err := ValidateJiraAttachmentInventory(complete); err != nil {
		t.Fatal(err)
	}
	partial := JiraAttachmentInventory{Attachments: []Attachment{}, PartialReason: JiraAttachmentPartialFieldUnavailable}
	if err := ValidateJiraAttachmentInventory(partial); err != nil {
		t.Fatal(err)
	}
	limited := JiraAttachmentInventory{Attachments: []Attachment{{ID: "1", Title: "a.bin"}}, PartialReason: JiraAttachmentPartialItemLimit}
	if err := ValidateJiraAttachmentInventory(limited); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]JiraAttachmentInventory{
		"nil collection":   {Complete: true},
		"unknown partial":  {Attachments: []Attachment{}, PartialReason: "other"},
		"missing identity": {Attachments: []Attachment{{Title: "a", FileSize: 0}}, Complete: true},
		"negative size":    {Attachments: []Attachment{{ID: "1", Title: "a", FileSize: -1}}, Complete: true},
		"duplicate":        {Attachments: []Attachment{{ID: "1", Title: "a"}, {ID: "1", Title: "b"}}, Complete: true},
		"too many items":   {Attachments: make([]Attachment, JiraAttachmentReadMaxItems+1), Complete: true},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "too many items" {
				for index := range value.Attachments {
					value.Attachments[index] = Attachment{ID: fmt.Sprintf("%d", index+1), Title: "a", FileSize: 0}
				}
			}
			if err := ValidateJiraAttachmentInventory(value); !errors.Is(err, ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestJiraEvidenceRecordGrammarMatchesDurableSidecars(t *testing.T) {
	valid := Comment{ID: "1", ParentID: "2", Author: "Fixture", Created: "2026-01-01", Body: "body"}
	if !ValidJiraEvidenceParentRevision("2026-01-01T00:00:00Z") || !ValidJiraCommentEvidenceRecord(valid) {
		t.Fatal("valid evidence grammar was rejected")
	}
	for name, value := range map[string]Comment{
		"opaque identity":      {ID: "comment-1", Body: "body"},
		"zero identity":        {ID: "0", Body: "body"},
		"oversized metadata":   {ID: "1", Author: strings.Repeat("a", JiraCommentEvidenceMetadataMaxBytes+1), Body: "body"},
		"invalid UTF-8 body":   {ID: "1", Body: string([]byte{0xff})},
		"invalid reply parent": {ID: "1", ParentID: "opaque", Body: "body"},
	} {
		t.Run(name, func(t *testing.T) {
			if ValidJiraCommentEvidenceRecord(value) {
				t.Fatalf("comment=%+v was accepted", value)
			}
		})
	}
	for _, revision := range []string{"", " revision", "revision ", strings.Repeat("r", JiraCommentEvidenceMetadataMaxBytes+1)} {
		if ValidJiraEvidenceParentRevision(revision) {
			t.Fatalf("revision %q was accepted", revision)
		}
	}
}
