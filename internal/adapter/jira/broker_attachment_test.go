package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const (
	brokerAttachmentTestIssueKey  = "PROJ-1"
	brokerAttachmentTestIssueID   = "101"
	brokerAttachmentTestID        = "7"
	brokerAttachmentTestFilename  = "a b.bin"
	brokerAttachmentTestMediaType = "application/octet-stream"
	brokerAttachmentTestUpdated   = "2026-09-09T10:00:00Z"
	brokerAttachmentTestCreated   = "2026-09-08T10:00:00Z"
	brokerAttachmentMetadataURI   = "/jira/rest/api/2/issue/PROJ-1?fields=attachment%2Cproject%2Cupdated"
	brokerAttachmentRelativeURI   = "/secure/attachment/7/a%20b.bin"
	brokerAttachmentBodyURI       = "/jira/secure/attachment/7/a%20b.bin"
)

type brokerAttachmentTestRecord struct {
	ID, Filename, MediaType, Created, Content string
	Size                                      int64
}

func TestBrokerJiraAttachmentQualifyPrepareAndOpenExactURI(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(map[bool]string{false: "relative", true: "absolute"}[absolute], func(t *testing.T) {
			var metadataCalls, bodyCalls atomic.Int32
			var response []byte
			payload := []byte("abc")
			server, reader := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer attachment-token" {
					t.Error("backend bearer missing")
				}
				switch request.URL.RequestURI() {
				case brokerAttachmentMetadataURI:
					metadataCalls.Add(1)
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write(response)
				case brokerAttachmentBodyURI:
					bodyCalls.Add(1)
					_, _ = writer.Write(payload)
				default:
					t.Errorf("unexpected request URI %q", request.URL.RequestURI())
					writer.WriteHeader(http.StatusNotFound)
				}
			})
			content := brokerAttachmentRelativeURI
			if absolute {
				content = server.URL + "/jira" + brokerAttachmentRelativeURI
			}
			response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(content)})
			ctx, cancel, budget := brokerAttachmentContext(t, 3, 4<<20)
			defer cancel()

			snapshot, err := reader.QualifyBrokerJiraAttachment(ctx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
			if err != nil || snapshot.IssueID != brokerAttachmentTestIssueID || snapshot.ParentID != brokerAttachmentTestIssueID || snapshot.AttachmentID != brokerAttachmentTestID || snapshot.Filename != brokerAttachmentTestFilename || snapshot.IssueEvidenceSHA256 == "" || snapshot.AttachmentEvidenceSHA256 == "" || snapshot.ProjectionSHA256 == "" {
				t.Fatalf("snapshot=%+v error=%v", snapshot, err)
			}
			snapshotJSON, marshalErr := json.Marshal(snapshot)
			if marshalErr != nil || bytes.Contains(snapshotJSON, []byte("/secure/attachment")) || bytes.Contains(snapshotJSON, []byte(server.URL)) {
				t.Fatalf("snapshot exposed private URI: json=%s error=%v", snapshotJSON, marshalErr)
			}
			handle, err := reader.PrepareBrokerJiraAttachment(ctx, snapshot)
			if err != nil || !reflect.DeepEqual(handle.Snapshot(), snapshot) {
				t.Fatalf("handle=%v snapshot=%+v error=%v", handle, handle.Snapshot(), err)
			}
			for _, formatted := range brokerAttachmentFormats(handle) {
				assertBrokerAttachmentTextSafe(t, formatted, server.URL, "/secure/attachment")
			}
			stream, err := handle.Open(ctx, time.Now().Add(time.Second))
			if err != nil || stream == nil {
				t.Fatalf("stream=%v error=%v", stream, err)
			}
			for _, formatted := range brokerAttachmentFormats(stream) {
				assertBrokerAttachmentTextSafe(t, formatted, server.URL, "/secure/attachment")
			}
			if second, secondErr := handle.Open(ctx, time.Now().Add(time.Second)); second != nil || !errors.Is(secondErr, domain.ErrCheckFailed) {
				t.Fatalf("second=%v error=%v", second, secondErr)
			}
			body, readErr := io.ReadAll(stream)
			var eofProbe [1]byte
			eofN, eofErr := stream.Read(eofProbe[:])
			closeErr := stream.Close()
			if !bytes.Equal(body, payload) || readErr != nil || eofN != 0 || eofErr != io.EOF || closeErr != nil || metadataCalls.Load() != 2 || bodyCalls.Load() != 1 {
				t.Fatalf("body=%q read=%v eof=%d/%v close=%v metadata=%d body_calls=%d", body, readErr, eofN, eofErr, closeErr, metadataCalls.Load(), bodyCalls.Load())
			}
			if closeErr = handle.Close(); closeErr != nil {
				t.Fatalf("idempotent close=%v", closeErr)
			}
			if reopened, reopenErr := handle.Open(ctx, time.Now().Add(time.Second)); reopened != nil || !errors.Is(reopenErr, domain.ErrCheckFailed) || bodyCalls.Load() != 1 {
				t.Fatalf("reopened=%v error=%v body_calls=%d", reopened, reopenErr, bodyCalls.Load())
			}
			usage := budget.Usage()
			if usage.Attempts != 3 || usage.ResponseBytes != int64(2*len(response)+len(payload)) {
				t.Fatalf("usage=%+v response=%d", usage, len(response))
			}
			concrete := handle.(*brokerJiraAttachmentHandle)
			concrete.mu.Lock()
			uri, state := concrete.uri, concrete.state
			concrete.mu.Unlock()
			if uri != "" || state != brokerJiraAttachmentClosed {
				t.Fatalf("retained URI=%q state=%d", uri, state)
			}
		})
	}
}

