//go:build linux

package brokerjournal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func TestProcessKillDurabilityBoundaries(t *testing.T) {
	cases := []struct {
		point       string
		unavailable bool
		phase       domain.BrokerOperationPhase
		sends       int
	}{
		{"reservation_allocated", true, "", 0},
		{"reservation_indexed", true, "", 0},
		{"reservation_record_durable", true, "", 0},
		{"reservation_state_durable", false, "", 0},
		{"reservation_durable", false, "", 0},
		{"binding_record_durable", true, "", 0},
		{"binding_state_durable", false, "", 0},
		{"binding_durable", false, "", 0},
		{"artifact_durable", true, "", 0},
		{"admitted_durable", false, domain.BrokerOperationNotApplied, 0},
		{"admitted_record_durable", true, "", 0},
		{"admitted_state_durable", false, domain.BrokerOperationNotApplied, 0},
		{"dispatching_record_durable", true, "", 0},
		{"dispatching_state_durable", false, domain.BrokerOperationOutcomeUnknown, 0},
		{"dispatch_durable", false, domain.BrokerOperationOutcomeUnknown, 0},
		{"backend_accepted", false, domain.BrokerOperationOutcomeUnknown, 1},
		{"terminal_durable", false, domain.BrokerOperationApplied, 1},
		{"applied_record_durable", true, "", 1},
		{"applied_state_durable", false, domain.BrokerOperationApplied, 1},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			f := newFixture(t, Limits{})
			must(t, f.journal.Close())
			cmd := journalChild(t, f.path, tc.point)
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child did not hit kill boundary: %v", err)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("unexpected child termination: %v", err)
			}
			j, err := openJournal(f.path, testIdentity(), Limits{}, false, func() time.Time { return time.UnixMilli(testMillis) })
			if tc.unavailable {
				if !errors.Is(err, errUnavailable) {
					if j != nil {
						_ = j.Close()
					}
					t.Fatalf("partial publication became ready=%v", err)
				}
			} else {
				must(t, err)
				defer func() { _ = j.Close() }()
				if len(j.records) != 1 {
					t.Fatalf("records=%d", len(j.records))
				}
				for _, r := range j.records {
					if r.Phase != tc.phase {
						t.Fatalf("phase=%s want=%s", r.Phase, tc.phase)
					}
					if _, err := j.ClaimDispatch(context.Background(), comparison(r)); err == nil {
						t.Fatal("restart granted another send")
					}
					if r.Phase == domain.BrokerOperationOutcomeUnknown {
						fenced, err := j.Fenced(context.Background(), r.Intent.TargetSHA256)
						must(t, err)
						if !fenced {
							t.Fatal("unknown fence lost")
						}
					}
				}
			}
			counter, readErr := os.ReadFile(filepath.Join(filepath.Dir(f.path), "synthetic-counter"))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if len(counter) != tc.sends {
				t.Fatalf("synthetic sends=%d want=%d", len(counter), tc.sends)
			}
		})
	}
}

func TestSecondProcessCannotAcquireWriter(t *testing.T) {
	f := newFixture(t, Limits{})
	if err := journalChild(t, f.path, "lock_denied").Run(); err != nil {
		t.Fatalf("second process writer lock=%v", err)
	}
}

func journalChild(t *testing.T, path, point string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJournalProcessHelper$")
	cmd.Env = []string{"ATL_JOURNAL_TEST_CHILD=1", "ATL_JOURNAL_TEST_PATH=" + path, "ATL_JOURNAL_TEST_POINT=" + point}
	return cmd
}

func TestJournalProcessHelper(t *testing.T) {
	if os.Getenv("ATL_JOURNAL_TEST_CHILD") != "1" {
		return
	}
	path, point := os.Getenv("ATL_JOURNAL_TEST_PATH"), os.Getenv("ATL_JOURNAL_TEST_POINT")
	j, err := openJournal(path, testIdentity(), Limits{}, false, func() time.Time { return time.UnixMilli(testMillis) })
	if point == "lock_denied" {
		if !errors.Is(err, errUnavailable) {
			if j != nil {
				_ = j.Close()
			}
			t.Fatal("second writer acquired")
		}
		return
	}
	must(t, err)
	kill := func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal("test kill failed")
		}
	}
	j.hook = func(at string) error {
		if at == point {
			kill()
		}
		return nil
	}
	r := admitted(t, j)
	r, err = j.ClaimDispatch(context.Background(), comparison(r))
	must(t, err)
	// A bounded synthetic operation-specific send counter. This foundation
	// does not implement the later HTTP/CLI/authorization assembly oracle.
	counter, err := os.OpenFile(filepath.Join(filepath.Dir(path), "synthetic-counter"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	must(t, err)
	_, err = counter.Write([]byte{1})
	must(t, err)
	must(t, counter.Sync())
	must(t, counter.Close())
	if point == "backend_accepted" {
		kill()
	}
	_, err = j.Complete(context.Background(), comparison(r), appliedCompletion())
	must(t, err)
	t.Fatal("requested crash checkpoint was not reached")
}
