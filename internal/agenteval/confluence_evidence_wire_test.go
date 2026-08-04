package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeConfluenceEvidenceViewsAcceptReleasedCompleteAndPartialShapes(t *testing.T) {
	metadata, err := DecodeConfluencePageMetadataView(bytes.NewReader(validConfluenceMetadataWire(t)))
	if err != nil || metadata.SchemaVersion != 1 || metadata.RestrictionState != ConfluenceRestrictionRestricted {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}

	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "partial"}[partial], func(t *testing.T) {
			outline, err := DecodeConfluencePageOutlineView(bytes.NewReader(validConfluenceOutlineWire(t, partial)))
			if err != nil || outline.Complete == partial || outline.Headings == nil || outline.Headings[0].Path == nil {
				t.Fatalf("outline=%+v err=%v", outline, err)
			}
			section, err := DecodeConfluencePageSectionView(bytes.NewReader(validConfluenceSectionWire(t, partial)))
			if err != nil || section.Complete == partial || section.Path == nil {
				t.Fatalf("section=%+v err=%v", section, err)
			}
			inventory, err := DecodeConfluenceAttachmentInventoryView(bytes.NewReader(validConfluenceAttachmentWire(t, partial)))
			if err != nil || inventory.Complete == partial || inventory.Attachments == nil {
				t.Fatalf("inventory=%+v err=%v", inventory, err)
			}
		})
	}

	emptyOutline := mutateConfluenceEvidenceWire(t, validConfluenceOutlineWire(t, false), func(root map[string]any) {
		root["count"], root["total"] = 0, 0
		root["original_bytes"], root["emitted_bytes"] = 0, 0
		root["headings"] = []any{}
	})
	outline, err := DecodeConfluencePageOutlineView(bytes.NewReader(emptyOutline))
	if err != nil || outline.Headings == nil || len(outline.Headings) != 0 {
		t.Fatalf("empty outline=%+v err=%v", outline, err)
	}
}

