//go:build linux

package brokerjournal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestExplicitCreateReopenIdentityAndWriterLock(t *testing.T) {
	f := newFixture(t, Limits{})
	if _, err := Create(f.path, testIdentity(), Limits{}); !errors.Is(err, errUnavailable) {
		t.Fatalf("adopt existing=%v", err)
	}
	if _, err := Open(f.path, testIdentity(), Limits{}); !errors.Is(err, errUnavailable) {
		t.Fatalf("second writer=%v", err)
	}
	if _, err := Open(filepath.Join(filepath.Dir(f.path), "missing"), testIdentity(), Limits{}); !errors.Is(err, errUnavailable) {
		t.Fatalf("missing reopen=%v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.path), "missing")); !os.IsNotExist(err) {
		t.Fatal("reopen created missing state")
	}
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	wrong := testIdentity()
	wrong.BrokerSHA256 = digest([]byte("different epoch"))
	if _, err := Open(f.path, wrong, Limits{}); !errors.Is(err, errUnavailable) {
		t.Fatalf("wrong identity=%v", err)
	}
	if _, err := Open(f.path, testIdentity(), Limits{Records: 1}); !errors.Is(err, errUnavailable) {
		t.Fatalf("changed capacity=%v", err)
	}
}

func TestOwnershipControlWithOtherwiseValidMetadata(t *testing.T) {
	const owner = 1000
	for _, directory := range []bool{false, true} {
		stat := unix.Stat_t{Uid: owner, Mode: unix.S_IFREG | 0o600, Nlink: 1}
		if directory {
			stat.Mode, stat.Nlink = unix.S_IFDIR|0o700, 2
		}
		if !ownedStat(stat, owner, directory) {
			t.Fatal("valid ownership rejected")
		}
		stat.Uid++
		if ownedStat(stat, owner, directory) {
			t.Fatal("wrong owner accepted despite otherwise valid mode/type/link metadata")
		}
	}
}

func TestFilesystemControlsRejectValidRecords(t *testing.T) {
	cases := map[string]func(*testing.T, fixture, string){
		"root mode":     func(t *testing.T, f fixture, _ string) { must(t, os.Chmod(f.path, 0o750)) },
		"parent mode":   func(t *testing.T, f fixture, _ string) { must(t, os.Chmod(filepath.Dir(f.path), 0o750)) },
		"record mode":   func(t *testing.T, f fixture, id string) { must(t, os.Chmod(filepath.Join(f.path, id+".rec"), 0o640)) },
		"artifact mode": func(t *testing.T, f fixture, id string) { must(t, os.Chmod(filepath.Join(f.path, id+".art"), 0o644)) },
		"record hardlink": func(t *testing.T, f fixture, id string) {
			must(t, os.Link(filepath.Join(f.path, id+".rec"), filepath.Join(filepath.Dir(f.path), "hardlink")))
		},
		"identity hardlink": func(t *testing.T, f fixture, _ string) {
			must(t, os.Link(filepath.Join(f.path, "identity"), filepath.Join(filepath.Dir(f.path), "hardlink")))
		},
		"artifact hardlink": func(t *testing.T, f fixture, id string) {
			must(t, os.Link(filepath.Join(f.path, id+".art"), filepath.Join(filepath.Dir(f.path), "hardlink")))
		},
		"record symlink": func(t *testing.T, f fixture, id string) {
			path := filepath.Join(f.path, id+".rec")
			target := filepath.Join(filepath.Dir(f.path), "record")
			must(t, os.Rename(path, target))
			must(t, os.Symlink(target, path))
		},
		"root symlink": func(t *testing.T, f fixture, _ string) {
			target := f.path + "-held"
			must(t, os.Rename(f.path, target))
			must(t, os.Symlink(target, f.path))
		},
		"special file": func(t *testing.T, f fixture, id string) {
			path := filepath.Join(f.path, id+".art")
			must(t, os.Rename(path, filepath.Join(filepath.Dir(f.path), "saved-artifact")))
			must(t, unix.Mkfifo(path, 0o600))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, Limits{})
			r := admitted(t, f.journal)
			// Confirm valid bytes before changing only the intended filesystem
			// property. The oracle cannot pass via an unrelated parse failure.
			if _, err := f.journal.Lookup(context.Background(), r.Reservation.Owner, r.OperationID); err != nil {
				t.Fatal(err)
			}
			must(t, f.journal.Close())
			mutate(t, f, r.OperationID)
			if _, err := openJournal(f.path, testIdentity(), Limits{}, false, func() time.Time { return time.UnixMilli(testMillis) }); !errors.Is(err, errUnavailable) {
				t.Fatalf("filesystem control=%v", err)
			}
		})
	}
}

