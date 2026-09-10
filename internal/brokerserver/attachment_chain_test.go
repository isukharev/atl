package brokerserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
)

// This is component-chain evidence: actual TLS authority/Jira adapters, app
// state, publisher and audit with the 4/8 scheduler. It invokes the private
// already-authenticated execution component, not the unavailable public route.
// Selected-binary/public-route enablement remains a separate acceptance gate.
func runAttachmentComponentChain(t *testing.T, fixture *attachmentChainFixture) (*attachmentPublicationWriter, AuditEvent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, brokertransport.ExecutePathV3, nil)
	request.Header.Set("Authorization", "Bearer "+attachmentChainWorkload)
	budgets, err := app.NewBrokerAttachmentExecutionBudgets()
	if err != nil {
		t.Fatal(err)
	}
	correlation := strings.Repeat("c", 32)
	initial, err := fixture.handler.authenticateAttachment(request, []byte(attachmentChainWorkload), correlation, budgets)
	if err != nil {
		t.Fatal(err)
	}
	var auditOutput bytes.Buffer
	audit, err := NewAudit(&auditOutput, fixture.handler.guard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = audit.Close(closeCtx)
	}()
	writer := &attachmentPublicationWriter{}
	audit.Wrap("data_execute_v3", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordAuditOperation(w, fixture.request.Operation)
		recordAuditCorrelation(w, correlation)
		fixture.handler.executeAttachment(w, r, fixture.request, initial, []byte(attachmentChainWorkload), correlation, budgets)
	})).ServeHTTP(writer, request)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := audit.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	var event AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(auditOutput.Bytes()), &event); err != nil || !validAuditEvent(event) {
		t.Fatalf("missing valid audit event: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.violations != 0 || len(fixture.nonces) != fixture.counts["authentication"] {
		t.Fatalf("fixture violations=%d nonces=%d authentication=%d", fixture.violations, len(fixture.nonces), fixture.counts["authentication"])
	}
	return writer, event
}

func TestAttachmentTLSComponentChainZeroOneTwoAndMaximumBodies(t *testing.T) {
	for _, size := range []int{0, 23, 1<<20 + 17, 16 << 20} {
		t.Run(fmt.Sprintf("bytes_%d", size), func(t *testing.T) {
			payload := bytes.Repeat([]byte{'x'}, size)
			fixture := newAttachmentChainFixture(t, payload)
			writer, event := runAttachmentComponentChain(t, fixture)
			if writer.status != http.StatusOK || event.Outcome != "success" || writer.Header().Get("Content-Type") != brokertransport.ExecutionStreamMediaTypeV3 {
				t.Fatalf("status=%d audit=%s reason=%s", writer.status, event.Outcome, event.Reason)
			}
			lines := bytes.Split(bytes.TrimSuffix(writer.body.Bytes(), []byte{'\n'}), []byte{'\n'})
			frames := (size + (1 << 20) - 1) / (1 << 20)
			if len(lines) != frames+2 {
				t.Fatalf("lines=%d frames=%d", len(lines), frames)
			}
			manifest, err := brokercontract.DecodeAttachmentManifestLineV3(lines[0])
			if err != nil || manifest.Core.Snapshot.DeclaredSize != int64(size) {
				t.Fatalf("manifest size=%d err=%v", manifest.Core.Snapshot.DeclaredSize, err)
			}
			var decoded []byte
			for index, line := range lines[1 : len(lines)-1] {
				data, err := brokercontract.DecodeAttachmentDataLineV3(line)
				if err != nil || data.Index != index || data.Offset != int64(len(decoded)) {
					t.Fatalf("data index=%d offset=%d err=%v", data.Index, data.Offset, err)
				}
				body, err := base64.StdEncoding.Strict().DecodeString(data.PayloadBase64)
				if err != nil {
					t.Fatal(err)
				}
				decoded = append(decoded, body...)
			}
			terminal, err := brokercontract.DecodeAttachmentTerminalLineV3(lines[len(lines)-1])
			whole := sha256.Sum256(payload)
			if err != nil || !bytes.Equal(decoded, payload) || terminal.WholeSHA256 != hex.EncodeToString(whole[:]) || terminal.ChunkCount != frames || terminal.TotalBytes != int64(size) {
				t.Fatalf("terminal size=%d frames=%d decode=%v bytes_match=%t", terminal.TotalBytes, terminal.ChunkCount, err, bytes.Equal(decoded, payload))
			}
			releases := max(frames, 1)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			counts := fixture.counts
			if counts["authentication"] != 1+releases || counts["admission"] != 1 || counts["qualification_initial"] != 1 || counts["qualification_pre_open"] != 1 || counts["qualification_release"] != releases || counts["operation_initial"] != 1 || counts["operation_body_dispatch"] != 1 || counts["operation_release"] != releases || counts["metadata"] != 2+releases || counts["body"] != 1 || writer.flushes != releases {
				t.Fatalf("physical calls=%v flushes=%d releases=%d", counts, writer.flushes, releases)
			}
		})
	}
}

func TestAttachmentTLSComponentChainRefusesDeniedDriftedAndCredentialReleases(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*attachmentChainFixture)
		partial bool
		noJira  bool
	}{
		{name: "forbidden sibling issue", noJira: true, prepare: func(f *attachmentChainFixture) { f.request.Arguments.IssueKey = "PROJ-2" }},
		{name: "forbidden sibling attachment", noJira: true, prepare: func(f *attachmentChainFixture) { f.request.Arguments.AttachmentID = "8" }},
		{name: "first release denied", prepare: func(f *attachmentChainFixture) { f.denyRelease = 1 }},
		{name: "second release denied", partial: true, prepare: func(f *attachmentChainFixture) { f.denyRelease = 2 }},
		{name: "metadata drift", prepare: func(f *attachmentChainFixture) { f.metadataDrift = 3 }},
		{name: "credential crosses native frame boundary", prepare: func(f *attachmentChainFixture) { copy(f.payload[(1<<20)-4:], attachmentChainBackend) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttachmentChainFixture(t, bytes.Repeat([]byte{'x'}, 1<<20+128))
			test.prepare(fixture)
			writer, event := runAttachmentComponentChain(t, fixture)
			if event.Outcome == "success" || bytes.Contains(writer.body.Bytes(), []byte(attachmentChainBackend)) || bytes.Contains(writer.body.Bytes(), []byte(`"kind":"terminal"`)) {
				t.Fatalf("unsafe failed stream audit=%s", event.Outcome)
			}
			if test.partial {
				if writer.status != http.StatusOK || event.Outcome != "partial" || writer.flushes != 1 {
					t.Fatalf("partial status=%d audit=%s flushes=%d", writer.status, event.Outcome, writer.flushes)
				}
			} else if writer.status == http.StatusOK || writer.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("prepublication failure status=%d type=%s", writer.status, writer.Header().Get("Content-Type"))
			}
			if test.noJira {
				fixture.mu.Lock()
				metadata, body := fixture.counts["metadata"], fixture.counts["body"]
				fixture.mu.Unlock()
				if metadata != 0 || body != 0 || event.Reason != "denied" {
					t.Fatalf("scope denial reached Jira: metadata=%d body=%d reason=%s", metadata, body, event.Reason)
				}
			}
		})
	}
}