func TestDecodeConfluenceEvidenceViewsRejectWireDrift(t *testing.T) {
	type wireCase struct {
		name     string
		valid    []byte
		optional string
		decode   func([]byte) error
	}
	cases := []wireCase{
		{
			name: "metadata", valid: validConfluenceMetadataWire(t), optional: "updated",
			decode: func(data []byte) error {
				_, err := DecodeConfluencePageMetadataView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "outline", valid: validConfluenceOutlineWire(t, false), optional: "partial_reason",
			decode: func(data []byte) error {
				_, err := DecodeConfluencePageOutlineView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "section", valid: validConfluenceSectionWire(t, false), optional: "partial_reason",
			decode: func(data []byte) error {
				_, err := DecodeConfluencePageSectionView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "attachment", valid: validConfluenceAttachmentWire(t, false), optional: "partial_reason",
			decode: func(data []byte) error {
				_, err := DecodeConfluenceAttachmentInventoryView(bytes.NewReader(data))
				return err
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			invalid := map[string][]byte{
				"unknown member": mutateConfluenceEvidenceWire(t, test.valid, func(root map[string]any) {
					root["backend_payload"] = true
				}),
				"missing required member": mutateConfluenceEvidenceWire(t, test.valid, func(root map[string]any) {
					delete(root, "schema_version")
				}),
				"null required member": mutateConfluenceEvidenceWire(t, test.valid, func(root map[string]any) {
					root["schema_version"] = nil
				}),
				"empty optional member": mutateConfluenceEvidenceWire(t, test.valid, func(root map[string]any) {
					root[test.optional] = ""
				}),
				"null optional member": mutateConfluenceEvidenceWire(t, test.valid, func(root map[string]any) {
					root[test.optional] = nil
				}),
				"trailing value": append(bytes.Clone(test.valid), []byte("\n{}")...),
			}
			duplicate := bytes.Replace(test.valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
			if bytes.Equal(duplicate, test.valid) {
				t.Fatal("duplicate mutation did not apply")
			}
			invalid["duplicate member"] = duplicate

			for name, data := range invalid {
				t.Run(name, func(t *testing.T) {
					if err := test.decode(data); err == nil {
						t.Fatal("invalid wire was accepted")
					}
				})
			}

			oversized := append(bytes.Clone(test.valid), bytes.Repeat([]byte(" "), confluenceEvidenceWireMaxBytes-len(test.valid)+1)...)
			if err := test.decode(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("oversized error=%v", err)
			}
		})
	}
}

func TestDecodeConfluenceEvidenceViewAcceptsExactWireBound(t *testing.T) {
	valid := validConfluenceMetadataWire(t)
	atLimit := append(bytes.Clone(valid),
		bytes.Repeat([]byte(" "), confluenceEvidenceWireMaxBytes-len(valid))...)
	if _, err := DecodeConfluencePageMetadataView(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact wire bound was rejected: %v", err)
	}
}

func TestDecodeConfluencePageMetadataViewRejectsUnreconciledValues(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"schema":      func(root map[string]any) { root["schema_version"] = 2 },
		"empty id":    func(root map[string]any) { root["id"] = " " },
		"empty title": func(root map[string]any) { root["title"] = " " },
		"empty space": func(root map[string]any) { root["space"] = " " },
		"version":     func(root map[string]any) { root["version"] = 0 },
		"restriction": func(root map[string]any) { root["restriction_state"] = "backend_defined" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceEvidenceWire(t, validConfluenceMetadataWire(t), mutate)
			if _, err := DecodeConfluencePageMetadataView(bytes.NewReader(data)); err == nil {
				t.Fatal("unreconciled metadata was accepted")
			}
		})
	}

	for _, restriction := range []string{
		ConfluenceRestrictionUnknown, ConfluenceRestrictionRestricted, ConfluenceRestrictionUnrestricted,
	} {
		data := mutateConfluenceEvidenceWire(t, validConfluenceMetadataWire(t), func(root map[string]any) {
			root["restriction_state"] = restriction
		})
		if _, err := DecodeConfluencePageMetadataView(bytes.NewReader(data)); err != nil {
			t.Fatalf("restriction %q rejected: %v", restriction, err)
		}
	}
}

func TestDecodeConfluencePageOutlineViewRejectsUnreconciledValues(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"schema":  func(root map[string]any) { root["schema_version"] = 2 },
		"version": func(root map[string]any) { root["version"] = 0 },
		"nil headings": func(root map[string]any) {
			root["headings"] = nil
		},
		"count":      func(root map[string]any) { root["count"] = 2 },
		"total":      func(root map[string]any) { root["total"] = 0 },
		"bytes":      func(root map[string]any) { root["emitted_bytes"] = 65 },
		"index":      mutateConfluenceOutlineEntry(func(entry map[string]any) { entry["index"] = 2 }),
		"level":      mutateConfluenceOutlineEntry(func(entry map[string]any) { entry["level"] = 7 }),
		"title":      mutateConfluenceOutlineEntry(func(entry map[string]any) { entry["title"] = " " }),
		"nil path":   mutateConfluenceOutlineEntry(func(entry map[string]any) { entry["path"] = nil }),
		"wrong path": mutateConfluenceOutlineEntry(func(entry map[string]any) { entry["path"] = []any{"Other"} }),
		"occurrence": mutateConfluenceOutlineEntry(func(entry map[string]any) { entry["occurrence"] = 0 }),
		"nested member inventory": mutateConfluenceOutlineEntry(func(entry map[string]any) {
			entry["private"] = true
		}),
		"complete reason": func(root map[string]any) { root["partial_reason"] = "byte_limit" },
		"complete truncated": func(root map[string]any) {
			root["truncated"] = true
		},
		"omitted empty truncated": func(root map[string]any) { root["truncated"] = false },
		"partial missing reason": func(root map[string]any) {
			root["complete"], root["truncated"], root["total"] = false, true, 2
		},
		"partial unknown reason": func(root map[string]any) {
			root["complete"], root["truncated"], root["partial_reason"], root["total"] = false, true, "max_bytes", 2
		},
		"partial without withheld heading": func(root map[string]any) {
			root["complete"], root["truncated"], root["partial_reason"] = false, true, "byte_limit"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceEvidenceWire(t, validConfluenceOutlineWire(t, false), mutate)
			if _, err := DecodeConfluencePageOutlineView(bytes.NewReader(data)); err == nil {
				t.Fatal("unreconciled outline was accepted")
			}
		})
	}
	for _, reason := range []string{"heading_limit", "byte_limit"} {
		data := mutateConfluenceEvidenceWire(t, validConfluenceOutlineWire(t, true), func(root map[string]any) {
			root["partial_reason"] = reason
		})
		if _, err := DecodeConfluencePageOutlineView(bytes.NewReader(data)); err != nil {
			t.Fatalf("partial reason %q rejected: %v", reason, err)
		}
	}
}

