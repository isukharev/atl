package app

import (
	"context"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerJiraCommentExecutionRetainsMonotonicParentAndPhaseBounds(t *testing.T) {
	started := time.Now()
	parentDeadline := time.UnixMilli(started.Add(2 * time.Second).UnixMilli())
	parent, cancelParent := context.WithDeadline(t.Context(), parentDeadline)
	defer cancelParent()
	definition, _ := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	execution, cancel, err := newBrokerJiraCommentExecution(parent, definition, nil, time.Now, started, time.UnixMilli(started.Add(time.Minute).UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if !execution.deadline.Equal(parentDeadline) || execution.deadline == execution.deadline.Round(0) { //nolint:staticcheck // Equality intentionally checks monotonic state.
		t.Fatalf("operation deadline=%v, parent=%v", execution.deadline, parentDeadline)
	}
	phaseMillis := started.Add(time.Second).UnixMilli()
	phase, cancelPhase, err := execution.businessContext(phaseMillis)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelPhase()
	deadline, ok := phase.Deadline()
	if !ok || deadline.UnixMilli() != phaseMillis || deadline == deadline.Round(0) { //nolint:staticcheck // Equality intentionally checks monotonic state.
		t.Fatalf("phase deadline=%v, expected millis=%d", deadline, phaseMillis)
	}
	cancelParent()
	closeout, cancelCloseout, err := execution.closeoutContext(phaseMillis)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelCloseout()
	closeoutDeadline, ok := closeout.Deadline()
	if !ok || closeout.Err() != nil || closeoutDeadline != deadline {
		t.Fatalf("closeout deadline=%v, phase=%v, error=%v", closeoutDeadline, deadline, closeout.Err())
	}
}
