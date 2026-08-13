package httpx

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func newDistinctTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "atl-test-backend"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	return server
}

func writeServerCABundle(t *testing.T, server *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backend-ca.pem")
	body := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCABundleTrustsOnlyConfiguredBackendForJSONAndStreaming(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			_, _ = io.WriteString(w, "stream-ok")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	trusted := newDistinctTLSServer(t, handler)
	defer trusted.Close()
	untrusted := newDistinctTLSServer(t, handler)
	defer untrusted.Close()

	bundle := writeServerCABundle(t, trusted)
	client, err := NewWithSchedulerTLS(trusted.URL, "token", "test", nil, TLSOptions{CABundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := client.GetJSON(context.Background(), "/json", &result); err != nil || result["status"] != "ok" {
		t.Fatalf("GetJSON result=%v err=%v", result, err)
	}
	body, err := client.GetStream(context.Background(), "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "stream-ok" {
		t.Fatalf("GetStream body=%q err=%v", got, err)
	}

	isolated, err := NewWithSchedulerTLS(untrusted.URL, "token", "test", nil, TLSOptions{CABundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithSingleAttempt(context.Background())
	if err := isolated.GetJSON(ctx, "/json", &result); err == nil {
		t.Fatal("CA bundle for one backend unexpectedly trusted another backend")
	}
}

func TestCABundleValidationIsBoundedPathFreeAndHTTPSOnly(t *testing.T) {
	privateDir := filepath.Join(t.TempDir(), "configured-private-path")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	invalid := map[string]string{
		"missing":   filepath.Join(privateDir, "missing.pem"),
		"directory": privateDir,
	}
	garbage := filepath.Join(privateDir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid["garbage"] = garbage
	large := filepath.Join(privateDir, "large.pem")
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), caBundleMaxSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid["large"] = large
	for name, path := range invalid {
		t.Run(name, func(t *testing.T) {
			err := ValidateCABundle(path)
			if !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("error=%v, want ErrConfig", err)
			}
			if strings.Contains(err.Error(), privateDir) || strings.Contains(err.Error(), filepath.Base(path)) {
				t.Fatalf("error leaked configured path: %v", err)
			}
		})
	}

	t.Setenv("ATL_ALLOW_INSECURE", "1")
	if _, err := NewWithSchedulerTLS("http://127.0.0.1:8080", "", "test", nil, TLSOptions{CABundle: garbage}); !errors.Is(err, domain.ErrConfig) || !strings.Contains(err.Error(), "https") {
		t.Fatalf("http backend with CA bundle error=%v", err)
	}
}

func TestCABundleDoesNotMutateDefaultTransport(t *testing.T) {
	server := newDistinctTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	defaultTransport := http.DefaultTransport.(*http.Transport)
	before := defaultTransport.TLSClientConfig
	if _, err := NewWithSchedulerTLS(server.URL, "", "test", nil, TLSOptions{CABundle: writeServerCABundle(t, server)}); err != nil {
		t.Fatal(err)
	}
	if defaultTransport.TLSClientConfig != before {
		t.Fatal("configured client mutated http.DefaultTransport")
	}
}

func TestQualifiedTLSOptionsBindsExactBytesAndUsesExclusiveInMemoryRoots(t *testing.T) {
	server := newDistinctTLSServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ok"}`)
	}))
	defer server.Close()
	path := writeServerCABundle(t, server)
	bundle, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(bundle)
	wantRoots := x509.NewCertPool()
	if !wantRoots.AppendCertsFromPEM(bundle) {
		t.Fatal("test CA bundle was not accepted")
	}
	options, digest, err := QualifiedTLSOptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if digest != hex.EncodeToString(wantDigest[:]) || options.CABundle != "" || options.rootCAs == nil || !options.rootCAs.Equal(wantRoots) {
		t.Fatalf("digest=%q options=%+v roots_equal=%t", digest, options, options.rootCAs != nil && options.rootCAs.Equal(wantRoots))
	}
	defaultTransport := http.DefaultTransport.(*http.Transport)
	defaultTLS := defaultTransport.TLSClientConfig
	defaultTransport.TLSClientConfig = &tls.Config{ServerName: "ambient.invalid", RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}
	defer func() { defaultTransport.TLSClientConfig = defaultTLS }()
	// Construction must use the already-qualified in-memory roots, not re-read
	// the configured path or inherit ambient/system trust configuration.
	if err := os.WriteFile(path, []byte("changed after qualification"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewWithSchedulerTLS(server.URL, "token", "test", nil, options)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := client.GetJSON(context.Background(), "/", &result); err != nil || result["status"] != "ok" {
		t.Fatalf("result=%v error=%v", result, err)
	}
}

func TestQualifiedTLSOptionsEmptyAndExactByteDigest(t *testing.T) {
	options, digest, err := QualifiedTLSOptions(" \t")
	if err != nil || digest != "" || options.configured() {
		t.Fatalf("empty options=%+v digest=%q error=%v", options, digest, err)
	}
	server := newDistinctTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	path := writeServerCABundle(t, server)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, firstDigest, err := QualifiedTLSOptions(path)
	if err != nil {
		t.Fatal(err)
	}
	annotated := append([]byte("# reviewed private trust roots\n"), first...)
	if err := os.WriteFile(path, annotated, 0o600); err != nil {
		t.Fatal(err)
	}
	annotatedOptions, annotatedDigest, err := QualifiedTLSOptions(path)
	if err != nil || !annotatedOptions.configured() || annotatedDigest == firstDigest {
		t.Fatalf("annotated options=%+v digest=%q first=%q error=%v", annotatedOptions, annotatedDigest, firstDigest, err)
	}
	second := append(append([]byte(nil), first...), '\n')
	if err := os.WriteFile(path, second, 0o600); err != nil {
		t.Fatal(err)
	}
	_, secondDigest, err := QualifiedTLSOptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatal("qualified digest did not bind exact bundle bytes")
	}
}

func TestQualifiedTLSOptionsRejectsInvalidCertificateWithoutPathLeak(t *testing.T) {
	privateDir := filepath.Join(t.TempDir(), "configured-private-path")
	if err := os.Mkdir(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateDir, "private-ca-canary.pem")
	invalid := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})
	if err := os.WriteFile(path, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	options, digest, err := QualifiedTLSOptions(path)
	if !errors.Is(err, domain.ErrConfig) || digest != "" || options.configured() {
		t.Fatalf("options=%+v digest=%q error=%v", options, digest, err)
	}
	if strings.Contains(err.Error(), privateDir) || strings.Contains(err.Error(), filepath.Base(path)) {
		t.Fatalf("error leaked configured path: %v", err)
	}
}
