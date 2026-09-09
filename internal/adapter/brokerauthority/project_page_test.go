package brokerauthority

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestAuthorityProjectPageUsesExactV2RoutesAndCodecs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	admission, admissionDecision := authorityProjectPageAdmissionFixture(t, now, domain.BrokerDecisionAllowed)
	qualification, qualificationDecision := authorityProjectPageQualificationFixture(t, now)
	operation, operationDecision := authorityProjectPageOperationFixture(t, now)
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer synthetic-server-credential" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %s %s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case projectPageAdmissionPath:
			decoded, err := brokercontract.DecodeProjectPageAdmissionRequestV2(body)
			if err != nil || !reflect.DeepEqual(decoded, admission) {
				t.Errorf("admission=%+v err=%v", decoded, err)
			}
			encoded, _ := brokercontract.EncodeProjectPageAdmissionDecisionV2(admissionDecision)
			_, _ = writer.Write(encoded)
		case projectPageQualificationPath:
			decoded, err := brokercontract.DecodeProjectPageQualificationRequestV2(body)
			if err != nil || !reflect.DeepEqual(decoded, qualification) {
				t.Errorf("qualification=%+v err=%v", decoded, err)
			}
			encoded, _ := brokercontract.EncodeProjectPageQualificationDecisionV2(qualificationDecision)
			_, _ = writer.Write(encoded)
		case projectPageOperationPath:
			decoded, err := brokercontract.DecodeProjectPageOperationAuthorizationRequestV2(body)
			if err != nil || !reflect.DeepEqual(decoded, operation) {
				t.Errorf("operation=%+v err=%v", decoded, err)
			}
			encoded, _ := brokercontract.EncodeProjectPageOperationDecisionV2(operationDecision)
			_, _ = writer.Write(encoded)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	authority.now = func() time.Time { return now }

	gotAdmission, admissionErr := authority.AdmitProjectPage(t.Context(), admission)
	gotQualification, qualificationErr := authority.AuthorizeProjectPageQualification(t.Context(), qualification)
	gotOperation, operationErr := authority.AuthorizeProjectPage(t.Context(), operation)
	if admissionErr != nil || qualificationErr != nil || operationErr != nil ||
		brokercontract.ValidateProjectPageAdmissionDecisionV2(gotAdmission, admission, now.UnixMilli()) != nil ||
		brokercontract.ValidateProjectPageQualificationDecisionV2(gotQualification, qualification, now.UnixMilli()) != nil ||
		brokercontract.ValidateProjectPageOperationDecisionV2(gotOperation, operation, now.UnixMilli()) != nil {
		t.Fatalf("decisions=%+v/%+v/%+v errors=%v/%v/%v", gotAdmission, gotQualification, gotOperation, admissionErr, qualificationErr, operationErr)
	}
	if !reflect.DeepEqual(paths, []string{projectPageAdmissionPath, projectPageQualificationPath, projectPageOperationPath}) {
		t.Fatalf("paths=%v", paths)
	}
}

func TestAuthorityProjectPageReturnsDenialForAppValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, decision := authorityProjectPageAdmissionFixture(t, now, domain.BrokerDecisionDenied)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		encoded, _ := brokercontract.EncodeProjectPageAdmissionDecisionV2(decision)
		_, _ = writer.Write(encoded)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	got, err := authority.AdmitProjectPage(t.Context(), request)
	if err != nil || got.Status != domain.BrokerDecisionDenied || got.Reason != domain.BrokerReasonDenied {
		t.Fatalf("decision=%+v err=%v", got, err)
	}
	if err := brokercontract.ValidateProjectPageAdmissionDecisionV2(got, request, now.UnixMilli()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("app validation err=%v", err)
	}
}

