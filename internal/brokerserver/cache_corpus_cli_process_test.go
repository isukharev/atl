package brokerserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/adapter/brokerauthority"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const selectedCacheCLIOutputLimit = 1 << 20

type selectedCacheCLIOutput struct {
	bytes.Buffer
	exceeded bool
}

func (output *selectedCacheCLIOutput) Write(data []byte) (int, error) {
	written := len(data)
	remaining := selectedCacheCLIOutputLimit - output.Len()
	if remaining < len(data) {
		output.exceeded = true
		if remaining > 0 {
			_, _ = output.Buffer.Write(data[:remaining])
		}
		return written, nil
	}
	_, _ = output.Buffer.Write(data)
	return written, nil
}

type selectedCacheCLIResult struct {
	stdout   string
	stderr   string
	exitCode int
}

type selectedCacheCLICounters struct {
	authentication atomic.Int32
	qualification  atomic.Int32
	brokerBackend  atomic.Int32
	directBackend  atomic.Int32
}

type selectedCacheCLIFile struct {
	mode fs.FileMode
	body []byte
}

func TestSelectedATLBinaryQualifiesExactCorpusWithoutPATOrDirectFallback(t *testing.T) {
	const (
		generatorVersion = "test-v1"
		generatorCommit  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		workloadSecret   = "synthetic-target-a-credential"
		patCanary        = "PRIVATE-ORDINARY-PAT-CANARY"
		remoteCanary     = "PRIVATE-AUTHORITY-DIAGNOSTIC"
	)
	storeA, candidateA, captureA, sourceA := buildCacheCorpusFixture(t, "source-a", "alpha")
	storeB, candidateB, captureB, sourceB := buildCacheCorpusFixture(t, "source-b", "beta")
	storeUnknown, _, _, sourceUnknown := buildCacheCorpusFixture(t, "source-unknown", "unknown")
	before := map[string]map[string]selectedCacheCLIFile{
		storeA:       selectedCacheCLITree(t, storeA),
		storeB:       selectedCacheCLITree(t, storeB),
		storeUnknown: selectedCacheCLITree(t, storeUnknown),
	}
	sourceCalls := map[*cacheCorpusFixture]int32{
		sourceA: sourceA.calls.Load(), sourceB: sourceB.calls.Load(), sourceUnknown: sourceUnknown.calls.Load(),
	}
	stamp := strings.Join([]string{
		"-s", "-w",
		"-X", "github.com/isukharev/atl/internal/version.Version=" + generatorVersion,
		"-X", "github.com/isukharev/atl/internal/version.Commit=" + generatorCommit,
		"-X", "github.com/isukharev/atl/internal/version.BuildState=clean",
	}, " ")
	binary := buildSelectedATLBinary(t, stamp)

	now := time.Now().UTC().Truncate(time.Millisecond)
	issuer := strings.Repeat("a", 64)
	origin := strings.Repeat("b", 64)
	target := cacheCorpusTargetContext(now, "target-a", "execution-a", "epoch-a", origin)
	trusted := map[domain.BrokerCacheCandidate]trustedCacheCapture{
		candidateA: {sourcePrincipal: cacheCorpusDigest("source-a"), sourceScope: captureA.ScopeDigest},
		candidateB: {sourcePrincipal: cacheCorpusDigest("source-b"), sourceScope: captureB.ScopeDigest},
	}
	counters := &selectedCacheCLICounters{}
	broker := selectedCacheCLIBroker(t, target, captureA.ScopeDigest, issuer, workloadSecret, remoteCanary, trusted, counters)

	direct := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		counters.directBackend.Add(1)
	}))
	t.Cleanup(direct.Close)
	configRoot := t.TempDir()
	if err := os.Chmod(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialStore := filepath.Join(configRoot, "credentials.json")
	if err := os.Mkdir(credentialStore, 0o700); err != nil {
		t.Fatal(err)
	}
	// A direct credential-store read would fail on this directory. Successful
	// Broker qualification therefore cannot depend on ordinary credential loading.
	writeSelectedCacheCLIFile(t, credentialStore, "canary", []byte(patCanary))
	writeSelectedCacheCLIFile(t, configRoot, "broker.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: broker.Certificate().Raw}))
	session, _ := json.Marshal(map[string]any{
		"schema_version": 1, "credential": workloadSecret,
		"execution_id": target.ExecutionID, "execution_epoch": target.ExecutionEpoch,
		"authority_revision": target.AuthorityRevision,
	})
	writeSelectedCacheCLIFile(t, configRoot, "session.json", session)
	config, _ := json.Marshal(map[string]any{
		"connection_mode": "broker", "confluence_url": direct.URL, "jira_list_views": map[string]any{},
		"broker": map[string]any{
			"base_url": broker.URL, "broker_id": target.BrokerID, "audience": target.Audience,
			"ca_file":                 filepath.Join(configRoot, "broker.ca"),
			"confluence_session_file": filepath.Join(configRoot, "session.json"),
		},
	})
	writeSelectedCacheCLIFile(t, configRoot, "config.json", config)

	environment := []string{"PATH=" + os.Getenv("PATH"), "ATL_CONFIG_DIR=" + configRoot, "ATL_NO_UPDATE=1", "ATL_READ_ONLY=1"}

	positive := runSelectedCacheCLI(t, binary, environment, storeA)
	var success app.QualifiedCorpusHandoffResult
	if positive.exitCode != 0 || positive.stderr != "" || json.Unmarshal([]byte(positive.stdout), &success) != nil ||
		success.Qualification != "current_allowed" || success.Generation.GenerationDigest != candidateA.GenerationSHA256 || success.HandoffArtifactWritten {
		t.Fatalf("positive exit=%d stdout=%s stderr=%s", positive.exitCode, positive.stdout, positive.stderr)
	}

	denied := runSelectedCacheCLI(t, binary, environment, storeB)
	assertSelectedCacheCLIError(t, denied, 6, "forbidden")
	unknown := runSelectedCacheCLI(t, binary, environment, storeUnknown)
	assertSelectedCacheCLIError(t, unknown, 8, "check_failed")

	if counters.authentication.Load() != 3 || counters.qualification.Load() != 3 || counters.brokerBackend.Load() != 0 || counters.directBackend.Load() != 0 {
		t.Fatalf("auth=%d qualification=%d broker_backend=%d direct_backend=%d", counters.authentication.Load(), counters.qualification.Load(), counters.brokerBackend.Load(), counters.directBackend.Load())
	}
	for fixture, calls := range sourceCalls {
		if fixture.calls.Load() != calls {
			t.Fatalf("source backend calls changed from %d to %d", calls, fixture.calls.Load())
		}
	}
	for root, want := range before {
		if got := selectedCacheCLITree(t, root); !reflect.DeepEqual(got, want) {
			t.Fatalf("sealed store changed after selected CLI handoff")
		}
	}
	allOutput := positive.stdout + positive.stderr + denied.stdout + denied.stderr + unknown.stdout + unknown.stderr
	for _, private := range []string{patCanary, remoteCanary, workloadSecret, configRoot, storeA, storeB, storeUnknown, broker.URL, direct.URL} {
		if strings.Contains(allOutput, private) {
			t.Fatalf("selected CLI output exposed private fixture data")
		}
	}
}

func selectedCacheCLIBroker(t *testing.T, target domain.BrokerVerifiedContext, allowedSourceScope, issuer, workloadSecret, remoteCanary string, trusted map[domain.BrokerCacheCandidate]trustedCacheCapture, counters *selectedCacheCLICounters) *httptest.Server {
	t.Helper()
	expectedCredential := cacheCorpusCredentialHash(t, workloadSecret)
	authorityServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer synthetic-server-credential" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		body, _ := io.ReadAll(request.Body)
		switch request.URL.Path {
		case "/v1/authenticate":
			counters.authentication.Add(1)
			value, err := brokertransport.DecodeAuthenticationRequestV1(body)
			if err != nil || value.CredentialSHA256 != expectedCredential {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			responseNow := time.Now()
			encoded, _ := brokertransport.EncodeAuthenticationResponseV1(brokertransport.AuthenticationResponse{
				SchemaVersion: 1, Nonce: value.Nonce, CredentialSHA256: value.CredentialSHA256, IssuerSHA256: issuer,
				IssuedAtMillis: responseNow.UnixMilli(), ExpiresAtMillis: responseNow.Add(4 * time.Second).UnixMilli(), Context: target,
			})
			_, _ = writer.Write(encoded)
		case "/v2/authorize/cache":
			counters.qualification.Add(1)
			candidate, err := brokercontract.DecodeCacheQualificationCandidateV2(body)
			record, found := trusted[candidate.Candidate]
			if err != nil || !found {
				writer.WriteHeader(http.StatusNotFound)
				_, _ = writer.Write([]byte(remoteCanary))
				return
			}
			status, reason := domain.BrokerDecisionDenied, domain.BrokerReasonDenied
			if candidate.Context.PrincipalID == target.PrincipalID && record.sourceScope == allowedSourceScope {
				status, reason = domain.BrokerDecisionAllowed, ""
			}
			requestValue := domain.BrokerCacheQualificationRequest{
				SchemaVersion: 1, Context: candidate.Context, SourcePrincipalSHA256: record.sourcePrincipal, SourceReadScopeSHA256: record.sourceScope,
				Operation: candidate.Candidate.Operation, SelectorSHA256: candidate.Candidate.SelectorSHA256,
				ProjectionSHA256: candidate.Candidate.ProjectionSHA256, EvidenceSchemaSHA256: candidate.Candidate.EvidenceSchemaSHA256,
				GenerationSHA256: candidate.Candidate.GenerationSHA256, ContentSHA256: candidate.Candidate.ContentSHA256, ExpiresAtMillis: candidate.NotAfterMillis,
			}
			requestDigest, _ := brokercontract.CacheQualificationRequestSHA256(requestValue)
			targetDigest, _ := brokercontract.CacheTargetExecutionSHA256V1(candidate.Context)
			revision, _ := brokercontract.CacheAuthorityRevisionSHA256V1(candidate.Context.AuthorityRevision)
			candidateDigest, _ := brokercontract.CacheQualificationCandidateSHA256V2(candidate)
			decisionNow := time.Now()
			resolved := domain.BrokerResolvedCacheQualificationV2{
				SchemaVersion: 2, CandidateSHA256: candidateDigest, Request: requestValue, Complete: true,
				Decision: domain.BrokerCacheQualification{
					SchemaVersion: 1, Status: status, Reason: reason, IssuerSHA256: issuer,
					TargetExecutionSHA256: targetDigest, AuthorityRevisionSHA256: revision, RequestSHA256: requestDigest,
					IssuedAtMillis: decisionNow.UnixMilli(), ExpiresAtMillis: min(candidate.NotAfterMillis, decisionNow.Add(3*time.Second).UnixMilli()),
				},
			}
			encoded, _ := brokercontract.EncodeResolvedCacheQualificationV2(resolved)
			_, _ = writer.Write(encoded)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(authorityServer.Close)
	authorityTLS, _, err := httpx.QualifiedTLSOptionsBytes(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: authorityServer.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := brokerauthority.New(brokerauthority.Config{BaseURL: authorityServer.URL, ServerCredential: "synthetic-server-credential", IssuerSHA256: issuer, Version: "test", TLS: authorityTLS})
	if err != nil {
		t.Fatal(err)
	}
	reads, err := app.NewBrokerReadService(authority, app.BrokerJiraIssueReader{}, app.BrokerConfluencePageReader{Backend: target.Backend, Reader: cacheNoBackendReader{origin: target.Backend.OriginSHA256, calls: &counters.brokerBackend}})
	if err != nil {
		t.Fatal(err)
	}
	cacheService, err := app.NewBrokerCacheQualificationService(authority, issuer)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewCredentialGuard([]byte("synthetic-server-credential"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	handler, err := New(Config{Audience: target.Audience, BrokerID: target.BrokerID, MaxConcurrent: 1}, Dependencies{Authenticator: authority, Reads: reads, Cache: cacheService, Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func buildSelectedATLBinary(t *testing.T, ldflags string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "atl")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	arguments := []string{"build", "-trimpath", "-buildvcs=false", "-o", binary}
	if ldflags != "" {
		arguments = append(arguments, "-ldflags", ldflags)
	}
	arguments = append(arguments, "../../cmd/atl")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", arguments...)
	build.WaitDelay = 2 * time.Second
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOROOT=") && !strings.HasPrefix(entry, "GOTOOLCHAIN=") && !strings.HasPrefix(entry, "GOWORK=") {
			build.Env = append(build.Env, entry)
		}
	}
	build.Env = append(build.Env, "GOTOOLCHAIN=auto", "GOWORK=off")
	var stdout, stderr selectedCacheCLIOutput
	build.Stdout, build.Stderr = &stdout, &stderr
	err := build.Run()
	if err != nil || ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
		t.Fatalf("build selected binary: %v: stdout=%s stderr=%s", errors.Join(err, ctx.Err()), stdout.String(), stderr.String())
	}
	return binary
}

func runSelectedCacheCLI(t *testing.T, binary string, environment []string, store string) selectedCacheCLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "corpus", "handoff-qualified", "--store", store)
	command.Env = environment
	var stdout, stderr selectedCacheCLIOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
		t.Fatalf("selected CLI exceeded deadline or output bound: context=%v stdout=%t stderr=%t", ctx.Err(), stdout.exceeded, stderr.exceeded)
	}
	code := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("selected CLI process: %v", err)
		}
		code = exitError.ExitCode()
	}
	return selectedCacheCLIResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: code}
}

func assertSelectedCacheCLIError(t *testing.T, result selectedCacheCLIResult, code int, kind string) {
	t.Helper()
	var envelope struct {
		Code int    `json:"code"`
		Kind string `json:"kind"`
	}
	if result.exitCode != code || result.stdout != "" || json.Unmarshal([]byte(result.stderr), &envelope) != nil || envelope.Code != code || envelope.Kind != kind {
		t.Fatalf("failure exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
}

func writeSelectedCacheCLIFile(t *testing.T, directory, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func selectedCacheCLITree(t *testing.T, root string) map[string]selectedCacheCLIFile {
	t.Helper()
	result := map[string]selectedCacheCLIFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected non-regular store member")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		result[filepath.ToSlash(relative)] = selectedCacheCLIFile{mode: info.Mode(), body: digest[:]}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
