package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRuntimeResourceExactFrozenProjectionForEveryProfile(t *testing.T) {
	snapshot := RuntimeSnapshot{
		GlobalReadOnlyPolicy: RuntimeReadOnlyPolicy{
			ConfiguredReadOnly: true,
			EffectiveReadOnly:  true,
			ReadOnlySource:     RuntimeReadOnlyFlag,
		},
		Plugin: RuntimePluginStatus{
			InterfaceContract: RuntimeInterfaceCompatible,
			ProductVersion:    RuntimeProductMismatch,
		},
	}
	for _, profile := range []ServiceProfile{ServiceDefault, ServiceJira, ServiceConfluence, ServiceOffline} {
		t.Run(string(profile), func(t *testing.T) {
			client, closeSessions := connectTestClient(t, NewForServiceWithRuntime("test", Dependencies{}, profile, snapshot))
			defer closeSessions()

			listed, err := client.ListResources(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed.Resources) != 2 || listed.Resources[0].URI != CapabilitiesResourceURI || listed.Resources[1].URI != RuntimeResourceURI {
				t.Fatalf("profile %q resource order=%+v", profile, listed.Resources)
			}
			resource := listed.Resources[1]
			if resource.Name != runtimeResourceName || resource.Title != runtimeResourceTitle ||
				resource.Description != runtimeResourceDescription || resource.MIMEType != runtimeResourceMIMEType {
				t.Fatalf("runtime resource descriptor=%+v", resource)
			}

			result, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: RuntimeResourceURI})
			if err != nil {
				t.Fatal(err)
			}
			if result.TTLMs != 0 || result.CacheScope != "private" {
				t.Fatalf("runtime cache ttlMs=%d cacheScope=%q", result.TTLMs, result.CacheScope)
			}
			if len(result.Contents) != 1 || result.Contents[0].URI != RuntimeResourceURI ||
				result.Contents[0].MIMEType != runtimeResourceMIMEType || len(result.Contents[0].Blob) != 0 {
				t.Fatalf("runtime contents=%+v", result.Contents)
			}
			want := `{"schema_version":1,"access":"hard_read_only","lifecycle":"startup_only","change_activation":"restart_required","service_profile":"` + string(profile) + `","global_read_only_policy":{"configured_read_only":true,"effective_read_only":true,"read_only_source":"flag"},"plugin":{"interface_contract":"compatible","product_version":"mismatch"}}`
			if result.Contents[0].Text != want {
				t.Fatalf("runtime body=%s want=%s", result.Contents[0].Text, want)
			}
			assertRuntimeResourceJSON(t, result.Contents[0].Text)
		})
	}
}

func TestRuntimeResourceSnapshotIsImmutableAndEffectFree(t *testing.T) {
	var dependencyCalls atomic.Int32
	unexpected := func() error {
		dependencyCalls.Add(1)
		return context.Canceled
	}
	snapshot := RuntimeSnapshot{
		GlobalReadOnlyPolicy: RuntimeReadOnlyPolicy{EffectiveReadOnly: true, ReadOnlySource: RuntimeReadOnlyEnvironment},
		Plugin:               RuntimePluginStatus{InterfaceContract: RuntimeInterfaceCompatible, ProductVersion: RuntimeProductMatch},
	}
	server := NewForServiceWithRuntime("test", Dependencies{
		Jira:       func() (JiraReader, error) { return nil, unexpected() },
		Confluence: func() (ConfluenceReader, error) { return nil, unexpected() },
		MirrorRoot: func() (string, error) { return "", unexpected() },
	}, ServiceDefault, snapshot)

	// Mutating caller-owned state after construction cannot change pre-rendered bytes.
	snapshot.GlobalReadOnlyPolicy.ReadOnlySource = RuntimeReadOnlyNone
	snapshot.Plugin.ProductVersion = RuntimeProductMismatch
	client, closeSessions := connectTestClient(t, server)
	defer closeSessions()
	var first string
	for i := 0; i < 3; i++ {
		result, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: RuntimeResourceURI})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = result.Contents[0].Text
		} else if result.Contents[0].Text != first {
			t.Fatalf("runtime read %d changed bytes", i+1)
		}
	}
	if dependencyCalls.Load() != 0 {
		t.Fatalf("runtime reads invoked %d dependencies", dependencyCalls.Load())
	}
	if first != `{"schema_version":1,"access":"hard_read_only","lifecycle":"startup_only","change_activation":"restart_required","service_profile":"default","global_read_only_policy":{"configured_read_only":false,"effective_read_only":true,"read_only_source":"environment"},"plugin":{"interface_contract":"compatible","product_version":"match"}}` {
		t.Fatalf("frozen runtime body=%s", first)
	}
}

