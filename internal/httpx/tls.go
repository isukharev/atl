package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const (
	dlHeaderTimeout = 30 * time.Second
	caBundleMaxSize = 4 << 20 // 4 MiB
)

// TLSOptions contains backend-scoped trust material. Paths are never included
// in returned errors or diagnostics.
type TLSOptions struct {
	CABundle string
}

func transportWithCABundle(path string) (*http.Transport, error) {
	bundle, err := readCABundle(path)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(bundle) {
		return nil, fmt.Errorf("%w: configured CA bundle contains no certificates", domain.ErrConfig)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := &tls.Config{}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
	}
	tlsConfig.RootCAs = pool
	tlsConfig.MinVersion = tls.VersionTLS12
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}

// ValidateCABundle checks configured trust material without constructing a
// client. It is used by path-free setup diagnostics.
func ValidateCABundle(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	_, err := transportWithCABundle(path)
	return err
}

func readCABundle(path string) ([]byte, error) {
	// Stat before open so a configured FIFO/device cannot block setup forever.
	// The descriptor is checked again after open to close the ordinary swap race.
	preInfo, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: configured CA bundle is unreadable", domain.ErrConfig)
	}
	if !preInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: configured CA bundle is not a regular file", domain.ErrConfig)
	}
	if preInfo.Size() > caBundleMaxSize {
		return nil, fmt.Errorf("%w: configured CA bundle exceeds the 4 MiB limit", domain.ErrConfig)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: configured CA bundle is unreadable", domain.ErrConfig)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: configured CA bundle is unavailable", domain.ErrConfig)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: configured CA bundle is not a regular file", domain.ErrConfig)
	}
	if info.Size() > caBundleMaxSize {
		return nil, fmt.Errorf("%w: configured CA bundle exceeds the 4 MiB limit", domain.ErrConfig)
	}
	bundle, err := io.ReadAll(io.LimitReader(f, caBundleMaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: configured CA bundle is unreadable", domain.ErrConfig)
	}
	if len(bundle) > caBundleMaxSize {
		return nil, fmt.Errorf("%w: configured CA bundle exceeds the 4 MiB limit", domain.ErrConfig)
	}
	return bundle, nil
}
