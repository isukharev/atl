package compose

import (
	"fmt"

	"github.com/isukharev/atl/internal/adapter/brokerjournal"
	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/brokerconfig"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/domain"
)

// InitializeBrokerJournal is a local operator action. It never loads runtime
// credentials or policy, opens an existing journal, or contacts a backend.
func InitializeBrokerJournal(configPath string) error {
	cfg, err := brokerconfig.LoadJournalConfiguration(configPath)
	if err != nil {
		return err
	}
	identity, limits, err := brokerJournalParameters(cfg)
	if err != nil {
		return err
	}
	journal, err := brokerjournal.Create(cfg.Directory, identity, limits)
	if err != nil {
		return err
	}
	return journal.Close()
}

func brokerJournalParameters(cfg *brokerconfig.JournalConfiguration) (brokerjournal.Identity, brokerjournal.Limits, error) {
	if cfg == nil || cfg.Backend.Service != "jira" {
		return brokerjournal.Identity{}, brokerjournal.Limits{}, fmt.Errorf("%w: invalid Broker journal configuration", domain.ErrConfig)
	}
	broker, brokerErr := brokercontract.BrokerJournalBrokerSHA256V1(cfg.BrokerID)
	backend, backendErr := brokercontract.BrokerJournalBackendSHA256V1(cfg.Backend)
	if brokerErr != nil || backendErr != nil {
		return brokerjournal.Identity{}, brokerjournal.Limits{}, fmt.Errorf("%w: invalid Broker journal identity", domain.ErrConfig)
	}
	return brokerjournal.Identity{BrokerSHA256: broker, BackendSHA256: backend}, brokerjournal.Limits{Records: cfg.Records, ReservedBytes: cfg.ReservedBytes}, nil
}

// Parse only the already pinned bytes. An empty config directory deliberately
// excludes ordinary client policy files and ambient environment layering.
func brokerCommentPolicy(material *brokerconfig.Material) (*contentpolicy.Authorizer, error) {
	if material.Config.JiraComment == nil {
		if material.Journal != nil || len(material.JiraCommentPolicy) != 0 {
			return nil, fmt.Errorf("%w: invalid Broker comment policy", domain.ErrConfig)
		}
		return nil, nil
	}
	if material.Config.Jira == nil || material.Journal == nil || len(material.JiraCommentPolicy) == 0 {
		return nil, fmt.Errorf("%w: missing Broker comment policy", domain.ErrConfig)
	}
	resolved, err := contentpolicy.Load("", contentpolicy.Environment{Inline: string(material.JiraCommentPolicy)})
	if err != nil || len(resolved.Layers) != 1 {
		return nil, fmt.Errorf("%w: invalid Broker comment policy", domain.ErrConfig)
	}
	origin, err := backendid.OriginSHA256(material.Config.Jira.BaseURL)
	if err != nil || resolved.Layers[0].Policy.Backend.JiraSHA256 != origin {
		return nil, fmt.Errorf("%w: Broker comment policy backend mismatch", domain.ErrConfig)
	}
	return contentpolicy.NewAuthorizer(resolved), nil
}

func openBrokerCommentJournal(cfg *brokerconfig.JournalConfiguration, backend domain.BrokerBackendBinding) (*brokerjournal.Journal, error) {
	if cfg == nil || cfg.Backend != backend {
		return nil, fmt.Errorf("%w: Broker journal backend mismatch", domain.ErrConfig)
	}
	identity, limits, err := brokerJournalParameters(cfg)
	if err != nil {
		return nil, err
	}
	storage, err := brokerjournal.Open(cfg.Directory, identity, limits)
	if err != nil {
		return nil, fmt.Errorf("%w: Broker journal unavailable", domain.ErrConfig)
	}
	return storage, nil
}
