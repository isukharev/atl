package brokerauthority

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestAuthorityAttachmentV3ComposesParentAttemptAndByteBudgets(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := newAuthorityAttachmentFixture(t, now)
	response := mustAuthorityCall(t, func() ([]byte, error) {
		return brokercontract.EncodeAttachmentAdmissionDecisionV3(fixture.admissionDecision)
	})

	t.Run("nil parent", func(t *testing.T) {
		authority, calls := attachmentAdmissionAuthority(t, now, response)
		if _, err := authority.AdmitAttachment(t.Context(), fixture.admission); err != nil || calls.Load() != 1 {
			t.Fatalf("err=%v calls=%d", err, calls.Load())
		}
	})

	t.Run("exhausted parent", func(t *testing.T) {
		authority, calls := attachmentAdmissionAuthority(t, now, response)
		parent, _ := domain.NewReadBudget(0, brokercontract.MaxAttachmentAuthorityCallBytesV3)
		_, err := authority.AdmitAttachment(domain.WithReadBudget(t.Context(), parent), fixture.admission)
		if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) || calls.Load() != 0 || parent.Usage() != (domain.ReadBudgetUsage{}) {
			t.Fatalf("err=%v calls=%d usage=%+v", err, calls.Load(), parent.Usage())
		}
	})

	t.Run("one parent attempt", func(t *testing.T) {
		authority, calls := attachmentAdmissionAuthority(t, now, response)
		parent, _ := domain.NewReadBudget(1, 2*brokercontract.MaxAttachmentAuthorityCallBytesV3)
		ctx := domain.WithReadBudget(t.Context(), parent)
		_, firstErr := authority.AdmitAttachment(ctx, fixture.admission)
		_, secondErr := authority.AdmitAttachment(ctx, fixture.admission)
		usage := parent.Usage()
		if firstErr != nil || !errors.Is(secondErr, domain.ErrReadAttemptBudgetExhausted) || calls.Load() != 1 || usage.Attempts != 1 || usage.ResponseBytes != int64(len(response)) {
			t.Fatalf("errors=%v/%v calls=%d usage=%+v", firstErr, secondErr, calls.Load(), usage)
		}
	})

	t.Run("parent byte exhaustion", func(t *testing.T) {
		authority, calls := attachmentAdmissionAuthority(t, now, response)
		parent, _ := domain.NewReadBudget(1, int64(len(response)-1))
		_, err := authority.AdmitAttachment(domain.WithReadBudget(t.Context(), parent), fixture.admission)
		usage := parent.Usage()
		if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) || calls.Load() != 1 || usage.Attempts != 1 || usage.ResponseBytes != int64(len(response)-1) {
			t.Fatalf("err=%v calls=%d usage=%+v", err, calls.Load(), usage)
		}
	})

	t.Run("per-call cap", func(t *testing.T) {
		oversized := append(bytes.Clone(response), bytes.Repeat([]byte{' '}, int(brokercontract.MaxAttachmentAuthorityCallBytesV3)+1-len(response))...)
		authority, calls := attachmentAdmissionAuthority(t, now, oversized)
		parent, _ := domain.NewReadBudget(2, 2*brokercontract.MaxAttachmentAuthorityCallBytesV3)
		_, err := authority.AdmitAttachment(domain.WithReadBudget(t.Context(), parent), fixture.admission)
		usage := parent.Usage()
		if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) || calls.Load() != 1 || usage.Attempts != 1 || usage.ResponseBytes != brokercontract.MaxAttachmentAuthorityCallBytesV3 {
			t.Fatalf("err=%v calls=%d usage=%+v", err, calls.Load(), usage)
		}
	})
}

func TestAuthorityAttachmentV3LeavesLegacyPostParentAccountingUnchanged(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	verified := authorityVerifiedContext(now)
	request := authorityAdmissionRequest(t, verified, now)
	decision := authorityAdmissionDecision(request, now.UnixMilli())
	response := mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeAdmissionDecisionV1(decision) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(response) }))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	parent, _ := domain.NewReadBudget(0, 0)
	got, err := authority.Admit(domain.WithReadBudget(t.Context(), parent), request)
	if err != nil || got.Status != domain.BrokerDecisionAllowed || parent.Usage() != (domain.ReadBudgetUsage{}) {
		t.Fatalf("decision=%+v err=%v legacy_parent=%+v", got, err, parent.Usage())
	}
}