func TestBrokerJiraAttachmentRejectsSnapshotDriftAndHostileURIWithoutBody(t *testing.T) {
	uriCases := map[string]func(string) string{
		"arbitrary REST":      func(string) string { return "/rest/api/2/serverInfo" },
		"wrong id":            func(string) string { return "/secure/attachment/70/a%20b.bin" },
		"id prefix":           func(string) string { return "/secure/attachment/7x/a%20b.bin" },
		"missing filename":    func(string) string { return "/secure/attachment/7" },
		"extra segment":       func(string) string { return "/secure/attachment/7/a%20b.bin/x" },
		"double slash":        func(string) string { return "/secure//attachment/7/a%20b.bin" },
		"dot segment":         func(string) string { return "/secure/attachment/7/../a%20b.bin" },
		"encoded slash":       func(string) string { return "/secure/attachment/7/a%2Fb.bin" },
		"encoded backslash":   func(string) string { return "/secure/attachment/7/a%5Cb.bin" },
		"double encoding":     func(string) string { return "/secure/attachment/7/a%2520b.bin" },
		"noncanonical escape": func(string) string { return "/secure/attachment/7/a%20b%2Ebin" },
		"invalid escape":      func(string) string { return "/secure/attachment/7/a%zz.bin" },
		"query":               func(string) string { return brokerAttachmentRelativeURI + "?token=private" },
		"force query":         func(string) string { return brokerAttachmentRelativeURI + "?" },
		"fragment":            func(string) string { return brokerAttachmentRelativeURI + "#private" },
		"network path":        func(string) string { return "//foreign.example/secure/attachment/7/a%20b.bin" },
		"userinfo": func(base string) string {
			return "https://user@" + strings.TrimPrefix(base, "https://") + "/jira" + brokerAttachmentRelativeURI
		},
		"foreign host": func(string) string { return "https://foreign.example/secure/attachment/7/a%20b.bin" },
		"foreign port": func(base string) string {
			parsed, _ := url.Parse(base)
			return parsed.Scheme + "://" + parsed.Hostname() + ":1/jira" + brokerAttachmentRelativeURI
		},
		"downgrade": func(base string) string {
			return strings.Replace(base, "https://", "http://", 1) + "/jira" + brokerAttachmentRelativeURI
		},
		"context path missing": func(base string) string { return base + brokerAttachmentRelativeURI },
	}
	for name, mutate := range uriCases {
		t.Run("URI "+name, func(t *testing.T) {
			testBrokerAttachmentSecondMetadataFailure(t, func(base string, _ brokerAttachmentTestRecord) brokerAttachmentTestRecord {
				return brokerAttachmentRecord(mutate(base))
			})
		})
	}

	driftCases := map[string]func(string, brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord){
		"issue id": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			return "102", brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, record
		},
		"issue key": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			return brokerAttachmentTestIssueID, "PROJ-2", "PROJ", brokerAttachmentTestUpdated, record
		},
		"project": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			return brokerAttachmentTestIssueID, "NEXT-1", "NEXT", brokerAttachmentTestUpdated, record
		},
		"updated": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			return brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", "2026-09-09T10:00:01Z", record
		},
		"attachment id": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			record.ID, record.Content = "8", "/secure/attachment/8/a%20b.bin"
			return brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, record
		},
		"filename": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			record.Filename, record.Content = "b.bin", "/secure/attachment/7/b.bin"
			return brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, record
		},
		"media type": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			record.MediaType = "application/test"
			return brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, record
		},
		"created": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			record.Created = "2026-09-08T10:00:01Z"
			return brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, record
		},
		"size": func(_ string, record brokerAttachmentTestRecord) (string, string, string, string, brokerAttachmentTestRecord) {
			record.Size++
			return brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, record
		},
	}
	for name, mutate := range driftCases {
		t.Run("drift "+name, func(t *testing.T) {
			var metadataCalls, bodyCalls atomic.Int32
			var response []byte
			server, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.RequestURI() == brokerAttachmentMetadataURI {
					call := metadataCalls.Add(1)
					if call == 1 {
						_, _ = writer.Write(brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)}))
						return
					}
					_, _ = writer.Write(response)
					return
				}
				bodyCalls.Add(1)
			})
			record := brokerAttachmentRecord(brokerAttachmentRelativeURI)
			issueID, issueKey, project, updated, record := mutate(server.URL, record)
			response = brokerAttachmentResponse(t, issueID, issueKey, project, updated, []brokerAttachmentTestRecord{record})
			ctx, cancel, _ := brokerAttachmentContext(t, 3, 4<<20)
			defer cancel()
			snapshot, err := jira.QualifyBrokerJiraAttachment(ctx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := jira.PrepareBrokerJiraAttachment(ctx, snapshot)
			if handle != nil || err == nil || bodyCalls.Load() != 0 || metadataCalls.Load() != 2 {
				t.Fatalf("handle=%v error=%v metadata=%d body=%d", handle, err, metadataCalls.Load(), bodyCalls.Load())
			}
		})
	}
}

