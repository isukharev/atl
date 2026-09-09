// Package brokerconfig owns the explicit owner-private configuration for the
// local Broker host. It is independent from ATL's ordinary client config and
// credential store.
package brokerconfig

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	SchemaVersion          = 1
	MaxConfigBytes         = int64(64 << 10)
	MaxCredentialBytes     = int64(8 << 10)
	MaxCABundleBytes       = int64(4 << 20)
	MaxCertificateBytes    = int64(256 << 10)
	MaxPrivateKeyBytes     = int64(64 << 10)
	MaxReferenceNameBytes  = 255
	MaxListenAddressBytes  = 64
	MaxBackendIdentitySize = 256
)

type TLSFiles struct {
	CertificateFile string `json:"certificate_file"`
	PrivateKeyFile  string `json:"private_key_file"`
}

type Authority struct {
	BaseURL        string `json:"base_url"`
	IssuerSHA256   string `json:"issuer_sha256"`
	CredentialFile string `json:"credential_file"`
	CAFile         string `json:"ca_file"`
}

type Backend struct {
	BaseURL           string `json:"base_url"`
	WorkloadBackendID string `json:"workload_backend_id"`
	CredentialFile    string `json:"credential_file"`
	CAFile            string `json:"ca_file"`
}

type Config struct {
	SchemaVersion int          `json:"schema_version"`
	BrokerID      string       `json:"broker_id"`
	DataAudience  string       `json:"data_audience"`
	AdminAudience string       `json:"admin_audience"`
	DataListen    string       `json:"data_listen"`
	AdminListen   string       `json:"admin_listen"`
	TLS           TLSFiles     `json:"tls"`
	Authority     Authority    `json:"authority"`
	Jira          *Backend     `json:"jira,omitempty"`
	Confluence    *Backend     `json:"confluence,omitempty"`
	JiraComment   *JiraComment `json:"jira_comment,omitempty"`
}

type Material struct {
	Config               Config
	AuthorityCredential  []byte
	AuthorityCA          []byte
	JiraCredential       []byte
	JiraCA               []byte
	ConfluenceCredential []byte
	ConfluenceCA         []byte
	ServerCertificate    []byte
	ServerPrivateKey     []byte
	JiraCommentPolicy    []byte
	Journal              *JournalConfiguration
}

func Load(configPath string) (*Material, error) {
	return LoadWithJiraComments(configPath, false)
}

// LoadWithJiraComments requires the operator opt-in and configuration block
// together. Existing read-only callers cannot silently accept mutation config.
func LoadWithJiraComments(configPath string, enable bool) (*Material, error) {
	if ambientProxyConfigured() {
		return nil, configError()
	}
	cfg, err := loadConfiguration(configPath)
	if err != nil || enable != (cfg.JiraComment != nil) {
		return nil, configError()
	}
	parent := filepath.Dir(configPath)
	material := &Material{Config: cfg}
	defer func() {
		if err != nil {
			material.Close()
		}
	}()
	if cfg.JiraComment != nil {
		material.Journal, err = journalConfiguration(configPath, cfg)
		if err != nil {
			return nil, configError()
		}
		material.JiraCommentPolicy, err = readCommentPolicy(parent, *cfg.JiraComment)
		if err != nil {
			return nil, configError()
		}
	}
	if material.ServerCertificate, err = readReference(parent, cfg.TLS.CertificateFile, MaxCertificateBytes); err != nil {
		return nil, configError()
	}
	if material.ServerPrivateKey, err = readReference(parent, cfg.TLS.PrivateKeyFile, MaxPrivateKeyBytes); err != nil {
		return nil, configError()
	}
	if material.AuthorityCredential, err = readCredential(parent, cfg.Authority.CredentialFile); err != nil {
		return nil, configError()
	}
	if material.AuthorityCA, err = readReference(parent, cfg.Authority.CAFile, MaxCABundleBytes); err != nil {
		return nil, configError()
	}
	if cfg.Jira != nil {
		if material.JiraCredential, err = readCredential(parent, cfg.Jira.CredentialFile); err != nil {
			return nil, configError()
		}
		if material.JiraCA, err = readReference(parent, cfg.Jira.CAFile, MaxCABundleBytes); err != nil {
			return nil, configError()
		}
	}
	if cfg.Confluence != nil {
		if material.ConfluenceCredential, err = readCredential(parent, cfg.Confluence.CredentialFile); err != nil {
			return nil, configError()
		}
		if material.ConfluenceCA, err = readReference(parent, cfg.Confluence.CAFile, MaxCABundleBytes); err != nil {
			return nil, configError()
		}
	}
	return material, nil
}

// loadConfiguration reads only the explicit config; referenced credentials,
// policies and TLS material are separate runtime concerns.
func loadConfiguration(configPath string) (Config, error) {
	if !directPrivateParent(configPath) {
		return Config{}, configError()
	}
	body, err := safepath.ReadFilePrivate(configPath, MaxConfigBytes)
	if err != nil {
		return Config{}, configError()
	}
	defer clear(body)
	var cfg Config
	if strictjson.DecodeExact(body, brokercontract.MaxCanonicalDepth, &cfg) != nil || validate(cfg) != nil {
		return Config{}, configError()
	}
	for _, reference := range configReferences(cfg) {
		if reference == filepath.Base(configPath) {
			return Config{}, configError()
		}
	}
	return cfg, nil
}