func TestAuthorityAttachmentAuthenticationV3ReusesV1WithFreshNonceAndParent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	issuer := strings.Repeat("a", 64)
	verified := authorityVerifiedContext(now)
	credential := []byte("synthetic-workload-credential")
	var calls atomic.Int32
	var nonces []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path != authenticatePath || request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer synthetic-server-credential" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request method=%s path=%s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		decoded, err := brokertransport.DecodeAuthenticationRequestV1(body)
		seenCredential := decoded.Credential()
		defer clear(seenCredential)
		if err != nil || !bytes.Equal(seenCredential, credential) {
			t.Errorf("authentication request=%+v err=%v", decoded, err)
		}
		nonces = append(nonces, decoded.Nonce)
		response := brokertransport.AuthenticationResponse{SchemaVersion: 1, Nonce: decoded.Nonce, CredentialSHA256: decoded.CredentialSHA256, IssuerSHA256: issuer, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(), Context: verified}
		encoded, encodeErr := brokertransport.EncodeAuthenticationResponseV1(response)
		if encodeErr != nil {
			t.Errorf("encode authentication response: %v", encodeErr)
			return
		}
		_, _ = writer.Write(encoded)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, issuer)
	authority.now = func() time.Time { return now }
	parent, _ := domain.NewReadBudget(2, 2*brokertransport.MaxAuthorityEnvelopeBytes)
	ctx := domain.WithReadBudget(t.Context(), parent)
	challenges := []brokertransport.AuthenticationChallenge{
		{Nonce: authorityNonce(), Audience: verified.Audience, BrokerID: verified.BrokerID},
		{Nonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{'z'}, 24)), Audience: verified.Audience, BrokerID: verified.BrokerID},
	}
	for _, challenge := range challenges {
		got, err := authority.AuthenticateAttachmentV3(ctx, credential, challenge)
		if err != nil || !reflect.DeepEqual(got.Context, verified) || !got.ReleaseDeadline.Equal(now.Add(5*time.Second)) {
			t.Fatalf("authentication=%+v err=%v", got, err)
		}
	}
	if calls.Load() != 2 || !reflect.DeepEqual(nonces, []string{challenges[0].Nonce, challenges[1].Nonce}) || parent.Usage().Attempts != 2 {
		t.Fatalf("calls=%d nonces=%v usage=%+v", calls.Load(), nonces, parent.Usage())
	}
}

func TestAuthorityAttachmentV3QueueAndCallerCancellationPreventLateStart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := newAuthorityAttachmentFixture(t, now)
	scheduler, err := httpx.NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	holderArrived := make(chan struct{}, 1)
	releaseHolder := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseHolder) })
	defer release()
	var attachmentCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/hold" {
			holderArrived <- struct{}{}
			select {
			case <-releaseHolder:
			case <-request.Context().Done():
			}
			_, _ = writer.Write([]byte(`{}`))
			return
		}
		attachmentCalls.Add(1)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthorityWithScheduler(t, server, strings.Repeat("a", 64), scheduler)
	holderBudget, _ := domain.NewReadBudget(1, 16)
	holderDone := make(chan error, 1)
	go func() {
		ctx := domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(t.Context(), holderBudget)))
		_, err := authority.client.DoBoundedResponse(ctx, http.MethodPost, "/hold", []byte(`{}`), nil, 16, 16)
		holderDone <- err
	}()
	waitAuthorityArrival(t, holderArrived, holderDone)

	parent, _ := domain.NewReadBudget(1, brokercontract.MaxAttachmentAuthorityCallBytesV3)
	ctx, cancel := context.WithTimeout(domain.WithReadBudget(t.Context(), parent), 100*time.Millisecond)
	defer cancel()
	_, callErr := authority.AdmitAttachment(ctx, fixture.admission)
	if !errors.Is(callErr, context.DeadlineExceeded) || attachmentCalls.Load() != 0 || parent.Usage() != (domain.ReadBudgetUsage{}) {
		t.Fatalf("err=%v calls=%d usage=%+v", callErr, attachmentCalls.Load(), parent.Usage())
	}
	release()
	if err := waitAuthorityResult(t, holderDone); err != nil {
		t.Fatalf("holder err=%v", err)
	}
}

func TestAuthorityAttachmentV3CancellationReachesArrivedRequest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := newAuthorityAttachmentFixture(t, now)
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		arrived <- struct{}{}
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	defer close(release)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := authority.AdmitAttachment(ctx, fixture.admission)
		done <- err
	}()
	waitAuthorityArrival(t, arrived, done)
	cancel()
	if err := waitAuthorityResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation err=%v", err)
	}
}

func TestAuthorityAttachmentV3TransportFailuresAreSingleAttemptAndContentFree(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := newAuthorityAttachmentFixture(t, now)
	for _, test := range []struct {
		name   string
		status int
		drop   bool
	}{
		{name: "redirect", status: http.StatusTemporaryRedirect},
		{name: "rate", status: http.StatusTooManyRequests},
		{name: "unavailable", status: http.StatusServiceUnavailable},
		{name: "drop", drop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if test.drop {
					panic(http.ErrAbortHandler)
				}
				writer.Header().Set("Location", "/private-authority-uri-canary")
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, "private-authority-body-canary")
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			_, err := authority.AdmitAttachment(t.Context(), fixture.admission)
			if err == nil || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
			var apiError *httpx.APIError
			if errors.As(err, &apiError) {
				t.Fatal("raw authority response remained reachable")
			}
			assertAuthorityErrorContentFree(t, err, server.URL, brokertransport.AuthorizeAttachmentAdmissionPathV3, "private-authority-uri-canary", "private-authority-body-canary")
		})
	}
}

func attachmentAdmissionAuthority(t *testing.T, now time.Time, response []byte) (*Authority, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = writer.Write(response)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	authority.now = func() time.Time { return now }
	return authority, &calls
}