func TestBrokerJiraAttachmentInventoryAndInputsFailClosed(t *testing.T) {
	responses := map[string][]byte{
		"null inventory": []byte(`{"id":"101","key":"PROJ-1","fields":{"attachment":null,"project":{"key":"PROJ"},"updated":"2026-09-09T10:00:00Z"}}`),
		"duplicate id":   brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI), brokerAttachmentRecord(brokerAttachmentRelativeURI)}),
		"unknown member": []byte(`{"id":"101","key":"PROJ-1","fields":{"attachment":[{"id":"7","filename":"a b.bin","mimeType":"application/octet-stream","size":3,"created":"2026-09-08T10:00:00Z","content":"/secure/attachment/7/a%20b.bin","private":true}],"project":{"key":"PROJ"},"updated":"2026-09-09T10:00:00Z"}}`),
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = writer.Write(response)
			})
			ctx, cancel, _ := brokerAttachmentContext(t, 1, 2<<20)
			defer cancel()
			snapshot, err := jira.QualifyBrokerJiraAttachment(ctx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
			if err == nil || snapshot != (domain.BrokerJiraAttachmentSnapshotV3{}) || calls.Load() != 1 {
				t.Fatalf("snapshot=%+v error=%v calls=%d", snapshot, err, calls.Load())
			}
		})
	}
	var calls atomic.Int32
	_, jira := newBrokerAttachmentTLSJira(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	if _, err := jira.QualifyBrokerJiraAttachment(t.Context(), brokerAttachmentTestIssueKey, brokerAttachmentTestID); !errors.Is(err, domain.ErrCheckFailed) || calls.Load() != 0 {
		t.Fatalf("missing budget error=%v calls=%d", err, calls.Load())
	}
	for _, input := range [][2]string{{"bad", "7"}, {brokerAttachmentTestIssueKey, "0"}, {brokerAttachmentTestIssueKey, "7x"}} {
		if _, err := jira.QualifyBrokerJiraAttachment(t.Context(), input[0], input[1]); !errors.Is(err, domain.ErrUsage) || calls.Load() != 0 {
			t.Fatalf("input=%q error=%v calls=%d", input, err, calls.Load())
		}
	}
}