func TestAuthorityProjectPageSeparatesValidWrongBindingFromMalformedDecision(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, decision := authorityProjectPageAdmissionFixture(t, now, domain.BrokerDecisionAllowed)
	for _, test := range []struct {
		name      string
		response  func() []byte
		wantError bool
	}{
		{name: "valid wrong binding", response: func() []byte {
			changed := decision
			changed.RequestSHA256 = strings.Repeat("f", 64)
			changed.DecisionSHA256 = ""
			encoded, err := brokercontract.EncodeProjectPageAdmissionDecisionV2(changed)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "malformed", wantError: true, response: func() []byte {
			encoded, _ := brokercontract.EncodeProjectPageAdmissionDecisionV2(decision)
			return bytes.Replace(encoded, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(test.response())
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			got, err := authority.AdmitProjectPage(t.Context(), request)
			if test.wantError {
				if err == nil {
					t.Fatal("malformed authority decision was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := brokercontract.ValidateProjectPageAdmissionDecisionV2(got, request, now.UnixMilli()); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("wrong binding reached app validation as err=%v", err)
			}
		})
	}
}

func TestAuthorityProjectPageTransportFailuresAreSingleAttemptAndContentFree(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, decision := authorityProjectPageAdmissionFixture(t, now, domain.BrokerDecisionAllowed)
	for _, test := range []struct {
		name          string
		status        int
		body          func() []byte
		wantExhausted bool
	}{
		{name: "absent route", status: http.StatusNotFound, body: func() []byte { return []byte("private-authority-canary") }},
		{name: "transient", status: http.StatusServiceUnavailable, body: func() []byte { return []byte("private-authority-canary") }},
		{name: "redirect", status: http.StatusTemporaryRedirect, body: func() []byte { return []byte("private-authority-canary") }},
		{name: "oversize", status: http.StatusOK, wantExhausted: true, body: func() []byte {
			encoded, _ := brokercontract.EncodeProjectPageAdmissionDecisionV2(decision)
			return append(encoded, bytes.Repeat([]byte(" "), int(maxAuthorityDecisionBytes)+1)...)
		}},
		{name: "oversize failure", status: http.StatusServiceUnavailable, wantExhausted: true, body: func() []byte {
			return bytes.Repeat([]byte(" "), int(maxAuthorityDecisionBytes)+1)
		}},
		{name: "malformed", status: http.StatusOK, body: func() []byte {
			encoded, _ := brokercontract.EncodeProjectPageAdmissionDecisionV2(decision)
			return append(encoded, []byte(`{"private":"private-authority-canary"}`)...)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				calls.Add(1)
				defer incoming.Body.Close()
				if _, err := io.Copy(io.Discard, incoming.Body); err != nil {
					t.Errorf("read submitted authority request: %v", err)
					return
				}
				if test.name == "redirect" {
					writer.Header().Set("Location", "/private-fallback")
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write(test.body())
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			_, err := authority.AdmitProjectPage(t.Context(), request)
			if err == nil || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
			if test.wantExhausted && !errors.Is(err, domain.ErrReadResponseBudgetExhausted) {
				t.Fatalf("oversize err=%v", err)
			}
			assertAuthorityErrorContentFree(t, err, server.URL, projectPageAdmissionPath, "private-authority-canary")
		})
	}
}

func TestAuthorityProjectPageCancellationReachesArrivedRequest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, _ := authorityProjectPageAdmissionFixture(t, now, domain.BrokerDecisionAllowed)
	arrived := make(chan struct{}, 1)
	releaseServer := make(chan struct{})
	defer close(releaseServer)
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, incoming *http.Request) {
		arrived <- struct{}{}
		select {
		case <-incoming.Context().Done():
		case <-releaseServer:
		}
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := authority.AdmitProjectPage(ctx, request)
		done <- err
	}()
	waitAuthorityArrival(t, arrived, done)
	cancel()
	if err := waitAuthorityResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation err=%v", err)
	}
}

func TestAuthorityProjectPageFiveSecondDeadlineAndParentClipUseSchedulerBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, _ := authorityProjectPageAdmissionFixture(t, now, domain.BrokerDecisionAllowed)
	scheduler, err := httpx.NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	holderArrived := make(chan struct{}, 1)
	releaseHolder := make(chan struct{})
	var projectPageCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.URL.Path == "/hold" {
			holderArrived <- struct{}{}
			select {
			case <-releaseHolder:
			case <-incoming.Context().Done():
			}
			_, _ = writer.Write([]byte(`{}`))
			return
		}
		projectPageCalls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthorityWithScheduler(t, server, strings.Repeat("a", 64), scheduler)
	budget, err := domain.NewReadBudget(1, 16)
	if err != nil {
		t.Fatal(err)
	}
	holderDone := make(chan error, 1)
	holderContext, cancelHolder := context.WithTimeout(t.Context(), 9*time.Second)
	defer cancelHolder()
	go func() {
		ctx := domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(holderContext, budget)))
		_, holdErr := authority.client.DoBoundedResponse(ctx, http.MethodPost, "/hold", []byte(`{}`), nil, 16, 16)
		holderDone <- holdErr
	}()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHolder) }) }
	defer release()
	waitAuthorityArrival(t, holderArrived, holderDone)

	started := time.Now()
	if _, err := authority.AdmitProjectPage(t.Context(), request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("five-second bound err=%v", err)
	}
	if elapsed := time.Since(started); elapsed < authorityCallTimeout-time.Second || elapsed > authorityCallTimeout+2*time.Second {
		t.Fatalf("authority bound elapsed=%s", elapsed)
	}

	parent, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started = time.Now()
	if _, err := authority.AdmitProjectPage(parent, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("parent clip err=%v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("parent deadline was not clipped: %s", elapsed)
	}
	if projectPageCalls.Load() != 0 {
		t.Fatalf("blocked authority calls reached server: %d", projectPageCalls.Load())
	}
	release()
	if err := waitAuthorityResult(t, holderDone); err != nil {
		t.Fatalf("holder err=%v", err)
	}
}

