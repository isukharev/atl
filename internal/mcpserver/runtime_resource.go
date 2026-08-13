package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	RuntimeResourceURI         = "atl://runtime"
	runtimeResourceName        = "atl-runtime"
	runtimeResourceTitle       = "atl runtime safety projection"
	runtimeResourceDescription = "Immutable content-free startup safety and compatibility metadata for this atl MCP invocation."
	runtimeResourceMIMEType    = "application/json"
	runtimeResourceSchema      = 1
)

type RuntimeReadOnlySource string

const (
	RuntimeReadOnlyFlag          RuntimeReadOnlySource = "flag"
	RuntimeReadOnlyEnvironment   RuntimeReadOnlySource = "environment"
	RuntimeReadOnlyConfiguration RuntimeReadOnlySource = "configuration"
	RuntimeReadOnlyNone          RuntimeReadOnlySource = "none"
)

type RuntimeInterfaceContract string

const (
	RuntimeInterfaceUnverified RuntimeInterfaceContract = "unverified"
	RuntimeInterfaceCompatible RuntimeInterfaceContract = "compatible"
)

type RuntimeProductVersion string

const (
	RuntimeProductUnverified RuntimeProductVersion = "unverified"
	RuntimeProductMatch      RuntimeProductVersion = "match"
	RuntimeProductMismatch   RuntimeProductVersion = "mismatch"
)

// RuntimeSnapshot is the content-free invocation state captured by the CLI
// before stdio starts. Server construction validates and pre-renders it once.
type RuntimeSnapshot struct {
	GlobalReadOnlyPolicy RuntimeReadOnlyPolicy
	Plugin               RuntimePluginStatus
}

type RuntimeReadOnlyPolicy struct {
	ConfiguredReadOnly bool
	EffectiveReadOnly  bool
	ReadOnlySource     RuntimeReadOnlySource
}

type RuntimePluginStatus struct {
	InterfaceContract RuntimeInterfaceContract
	ProductVersion    RuntimeProductVersion
}

type runtimeResource struct {
	SchemaVersion        int                   `json:"schema_version"`
	Access               string                `json:"access"`
	Lifecycle            string                `json:"lifecycle"`
	ChangeActivation     string                `json:"change_activation"`
	ServiceProfile       ServiceProfile        `json:"service_profile"`
	GlobalReadOnlyPolicy runtimeReadOnlyPolicy `json:"global_read_only_policy"`
	Plugin               runtimePluginStatus   `json:"plugin"`
}

type runtimeReadOnlyPolicy struct {
	ConfiguredReadOnly bool                  `json:"configured_read_only"`
	EffectiveReadOnly  bool                  `json:"effective_read_only"`
	ReadOnlySource     RuntimeReadOnlySource `json:"read_only_source"`
}

type runtimePluginStatus struct {
	InterfaceContract RuntimeInterfaceContract `json:"interface_contract"`
	ProductVersion    RuntimeProductVersion    `json:"product_version"`
}

func defaultRuntimeSnapshot() RuntimeSnapshot {
	return RuntimeSnapshot{
		GlobalReadOnlyPolicy: RuntimeReadOnlyPolicy{ReadOnlySource: RuntimeReadOnlyNone},
		Plugin: RuntimePluginStatus{
			InterfaceContract: RuntimeInterfaceUnverified,
			ProductVersion:    RuntimeProductUnverified,
		},
	}
}

func (snapshot RuntimeSnapshot) valid() bool {
	policy := snapshot.GlobalReadOnlyPolicy
	if policy.ConfiguredReadOnly && !policy.EffectiveReadOnly {
		return false
	}
	switch policy.ReadOnlySource {
	case RuntimeReadOnlyNone:
		if policy.EffectiveReadOnly {
			return false
		}
	case RuntimeReadOnlyConfiguration:
		if !policy.ConfiguredReadOnly || !policy.EffectiveReadOnly {
			return false
		}
	case RuntimeReadOnlyFlag, RuntimeReadOnlyEnvironment:
		if !policy.EffectiveReadOnly {
			return false
		}
	default:
		return false
	}
	switch snapshot.Plugin.InterfaceContract {
	case RuntimeInterfaceUnverified:
		if snapshot.Plugin.ProductVersion != RuntimeProductUnverified {
			return false
		}
	case RuntimeInterfaceCompatible:
		if snapshot.Plugin.ProductVersion != RuntimeProductMatch && snapshot.Plugin.ProductVersion != RuntimeProductMismatch {
			return false
		}
	default:
		return false
	}
	return true
}

func registerRuntimeResource(server *mcp.Server, profile ServiceProfile, snapshot RuntimeSnapshot) {
	body := preRenderRuntimeResource(profile, snapshot)
	server.AddResource(&mcp.Resource{
		URI:         RuntimeResourceURI,
		Name:        runtimeResourceName,
		Title:       runtimeResourceTitle,
		Description: runtimeResourceDescription,
		MIMEType:    runtimeResourceMIMEType,
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: RuntimeResourceURI, MIMEType: runtimeResourceMIMEType, Text: body,
		}}}, nil
	})
}

func preRenderRuntimeResource(profile ServiceProfile, snapshot RuntimeSnapshot) string {
	document := runtimeResource{
		SchemaVersion:    runtimeResourceSchema,
		Access:           "hard_read_only",
		Lifecycle:        "startup_only",
		ChangeActivation: "restart_required",
		ServiceProfile:   profile,
		GlobalReadOnlyPolicy: runtimeReadOnlyPolicy{
			ConfiguredReadOnly: snapshot.GlobalReadOnlyPolicy.ConfiguredReadOnly,
			EffectiveReadOnly:  snapshot.GlobalReadOnlyPolicy.EffectiveReadOnly,
			ReadOnlySource:     snapshot.GlobalReadOnlyPolicy.ReadOnlySource,
		},
		Plugin: runtimePluginStatus{
			InterfaceContract: snapshot.Plugin.InterfaceContract,
			ProductVersion:    snapshot.Plugin.ProductVersion,
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic("encode MCP runtime resource: " + err.Error())
	}
	return string(encoded)
}

// privateRuntimeResourceCache runs outside the SDK resource handler so its
// post-handler assignment wins over v1.7's public cache default.
func privateRuntimeResourceCache(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, req)
		if err != nil || method != "resources/read" {
			return result, err
		}
		params, ok := req.GetParams().(*mcp.ReadResourceParams)
		if !ok || params == nil || params.URI != RuntimeResourceURI {
			return result, nil
		}
		read, ok := result.(*mcp.ReadResourceResult)
		if !ok || read == nil {
			return result, nil
		}
		read.TTLMs = 0
		read.CacheScope = "private"
		return read, nil
	}
}