func TestBrokerJiraAttachmentMetadataFailureIsSingleAttemptAndContentFree(t *testing.T) {
	var calls atomic.Int32
	_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte("PRIVATE-METADATA-CANARY"))
	})
	ctx, cancel, budget := brokerAttachmentContext(t, 1, 1<<20)
	defer cancel()
	snapshot, err := jira.QualifyBrokerJiraAttachment(ctx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
	if err == nil || snapshot != (domain.BrokerJiraAttachmentSnapshotV3{}) || calls.Load() != 1 || budget.Usage().Attempts != 1 {
		t.Fatalf("snapshot=%+v error=%v calls=%d usage=%+v", snapshot, err, calls.Load(), budget.Usage())
	}
	for _, formatted := range append([]string{err.Error()}, brokerAttachmentFormats(err)...) {
		assertBrokerAttachmentTextSafe(t, formatted, "PRIVATE-METADATA-CANARY", brokerAttachmentMetadataURI)
	}
}

func TestBrokerJiraAttachmentOneShotCloseCancelsOpening(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	var bodyCalls atomic.Int32
	var response []byte
	_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RequestURI() == brokerAttachmentMetadataURI {
			_, _ = writer.Write(response)
			return
		}
		bodyCalls.Add(1)
		close(entered)
		<-request.Context().Done()
		close(exited)
	})
	response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)})
	ctx, cancel, _ := brokerAttachmentContext(t, 3, 4<<20)
	defer cancel()
	snapshot, err := jira.QualifyBrokerJiraAttachment(ctx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := jira.PrepareBrokerJiraAttachment(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		stream, openErr := handle.Open(ctx, time.Now().Add(2*time.Second))
		if stream != nil {
			_ = stream.Close()
		}
		result <- openErr
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("body request did not enter")
	}
	if second, secondErr := handle.Open(ctx, time.Now().Add(time.Second)); second != nil || !errors.Is(secondErr, domain.ErrCheckFailed) {
		t.Fatalf("concurrent second=%v error=%v", second, secondErr)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case openErr := <-result:
		if !errors.Is(openErr, context.Canceled) {
			t.Fatalf("open error=%v", openErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("open did not stop")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("server request did not cancel")
	}
	if bodyCalls.Load() != 1 {
		t.Fatalf("body calls=%d", bodyCalls.Load())
	}
}

func TestBrokerJiraAttachmentTransportFailuresAreOneAttemptAndContentFree(t *testing.T) {
	for _, test := range []struct {
		name, location     string
		status, wantStatus int
		drop               bool
	}{
		{name: "301 same origin", status: 301, wantStatus: 301, location: "same"},
		{name: "302 foreign origin", status: 302, location: "foreign"},
		{name: "303 downgrade", status: 303, location: "downgrade"},
		{name: "307 same origin", status: 307, wantStatus: 307, location: "same"},
		{name: "308 foreign origin", status: 308, location: "foreign"},
		{name: "429", status: 429, wantStatus: 429}, {name: "503", status: 503, wantStatus: 503}, {name: "dropped reply", drop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var metadataCalls, bodyCalls, targetCalls atomic.Int32
			var response []byte
			var server *httptest.Server
			var jira *Jira
			server, jira = newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.RequestURI() {
				case brokerAttachmentMetadataURI:
					metadataCalls.Add(1)
					_, _ = writer.Write(response)
				case brokerAttachmentBodyURI:
					bodyCalls.Add(1)
					if test.drop {
						hijacker, ok := writer.(http.Hijacker)
						if !ok {
							t.Error("hijacking unavailable")
							return
						}
						connection, _, hijackErr := hijacker.Hijack()
						if hijackErr != nil {
							t.Error(hijackErr)
							return
						}
						_ = connection.Close()
						return
					}
					if test.location != "" {
						location := "/jira/private-target"
						switch test.location {
						case "foreign":
							location = "https://foreign.example/private-target"
						case "downgrade":
							location = strings.Replace(server.URL, "https://", "http://", 1) + "/jira/private-target"
						}
						writer.Header().Set("Location", location)
					}
					writer.WriteHeader(test.status)
					_, _ = writer.Write([]byte("PRIVATE-BACKEND-BODY"))
				case "/jira/private-target":
					targetCalls.Add(1)
				default:
					t.Errorf("unexpected URI %q", request.URL.RequestURI())
				}
			})
			response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)})
			ctx, cancel, budget := brokerAttachmentContext(t, 3, 4<<20)
			defer cancel()
			snapshot, err := jira.QualifyBrokerJiraAttachment(ctx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := jira.PrepareBrokerJiraAttachment(ctx, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			stream, openErr := handle.Open(ctx, time.Now().Add(time.Second))
			if stream != nil || openErr == nil || metadataCalls.Load() != 2 || bodyCalls.Load() != 1 || targetCalls.Load() != 0 || budget.Usage().Attempts != 3 {
				t.Fatalf("stream=%v error=%v metadata=%d body=%d target=%d usage=%+v", stream, openErr, metadataCalls.Load(), bodyCalls.Load(), targetCalls.Load(), budget.Usage())
			}
			var status interface{ HTTPStatus() int }
			if !errors.As(openErr, &status) || status.HTTPStatus() != test.wantStatus {
				t.Fatalf("error status=%v want=%d", status, test.wantStatus)
			}
			for _, formatted := range append([]string{openErr.Error()}, brokerAttachmentFormats(openErr)...) {
				assertBrokerAttachmentTextSafe(t, formatted, "PRIVATE-BACKEND-BODY", brokerAttachmentRelativeURI, "private-target")
			}
			if second, secondErr := handle.Open(ctx, time.Now().Add(time.Second)); second != nil || secondErr == nil || bodyCalls.Load() != 1 {
				t.Fatalf("second=%v error=%v body=%d", second, secondErr, bodyCalls.Load())
			}
			concrete := handle.(*brokerJiraAttachmentHandle)
			concrete.mu.Lock()
			uri := concrete.uri
			concrete.mu.Unlock()
			if uri != "" {
				t.Fatalf("failed open retained URI=%q", uri)
			}
		})
	}
}

