package brokerclient

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type countingSessionLoader struct {
	calls atomic.Int32
	value Session
}

func (l *countingSessionLoader) Load() (Session, error) {
	l.calls.Add(1)
	value := l.value
	value.Credential = append([]byte(nil), value.Credential...)
	return value, nil
}

func TestClientPerformsNegotiatedSingleAttemptJiraRead(t *testing.T) {
	loader := &countingSessionLoader{value: testSession()}
	var protocolCalls, executeCalls atomic.Int32
	server := newTestBroker(t, "broker-1", func(request domain.BrokerRequest) ([]byte, error) {
		executeCalls.Add(1)
		if request.Expect != testSession().expectations() {
			t.Fatalf("expectations=%+v", request.Expect)
		}
		digest, _ := brokercontract.ArgumentsSHA256(request)
		return brokercontract.EncodeJiraIssueReadResultV1(domain.BrokerJiraIssueReadResult{
			SchemaVersion: 1, ArgumentsSHA256: digest, IssueID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", Updated: "2026-09-08T12:00:00Z",
			Fields: []domain.BrokerJiraIssueReadField{{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "Synthetic summary"}}, Complete: true,
		})
	}, &protocolCalls)
	client := newTestClient(t, server, loader, "broker-1")
	result, err := client.ReadJiraIssue(context.Background(), "EXAMPLE-1", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary})
	if err != nil || result.Key != "EXAMPLE-1" || result.Fields[0].Value != "Synthetic summary" {
		t.Fatalf("result=%+v err=%v protocol=%d execute=%d", result, err, protocolCalls.Load(), executeCalls.Load())
	}
	if protocolCalls.Load() != 1 || executeCalls.Load() != 1 || loader.calls.Load() != 1 {
		t.Fatalf("protocol=%d execute=%d sessions=%d", protocolCalls.Load(), executeCalls.Load(), loader.calls.Load())
	}
}

func TestClientPreservesConfluenceStorageAndRejectsWrongBrokerIdentity(t *testing.T) {
	native := []byte("<p> first\nsecond </p>\n")
	loader := &countingSessionLoader{value: testSession()}
	var protocolCalls, executeCalls atomic.Int32
	server := newTestBroker(t, "broker-1", func(request domain.BrokerRequest) ([]byte, error) {
		executeCalls.Add(1)
		digest, _ := brokercontract.ArgumentsSHA256(request)
		return brokercontract.EncodeConfluencePageReadResultV1(domain.BrokerConfluencePageReadResult{
			SchemaVersion: 1, ArgumentsSHA256: digest, PageID: "42", Type: "page", Space: "DOCS", Version: 3,
			Title: "Synthetic page", Updated: "2026-09-08T12:00:00Z", Projection: domain.BrokerConfluenceProjectionStorage,
			Storage: native, StoragePresent: true, Complete: true,
		})
	}, &protocolCalls)
	client := newTestClient(t, server, loader, "broker-1")
	result, err := client.ReadConfluencePage(context.Background(), "42", domain.BrokerConfluenceProjectionStorage)
	if err != nil || !reflect.DeepEqual(result.Storage, native) {
		t.Fatalf("result=%+v err=%v protocol=%d execute=%d", result, err, protocolCalls.Load(), executeCalls.Load())
	}
	wrong := newTestClient(t, server, loader, "broker-other")
	if _, err := wrong.ReadConfluencePage(context.Background(), "42", domain.BrokerConfluenceProjectionStorage); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("wrong identity err=%v", err)
	}
	if executeCalls.Load() != 1 || protocolCalls.Load() != 2 {
		t.Fatalf("protocol=%d execute=%d", protocolCalls.Load(), executeCalls.Load())
	}
}

func TestClientRejectsUnsupportedArgumentsBeforeSessionOrNetwork(t *testing.T) {
	loader := &countingSessionLoader{value: testSession()}
	client, err := New(Config{BaseURL: "https://127.0.0.1:1", BrokerID: "broker-1", Audience: "atl-broker", Session: loader})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadJiraIssue(context.Background(), "EXAMPLE-1", nil); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("empty fields err=%v", err)
	}
	if loader.calls.Load() != 0 {
		t.Fatalf("session loads=%d", loader.calls.Load())
	}
}

