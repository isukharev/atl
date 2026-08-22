package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// prepareSyntheticJiraCreateMetadata augments a copied legacy create fixture
// with the content-free type inventory that current ATL resolves before every
// issue create. Retained corpus fixtures keep their historical name payloads;
// the selected-process copy instead proves the current id-only write shape.
func prepareSyntheticJiraCreateMetadata(t *testing.T, fixture MockFixture) MockFixture {
	t.Helper()
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)

	type createRoute struct {
		index              int
		project, issueType string
	}
	creates := make([]createRoute, 0)
	issueTypes := map[string]map[string]struct{}{}
	for index := range prepared.Routes {
		route := prepared.Routes[index]
		if route.Method != http.MethodPost || route.Path != prepared.JiraContext+"/rest/api/2/issue" {
			continue
		}
		var request struct {
			Fields struct {
				Project struct {
					Key string `json:"key"`
				} `json:"project"`
				IssueType struct {
					Name string `json:"name"`
				} `json:"issuetype"`
			} `json:"fields"`
		}
		if err := json.Unmarshal(route.RequestBody, &request); err != nil || request.Fields.Project.Key == "" || request.Fields.IssueType.Name == "" {
			t.Fatalf("decode legacy Jira create route %q: %v", route.Name, err)
		}
		if issueTypes[request.Fields.Project.Key] == nil {
			issueTypes[request.Fields.Project.Key] = map[string]struct{}{}
		}
		issueTypes[request.Fields.Project.Key][request.Fields.IssueType.Name] = struct{}{}
		creates = append(creates, createRoute{index: index, project: request.Fields.Project.Key, issueType: request.Fields.IssueType.Name})
	}
	if len(creates) == 0 {
		t.Fatal("synthetic fixture has no Jira issue-create route")
	}

	projects := make([]string, 0, len(issueTypes))
	for project := range issueTypes {
		projects = append(projects, project)
	}
	sort.Strings(projects)
	metadataNames := make(map[string]string, len(projects))
	typeIDs := make(map[string]map[string]string, len(projects))
	for projectIndex, project := range projects {
		types := make([]string, 0, len(issueTypes[project]))
		for issueType := range issueTypes[project] {
			types = append(types, issueType)
		}
		sort.Strings(types)
		values := make([]map[string]any, len(types))
		typeIDs[project] = make(map[string]string, len(types))
		for typeIndex, issueType := range types {
			id := strconv.Itoa(typeIndex + 1)
			typeIDs[project][issueType] = id
			values[typeIndex] = map[string]any{"id": id, "name": issueType}
		}
		body, err := json.Marshal(map[string]any{"startAt": 0, "isLast": true, "values": values})
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("create-metadata-%d", projectIndex+1)
		metadataNames[project] = name
		prepared.Routes = append(prepared.Routes, MockRoute{
			Name: name, Method: http.MethodGet,
			Path:        prepared.JiraContext + "/rest/api/2/issue/createmeta/" + url.PathEscape(project) + "/issuetypes",
			QueryEquals: map[string]string{"startAt": "0", "maxResults": "200"},
			Status:      http.StatusOK, Body: body, closedQuery: true,
		})
	}

	metadataByCreateRoute := make(map[string]string, len(creates))
	for _, create := range creates {
		var request map[string]any
		if err := json.Unmarshal(prepared.Routes[create.index].RequestBody, &request); err != nil {
			t.Fatal(err)
		}
		fields, ok := request["fields"].(map[string]any)
		if !ok {
			t.Fatalf("legacy Jira create route %q has no fields object", prepared.Routes[create.index].Name)
		}
		fields["issuetype"] = map[string]any{"id": typeIDs[create.project][create.issueType]}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		prepared.Routes[create.index].RequestBody = body
		metadataByCreateRoute[prepared.Routes[create.index].Name] = metadataNames[create.project]
	}
	if len(prepared.RequestSequence) > 0 {
		sequence := make([]string, 0, len(prepared.RequestSequence)+len(creates))
		for _, name := range prepared.RequestSequence {
			if metadata, ok := metadataByCreateRoute[name]; ok {
				sequence = append(sequence, metadata)
			}
			sequence = append(sequence, name)
		}
		prepared.RequestSequence = sequence
	}
	return prepared
}

