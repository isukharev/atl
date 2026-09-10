package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const brokerFractionalClockBaseMillis = int64(1_800_000_000_000)

func brokerFractionalClockStart(baseMillis int64) time.Time {
	return time.UnixMilli(baseMillis).Add(900 * time.Microsecond)
}

func brokerFractionalClockAt(start time.Time, millisAfterBase int64) time.Time {
	return start.Add(time.Duration(millisAfterBase-1)*time.Millisecond + 200*time.Microsecond)
}

func TestBrokerFractionalClocksCarryAndRetainRollback(t *testing.T) {
	start := brokerFractionalClockStart(brokerFractionalClockBaseMillis)
	advanced := brokerFractionalClockAt(start, 1)
	if advanced.UnixMilli() != brokerFractionalClockBaseMillis+1 {
		t.Fatalf("fixture did not cross millisecond: start=%v advanced=%v", start, advanced)
	}

	definition, ok := brokercontract.Definition(domain.BrokerOperationJiraIssueRead, 1)
	if !ok {
		t.Fatal("missing exact-read definition")
	}
	readCurrent := advanced
	readExecution, err := newBrokerReadExecution(t.Context(), definition, nil, func() time.Time { return readCurrent }, start)
	if err != nil {
		t.Fatal(err)
	}

	commentDefinition, ok := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	if !ok {
		t.Fatal("missing Jira comment definition")
	}
	commentCurrent := advanced
	commentExecution, cancel, err := newBrokerJiraCommentExecution(t.Context(), commentDefinition, nil, func() time.Time { return commentCurrent }, start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	observationCurrent := advanced
	observationClock := brokerOperationObservationClock{
		now:        func() time.Time { return observationCurrent },
		startedAt:  start,
		lastMillis: start.UnixMilli(),
	}

	for _, test := range []struct {
		name     string
		current  func() int64
		rollback func()
	}{
		{name: "exact and project-page reads", current: readExecution.currentMillis, rollback: func() { readCurrent = start.Add(-time.Hour) }},
		{name: "Jira comment", current: commentExecution.currentMillis, rollback: func() { commentCurrent = start.Add(-time.Hour) }},
		{name: "operation observation", current: observationClock.currentMillis, rollback: func() { observationCurrent = start.Add(-time.Hour) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, want := test.current(), advanced.UnixMilli(); got != want {
				t.Fatalf("fractional carry=%d want=%d", got, want)
			}
			test.rollback()
			if got, want := test.current(), advanced.UnixMilli(); got != want {
				t.Fatalf("rollback=%d want high-water=%d", got, want)
			}
		})
	}
}

func TestBrokerFractionalReleaseDeadlinesUseExactIntegerBounds(t *testing.T) {
	start := brokerFractionalClockStart(brokerFractionalClockBaseMillis)
	expiryMillis := brokerFractionalClockBaseMillis + 5_000
	want := time.UnixMilli(expiryMillis)

	readDefinition, ok := brokercontract.Definition(domain.BrokerOperationJiraIssueRead, 1)
	if !ok {
		t.Fatal("missing exact-read definition")
	}
	readExecution, err := newBrokerReadExecution(t.Context(), readDefinition, nil, func() time.Time { return start }, start)
	if err != nil {
		t.Fatal(err)
	}
	if got := readExecution.releaseDeadline(expiryMillis); !got.Equal(want) {
		t.Fatalf("read deadline=%v want=%v", got, want)
	}

	commentDefinition, ok := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	if !ok {
		t.Fatal("missing Jira comment definition")
	}
	commentExecution, cancel, err := newBrokerJiraCommentExecution(t.Context(), commentDefinition, nil, func() time.Time { return start }, start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if got := commentExecution.releaseDeadline(expiryMillis); !got.Equal(want) {
		t.Fatalf("comment deadline=%v want=%v", got, want)
	}
}

type brokerFractionalConfluenceReader struct {
	*brokerReadConfluenceStub
	afterBusiness func()
}

func (r *brokerFractionalConfluenceReader) ReadBrokerPage(ctx context.Context, id string, projection domain.BrokerConfluenceProjection) (domain.BrokerConfluencePageSnapshot, error) {
	snapshot, err := r.brokerReadConfluenceStub.ReadBrokerPage(ctx, id, projection)
	if err == nil && r.afterBusiness != nil {
		r.afterBusiness()
	}
	return snapshot, err
}

func TestBrokerFractionalExactReadsRefuseFinalPublicationAtExpiry(t *testing.T) {
	for _, backend := range []string{"jira", "confluence"} {
		for _, test := range []struct {
			name          string
			finalMillis   int64
			wantPublished bool
		}{
			{name: "positive pre-boundary", finalMillis: 4_999, wantPublished: true},
			{name: "exact expiry", finalMillis: 5_000},
		} {
			t.Run(backend+"/"+test.name, func(t *testing.T) {
				start := brokerFractionalClockStart(brokerFractionalClockBaseMillis)
				current := start
				request, verified, jira, confluence := brokerReadFixture(brokerFractionalClockBaseMillis)
				authorizer := &brokerReadAuthorizerStub{nowMillis: brokerFractionalClockBaseMillis, lease: 5 * time.Second}
				advance := func() { current = brokerFractionalClockAt(start, test.finalMillis) }

				var service *BrokerReadService
				if backend == "jira" {
					jira.afterBusiness = advance
					service = brokerReadTestService(authorizer, jira, confluence)
				} else {
					request.Operation = domain.BrokerOperationConfluencePageRead
					request.Arguments = domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionStorage}}
					verified.Backend = brokerConfluenceTestBackend()
					reader := &brokerFractionalConfluenceReader{brokerReadConfluenceStub: confluence, afterBusiness: advance}
					var err error
					service, err = NewBrokerReadService(
						authorizer,
						BrokerJiraIssueReader{Backend: brokerJiraTestBackend(), Reader: jira},
						BrokerConfluencePageReader{Backend: brokerConfluenceTestBackend(), Reader: reader},
					)
					if err != nil {
						t.Fatal(err)
					}
				}
				service.now = func() time.Time { return current }

				result, err := service.Execute(t.Context(), request, verified)
				if test.wantPublished {
					published := backend == "jira" && result.JiraIssue != nil || backend == "confluence" && result.ConfluencePage != nil
					wantDeadline := time.UnixMilli(brokerFractionalClockBaseMillis + 5_000)
					if err != nil || !published || !result.ReleaseDeadline.Equal(wantDeadline) {
						t.Fatalf("result=%+v err=%v deadline=%v want=%v", result, err, result.ReleaseDeadline, wantDeadline)
					}
					return
				}
				if err == nil || result != (BrokerExactReadResult{}) {
					t.Fatalf("expired result=%+v err=%v current=%v", result, err, current)
				}
			})
		}
	}
}

