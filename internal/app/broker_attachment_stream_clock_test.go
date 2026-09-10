package app

import (
	"context"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerAttachmentStreamSubmillisecondFreshLease(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
	operation := fixture.start(t, 7)
	defer operation.Close()
	// A 100us authentication call starts across the next millisecond tick.
	// Its five-second deadline is anchored BEFORE I/O, as in the real adapter.
	started := operation.startedAt.Truncate(time.Millisecond).Add(900 * time.Microsecond)
	authenticationStarted := started.Add(200 * time.Microsecond)
	observed := authenticationStarted.Add(100 * time.Microsecond)
	operation.startedAt, operation.lastMillis = started, started.UnixMilli()
	operation.now = func() time.Time { return observed }
	nowMillis := operation.currentMillis()
	if nowMillis != observed.UnixMilli() {
		t.Errorf("fractional carry lost: now=%d want=%d", nowMillis, observed.UnixMilli())
	}
	for _, test := range []struct {
		name     string
		deadline time.Time
		allowed  bool
	}{
		{"fresh", authenticationStarted.Add(5 * time.Second), true},
		{"expired", observed.Truncate(time.Millisecond), false},
		{"overlong", time.UnixMilli(observed.UnixMilli() + domain.BrokerMaxDecisionLeaseMillis + 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := brokercontract.MatchAttachmentReleaseContextV3(operation.anchor, operation.anchor.Context, nowMillis, test.deadline.UnixMilli())
			if test.allowed {
				if err != nil {
					t.Fatalf("valid pre-I/O lease rejected: %v", err)
				}
				return
			}
			if reason, ok := brokercontract.Reason(err); !ok || reason != domain.BrokerReasonDecisionExpired {
				t.Fatalf("invalid lease reason=%s classified=%t", reason, ok)
			}
		})
	}
}

func TestBrokerAttachmentStreamFractionalClockRollbackAndDeadlineProjection(t *testing.T) {
	started := time.Now()
	// Adjust with Add to retain the monotonic anchor while fixing its fraction.
	started = started.Add(900*time.Microsecond - time.Duration(started.Nanosecond()%int(time.Millisecond)))
	observed := started
	operation := BrokerJiraAttachmentStreamOperation{startedAt: started, lastMillis: started.UnixMilli(), now: func() time.Time { return observed }}
	want := started.UnixMilli()
	for _, elapsed := range []time.Duration{0, 200 * time.Microsecond, -time.Second, 1100 * time.Microsecond, 500 * time.Microsecond} {
		observed = started.Add(elapsed)
		want = max(want, observed.UnixMilli())
		if got := operation.currentMillis(); got != want {
			t.Fatalf("elapsed=%v now=%d want=%d", elapsed, got, want)
		}
	}
	for _, millis := range []int64{started.UnixMilli(), started.UnixMilli() + 1, started.UnixMilli() + 5000} {
		deadline := operation.timeForMillis(millis)
		if !deadline.Equal(time.UnixMilli(millis)) {
			t.Fatalf("deadline added a fractional extension: got=%v want=%v", deadline, time.UnixMilli(millis))
		}
	}
}

func TestBrokerAttachmentStreamOperationDeadlineKeepsExactMillisecondBound(t *testing.T) {
	fixture := newAttachmentAppFixture(t, nil, 0, 0)
	started := fixture.clock.Now().Truncate(time.Millisecond).Add(900 * time.Microsecond)
	for _, parentDuration := range []time.Duration{2*time.Second + 123*time.Microsecond, 90 * time.Second} {
		parent := started.Add(parentDuration)
		ctx, cancel := context.WithDeadline(t.Context(), parent)
		deadline, millis, err := brokerAttachmentOperationDeadline(ctx, started, fixture.verified)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		want := min(parent.UnixMilli(), started.UnixMilli()+domain.BrokerMaxOperationMillis)
		if millis != want || !deadline.Equal(time.UnixMilli(want)) || deadline.After(parent) {
			t.Fatalf("deadline=%v millis=%d want=%v parent=%v", deadline, millis, time.UnixMilli(want), parent)
		}
	}
}