// prepareSyntheticJiraGuardedCreate upgrades the selected spec-to-backlog
// fixture from its explicitly historical direct-create wire to the current
// preview/apply product contract. Other retained workflow corpora keep using
// prepareSyntheticJiraCreateMetadata until their own reviewed migrations.
func prepareSyntheticJiraGuardedCreate(t *testing.T, fixture MockFixture) MockFixture {
	t.Helper()
	prepared := prepareSyntheticJiraCreateMetadata(t, fixture)
	originalSequence := slices.Clone(fixture.RequestSequence)
	projectIDs := map[string]string{}
	typeNames := map[string]string{}
	createRoutes := map[string]struct {
		project, projectID, typeID, typeName, id, key, summary, description string
		success                                                             bool
		readbackFields                                                      map[string]any
	}{}
	extraFieldsByType := map[string]map[string]string{}

	for index := range prepared.Routes {
		route := &prepared.Routes[index]
		if route.Method != http.MethodPost || route.Path != prepared.JiraContext+"/rest/api/2/issue" {
			continue
		}
		var request struct {
			Fields struct {
				Project struct {
					Key string `json:"key"`
				} `json:"project"`
				IssueType struct {
					ID string `json:"id"`
				} `json:"issuetype"`
				Summary, Description string
			} `json:"fields"`
		}
		var response struct {
			Key string `json:"key"`
		}
		var rawRequest struct {
			Fields map[string]json.RawMessage `json:"fields"`
		}
		if err := json.Unmarshal(route.RequestBody, &request); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(route.Body, &response); err != nil && route.Status >= 200 && route.Status < 300 {
			t.Fatal(err)
		}
		if err := json.Unmarshal(route.RequestBody, &rawRequest); err != nil {
			t.Fatal(err)
		}
		projectID := projectIDs[request.Fields.Project.Key]
		if projectID == "" {
			projectID = strconv.Itoa(len(projectIDs) + 1)
			projectIDs[request.Fields.Project.Key] = projectID
		}
		id := strings.TrimPrefix(response.Key, request.Fields.Project.Key+"-")
		success := route.Status >= 200 && route.Status < 300
		if success && id == "" {
			t.Fatalf("guarded create route %q has no immutable id", route.Name)
		}
		typeKey := request.Fields.Project.Key + "\x00" + request.Fields.IssueType.ID
		extraFields := map[string]string{}
		readbackFields := map[string]any{}
		for field := range rawRequest.Fields {
			switch field {
			case "project", "issuetype", "summary", "description":
				continue
			case "assignee":
				extraFields[field] = "user"
			case "duedate":
				extraFields[field] = "date"
			default:
				extraFields[field] = "string"
			}
			var value any
			if err := json.Unmarshal(rawRequest.Fields[field], &value); err != nil {
				t.Fatal(err)
			}
			readbackFields[field] = value
		}
		if extraFieldsByType[typeKey] == nil {
			extraFieldsByType[typeKey] = map[string]string{}
		}
		for field, typ := range extraFields {
			extraFieldsByType[typeKey][field] = typ
		}
		createRoutes[route.Name] = struct {
			project, projectID, typeID, typeName, id, key, summary, description string
			success                                                             bool
			readbackFields                                                      map[string]any
		}{request.Fields.Project.Key, projectID, request.Fields.IssueType.ID, typeKey, id, response.Key, request.Fields.Summary, request.Fields.Description, success, readbackFields}
		if success {
			route.Body, _ = json.Marshal(map[string]string{"id": id, "key": response.Key})
		}
	}

	for index := range prepared.Routes {
		route := &prepared.Routes[index]
		if !strings.HasPrefix(route.Name, "create-metadata-") {
			continue
		}
		var page struct {
			Values []struct{ ID, Name string }
		}
		if err := json.Unmarshal(route.Body, &page); err != nil {
			t.Fatal(err)
		}
		for _, value := range page.Values {
			for project := range projectIDs {
				if strings.Contains(route.Path, "/"+project+"/") {
					typeNames[project+"\x00"+value.ID] = value.Name
				}
			}
		}
		route.Body, _ = json.Marshal(map[string]any{"startAt": 0, "total": len(page.Values), "isLast": true, "values": page.Values})
	}

	for name, create := range createRoutes {
		create.typeName = typeNames[create.typeName]
		createRoutes[name] = create
	}
	projects := make([]map[string]any, 0, len(projectIDs))
	for project, id := range projectIDs {
		projects = append(projects, map[string]any{"id": id, "key": project, "name": project, "archived": false})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i]["key"].(string) < projects[j]["key"].(string) })
	projectBody, _ := json.Marshal(projects)
	prepared.Routes = append(prepared.Routes, MockRoute{
		Name: "guarded-create-projects", Method: http.MethodGet, Path: prepared.JiraContext + "/rest/api/2/project",
		QueryEquals: map[string]string{"includeArchived": "true"}, Status: http.StatusOK, Body: projectBody, closedQuery: true,
	})

	fieldRouteNames := map[string]string{}
	for _, create := range createRoutes {
		key := create.project + "\x00" + create.typeID
		if fieldRouteNames[key] != "" {
			continue
		}
		name := "guarded-create-fields-" + strings.ToLower(create.project) + "-" + create.typeID
		fieldRouteNames[key] = name
		field := func(id, typ string, required bool) map[string]any {
			return map[string]any{"fieldId": id, "name": id, "required": required, "schema": map[string]any{"type": typ, "system": id}, "hasDefaultValue": false, "allowedValues": []any{}, "autoCompleteUrl": nil}
		}
		values := []map[string]any{field("project", "project", true), field("issuetype", "issuetype", true), field("summary", "string", true), field("description", "string", false)}
		extraNames := make([]string, 0, len(extraFieldsByType[key]))
		for name := range extraFieldsByType[key] {
			extraNames = append(extraNames, name)
		}
		sort.Strings(extraNames)
		for _, name := range extraNames {
			values = append(values, field(name, extraFieldsByType[key][name], false))
		}
		body, _ := json.Marshal(map[string]any{"startAt": 0, "total": len(values), "isLast": true, "values": values})
		prepared.Routes = append(prepared.Routes, MockRoute{
			Name: name, Method: http.MethodGet,
			Path:        prepared.JiraContext + "/rest/api/2/issue/createmeta/" + url.PathEscape(create.project) + "/issuetypes/" + url.PathEscape(create.typeID),
			QueryEquals: map[string]string{"startAt": "0", "maxResults": "200"}, Status: http.StatusOK, Body: body, closedQuery: true,
		})
	}

	sequence := make([]string, 0, len(originalSequence)+len(createRoutes)*10)
	for _, name := range originalSequence {
		create, ok := createRoutes[name]
		if !ok {
			sequence = append(sequence, name)
			continue
		}
		typeRoute := ""
		for _, route := range prepared.Routes {
			if strings.HasPrefix(route.Name, "create-metadata-") && strings.Contains(route.Path, "/"+create.project+"/") {
				typeRoute = route.Name
				break
			}
		}
		fieldRoute := fieldRouteNames[create.project+"\x00"+create.typeID]
		for range 3 {
			sequence = append(sequence, "guarded-create-projects", typeRoute, fieldRoute)
		}
		sequence = append(sequence, name)
		if !create.success {
			continue
		}
		readName := name + "-readback"
		sequence = append(sequence, readName)
		readback := map[string]any{
			"project":   map[string]string{"id": create.projectID, "key": create.project},
			"issuetype": map[string]string{"id": create.typeID, "name": create.typeName},
			"summary":   create.summary, "description": create.description,
			"created": "2026-08-22T10:00:00.000+0000", "updated": "2026-08-22T10:00:01.000+0000",
		}
		readbackFieldNames := []string{"created", "description", "issuetype", "project", "summary", "updated"}
		for field, value := range create.readbackFields {
			readback[field] = value
			readbackFieldNames = append(readbackFieldNames, field)
		}
		sort.Strings(readbackFieldNames)
		readBody, _ := json.Marshal(map[string]any{
			"id": create.id, "key": create.key,
			"fields": readback,
		})
		prepared.Routes = append(prepared.Routes, MockRoute{
			Name: readName, Method: http.MethodGet, Path: prepared.JiraContext + "/rest/api/2/issue/" + create.id,
			QueryEquals: map[string]string{"fields": strings.Join(readbackFieldNames, ",")}, Status: http.StatusOK, Body: readBody, closedQuery: true,
		})
	}
	prepared.RequestSequence = sequence
	return prepared
}