func TestBrokerFractionalProjectPageRefusesFinalPublicationAtExpiry(t *testing.T) {
	for _, test := range []struct {
		name          string
		finalMillis   int64
		wantPublished bool
	}{
		{name: "positive pre-boundary", finalMillis: 4_999, wantPublished: true},
		{name: "exact expiry", finalMillis: 5_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := brokerFractionalClockStart(brokerFractionalClockBaseMillis)
			current := start
			request, verified, reader := brokerProjectPageFixture(brokerFractionalClockBaseMillis)
			authorizer := &brokerProjectPageAuthorizerStub{nowMillis: brokerFractionalClockBaseMillis, lease: 5 * time.Second}
			reader.afterBusiness = func() { current = brokerFractionalClockAt(start, test.finalMillis) }
			service := brokerProjectPageTestService(authorizer, reader)
			service.now = func() time.Time { return current }

			result, err := service.Execute(t.Context(), request, verified)
			if test.wantPublished {
				wantDeadline := time.UnixMilli(brokerFractionalClockBaseMillis + 5_000)
				if err != nil || result.Page == nil || !result.ReleaseDeadline.Equal(wantDeadline) {
					t.Fatalf("result=%+v err=%v deadline=%v want=%v", result, err, result.ReleaseDeadline, wantDeadline)
				}
				return
			}
			if err == nil || result != (BrokerProjectPageResult{}) {
				t.Fatalf("expired result=%+v err=%v current=%v", result, err, current)
			}
		})
	}
}

func TestBrokerFractionalCommentRefusesDispatchAtExpiry(t *testing.T) {
	for _, test := range []struct {
		name        string
		claimMillis int64
		wantWrite   bool
	}{
		{name: "positive pre-boundary", claimMillis: 4_999, wantWrite: true},
		{name: "exact expiry", claimMillis: 5_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := brokerFractionalClockStart(brokerJiraCommentTestMillis)
			current := start
			service, authorizer, port, journal, _, events := brokerJiraCommentFixture(t, 1)
			authorizer.nowMillis = brokerJiraCommentTestMillis
			authorizer.leaseMillis = 5_000
			service.now = func() time.Time { return current }
			preview := previewBrokerJiraComment(t, service)
			journal.afterClaim = func() { current = brokerFractionalClockAt(start, test.claimMillis) }

			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			request.RequestID = "request-fractional-apply"
			result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
			if test.wantWrite {
				if err != nil || result.Comment.Status != "applied" || port.writeCalls != 1 || journal.record.Phase != domain.BrokerOperationApplied {
					t.Fatalf("result=%+v err=%v writes=%d record=%+v events=%v", result, err, port.writeCalls, journal.record, *events)
				}
				return
			}
			if err == nil || result != (BrokerJiraCommentResult{}) || port.writeCalls != 0 ||
				journal.record.Phase != domain.BrokerOperationNotApplied || !journal.record.DispatchClaimed || journal.record.ResultSHA256 == "" {
				t.Fatalf("expired result=%+v err=%v writes=%d record=%+v events=%v", result, err, port.writeCalls, journal.record, *events)
			}
		})
	}
}

func TestBrokerFractionalObservationRefusesPublicationAtWindow(t *testing.T) {
	for _, test := range []struct {
		name          string
		finalMillis   int64
		wantPublished bool
	}{
		{name: "positive pre-boundary", finalMillis: 2_999, wantPublished: true},
		{name: "exact expiry", finalMillis: 3_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := brokerFractionalClockStart(brokerObservationTestMillis)
			current := start
			record := brokerObservationRecord(t, domain.BrokerOperationApplied)
			journal := &brokerObservationJournal{record: record, afterLookup: func() { current = brokerFractionalClockAt(start, test.finalMillis) }}
			authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis}

			result, err := brokerObservationService(t, authorizer, journal, &current).Observe(t.Context(), brokerObservationRequest(), brokerObservationContext())
			if test.wantPublished {
				wantDeadline := time.UnixMilli(record.Reservation.ObservationUntilMillis)
				if err != nil || result.Outcome.ObservedAtMillis != brokerObservationTestMillis+test.finalMillis || !result.ReleaseDeadline.Equal(wantDeadline) {
					t.Fatalf("result=%+v err=%v deadline=%v want=%v", result, err, result.ReleaseDeadline, wantDeadline)
				}
				return
			}
			if !errors.Is(err, domain.ErrForbidden) || result != (BrokerOperationObservationResult{}) || journal.lookups != 1 {
				t.Fatalf("expired result=%+v err=%v lookups=%d current=%v", result, err, journal.lookups, current)
			}
		})
	}
}