func assertAuthorityErrorContentFree(t testing.TB, err error, values ...string) {
	t.Helper()
	formatted := []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprint(errors.Unwrap(err))}
	for _, value := range values {
		for _, text := range formatted {
			if value != "" && strings.Contains(text, value) {
				t.Fatalf("unsafe authority error=%q", text)
			}
		}
	}
}

func waitAuthorityArrival(t testing.TB, arrived <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-arrived:
	case err := <-done:
		t.Fatalf("request ended before server arrival: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("request did not arrive within the test bound")
	}
}

func waitAuthorityResult(t testing.TB, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(8 * time.Second):
		t.Fatal("request did not finish within the test bound")
		return nil
	}
}

func authorityProjectPageAdmissionFixture(t testing.TB, now time.Time, status domain.BrokerDecisionStatus) (domain.BrokerProjectPageAdmissionRequestV2, domain.BrokerProjectPageAdmissionDecisionV2) {
	t.Helper()
	verified := authorityVerifiedContext(now)
	definition, ok := brokercontract.DefinitionV2(domain.BrokerOperationJiraProjectIssuePageRead, brokercontract.ProjectPageOperationVersion)
	if !ok {
		t.Fatal("missing project-page definition")
	}
	semantic := domain.BrokerProjectPageRequestV2{
		SchemaVersion: brokercontract.ExecutionSchemaVersionV2, Operation: domain.BrokerOperationJiraProjectIssuePageRead,
		OperationVersion: brokercontract.ProjectPageOperationVersion, RequestID: "request-page-1",
		Features:  append([]string(nil), definition.Definition.RequiredFeatures...),
		Expect:    domain.BrokerRequestExpectations{ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision},
		Arguments: domain.BrokerProjectPageArguments{ProjectKey: "EXAMPLE", Fields: []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldSummary}, StartAt: 0, MaxResults: 1},
	}
	argumentsSHA256, err := brokercontract.ProjectPageArgumentsSHA256V2(semantic)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.BrokerProjectPageAdmissionRequestV2{
		Context: verified, Operation: semantic.Operation, OperationVersion: semantic.OperationVersion, RequestID: semantic.RequestID,
		Features: semantic.Features, Arguments: semantic.Arguments, ArgumentsSHA256: argumentsSHA256, DeadlineMillis: now.Add(30 * time.Second).UnixMilli(),
	}
	requestSHA256, err := brokercontract.ProjectPageAdmissionRequestSHA256V2(request)
	if err != nil {
		t.Fatal(err)
	}
	core := authorityProjectPageDecisionCore(t, request.Context, requestSHA256, "admission-page-1", now, status)
	encoded, err := brokercontract.EncodeProjectPageAdmissionDecisionV2(domain.BrokerProjectPageAdmissionDecisionV2{BrokerDecisionCore: core})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := brokercontract.DecodeProjectPageAdmissionDecisionV2(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return request, decision
}