func TestRuntimeSnapshotRejectsContradictoryClosedStates(t *testing.T) {
	valid := []RuntimeSnapshot{
		defaultRuntimeSnapshot(),
		{GlobalReadOnlyPolicy: RuntimeReadOnlyPolicy{ConfiguredReadOnly: true, EffectiveReadOnly: true, ReadOnlySource: RuntimeReadOnlyConfiguration}, Plugin: RuntimePluginStatus{InterfaceContract: RuntimeInterfaceCompatible, ProductVersion: RuntimeProductMatch}},
		{GlobalReadOnlyPolicy: RuntimeReadOnlyPolicy{EffectiveReadOnly: true, ReadOnlySource: RuntimeReadOnlyFlag}, Plugin: RuntimePluginStatus{InterfaceContract: RuntimeInterfaceCompatible, ProductVersion: RuntimeProductMismatch}},
	}
	for i, snapshot := range valid {
		if !snapshot.valid() {
			t.Errorf("valid snapshot %d rejected: %+v", i, snapshot)
		}
	}
	invalid := []RuntimeSnapshot{
		{},
		{GlobalReadOnlyPolicy: RuntimeReadOnlyPolicy{ConfiguredReadOnly: true, ReadOnlySource: RuntimeReadOnlyConfiguration}, Plugin: defaultRuntimeSnapshot().Plugin},
		{GlobalReadOnlyPolicy: RuntimeReadOnlyPolicy{EffectiveReadOnly: true, ReadOnlySource: RuntimeReadOnlyNone}, Plugin: defaultRuntimeSnapshot().Plugin},
		{GlobalReadOnlyPolicy: RuntimeReadOnlyPolicy{ReadOnlySource: RuntimeReadOnlyFlag}, Plugin: defaultRuntimeSnapshot().Plugin},
		{GlobalReadOnlyPolicy: defaultRuntimeSnapshot().GlobalReadOnlyPolicy, Plugin: RuntimePluginStatus{InterfaceContract: RuntimeInterfaceUnverified, ProductVersion: RuntimeProductMatch}},
		{GlobalReadOnlyPolicy: defaultRuntimeSnapshot().GlobalReadOnlyPolicy, Plugin: RuntimePluginStatus{InterfaceContract: RuntimeInterfaceCompatible, ProductVersion: RuntimeProductUnverified}},
		{GlobalReadOnlyPolicy: defaultRuntimeSnapshot().GlobalReadOnlyPolicy, Plugin: RuntimePluginStatus{InterfaceContract: "future", ProductVersion: RuntimeProductMatch}},
	}
	for i, snapshot := range invalid {
		if snapshot.valid() {
			t.Errorf("invalid snapshot %d accepted: %+v", i, snapshot)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("external constructor accepted invalid snapshot %d: %+v", i, snapshot)
				}
			}()
			_ = NewForServiceWithRuntime("test", Dependencies{}, ServiceOffline, snapshot)
		}()
		if err := ServeServiceWithRuntime(context.Background(), "test", ServiceOffline, snapshot); err == nil {
			t.Errorf("production server accepted invalid snapshot %d: %+v", i, snapshot)
		}
	}
}

func TestRuntimeCacheMiddlewareIsNarrow(t *testing.T) {
	cases := []struct {
		name, method, uri string
		wantScope         string
	}{
		{name: "runtime read", method: "resources/read", uri: RuntimeResourceURI, wantScope: "private"},
		{name: "capabilities read", method: "resources/read", uri: CapabilitiesResourceURI, wantScope: "public"},
		{name: "runtime different method", method: "resources/list", uri: RuntimeResourceURI, wantScope: "public"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := &mcp.ReadResourceResult{Cacheable: mcp.Cacheable{TTLMs: 42, CacheScope: "public"}}
			handler := privateRuntimeResourceCache(func(context.Context, string, mcp.Request) (mcp.Result, error) {
				return result, nil
			})
			req := &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: test.uri}}
			got, err := handler(context.Background(), test.method, req)
			if err != nil || got != result {
				t.Fatalf("middleware result=%T err=%v", got, err)
			}
			if test.wantScope == "private" {
				if result.TTLMs != 0 || result.CacheScope != "private" {
					t.Fatalf("runtime cache=%+v", result.Cacheable)
				}
			} else if result.TTLMs != 42 || result.CacheScope != "public" {
				t.Fatalf("unrelated cache changed=%+v", result.Cacheable)
			}
		})
	}
}

func assertRuntimeResourceJSON(t *testing.T, text string) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		t.Fatal(err)
	}
	wantTop := []string{"access", "change_activation", "global_read_only_policy", "lifecycle", "plugin", "schema_version", "service_profile"}
	if got := sortedRuntimeKeys(top); !slices.Equal(got, wantTop) {
		t.Fatalf("runtime keys=%v want=%v", got, wantTop)
	}
	var policy map[string]json.RawMessage
	if err := json.Unmarshal(top["global_read_only_policy"], &policy); err != nil {
		t.Fatal(err)
	}
	if got, want := sortedRuntimeKeys(policy), []string{"configured_read_only", "effective_read_only", "read_only_source"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime policy keys=%v want=%v", got, want)
	}
	var plugin map[string]json.RawMessage
	if err := json.Unmarshal(top["plugin"], &plugin); err != nil {
		t.Fatal(err)
	}
	if got, want := sortedRuntimeKeys(plugin), []string{"interface_contract", "product_version"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime plugin keys=%v want=%v", got, want)
	}
}

func sortedRuntimeKeys(document map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