func TestBrokerJiraAttachmentReadBoundsAndCancellationRemainDistinct(t *testing.T) {
	t.Run("body outlives dispatch lease", func(t *testing.T) {
		releaseBody := make(chan struct{})
		var response []byte
		_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.RequestURI() == brokerAttachmentMetadataURI {
				_, _ = writer.Write(response)
				return
			}
			writer.WriteHeader(http.StatusOK)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case <-releaseBody:
				_, _ = writer.Write([]byte("body"))
			case <-request.Context().Done():
			}
		})
		response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)})
		setupCtx, setupCancel, _ := brokerAttachmentContext(t, 2, 4<<20)
		defer setupCancel()
		snapshot, err := jira.QualifyBrokerJiraAttachment(setupCtx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := jira.PrepareBrokerJiraAttachment(setupCtx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		bodyCtx, bodyCancel, _ := brokerAttachmentContext(t, 1, 1<<20)
		defer bodyCancel()
		dispatch := time.Now().Add(500 * time.Millisecond)
		stream, err := handle.Open(bodyCtx, dispatch)
		if err != nil {
			t.Fatal(err)
		}
		timer := time.NewTimer(time.Until(dispatch) + 20*time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-bodyCtx.Done():
			t.Fatal(bodyCtx.Err())
		}
		close(releaseBody)
		body, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if string(body) != "body" || readErr != nil || closeErr != nil {
			t.Fatalf("body=%q read=%v close=%v", body, readErr, closeErr)
		}
	})

	t.Run("expired dispatch", func(t *testing.T) {
		var bodyCalls atomic.Int32
		var response []byte
		_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.RequestURI() == brokerAttachmentMetadataURI {
				_, _ = writer.Write(response)
				return
			}
			bodyCalls.Add(1)
		})
		response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)})
		setupCtx, setupCancel, _ := brokerAttachmentContext(t, 2, 4<<20)
		defer setupCancel()
		snapshot, err := jira.QualifyBrokerJiraAttachment(setupCtx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := jira.PrepareBrokerJiraAttachment(setupCtx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		bodyCtx, bodyCancel, bodyBudget := brokerAttachmentContext(t, 1, 1<<20)
		defer bodyCancel()
		stream, openErr := handle.Open(bodyCtx, time.Now().Add(-time.Second))
		if stream != nil || !errors.Is(openErr, domain.ErrReadDispatchExpired) || bodyCalls.Load() != 0 || bodyBudget.Usage() != (domain.ReadBudgetUsage{}) {
			t.Fatalf("stream=%v error=%v body=%d usage=%+v", stream, openErr, bodyCalls.Load(), bodyBudget.Usage())
		}
	})

	t.Run("parent byte ceiling", func(t *testing.T) {
		var response []byte
		_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.RequestURI() == brokerAttachmentMetadataURI {
				_, _ = writer.Write(response)
				return
			}
			_, _ = writer.Write([]byte("four"))
		})
		response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)})
		setupCtx, setupCancel, _ := brokerAttachmentContext(t, 2, 4<<20)
		defer setupCancel()
		snapshot, err := jira.QualifyBrokerJiraAttachment(setupCtx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := jira.PrepareBrokerJiraAttachment(setupCtx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		bodyCtx, bodyCancel, bodyBudget := brokerAttachmentContext(t, 1, 3)
		defer bodyCancel()
		stream, err := handle.Open(bodyCtx, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if string(body) != "fou" || !errors.Is(readErr, domain.ErrReadResponseBudgetExhausted) || bodyBudget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 3}) {
			t.Fatalf("body=%q error=%v response_exhausted=%t usage=%+v", body, readErr, errors.Is(readErr, domain.ErrReadResponseBudgetExhausted), bodyBudget.Usage())
		}
	})

	t.Run("fixed sixteen MiB ceiling", func(t *testing.T) {
		var response []byte
		_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.RequestURI() == brokerAttachmentMetadataURI {
				_, _ = writer.Write(response)
				return
			}
			_, _ = io.CopyN(writer, brokerAttachmentFillReader{}, brokercontract.MaxAttachmentNativeBodyBytesV3+1)
		})
		record := brokerAttachmentRecord(brokerAttachmentRelativeURI)
		record.Size = brokercontract.MaxAttachmentNativeBodyBytesV3
		response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{record})
		setupCtx, setupCancel, _ := brokerAttachmentContext(t, 2, 4<<20)
		defer setupCancel()
		snapshot, err := jira.QualifyBrokerJiraAttachment(setupCtx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := jira.PrepareBrokerJiraAttachment(setupCtx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		bodyCtx, bodyCancel, bodyBudget := brokerAttachmentContext(t, 1, 32<<20)
		defer bodyCancel()
		stream, err := handle.Open(bodyCtx, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if int64(len(body)) != brokercontract.MaxAttachmentNativeBodyBytesV3 || !errors.Is(readErr, domain.ErrReadResponseBudgetExhausted) ||
			bodyBudget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: brokercontract.MaxAttachmentNativeBodyBytesV3}) {
			t.Fatalf("body=%d error=%v usage=%+v", len(body), readErr, bodyBudget.Usage())
		}
	})

	t.Run("malformed chunk error is content free", func(t *testing.T) {
		var response []byte
		_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.RequestURI() == brokerAttachmentMetadataURI {
				_, _ = writer.Write(response)
				return
			}
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				t.Error("hijacking unavailable")
				return
			}
			connection, buffered, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nPRIVATE-CHUNK-CANARY\r\n")
			_ = buffered.Flush()
			_ = connection.Close()
		})
		response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)})
		setupCtx, setupCancel, _ := brokerAttachmentContext(t, 2, 4<<20)
		defer setupCancel()
		snapshot, _ := jira.QualifyBrokerJiraAttachment(setupCtx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
		handle, _ := jira.PrepareBrokerJiraAttachment(setupCtx, snapshot)
		bodyCtx, bodyCancel, _ := brokerAttachmentContext(t, 1, 1<<20)
		defer bodyCancel()
		stream, err := handle.Open(bodyCtx, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if readErr == nil {
			t.Fatal("malformed chunk stream succeeded")
		}
		for _, formatted := range append([]string{readErr.Error()}, brokerAttachmentFormats(readErr)...) {
			assertBrokerAttachmentTextSafe(t, formatted, "PRIVATE-CHUNK-CANARY", brokerAttachmentRelativeURI)
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		bodyEntered := make(chan struct{})
		var response []byte
		_, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.RequestURI() == brokerAttachmentMetadataURI {
				_, _ = writer.Write(response)
				return
			}
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.WriteHeader(http.StatusOK)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			close(bodyEntered)
			<-request.Context().Done()
		})
		response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)})
		setupCtx, setupCancel, _ := brokerAttachmentContext(t, 2, 4<<20)
		defer setupCancel()
		snapshot, _ := jira.QualifyBrokerJiraAttachment(setupCtx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
		handle, _ := jira.PrepareBrokerJiraAttachment(setupCtx, snapshot)
		base, baseCancel := context.WithTimeout(t.Context(), 3*time.Second)
		bodyCtx, cancel := context.WithCancel(base)
		budget, _ := domain.NewReadBudget(1, 1<<20)
		bodyCtx = domain.WithReadBudget(bodyCtx, budget)
		stream, err := handle.Open(bodyCtx, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-bodyEntered:
		case <-time.After(time.Second):
			t.Fatal("body did not start")
		}
		cancel()
		var buffer [1]byte
		_, readErr := stream.Read(buffer[:])
		_ = stream.Close()
		baseCancel()
		if !errors.Is(readErr, context.Canceled) {
			t.Fatalf("read error=%v", readErr)
		}
	})
}