func authorityProjectPageQualificationFixture(t testing.TB, now time.Time) (domain.BrokerProjectPageQualificationRequestV2, domain.BrokerProjectPageQualificationDecisionV2) {
	t.Helper()
	admission, admissionDecision := authorityProjectPageAdmissionFixture(t, now, domain.BrokerDecisionAllowed)
	plan, err := brokercontract.NewProjectPageQualificationPlanV2(admission.ArgumentsSHA256)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.BrokerProjectPageQualificationRequestV2{Admission: admission, AdmissionDecision: admissionDecision, Plan: plan}
	requestSHA256, _ := brokercontract.ProjectPageQualificationRequestSHA256V2(request)
	admissionSHA256, _ := brokercontract.ProjectPageAdmissionRequestSHA256V2(admission)
	planSHA256, _ := brokercontract.ProjectPageQualificationPlanSHA256V2(plan)
	value := domain.BrokerProjectPageQualificationDecisionV2{
		BrokerDecisionCore:      authorityProjectPageDecisionCore(t, admission.Context, requestSHA256, "qualification-page-1", now, domain.BrokerDecisionAllowed),
		AdmissionRequestSHA256:  admissionSHA256,
		AdmissionDecisionSHA256: admissionDecision.DecisionSHA256,
		PlanSHA256:              planSHA256,
	}
	encoded, err := brokercontract.EncodeProjectPageQualificationDecisionV2(value)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := brokercontract.DecodeProjectPageQualificationDecisionV2(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return request, decision
}

func authorityProjectPageOperationFixture(t testing.TB, now time.Time) (domain.BrokerProjectPageOperationAuthorizationRequestV2, domain.BrokerProjectPageOperationDecisionV2) {
	t.Helper()
	qualification, qualificationDecision := authorityProjectPageQualificationFixture(t, now)
	projectIdentity := domain.BrokerJiraProjectIdentityV2{ID: "7", Key: "EXAMPLE", Complete: true}
	projectSHA256, _ := brokercontract.JiraProjectIdentityEvidenceSHA256V2(projectIdentity)
	project := domain.BrokerQualifiedJiraProjectV2{ID: projectIdentity.ID, Key: projectIdentity.Key, IdentityProjectionSHA256: projectSHA256}
	issueIdentity := domain.BrokerJiraProjectPageIssueIdentityV2{ID: "3", Key: "EXAMPLE-3", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Complete: true}
	versionSHA256, projectionSHA256, _ := brokercontract.JiraProjectPageIssueIdentityEvidenceSHA256V2(issueIdentity)
	issue := domain.BrokerQualifiedJiraProjectPageIssueV2{ID: issueIdentity.ID, Key: issueIdentity.Key, ProjectID: issueIdentity.ProjectID, ProjectKey: issueIdentity.ProjectKey, Updated: issueIdentity.Updated, VersionEvidenceSHA256: versionSHA256, ProjectionSHA256: projectionSHA256}
	request := domain.BrokerProjectPageOperationAuthorizationRequestV2{
		QualificationRequest: qualification, QualificationDecision: qualificationDecision, Project: project,
		Issues: []domain.BrokerQualifiedJiraProjectPageIssueV2{issue},
		Page: domain.BrokerProjectPageEvidenceV2{
			RequestedStartAt: 0, RequestedMaxResults: 1, ReturnedStartAt: 0, ReturnedMaxResults: 1, Total: 1,
			CoordinateExhausted: true, OrderedIssueIDs: []string{"3"},
		},
		Effects: []domain.BrokerProjectPageEffectV2{
			{Kind: domain.BrokerEffectRead, Project: &project, Fields: []string{"id", "key", "pagination"}},
			{Kind: domain.BrokerEffectRead, Issue: &issue, Fields: []string{"id", "key", "project", "summary", "updated"}},
		},
	}
	requestSHA256, _ := brokercontract.ProjectPageOperationAuthorizationRequestSHA256V2(request)
	resourcesSHA256, _ := brokercontract.ProjectPageResourcesSHA256V2(project, request.Issues)
	pageSHA256, _ := brokercontract.ProjectPageEvidenceSHA256V2(request.Page)
	effectsSHA256, _ := brokercontract.ProjectPageEffectsSHA256V2(request.Effects)
	admission := qualification.Admission
	value := domain.BrokerProjectPageOperationDecisionV2{
		BrokerDecisionCore:          authorityProjectPageDecisionCore(t, admission.Context, requestSHA256, "operation-page-1", now, domain.BrokerDecisionAllowed),
		QualificationDecisionSHA256: qualificationDecision.DecisionSHA256, Operation: admission.Operation, OperationVersion: admission.OperationVersion,
		ArgumentsSHA256: admission.ArgumentsSHA256, ResourcesSHA256: resourcesSHA256, PageSHA256: pageSHA256, EffectsSHA256: effectsSHA256,
	}
	encoded, err := brokercontract.EncodeProjectPageOperationDecisionV2(value)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := brokercontract.DecodeProjectPageOperationDecisionV2(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return request, decision
}

func authorityProjectPageDecisionCore(t testing.TB, verified domain.BrokerVerifiedContext, requestSHA256, id string, now time.Time, status domain.BrokerDecisionStatus) domain.BrokerDecisionCore {
	t.Helper()
	contextSHA256, err := brokercontract.VerifiedContextSHA256(verified)
	if err != nil {
		t.Fatal(err)
	}
	reason := domain.BrokerReason("")
	if status == domain.BrokerDecisionDenied {
		reason = domain.BrokerReasonDenied
	}
	return domain.BrokerDecisionCore{
		Status: status, Reason: reason, DecisionID: id, AuthorityRevision: verified.AuthorityRevision,
		ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(),
	}
}