func TestPrepareSyntheticJiraCreateMetadataSplicesPreflightAndIDWrite(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Name: "create", Method: http.MethodPost, Path: "/jira/rest/api/2/issue",
			RequestBody: json.RawMessage(`{"fields":{"project":{"key":"TEST"},"issuetype":{"name":"Task"},"summary":"synthetic"}}`),
			Status:      http.StatusCreated, Body: json.RawMessage(`{"key":"TEST-1"}`),
		}},
		RequestSequence: []string{"create"},
	}
	prepared := prepareSyntheticJiraCreateMetadata(t, fixture)
	if !slices.Equal(prepared.RequestSequence, []string{"create-metadata-1", "create"}) {
		t.Fatalf("sequence=%v", prepared.RequestSequence)
	}
	backend, err := StartMockBackend(prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	metadata, err := http.Get(backend.Environment()["ATL_JIRA_URL"] + "/rest/api/2/issue/createmeta/TEST/issuetypes?startAt=0&maxResults=200")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.StatusCode != http.StatusOK {
		t.Fatalf("metadata status=%d", metadata.StatusCode)
	}
	_ = metadata.Body.Close()
	response, err := http.Post(backend.Environment()["ATL_JIRA_URL"]+"/rest/api/2/issue", "application/json", bytes.NewReader(prepared.Routes[0].RequestBody))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	if !backend.RequestSequenceComplete() {
		t.Fatal("metadata preflight and create did not complete the configured sequence")
	}
	var request struct {
		Fields struct {
			IssueType struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"issuetype"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(prepared.Routes[0].RequestBody, &request); err != nil || request.Fields.IssueType.ID != "1" || request.Fields.IssueType.Name != "" {
		t.Fatalf("prepared create payload=%s err=%v", prepared.Routes[0].RequestBody, err)
	}
}

// syntheticJiraCreateHistoricalContract records an exact committed corpus
// contract. Historical fixture owners and current run-spec owners both use the
// same assertion without deriving replacement geometry in memory.
type syntheticJiraCreateHistoricalContract struct {
	HTTPMethods                 map[string]int
	MaxBackendRequests          int
	MaxDuplicateBackendRequests int
}

func assertSyntheticJiraCreateHistoricalContract(
	t *testing.T,
	spec RunSpec,
	scenario Scenario,
	want syntheticJiraCreateHistoricalContract,
) {
	t.Helper()
	found := 0
	var methods map[string]int
	for _, check := range spec.Checks {
		if check.Kind != "http_methods_equal" {
			continue
		}
		var ok bool
		methods, ok = expectedHTTPMethods(check.Expected)
		if !ok {
			t.Fatalf("historical HTTP method oracle %q is invalid", check.Name)
		}
		found++
	}
	if found != 1 || !maps.Equal(methods, want.HTTPMethods) {
		t.Fatalf("historical HTTP method oracle=%v count=%d want=%v", methods, found, want.HTTPMethods)
	}
	if scenario.Budgets.MaxBackendRequests != want.MaxBackendRequests ||
		scenario.Budgets.MaxDuplicateBackendRequests != want.MaxDuplicateBackendRequests {
		t.Fatalf("historical backend budgets=%+v want requests=%d duplicates=%d",
			scenario.Budgets, want.MaxBackendRequests, want.MaxDuplicateBackendRequests)
	}
}