func TestDecodeConfluencePageSectionViewRejectsUnreconciledValues(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"schema":                  func(root map[string]any) { root["schema_version"] = 2 },
		"version":                 func(root map[string]any) { root["version"] = 0 },
		"heading":                 func(root map[string]any) { root["heading"] = " " },
		"level":                   func(root map[string]any) { root["level"] = 7 },
		"nil path":                func(root map[string]any) { root["path"] = nil },
		"wrong path":              func(root map[string]any) { root["path"] = []any{"Other"} },
		"occurrence":              func(root map[string]any) { root["occurrence"] = 0 },
		"emitted bytes":           func(root map[string]any) { root["emitted_bytes"] = 12 },
		"original bytes":          func(root map[string]any) { root["original_bytes"] = 10 },
		"complete reason":         func(root map[string]any) { root["partial_reason"] = "max_bytes" },
		"complete truncated":      func(root map[string]any) { root["truncated"] = true },
		"omitted empty truncated": func(root map[string]any) { root["truncated"] = false },
		"complete withheld bytes": func(root map[string]any) { root["original_bytes"] = 40 },
		"partial missing reason": func(root map[string]any) {
			root["complete"], root["truncated"], root["original_bytes"] = false, true, 40
		},
		"partial unknown reason": func(root map[string]any) {
			root["complete"], root["truncated"], root["partial_reason"], root["original_bytes"] = false, true, "byte_limit", 40
		},
		"partial withheld nothing": func(root map[string]any) {
			root["complete"], root["truncated"], root["partial_reason"] = false, true, "max_bytes"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceEvidenceWire(t, validConfluenceSectionWire(t, false), mutate)
			if _, err := DecodeConfluencePageSectionView(bytes.NewReader(data)); err == nil {
				t.Fatal("unreconciled section was accepted")
			}
		})
	}
	for _, reason := range []string{"max_bytes", "invalid_utf8"} {
		data := mutateConfluenceEvidenceWire(t, validConfluenceSectionWire(t, true), func(root map[string]any) {
			root["partial_reason"] = reason
		})
		if _, err := DecodeConfluencePageSectionView(bytes.NewReader(data)); err != nil {
			t.Fatalf("partial reason %q rejected: %v", reason, err)
		}
	}
}

