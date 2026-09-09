package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/compose"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

func jiraCommentServiceForInvocation(cmd *cobra.Command, operationTicket string, ticketChanged, apply bool) (*app.JiraService, bool, error) {
	cfg, authorizer, err := jiraCompositionInputs(cmd)
	if err != nil {
		return nil, false, err
	}
	brokerMode := config.EffectiveConnectionMode(cfg.ConnectionMode) == config.ConnectionModeBroker
	if brokerMode {
		if apply && strings.TrimSpace(operationTicket) == "" {
			return nil, true, usageErr("--operation-ticket is required with --apply in Broker mode; run the Broker preview first")
		}
	} else if ticketChanged {
		return nil, false, usageErr("--operation-ticket is supported only in Broker mode")
	}
	service, err := compose.NewJiraWithWriteAuthorizer(cfg, version.Version, authorizer, invocationCompositionOptions(cmd)...)
	return service, brokerMode, err
}

func jiraCommentOutcomeCmd() *cobra.Command {
	var operationTicket string
	cmd := &cobra.Command{
		Use:   "outcome",
		Short: "Observe one durable Broker comment-operation outcome",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !validCLICommentOperationTicket(operationTicket) {
				return usageErr("--operation-ticket must be one non-empty printable Broker identifier")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if config.EffectiveConnectionMode(cfg.ConnectionMode) != config.ConnectionModeBroker {
				return usageErr("Jira comment outcome observation requires Broker mode")
			}
			service, err := compose.NewBrokerJiraObservationService(cfg, version.Version)
			if err != nil {
				return err
			}
			result, err := service.ObserveBrokerCommentOperation(cmd.Context(), operationTicket)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.JiraBrokerOperationOutcomeText(result) })
		},
	}
	cmd.Flags().StringVar(&operationTicket, "operation-ticket", "", "opaque Broker operation ticket returned by comment preview")
	return cmd
}

func validCLICommentOperationTicket(value string) bool {
	if value == "" || len(value) > domain.BrokerMaxIdentifierBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

type brokerCommentOutputAmbiguity struct{ cause error }

func (*brokerCommentOutputAmbiguity) Error() string {
	return "Broker Jira comment apply result could not be published; do not retry apply; run `atl jira issue comment outcome --operation-ticket <SAME-TICKET>` with the original operation ticket"
}
func (e *brokerCommentOutputAmbiguity) Unwrap() []error {
	return []error{domain.ErrCheckFailed, e.cause}
}
func (*brokerCommentOutputAmbiguity) DiagnosticAmbiguousWrite() bool { return true }

func brokerCommentResultErr(mutationErr, emitErr error, attempted bool) error {
	if emitErr == nil {
		return mutationErr
	}
	emitCause := fmt.Errorf("write Broker Jira comment result: %w", emitErr)
	if !attempted {
		return errors.Join(mutationErr, emitCause)
	}
	return &brokerCommentOutputAmbiguity{cause: errors.Join(mutationErr, emitCause)}
}
