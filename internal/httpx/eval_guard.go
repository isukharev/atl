package httpx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

const evaluationHTTPGuardEnv = "ATL_EVAL_HTTP_GUARD_FILE"

var evaluationHTTPGuardMu sync.Mutex

type evaluationHTTPRecord struct {
	Method      string `json:"method"`
	RequestHash string `json:"request_hash"`
}

// evaluationGuardTransport is an internal benchmark boundary. When the
// runner supplies a private audit path it rejects every non-read request before
// the underlying transport can observe it, then records only the method and a
// one-way request identity. URLs, selectors, headers, and bodies never enter
// the audit file.
type evaluationGuardTransport struct {
	next http.RoundTripper
	path string
}

func withEvaluationHTTPGuard(next http.RoundTripper) http.RoundTripper {
	path := strings.TrimSpace(os.Getenv(evaluationHTTPGuardEnv))
	if path == "" {
		return next
	}
	return &evaluationGuardTransport{next: next, path: path}
}

func (t *evaluationGuardTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	method := strings.ToUpper(request.Method)
	if method != http.MethodGet && method != http.MethodHead {
		return nil, fmt.Errorf("agent evaluation HTTP guard blocked non-read method %s", method)
	}
	identity := sha256.Sum256([]byte(method + "\x00" + request.URL.String()))
	record := evaluationHTTPRecord{Method: method, RequestHash: hex.EncodeToString(identity[:])}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("agent evaluation HTTP guard encode: %w", err)
	}
	encoded = append(encoded, '\n')
	evaluationHTTPGuardMu.Lock()
	file, err := os.OpenFile(t.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		err = os.Chmod(t.path, 0o600)
	}
	if err == nil {
		_, err = file.Write(encoded)
	}
	if file != nil {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}
	evaluationHTTPGuardMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("agent evaluation HTTP guard audit: %w", err)
	}
	return t.next.RoundTrip(request)
}
