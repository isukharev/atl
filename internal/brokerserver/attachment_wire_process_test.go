//go:build !windows

package brokerserver

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
)

func TestSelectedAttachmentCLIRejectsCorruptedWireWithoutPublication(t *testing.T) {
	binary := buildSelectedATLBinary(t, "")
	for _, mode := range []string{"unchanged", "early EOF", "trailing line", "duplicate data", "malformed data"} {
		t.Run(mode, func(t *testing.T) {
			payload := []byte("synthetic attachment bytes")
			fixture := newAttachmentCLIProcessFixture(t, payload)
			targetURL, err := url.Parse(fixture.brokerURL)
			if err != nil {
				t.Fatal(err)
			}
			upstream := hostTLSClient(t, tls.Certificate{Certificate: [][]byte{fixture.brokerCertificate}})
			proxy := httputil.NewSingleHostReverseProxy(targetURL)
			proxy.Transport = upstream.Transport
			var transformations, requests atomic.Int32
			proxy.ModifyResponse = func(response *http.Response) error {
				if response.Request.URL.Path != brokertransport.ExecutePathV3 {
					return nil
				}
				body, readErr := io.ReadAll(io.LimitReader(response.Body, brokercontract.MaxAttachmentFramedResponseBytesV3+1))
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil || int64(len(body)) > brokercontract.MaxAttachmentFramedResponseBytesV3 || response.StatusCode != http.StatusOK {
					return fmt.Errorf("synthetic upstream stream failed")
				}
				lines := bytes.Split(bytes.TrimSuffix(body, []byte{'\n'}), []byte{'\n'})
				if len(lines) != 3 {
					return fmt.Errorf("synthetic upstream stream has unexpected shape")
				}
				switch mode {
				case "early EOF":
					lines = lines[:2]
				case "trailing line":
					lines = append(lines, []byte(`{}`))
				case "duplicate data":
					lines = [][]byte{lines[0], lines[1], lines[1], lines[2]}
				case "malformed data":
					lines[1] = []byte(`{}`)
				}
				wire := append(bytes.Join(lines, []byte{'\n'}), '\n')
				response.Body = io.NopCloser(bytes.NewReader(wire))
				response.ContentLength = -1
				response.Header.Del("Content-Length")
				transformations.Add(1)
				return nil
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			configureAttachmentCLIProcessFixture(t, fixture, server.URL, server.Certificate().Raw)
			destination := t.TempDir()
			target := filepath.Join(destination, "example.bin")
			prior := []byte("prior-owned-content")
			projectPageProcessWriteFile(t, target, prior)
			stdout, stderr, callErr := runAttachmentSelectedCLI(t, binary, fixture.environment, destination)
			fixture.stop()
			if requests.Load() != 3 || transformations.Load() != 1 || fixture.directCalls.Load() != 0 {
				t.Fatalf("proxy requests=%d streams=%d direct=%d", requests.Load(), transformations.Load(), fixture.directCalls.Load())
			}
			want := prior
			if mode == "unchanged" {
				want = payload
				var result map[string]string
				if callErr != nil || stderr != "" || json.Unmarshal([]byte(stdout), &result) != nil || result["id"] != "7" || result["name"] != "example.bin" {
					t.Fatalf("unchanged control failed: %v", callErr)
				}
			} else if callErr == nil || stdout != "" {
				t.Fatal("corrupted stream returned a success result")
			}
			actual, readErr := os.ReadFile(target)
			entries, listErr := os.ReadDir(destination)
			if readErr != nil || listErr != nil || !bytes.Equal(actual, want) || len(entries) != 1 || entries[0].Name() != "example.bin" {
				t.Fatalf("unexpected destination: read=%v list=%v matches=%t entries=%d", readErr, listErr, bytes.Equal(actual, want), len(entries))
			}
			// The real server succeeded before the synthetic intermediary changed
			// the wire. Refusal must therefore come from the selected CLI.
			decoder := json.NewDecoder(bytes.NewReader(fixture.audit.Bytes()))
			success := 0
			for {
				var event AuditEvent
				if err := decoder.Decode(&event); err == io.EOF {
					break
				} else if err != nil || !validAuditEvent(event) {
					t.Fatalf("audit decode=%v", err)
				}
				if event.Route == "data_execute_v3" && event.Outcome == "success" {
					success++
				}
			}
			if success != 1 {
				t.Fatalf("upstream successful streams=%d", success)
			}
		})
	}
}