type brokerAttachmentFillReader struct{}

func (brokerAttachmentFillReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}

func testBrokerAttachmentSecondMetadataFailure(t *testing.T, mutate func(string, brokerAttachmentTestRecord) brokerAttachmentTestRecord) {
	t.Helper()
	var metadataCalls, bodyCalls atomic.Int32
	var second []byte
	server, jira := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RequestURI() == brokerAttachmentMetadataURI {
			if metadataCalls.Add(1) == 1 {
				_, _ = writer.Write(brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)}))
			} else {
				_, _ = writer.Write(second)
			}
			return
		}
		bodyCalls.Add(1)
	})
	record := mutate(server.URL, brokerAttachmentRecord(brokerAttachmentRelativeURI))
	second = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{record})
	ctx, cancel, _ := brokerAttachmentContext(t, 3, 4<<20)
	defer cancel()
	snapshot, err := jira.QualifyBrokerJiraAttachment(ctx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := jira.PrepareBrokerJiraAttachment(ctx, snapshot)
	if handle != nil || err == nil || metadataCalls.Load() != 2 || bodyCalls.Load() != 0 {
		t.Fatalf("handle=%v error=%v metadata=%d body=%d", handle, err, metadataCalls.Load(), bodyCalls.Load())
	}
}

func brokerAttachmentRecord(content string) brokerAttachmentTestRecord {
	return brokerAttachmentTestRecord{ID: brokerAttachmentTestID, Filename: brokerAttachmentTestFilename, MediaType: brokerAttachmentTestMediaType, Size: 3, Created: brokerAttachmentTestCreated, Content: content}
}

func brokerAttachmentResponse(t *testing.T, issueID, issueKey, project, updated string, records []brokerAttachmentTestRecord) []byte {
	t.Helper()
	attachments := make([]map[string]any, len(records))
	for index, record := range records {
		attachments[index] = map[string]any{
			"id": record.ID, "filename": record.Filename, "mimeType": record.MediaType, "size": record.Size,
			"created": record.Created, "content": record.Content,
			"author": map[string]any{"name": "fixture", "key": "stable", "displayName": "Fixture"},
		}
	}
	body, err := json.Marshal(map[string]any{"id": issueID, "key": issueKey, "fields": map[string]any{"attachment": attachments, "project": map[string]any{"key": project}, "updated": updated}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