func ambientProxyConfigured() bool {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func directPrivateParent(target string) bool {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	parent := filepath.Dir(absolute)
	info, err := os.Lstat(parent)
	return err == nil && info.IsDir()
}

func (m *Material) Close() {
	if m == nil {
		return
	}
	for _, value := range [][]byte{m.AuthorityCredential, m.AuthorityCA, m.JiraCredential, m.JiraCA, m.ConfluenceCredential, m.ConfluenceCA, m.ServerCertificate, m.ServerPrivateKey, m.JiraCommentPolicy} {
		clear(value)
	}
	m.AuthorityCredential = nil
	m.AuthorityCA = nil
	m.JiraCredential = nil
	m.JiraCA = nil
	m.ConfluenceCredential = nil
	m.ConfluenceCA = nil
	m.ServerCertificate = nil
	m.ServerPrivateKey = nil
	m.JiraCommentPolicy = nil
	m.Journal = nil
}

func validate(cfg Config) error {
	if cfg.SchemaVersion != SchemaVersion || cfg.DataAudience == cfg.AdminAudience ||
		validateIdentity(cfg.BrokerID) != nil || validateIdentity(cfg.DataAudience) != nil || validateIdentity(cfg.AdminAudience) != nil ||
		validateListen(cfg.DataListen) != nil || validateListen(cfg.AdminListen) != nil || cfg.DataListen == cfg.AdminListen ||
		validateReference(cfg.TLS.CertificateFile) != nil || validateReference(cfg.TLS.PrivateKeyFile) != nil ||
		validateAuthority(cfg.Authority) != nil || cfg.Jira == nil && cfg.Confluence == nil {
		return configError()
	}
	if cfg.Jira != nil && validateBackend(*cfg.Jira) != nil {
		return configError()
	}
	if cfg.Confluence != nil && validateBackend(*cfg.Confluence) != nil {
		return configError()
	}
	if cfg.JiraComment != nil && (cfg.Jira == nil || validateJiraComment(*cfg.JiraComment) != nil) {
		return configError()
	}
	seenReferences := map[string]bool{}
	for _, reference := range configReferences(cfg) {
		if seenReferences[reference] {
			return configError()
		}
		seenReferences[reference] = true
	}
	seenOrigins := map[string]bool{}
	for _, raw := range configOrigins(cfg) {
		digest, err := backendid.OriginSHA256(raw)
		if err != nil || seenOrigins[digest] {
			return configError()
		}
		seenOrigins[digest] = true
	}
	return nil
}

func configReferences(cfg Config) []string {
	values := []string{cfg.TLS.CertificateFile, cfg.TLS.PrivateKeyFile, cfg.Authority.CredentialFile, cfg.Authority.CAFile}
	if cfg.Jira != nil {
		values = append(values, cfg.Jira.CredentialFile, cfg.Jira.CAFile)
	}
	if cfg.Confluence != nil {
		values = append(values, cfg.Confluence.CredentialFile, cfg.Confluence.CAFile)
	}
	if cfg.JiraComment != nil {
		values = append(values, cfg.JiraComment.JournalDirectory, cfg.JiraComment.LocalPolicyFile)
	}
	return values
}

func configOrigins(cfg Config) []string {
	values := []string{cfg.Authority.BaseURL}
	if cfg.Jira != nil {
		values = append(values, cfg.Jira.BaseURL)
	}
	if cfg.Confluence != nil {
		values = append(values, cfg.Confluence.BaseURL)
	}
	return values
}

func validateAuthority(value Authority) error {
	if validateHTTPSOrigin(value.BaseURL) != nil || !validDigest(value.IssuerSHA256) || validateReference(value.CredentialFile) != nil || validateReference(value.CAFile) != nil {
		return configError()
	}
	return nil
}

func validateBackend(value Backend) error {
	if validateHTTPSOrigin(value.BaseURL) != nil || validateIdentity(value.WorkloadBackendID) != nil || validateReference(value.CredentialFile) != nil || validateReference(value.CAFile) != nil {
		return configError()
	}
	return nil
}

func validateHTTPSOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	_, originErr := backendid.OriginSHA256(raw)
	if err != nil || originErr != nil || parsed.Scheme != "https" || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.TrimSpace(raw) != raw {
		return configError()
	}
	return nil
}

func validateListen(value string) error {
	if value == "" || len(value) > MaxListenAddressBytes || strings.TrimSpace(value) != value {
		return configError()
	}
	host, port, err := net.SplitHostPort(value)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || port == "" || port == "0" {
		return configError()
	}
	if parsedPort, parseErr := strconv.Atoi(port); parseErr != nil || parsedPort < 1 || parsedPort > 65535 || strconv.Itoa(parsedPort) != port {
		return configError()
	}
	return nil
}

func validateIdentity(value string) error {
	challenge := brokertransport.AuthenticationChallenge{Nonce: strings.Repeat("A", 32), Audience: value, BrokerID: value}
	if brokertransport.ValidateAuthenticationChallenge(challenge) != nil || len(value) > MaxBackendIdentitySize {
		return configError()
	}
	return nil
}

func validateReference(value string) error {
	if value == "" || len(value) > MaxReferenceNameBytes || filepath.Base(value) != value || value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
		return configError()
	}
	return nil
}

func readReference(parent, name string, maximum int64) ([]byte, error) {
	if validateReference(name) != nil {
		return nil, configError()
	}
	return safepath.ReadFilePrivate(filepath.Join(parent, name), maximum)
}

func readCredential(parent, name string) ([]byte, error) {
	value, err := readReference(parent, name, MaxCredentialBytes)
	if err != nil || len(value) < 8 || !utf8.Valid(value) || !bytes.Equal(bytes.TrimSpace(value), value) {
		clear(value)
		return nil, configError()
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			clear(value)
			return nil, configError()
		}
	}
	return value, nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' && current < 'a' || current > 'f' {
			return false
		}
	}
	return true
}

func configError() error {
	return fmt.Errorf("%w: invalid Broker host configuration", domain.ErrConfig)
}