func TestDecodeConfluenceAttachmentInventoryViewRejectsUnreconciledValues(t *testing.T) {
	mutateAttachment := func(mutate func(map[string]any)) func(map[string]any) {
		return func(root map[string]any) {
			attachment := root["attachments"].([]any)[0].(map[string]any)
			mutate(attachment)
		}
	}
	mutations := map[string]func(map[string]any){
		"schema":                 func(root map[string]any) { root["schema_version"] = 2 },
		"page id":                func(root map[string]any) { root["page_id"] = " " },
		"page version":           func(root map[string]any) { root["page_version"] = 0 },
		"nil array":              func(root map[string]any) { root["attachments"] = nil },
		"count":                  func(root map[string]any) { root["count"] = 2 },
		"complete reason":        func(root map[string]any) { root["partial_reason"] = "page_limit" },
		"partial missing reason": func(root map[string]any) { root["complete"] = false },
		"partial unknown reason": func(root map[string]any) {
			root["complete"], root["partial_reason"] = false, "backend_defined"
		},
		"empty attachment id": mutateAttachment(func(attachment map[string]any) { attachment["id"] = " " }),
		"negative size":       mutateAttachment(func(attachment map[string]any) { attachment["file_size"] = -1 }),
		"negative version":    mutateAttachment(func(attachment map[string]any) { attachment["version"] = -1 }),
		"nested member inventory": mutateAttachment(func(attachment map[string]any) {
			attachment["comment"] = "not released"
		}),
		"empty optional media type": mutateAttachment(func(attachment map[string]any) { attachment["media_type"] = "" }),
		"duplicate ids": func(root map[string]any) {
			attachments := root["attachments"].([]any)
			root["attachments"] = append(attachments, attachments[0])
			root["count"] = 2
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceEvidenceWire(t, validConfluenceAttachmentWire(t, false), mutate)
			if _, err := DecodeConfluenceAttachmentInventoryView(bytes.NewReader(data)); err == nil {
				t.Fatal("unreconciled inventory was accepted")
			}
		})
	}

	for _, reason := range []string{"page_limit", "item_limit", "pagination_stalled", "legacy_unqualified"} {
		data := mutateConfluenceEvidenceWire(t, validConfluenceAttachmentWire(t, true), func(root map[string]any) {
			root["partial_reason"] = reason
		})
		if _, err := DecodeConfluenceAttachmentInventoryView(bytes.NewReader(data)); err != nil {
			t.Fatalf("partial reason %q rejected: %v", reason, err)
		}
	}
}

func validConfluenceMetadataWire(t *testing.T) []byte {
	t.Helper()
	return marshalConfluenceEvidenceWire(t, map[string]any{
		"schema_version": 1, "id": "42", "title": "Synthetic Page", "space": "DOC",
		"version": 7, "updated": "2026-08-04T12:00:00Z", "restriction_state": "restricted",
	})
}

func validConfluenceOutlineWire(t *testing.T, partial bool) []byte {
	t.Helper()
	root := map[string]any{
		"schema_version": 1, "id": "42", "title": "Synthetic Page", "space": "DOC", "version": 7,
		"count": 1, "total": 1, "complete": true, "original_bytes": 64, "emitted_bytes": 64,
		"headings": []any{map[string]any{
			"index": 1, "level": 1, "title": "Overview", "path": []any{"Overview"}, "occurrence": 1,
		}},
	}
	if partial {
		root["total"], root["complete"], root["truncated"] = 2, false, true
		root["partial_reason"], root["original_bytes"] = "byte_limit", 96
	}
	return marshalConfluenceEvidenceWire(t, root)
}

func validConfluenceSectionWire(t *testing.T, partial bool) []byte {
	t.Helper()
	markdown := "# Overview\n"
	root := map[string]any{
		"schema_version": 1, "id": "42", "page_title": "Synthetic Page", "space": "DOC", "version": 7,
		"page_version_gated": true, "heading": "Overview", "level": 1, "path": []any{"Overview"}, "occurrence": 1,
		"markdown": markdown, "complete": true, "original_bytes": len(markdown), "emitted_bytes": len(markdown),
	}
	if partial {
		root["complete"], root["truncated"], root["partial_reason"] = false, true, "max_bytes"
		root["original_bytes"] = len(markdown) + 24
	}
	return marshalConfluenceEvidenceWire(t, root)
}

func validConfluenceAttachmentWire(t *testing.T, partial bool) []byte {
	t.Helper()
	root := map[string]any{
		"schema_version": 1, "page_id": "42", "page_version": 7, "count": 1, "complete": true,
		"attachments": []any{map[string]any{
			"id": "att-1", "title": "diagram.png", "media_type": "image/png", "file_size": 128, "version": 2,
		}},
	}
	if partial {
		root["count"], root["complete"], root["partial_reason"] = 0, false, "page_limit"
		root["attachments"] = []any{}
	}
	return marshalConfluenceEvidenceWire(t, root)
}

func mutateConfluenceOutlineEntry(mutate func(map[string]any)) func(map[string]any) {
	return func(root map[string]any) {
		entry := root["headings"].([]any)[0].(map[string]any)
		mutate(entry)
	}
}

func mutateConfluenceEvidenceWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	return marshalConfluenceEvidenceWire(t, root)
}

func marshalConfluenceEvidenceWire(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
