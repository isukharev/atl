package brokerclient

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestBrokerClientNeverConsultsAmbientProxyWithAnyTrustMode(t *testing.T) {
	var backendCalls, proxyCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-workload-credential" {
			t.Error("unexpected credential")
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	original := http.DefaultTransport
	ambient := original.(*http.Transport).Clone()
	ambient.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	ambient.Proxy = func(*http.Request) (*url.URL, error) {
		proxyCalls.Add(1)
		return nil, nil
	}
	http.DefaultTransport = ambient
	defer func() { http.DefaultTransport = original; ambient.CloseIdleConnections() }()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	qualified, _, err := httpx.QualifiedTLSOptions(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		tls  httpx.TLSOptions
	}{
		{"system trust", httpx.TLSOptions{}},
		{"bundle path", httpx.TLSOptions{CABundle: path}},
		{"qualified path", qualified},
	} {
		t.Run(test.name, func(t *testing.T) {
			backendCalls.Store(0)
			proxyCalls.Store(0)
			client, err := New(Config{BaseURL: server.URL, BrokerID: "broker-1", Audience: "atl-broker", Session: &countingSessionLoader{value: testSession()}, TLS: test.tls})
			if err != nil {
				t.Fatal(err)
			}
			transport, err := client.newHTTPClient(testSession())
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			defer transport.ClearCredential()
			budget, err := domain.NewReadBudget(1, 128)
			if err != nil {
				t.Fatal(err)
			}
			ctx := domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(t.Context(), budget)))
			response, err := transport.DoBoundedResponse(ctx, http.MethodGet, "/read", nil, nil, 128, 128)
			if err != nil || response.Status != http.StatusOK || backendCalls.Load() != 1 || proxyCalls.Load() != 0 {
				t.Fatalf("error=%v status=%d backend=%d proxy=%d", err, response.Status, backendCalls.Load(), proxyCalls.Load())
			}
			streamBudget, err := domain.NewReadBudget(1, 128)
			if err != nil {
				t.Fatal(err)
			}
			streamContext := domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(t.Context(), streamBudget)))
			body, err := transport.GetStream(streamContext, "/stream")
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.Copy(io.Discard, io.LimitReader(body, 129))
			closeErr := body.Close()
			if readErr != nil || closeErr != nil || backendCalls.Load() != 2 || proxyCalls.Load() != 0 {
				t.Fatalf("stream read=%v close=%v backend=%d proxy=%d", readErr, closeErr, backendCalls.Load(), proxyCalls.Load())
			}
		})
	}
	// Broker opt-in must not mutate ambient transport or change ordinary
	// adapter routing. The same successful read still consults that callback.
	proxyCalls.Store(0)
	ordinary := httpx.New(server.URL, "synthetic-workload-credential", "test")
	defer ordinary.CloseIdleConnections()
	var result map[string]bool
	if err := ordinary.GetJSON(t.Context(), "/ordinary", &result); err != nil || !result["ok"] || proxyCalls.Load() != 1 {
		t.Fatalf("ordinary routing changed: error=%v result=%v proxy=%d", err, result, proxyCalls.Load())
	}
}
