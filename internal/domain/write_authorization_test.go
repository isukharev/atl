package domain

import (
	"context"
	"reflect"
	"testing"
)

func TestWriteVerbVocabularyAndSetValidation(t *testing.T) {
	verbs := WriteVerbSet{
		WriteVerbCreate,
		WriteVerbUpdate,
		WriteVerbComment,
		WriteVerbTransition,
		WriteVerbMove,
		WriteVerbDelete,
	}
	for _, verb := range verbs {
		if !ValidWriteVerb(verb) {
			t.Errorf("reviewed verb %q is invalid", verb)
		}
	}
	if !ValidWriteVerbSet(verbs) {
		t.Fatal("complete reviewed verb set is invalid")
	}
	for name, candidate := range map[string]WriteVerbSet{
		"empty":     nil,
		"unknown":   {WriteVerb("read")},
		"duplicate": {WriteVerbUpdate, WriteVerbUpdate},
	} {
		t.Run(name, func(t *testing.T) {
			if ValidWriteVerbSet(candidate) {
				t.Fatalf("verb set %v unexpectedly valid", candidate)
			}
		})
	}
}

type writeAuthorizerFunc func(context.Context, WriteAuthorizationRequest) (context.Context, error)

func (f writeAuthorizerFunc) Authorize(ctx context.Context, request WriteAuthorizationRequest) (context.Context, error) {
	return f(ctx, request)
}

func TestWriteAuthorizerCarriesCompoundRequestAndAdmittedContext(t *testing.T) {
	want := WriteAuthorizationRequest{
		Verbs: WriteVerbSet{WriteVerbMove, WriteVerbUpdate},
		Targets: []WriteTarget{
			{Service: "confluence", Kind: "page", ID: "123", Space: "DOCS", AncestorIDs: []string{"100"}},
			{Service: "confluence", Kind: "page", ID: "200", Space: "DOCS", AncestorIDs: []string{}},
		},
	}
	var got WriteAuthorizationRequest
	var authorizer WriteAuthorizer = writeAuthorizerFunc(func(ctx context.Context, request WriteAuthorizationRequest) (context.Context, error) {
		got = request
		return WithWriteClearance(ctx), nil
	})

	ctx, err := authorizer.Authorize(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
	if !HasWriteClearance(ctx) {
		t.Fatal("admitted authorizer context lacks write clearance")
	}
}
