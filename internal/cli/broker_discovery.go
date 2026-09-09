package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/compose"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

var loadBrokerDiscovery = compose.LoadBrokerDiscovery

func newBrokerDiscoverCommand() *cobra.Command {
	var service, family string
	command := &cobra.Command{
		Use: "discover", Short: "Read current advisory Broker operation access", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if service != domain.ServerProductJira && service != domain.ServerProductConfluence {
				return usageErr("--service must be jira or confluence")
			}
			if family != "" && (family != domain.BrokerContractFamilyExecutionV2 || service != domain.ServerProductJira) {
				return usageErr("--family must be omitted or atl.broker.execution.v2 with --service jira")
			}
			reader, err := loadBrokerDiscovery(service, version.Version)
			if err != nil {
				return err
			}
			if family != "" {
				familyReader, ok := reader.(domain.BrokerFamilyDiscoveryReaderV3)
				if !ok {
					_, err := brokercontract.ErrorForReason(domain.BrokerReasonUnsupported)
					return err
				}
				projection, err := familyReader.DiscoverFamily(cmd.Context(), service, family)
				if err != nil {
					return err
				}
				body, err := brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
				if err != nil {
					return err
				}
				return emit(cmd, json.RawMessage(body), func() string {
					return brokerFamilyDiscoveryText(projection)
				})
			}
			projection, err := reader.Discover(cmd.Context(), service)
			if err != nil {
				return err
			}
			body, err := brokercontract.EncodeDiscoveryProjectionV2(projection)
			if err != nil {
				return err
			}
			return emit(cmd, json.RawMessage(body), func() string {
				var lines []string
				for _, operation := range projection.Operations {
					lines = append(lines, fmt.Sprintf("%s v%d: %s", operation.ID, operation.Version, operation.Access))
				}
				return strings.Join(lines, "\n")
			})
		},
	}
	command.Flags().StringVar(&service, "service", "", "Broker service: jira or confluence (required)")
	command.Flags().StringVar(&family, "family", "", "contract family (optional; atl.broker.execution.v2 requires --service jira)")
	return command
}

func brokerFamilyDiscoveryText(projection domain.BrokerFamilyDiscoveryProjectionV3) string {
	lines := make([]string, len(projection.Operations))
	for index, operation := range projection.Operations {
		lines[index] = fmt.Sprintf("%s v%d: %s", operation.ID, operation.Version, operation.Access)
	}
	return strings.Join(lines, "\n")
}
