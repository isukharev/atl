package csf

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExtractInlineCommentMarkers(t *testing.T) {
	raw := []byte(`<p>
  <inline-comment-marker ac:ref="wrong-element">ignored</inline-comment-marker>
  <ri:inline-comment-marker ac:ref="wrong-prefix">ignored</ri:inline-comment-marker>
  <ac:inline-comment-marker ref="unqualified-attribute">ignored</ac:inline-comment-marker>
  <ac:inline-comment-marker ac:ref="">ignored</ac:inline-comment-marker>
  <ac:inline-comment-marker ac:ref="  ">ignored</ac:inline-comment-marker>
  <ac:inline-comment-marker ac:ref="thread-2">alpha <strong>nested &amp; café 😀</strong> tail</ac:inline-comment-marker>
  <ac:inline-comment-marker ac:ref="thread-1"><em>second&nbsp;selection</em></ac:inline-comment-marker>
  <ac:inline-comment-marker ac:ref="thread-2">duplicate ref</ac:inline-comment-marker>
  <ac:inline-comment-marker ac:ref="empty-selection"></ac:inline-comment-marker>
</p>`)

	want := []InlineCommentMarker{
		{Ref: "thread-2", Selection: "alpha nested & café 😀 tail"},
		{Ref: "thread-1", Selection: "second\u00a0selection"},
		{Ref: "thread-2", Selection: "duplicate ref"},
		{Ref: "empty-selection", Selection: ""},
	}
	got, err := ExtractInlineCommentMarkers(raw)
	if err != nil {
		t.Fatalf("ExtractInlineCommentMarkers() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractInlineCommentMarkers() = %#v, want %#v", got, want)
	}
}

func TestExtractInlineCommentMarkersIsDeterministicAndReadOnly(t *testing.T) {
	raw := []byte(`<p><ac:inline-comment-marker ac:ref=" first ">one</ac:inline-comment-marker><ac:inline-comment-marker ac:ref="second">two</ac:inline-comment-marker></p>`)
	orig := append([]byte(nil), raw...)

	first, err := ExtractInlineCommentMarkers(raw)
	if err != nil {
		t.Fatalf("first ExtractInlineCommentMarkers() error = %v", err)
	}
	second, err := ExtractInlineCommentMarkers(raw)
	if err != nil {
		t.Fatalf("second ExtractInlineCommentMarkers() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ: first=%#v second=%#v", first, second)
	}
	if !bytes.Equal(raw, orig) {
		t.Fatalf("input mutated: before=%q after=%q", orig, raw)
	}
	if len(first) != 2 || first[0].Ref != " first " {
		t.Fatalf("unexpected refs: %#v", first)
	}
}

func TestExtractInlineCommentMarkersReturnsParseErrors(t *testing.T) {
	tests := []struct {
		name  string
		raw   []byte
		isErr error
	}{
		{
			name: "malformed",
			raw:  []byte(`<ac:inline-comment-marker ac:ref="r">broken`),
		},
		{
			name:  "over depth",
			raw:   nestedCSF(MaxNestingDepth + 1),
			isErr: ErrMaxNestingDepth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := append([]byte(nil), tt.raw...)
			got, err := ExtractInlineCommentMarkers(tt.raw)
			if err == nil {
				t.Fatal("ExtractInlineCommentMarkers() error = nil, want parse error")
			}
			if tt.isErr != nil && !errors.Is(err, tt.isErr) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.isErr)
			}
			if got != nil {
				t.Fatalf("markers = %#v, want nil on parse error", got)
			}
			if !bytes.Equal(tt.raw, orig) {
				t.Fatalf("input mutated: before=%q after=%q", orig, tt.raw)
			}
		})
	}
}

func FuzzExtractInlineCommentMarkers(f *testing.F) {
	seedCSF(f)
	f.Add([]byte(`<ac:inline-comment-marker ac:ref="r">a <strong>b &amp; c</strong></ac:inline-comment-marker>`))
	f.Add([]byte(`<ac:inline-comment-marker ac:ref="same">one</ac:inline-comment-marker><ac:inline-comment-marker ac:ref="same">two</ac:inline-comment-marker>`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		orig := append([]byte(nil), raw...)

		first, firstErr := ExtractInlineCommentMarkers(raw)
		second, secondErr := ExtractInlineCommentMarkers(raw)

		if !bytes.Equal(raw, orig) {
			t.Fatalf("ExtractInlineCommentMarkers mutated its input: before=%q after=%q", orig, raw)
		}
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("non-deterministic errors: first=%v second=%v", firstErr, secondErr)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("non-deterministic results: first=%#v second=%#v", first, second)
		}
		if firstErr != nil {
			if first != nil || second != nil {
				t.Fatalf("parse error returned records: first=%#v second=%#v", first, second)
			}
			return
		}
		for _, marker := range first {
			if strings.TrimSpace(marker.Ref) == "" {
				t.Fatalf("invalid returned ref %q in %#v", marker.Ref, first)
			}
		}
	})
}
