package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const (
	BrokerDiscoveryJiraURI       = "atl://broker/discovery/jira"
	BrokerDiscoveryConfluenceURI = "atl://broker/discovery/confluence"
)

var brokerDiscoveryReadPolicy = toolErrorPolicy{fallback: staticMessage("Broker discovery read failed")}

func registerBrokerDiscoveryResources(server *mcp.Server, deps Dependencies, profile ServiceProfile) {
	for _, service := range []string{domain.ServerProductJira, domain.ServerProductConfluence} {
		if profile != ServiceDefault && string(profile) != service {
			continue
		}
		uri := "atl://broker/discovery/" + service
		server.AddResource(&mcp.Resource{
			URI: uri, Name: "atl-broker-discovery-" + service,
			Title:       "atl Broker " + service + " discovery",
			Description: "Fresh private advisory Broker operation access; every invocation reauthorizes.",
			MIMEType:    "application/json",
		}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			if deps.BrokerDiscovery == nil {
				return nil, brokerDiscoveryReadPolicy.classify(fmt.Errorf("%w: Broker discovery is not configured", domain.ErrConfig))
			}
			reader, err := deps.BrokerDiscovery(service)
			if err != nil {
				return nil, brokerDiscoveryReadPolicy.classify(err)
			}
			projection, err := reader.Discover(ctx, service)
			if err != nil {
				return nil, brokerDiscoveryReadPolicy.classify(err)
			}
			body, err := brokercontract.EncodeDiscoveryProjectionV2(projection)
			if err != nil {
				return nil, brokerDiscoveryReadPolicy.classify(err)
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(body)}}}, nil
		})
	}
}
