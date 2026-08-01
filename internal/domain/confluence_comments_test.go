package domain

import (
	"errors"
	"testing"
)

func validCommentCapabilities() ConfluenceCommentCapabilities {
	return ConfluenceCommentCapabilities{
		Footer: ConfluenceCapabilityDocumented, Inline: ConfluenceCapabilityDocumented,
		Resolved: ConfluenceCapabilityDocumented, DepthAll: ConfluenceCapabilityDocumented,
		ThreadAncestry: ConfluenceCapabilityDocumented, InlineProperties: ConfluenceCapabilityDocumented,
		Resolution: ConfluenceCapabilityDocumented,
	}
}

func TestConfluenceCommentEnumsSeparateResolvedSelectorFromLocation(t *testing.T) {
	if !ValidConfluenceCommentSelector(ConfluenceCommentSelectorResolved) {
		t.Fatal("resolved must remain a valid REST selector")
	}
	if ValidConfluenceCommentLocation(ConfluenceCommentLocation("resolved")) {
		t.Fatal("resolved must not be an emitted semantic location")
	}
	for _, reason := range []string{ConfluenceCommentPartialPageLimit, ConfluenceCommentPartialAnchorAmbiguous, ConfluenceCommentPartialLegacyUnqualified} {
		if !ValidConfluenceCommentPartialReason(reason) {
			t.Fatalf("static reason %q is not valid", reason)
		}
	}
	if ValidConfluenceCommentPartialReason("backend supplied explanation") {
		t.Fatal("backend-controlled reasons must not be accepted")
	}
}

func TestValidateConfluenceUserIdentity(t *testing.T) {
	valid := ConfluenceUserIdentity{ID: "stable-user-key", DisplayName: "Current User"}
	if err := ValidateConfluenceUserIdentity(valid); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	for _, identity := range []ConfluenceUserIdentity{
		{DisplayName: "Current User"},
		{ID: "stable-user-key"},
		{ID: " ", DisplayName: "Current User"},
		{ID: "stable-user-key", DisplayName: "\t"},
	} {
		if err := ValidateConfluenceUserIdentity(identity); !errors.Is(err, ErrCheckFailed) {
			t.Fatalf("identity %+v error = %v, want ErrCheckFailed", identity, err)
		}
	}
}

func TestValidateConfluenceCommentReadOptions(t *testing.T) {
	for _, options := range []ConfluenceCommentReadOptions{
		{ParentVersion: 1},
		{ParentVersion: 7, Locations: []ConfluenceCommentSelector{ConfluenceCommentSelectorFooter}, MaxPages: 1, MaxItems: 2},
	} {
		if err := ValidateConfluenceCommentReadOptions(options); err != nil {
			t.Fatalf("valid options %+v rejected: %v", options, err)
		}
	}
	for _, options := range []ConfluenceCommentReadOptions{
		{},
		{ParentVersion: -1},
		{ParentVersion: 1, MaxPages: -1},
		{ParentVersion: 1, MaxItems: -1},
		{ParentVersion: 1, Locations: []ConfluenceCommentSelector{"other"}},
	} {
		if err := ValidateConfluenceCommentReadOptions(options); !errors.Is(err, ErrUsage) {
			t.Fatalf("options %+v error = %v, want ErrUsage", options, err)
		}
	}
}

func TestValidateConfluenceCommentInventoryRejectsInvalidRelationshipsAndCollections(t *testing.T) {
	rootID := "10"
	valid := ConfluenceCommentInventory{
		Comments: []ConfluenceCommentRecord{{
			ID: "10", PageID: "1", RootID: &rootID, Relation: ConfluenceCommentRelationRoot,
			Location: ConfluenceCommentLocationFooter, Resolution: ConfluenceCommentResolutionUnknown,
		}},
		CommentsComplete: true, ThreadsComplete: true, PartialReasons: []string{},
		Capabilities: validCommentCapabilities(), Diagnostics: []ConfluenceCommentDiagnostic{},
	}
	if err := ValidateConfluenceCommentInventory(valid); err != nil {
		t.Fatalf("valid inventory rejected: %v", err)
	}
	invalid := valid
	invalid.Comments = nil
	if err := ValidateConfluenceCommentInventory(invalid); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("nil collection error = %v", err)
	}
	invalid = valid
	invalid.Comments = append([]ConfluenceCommentRecord(nil), valid.Comments...)
	parent := "20"
	invalid.Comments[0].Relation, invalid.Comments[0].ParentID = ConfluenceCommentRelationRoot, &parent
	if err := ValidateConfluenceCommentInventory(invalid); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("invalid root relationship error = %v", err)
	}
}

func TestValidateConfluenceCommentInventoryRejectsCompleteInconsistentAncestry(t *testing.T) {
	rootID, firstID, secondID := "10", "20", "30"
	base := ConfluenceCommentInventory{
		Comments: []ConfluenceCommentRecord{
			{ID: rootID, PageID: "1", RootID: &rootID, Relation: ConfluenceCommentRelationRoot, Location: ConfluenceCommentLocationFooter, Resolution: ConfluenceCommentResolutionUnknown},
			{ID: firstID, PageID: "1", ParentID: &secondID, RootID: &rootID, Relation: ConfluenceCommentRelationReply, Location: ConfluenceCommentLocationFooter, Resolution: ConfluenceCommentResolutionUnknown},
			{ID: secondID, PageID: "1", ParentID: &firstID, RootID: &rootID, Relation: ConfluenceCommentRelationReply, Location: ConfluenceCommentLocationFooter, Resolution: ConfluenceCommentResolutionUnknown},
		},
		CommentsComplete: true, ThreadsComplete: true, PartialReasons: []string{},
		Capabilities: validCommentCapabilities(), Diagnostics: []ConfluenceCommentDiagnostic{},
	}
	if err := ValidateConfluenceCommentInventory(base); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("cycle error = %v, want ErrCheckFailed", err)
	}
	unknown := base
	unknown.Comments = append([]ConfluenceCommentRecord(nil), base.Comments[:1]...)
	unknown.Comments[0].Relation, unknown.Comments[0].RootID = ConfluenceCommentRelationUnknown, nil
	if err := ValidateConfluenceCommentInventory(unknown); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("unknown complete relation error = %v, want ErrCheckFailed", err)
	}
}
