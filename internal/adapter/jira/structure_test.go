package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestGetStructureWiresMetadataEndpoint(t *testing.T) {
	const body = `{
		"id":123,
		"name":"Release plan",
		"description":"Public example",
		"readOnly":true,
		"editRequiresParentIssuePermission":true,
		"owner":{"name":"alice"},
		"permissions":[{"type":"view"}]
	}`
	j, reqs := agileServer(t, map[string]string{"GET /rest/structure/2.0/structure/123": body})

	st, err := j.GetStructure(context.Background(), 123)
	if err != nil {
		t.Fatalf("GetStructure: %v", err)
	}
	if st.ID != 123 || st.Name != "Release plan" || !st.ReadOnly || !st.EditRequiresParentIssuePermission {
		t.Fatalf("structure = %+v, want parsed metadata", st)
	}
	req := (*reqs)[0]
	if req.path != "/rest/structure/2.0/structure/123" {
		t.Errorf("path = %q, want /rest/structure/2.0/structure/123", req.path)
	}
	q, err := url.ParseQuery(req.query)
	if err != nil {
		t.Fatalf("parse query %q: %v", req.query, err)
	}
	if q.Get("withPermissions") != "true" || q.Get("withOwner") != "true" {
		t.Errorf("query = %q, want withPermissions/withOwner true", req.query)
	}
}

func TestGetStructureAcceptsStringOwner(t *testing.T) {
	const body = `{
		"id":124,
		"name":"Release plan",
		"owner":"alice"
	}`
	j, _ := agileServer(t, map[string]string{"GET /rest/structure/2.0/structure/124": body})

	st, err := j.GetStructure(context.Background(), 124)
	if err != nil {
		t.Fatalf("GetStructure: %v", err)
	}
	owner, ok := st.Owner.(string)
	if !ok || owner != "alice" {
		t.Fatalf("owner = %#v, want alice string", st.Owner)
	}
}

func TestStructureForestWiresLatestForestSpec(t *testing.T) {
	const body = `{
		"spec":{"structureId":123},
		"formula":"100:0:10001,101:1:10002",
		"itemTypes":{"1":"folder"},
		"version":{"signature":55,"version":7}
	}`
	j, reqs := agileServer(t, map[string]string{"GET /rest/structure/2.0/forest/latest": body})

	forest, err := j.StructureForest(context.Background(), 123)
	if err != nil {
		t.Fatalf("StructureForest: %v", err)
	}
	if forest.Formula != "100:0:10001,101:1:10002" || forest.Version.Signature != 55 || forest.Version.Version != 7 {
		t.Fatalf("forest = %+v, want formula/version parsed", forest)
	}
	q, err := url.ParseQuery((*reqs)[0].query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	var spec struct {
		StructureID int64 `json:"structureId"`
	}
	if err := json.Unmarshal([]byte(q.Get("s")), &spec); err != nil {
		t.Fatalf("decode spec %q: %v", q.Get("s"), err)
	}
	if spec.StructureID != 123 {
		t.Errorf("structureId = %d, want 123", spec.StructureID)
	}
}

func TestStructureValuesWiresRowsAndAttributes(t *testing.T) {
	const body = `{"responses":[{"inaccessibleRows":[12],"itemTypes":{"1":"folder"},"itemsVersion":{"signature":9,"version":2},"rows":[10,12],"data":[]}]}`
	j, reqs := agileServer(t, map[string]string{"POST /rest/structure/2.0/value": body})

	vals, err := j.StructureValues(context.Background(), 123, []int64{10, 12}, []string{"key", "summary"})
	if err != nil {
		t.Fatalf("StructureValues: %v", err)
	}
	if len(vals.Responses) != 1 || len(vals.InaccessibleRows) != 1 || vals.InaccessibleRows[0] != 12 {
		t.Fatalf("values = %+v, want one response and inaccessible row 12", vals)
	}
	if vals.ItemTypes["1"] != "folder" || vals.ItemsVersion.Version != 2 {
		t.Errorf("values metadata = %+v, want itemTypes and itemsVersion", vals)
	}
	req := (*reqs)[0]
	if req.method != http.MethodPost || req.path != "/rest/structure/2.0/value" {
		t.Fatalf("req = %s %s, want POST /rest/structure/2.0/value", req.method, req.path)
	}
	var payload struct {
		Requests []struct {
			ForestSpec struct {
				StructureID int64 `json:"structureId"`
			} `json:"forestSpec"`
			Rows       []int64 `json:"rows"`
			Attributes []struct {
				ID     string `json:"id"`
				Format string `json:"format"`
			} `json:"attributes"`
		} `json:"requests"`
	}
	if err := json.Unmarshal([]byte(req.body), &payload); err != nil {
		t.Fatalf("decode body %q: %v", req.body, err)
	}
	got := payload.Requests[0]
	if got.ForestSpec.StructureID != 123 {
		t.Errorf("structureId = %d, want 123", got.ForestSpec.StructureID)
	}
	if rows := ints(got.Rows); rows != "10,12" {
		t.Errorf("rows = %s, want 10,12", rows)
	}
	if len(got.Attributes) != 2 || got.Attributes[0].ID != "key" || got.Attributes[0].Format != "text" || got.Attributes[1].ID != "summary" {
		t.Errorf("attributes = %+v, want key/summary text attrs", got.Attributes)
	}
}

func ints(values []int64) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strconv.FormatInt(v, 10)
	}
	return strings.Join(out, ",")
}