func TestClientDoesNotRetryUnavailableOrMismatchedResults(t *testing.T) {
	for _, test := range []struct {
		name    string
		respond func(http.ResponseWriter, domain.BrokerRequest)
	}{
		{name: "unavailable", respond: func(writer http.ResponseWriter, _ domain.BrokerRequest) {
			failure, _ := brokertransport.NewFailure(domain.BrokerReasonAuthorizationUnavailable)
			body, _ := brokertransport.EncodeFailureV1(failure)
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write(body)
		}},
		{name: "mismatched arguments", respond: func(writer http.ResponseWriter, _ domain.BrokerRequest) {
			body, _ := brokercontract.EncodeJiraIssueReadResultV1(domain.BrokerJiraIssueReadResult{
				SchemaVersion: 1, ArgumentsSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				IssueID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", Updated: "2026-09-08T12:00:00Z",
				Fields: []domain.BrokerJiraIssueReadField{{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "Synthetic summary"}}, Complete: true,
			})
			_, _ = writer.Write(body)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var protocolCalls, executeCalls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
				if request.URL.Path == brokertransport.ProtocolPath {
					protocolCalls.Add(1)
					protocol, _ := brokertransport.ProtocolV1("broker-1", "atl-broker")
					body, _ := brokertransport.EncodeProtocolV1(protocol)
					_, _ = writer.Write(body)
					return
				}
				executeCalls.Add(1)
				body, _ := io.ReadAll(io.LimitReader(request.Body, 64<<10))
				invocation, err := brokercontract.DecodeRequestV1(body)
				if err != nil {
					t.Fatal(err)
				}
				test.respond(writer, invocation)
			}))
			loader := &countingSessionLoader{value: testSession()}
			client := newTestClient(t, server, loader, "broker-1")
			if _, err := client.ReadJiraIssue(t.Context(), "EXAMPLE-1", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary}); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
			if protocolCalls.Load() != 1 || executeCalls.Load() != 1 || loader.calls.Load() != 1 {
				t.Fatalf("protocol=%d execute=%d session=%d", protocolCalls.Load(), executeCalls.Load(), loader.calls.Load())
			}
		})
	}
}

func TestClientBrokerBudgetRemainsChildOfCallerBudget(t *testing.T) {
	loader := &countingSessionLoader{value: testSession()}
	var protocolCalls, executeCalls atomic.Int32
	server := newTestBroker(t, "broker-1", func(domain.BrokerRequest) ([]byte, error) {
		executeCalls.Add(1)
		return nil, errors.New("execute must not be reached")
	}, &protocolCalls)
	client := newTestClient(t, server, loader, "broker-1")
	parent, err := domain.NewReadBudget(1, brokercontract.MaxReadResultWireBytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithReadBudget(t.Context(), parent)
	if _, err := client.ReadJiraIssue(ctx, "EXAMPLE-1", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary}); !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
		t.Fatalf("err=%v", err)
	}
	if protocolCalls.Load() != 1 || executeCalls.Load() != 0 || parent.Usage().Attempts != 1 {
		t.Fatalf("protocol=%d execute=%d parent=%+v", protocolCalls.Load(), executeCalls.Load(), parent.Usage())
	}
}

func TestClientBrokerResponseBudgetRemainsChildOfCallerBudget(t *testing.T) {
	loader := &countingSessionLoader{value: testSession()}
	var protocolCalls, executeCalls atomic.Int32
	server := newTestBroker(t, "broker-1", func(domain.BrokerRequest) ([]byte, error) {
		executeCalls.Add(1)
		return nil, errors.New("execute must not be reached")
	}, &protocolCalls)
	client := newTestClient(t, server, loader, "broker-1")
	parent, err := domain.NewReadBudget(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithReadBudget(t.Context(), parent)
	if _, err := client.ReadJiraIssue(ctx, "EXAMPLE-1", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary}); !errors.Is(err, domain.ErrReadResponseBudgetExhausted) {
		t.Fatalf("err=%v", err)
	}
	if protocolCalls.Load() != 1 || executeCalls.Load() != 0 || parent.Usage().ResponseBytes > 1 {
		t.Fatalf("protocol=%d execute=%d parent=%+v", protocolCalls.Load(), executeCalls.Load(), parent.Usage())
	}
}

func TestFileSessionLoaderRequiresStrictOwnerPrivateSnapshot(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "jira-session.json")
	body := []byte(`{"schema_version":1,"credential":"synthetic-workload-credential","execution_id":"execution-1","execution_epoch":"epoch-1","authority_revision":"revision-1"}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := (FileSessionLoader{Path: path}).Load()
	if err != nil || !reflect.DeepEqual(loaded, testSession()) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	loaded.Clear()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileSessionLoader{Path: path}).Load(); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("loose session err=%v", err)
	}
}

func newTestBroker(t *testing.T, protocolBrokerID string, execute func(domain.BrokerRequest) ([]byte, error), protocolCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer synthetic-workload-credential" {
			t.Fatalf("authorization header mismatch")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		switch request.URL.Path {
		case brokertransport.ProtocolPath:
			protocolCalls.Add(1)
			protocol, _ := brokertransport.ProtocolV1(protocolBrokerID, "atl-broker")
			body, _ := brokertransport.EncodeProtocolV1(protocol)
			_, _ = writer.Write(body)
		case brokertransport.ExecutePath:
			body, readErr := io.ReadAll(io.LimitReader(request.Body, 64<<10))
			if readErr != nil {
				t.Fatal(readErr)
			}
			decoded, err := brokercontract.DecodeRequestV1(body)
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			result, err := execute(decoded)
			if err != nil {
				t.Fatalf("execute fixture: %v", err)
			}
			_, _ = writer.Write(result)
		default:
			http.NotFound(writer, request)
		}
	}))
}

func newTestClient(t *testing.T, server *httptest.Server, loader SessionLoader, expectedBrokerID string) *Client {
	t.Helper()
	t.Cleanup(server.Close)
	tlsOptions, _, err := httpx.QualifiedTLSOptionsBytes(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{BaseURL: server.URL, BrokerID: expectedBrokerID, Audience: "atl-broker", Version: "test", Session: loader, TLS: tlsOptions})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testSession() Session {
	return Session{Credential: []byte("synthetic-workload-credential"), ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"}
}
