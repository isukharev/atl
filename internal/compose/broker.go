package compose

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"sync"

	"github.com/isukharev/atl/internal/adapter/brokerauthority"
	confluenceadapter "github.com/isukharev/atl/internal/adapter/confluence"
	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokerconfig"
	"github.com/isukharev/atl/internal/brokerserver"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const (
	brokerOutboundMaxInFlight = 4
	brokerOutboundRate        = 8
)

type idleConnectionCloser interface{ CloseIdleConnections() }

type BrokerRuntime struct {
	host     *brokerserver.Host
	material *brokerconfig.Material
	guard    *brokerserver.CredentialGuard
	closers  []idleConnectionCloser
	close    sync.Once
}

func LoadBrokerRuntime(configPath, version string, auditWriter io.Writer) (*BrokerRuntime, error) {
	material, err := brokerconfig.Load(configPath)
	if err != nil {
		return nil, err
	}
	runtime, err := newBrokerRuntime(material, version, auditWriter)
	if err != nil {
		material.Close()
		return nil, err
	}
	return runtime, nil
}

func (r *BrokerRuntime) Run(ctx context.Context) error {
	if r == nil || r.host == nil {
		return fmt.Errorf("%w: Broker runtime is unavailable", domain.ErrConfig)
	}
	return r.host.Run(ctx)
}

func (r *BrokerRuntime) Close() {
	if r == nil {
		return
	}
	r.close.Do(func() {
		host := r.host
		if host != nil {
			host.Close()
		}
		for _, closer := range r.closers {
			closer.CloseIdleConnections()
		}
		r.closers = nil
		if host == nil || host.Drained() {
			r.clearSecrets()
			return
		}
		go func() {
			<-host.Done()
			r.clearSecrets()
		}()
	})
}

func (r *BrokerRuntime) clearSecrets() {
	if r.guard != nil {
		r.guard.Close()
	}
	if r.material != nil {
		r.material.Close()
	}
	r.host = nil
	r.guard = nil
	r.material = nil
}

func newBrokerRuntime(material *brokerconfig.Material, version string, auditWriter io.Writer) (*BrokerRuntime, error) {
	if material == nil || auditWriter == nil {
		return nil, fmt.Errorf("%w: invalid Broker composition", domain.ErrConfig)
	}
	certificate, err := tls.X509KeyPair(material.ServerCertificate, material.ServerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Broker TLS identity", domain.ErrConfig)
	}
	scheduler, err := httpx.NewScheduler(brokerOutboundMaxInFlight, brokerOutboundRate)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Broker request schedule", domain.ErrConfig)
	}
	authorityTLS, _, err := httpx.QualifiedTLSOptionsBytes(material.AuthorityCA)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Broker authority trust", domain.ErrConfig)
	}
	authority, err := brokerauthority.New(brokerauthority.Config{
		BaseURL: material.Config.Authority.BaseURL, ServerCredential: string(material.AuthorityCredential), IssuerSHA256: material.Config.Authority.IssuerSHA256,
		Version: version, Scheduler: scheduler, TLS: authorityTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Broker authority", domain.ErrConfig)
	}

	var jiraReader app.BrokerJiraIssueReader
	var confluenceReader app.BrokerConfluencePageReader
	guardCredentials := [][]byte{material.AuthorityCredential}
	closers := []idleConnectionCloser{authority}
	if selected := material.Config.Jira; selected != nil {
		tlsOptions, _, tlsErr := httpx.QualifiedTLSOptionsBytes(material.JiraCA)
		if tlsErr != nil {
			return nil, fmt.Errorf("%w: invalid Broker Jira trust", domain.ErrConfig)
		}
		reader, readerErr := jiraadapter.NewWithSchedulerTLS(selected.BaseURL, string(material.JiraCredential), version, scheduler, tlsOptions)
		if readerErr != nil {
			return nil, fmt.Errorf("%w: invalid Broker Jira adapter", domain.ErrConfig)
		}
		origin, originErr := reader.BrokerOriginSHA256()
		if originErr != nil {
			return nil, fmt.Errorf("%w: invalid Broker Jira origin", domain.ErrConfig)
		}
		jiraReader = app.BrokerJiraIssueReader{Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: origin, WorkloadBackendID: selected.WorkloadBackendID}, Reader: reader}
		closers = append(closers, reader)
		guardCredentials = append(guardCredentials, material.JiraCredential)
	}
	if selected := material.Config.Confluence; selected != nil {
		tlsOptions, _, tlsErr := httpx.QualifiedTLSOptionsBytes(material.ConfluenceCA)
		if tlsErr != nil {
			return nil, fmt.Errorf("%w: invalid Broker Confluence trust", domain.ErrConfig)
		}
		reader, readerErr := confluenceadapter.NewWithSchedulerTLS(selected.BaseURL, string(material.ConfluenceCredential), version, scheduler, tlsOptions)
		if readerErr != nil {
			return nil, fmt.Errorf("%w: invalid Broker Confluence adapter", domain.ErrConfig)
		}
		origin, originErr := reader.BrokerOriginSHA256()
		if originErr != nil {
			return nil, fmt.Errorf("%w: invalid Broker Confluence origin", domain.ErrConfig)
		}
		confluenceReader = app.BrokerConfluencePageReader{Backend: domain.BrokerBackendBinding{Service: "confluence", OriginSHA256: origin, WorkloadBackendID: selected.WorkloadBackendID}, Reader: reader}
		closers = append(closers, reader)
		guardCredentials = append(guardCredentials, material.ConfluenceCredential)
	}
	reads, err := app.NewBrokerReadService(authority, jiraReader, confluenceReader)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Broker read service", domain.ErrConfig)
	}
	guard, err := brokerserver.NewCredentialGuard(guardCredentials...)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Broker credential guard", domain.ErrConfig)
	}
	data, err := brokerserver.New(brokerserver.Config{Audience: material.Config.DataAudience, BrokerID: material.Config.BrokerID, MaxConcurrent: 1}, brokerserver.Dependencies{Authenticator: authority, Reads: reads, Guard: guard})
	if err != nil {
		guard.Close()
		return nil, fmt.Errorf("%w: invalid Broker data handler", domain.ErrConfig)
	}
	host, err := brokerserver.NewHost(brokerserver.HostConfig{
		DataAddress: material.Config.DataListen, AdminAddress: material.Config.AdminListen, AdminAudience: material.Config.AdminAudience,
		BrokerID: material.Config.BrokerID, Certificate: certificate,
	}, brokerserver.HostDependencies{Data: data, Authenticator: authority, Guard: guard, AuditWriter: auditWriter})
	if err != nil {
		guard.Close()
		return nil, fmt.Errorf("%w: invalid Broker host", domain.ErrConfig)
	}
	return &BrokerRuntime{host: host, material: material, guard: guard, closers: closers}, nil
}