func TestPublicationPathReplacementPoisonsHeldWriter(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "parent"}[parent], func(t *testing.T) {
			f := newFixture(t, Limits{})
			r := admitted(t, f.journal)
			d := f.journal.disk.(*localDisk)
			swapped := false
			d.hook = func(point string) error {
				if point != "after_file_sync" || swapped {
					return nil
				}
				swapped = true
				path := f.path
				if parent {
					path = filepath.Dir(path)
				}
				if err := os.Rename(path, path+"-held"); err != nil {
					return err
				}
				return os.Mkdir(path, 0o700)
			}
			_, err := f.journal.ClaimDispatch(context.Background(), comparison(r))
			if !swapped || !errors.Is(err, errUnavailable) || !f.journal.failed {
				t.Fatalf("held identity control: swapped=%t err=%v", swapped, err)
			}
			if _, err := f.journal.ClaimDispatch(context.Background(), comparison(r)); !errors.Is(err, errUnavailable) {
				t.Fatalf("poisoned replay=%v", err)
			}
			// Restore this synthetic test's directory solely so TempDir cleanup
			// owns every test artifact. Production journal never renames/deletes.
			path := f.path
			if parent {
				path = filepath.Dir(path)
			}
			must(t, os.Remove(path))
			must(t, os.Rename(path+"-held", path))
		})
	}
}

func TestDurabilityFaultsNeverReturnFreshRights(t *testing.T) {
	for _, point := range []string{"allocate", "directory_sync", "before_write", "file_sync", "after_file_sync"} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t, Limits{})
			d := f.journal.disk.(*localDisk)
			triggered := false
			d.hook = func(at string) error {
				if at == point {
					triggered = true
					return unix.ENOSPC
				}
				return nil
			}
			r, err := f.journal.Reserve(context.Background(), testReservation())
			if !triggered || !errors.Is(err, errUnavailable) || r.OperationID != "" || !f.journal.failed {
				t.Fatalf("issuance fault=%v triggered=%t", err, triggered)
			}
			if _, err := f.journal.Reserve(context.Background(), testReservation()); !errors.Is(err, errUnavailable) {
				t.Fatalf("retry after poison=%v", err)
			}
		})
	}
	for _, point := range []string{"before_write", "file_sync", "after_file_sync"} {
		t.Run("terminal "+point, func(t *testing.T) {
			f := newFixture(t, Limits{})
			r := admitted(t, f.journal)
			r, err := f.journal.ClaimDispatch(context.Background(), comparison(r))
			must(t, err)
			f.journal.disk.(*localDisk).hook = func(at string) error {
				if at == point {
					return unix.ENOSPC
				}
				return nil
			}
			result, err := f.journal.Complete(context.Background(), comparison(r), appliedCompletion())
			if !errors.Is(err, errUnavailable) || result.OperationID != "" {
				t.Fatalf("terminal success escaped failed sync=%v", err)
			}
			if point != "before_write" {
				must(t, f.journal.Close())
				assertReopenUnavailable(t, f)
				return
			}
			j := reopen(t, f, Limits{})
			got, err := j.Lookup(context.Background(), r.Reservation.Owner, r.OperationID)
			must(t, err)
			if got.Phase != "outcome_unknown" && got.Phase != "applied" {
				t.Fatalf("ambiguous terminal=%s", got.Phase)
			}
			if _, err := j.ClaimDispatch(context.Background(), comparison(got)); !errors.Is(err, errConflict) {
				t.Fatalf("terminal replay=%v", err)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestExpiryAndCancellationDuringDispatchSyncConsumeClaim(t *testing.T) {
	for _, cancelDuringSync := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "cancellation"}[cancelDuringSync], func(t *testing.T) {
			f := newFixture(t, Limits{})
			r := admitted(t, f.journal)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.journal.disk.(*localDisk).hook = func(point string) error {
				if point == "after_file_sync" {
					if cancelDuringSync {
						cancel()
					} else {
						f.clock.Store(r.Reservation.OperationDeadlineMillis)
					}
				}
				return nil
			}
			_, err := f.journal.ClaimDispatch(ctx, comparison(r))
			want := errExpired
			if cancelDuringSync {
				want = errDenied
			}
			if !errors.Is(err, want) {
				t.Fatalf("late refusal=%v", err)
			}
			got, err := f.journal.Lookup(context.Background(), r.Reservation.Owner, r.OperationID)
			must(t, err)
			if !got.DispatchClaimed || got.Phase != "dispatching" {
				t.Fatal("late refusal lost durable claim")
			}
			if _, err := f.journal.ClaimDispatch(context.Background(), comparison(got)); !errors.Is(err, errConflict) {
				t.Fatalf("late-refusal replay=%v", err)
			}
		})
	}
}
