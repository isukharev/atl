//go:build linux

package agentskills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAdmitStructureRefusesSpecialFiles(t *testing.T) {
	root := writeAdmissionFixture(t)
	if err := unix.Mkfifo(filepath.Join(root, "named-pipe"), 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	result, err := AdmitStructure(admissionRequest(root))
	requireStructuralFinding(t, result, err, FindingSpecialFile,
		FindingPolicyRefusal, "skill/named-pipe")
}

func TestAdmitStructureClassifiesUnreadableRegularFile(t *testing.T) {
	root := writeAdmissionFixture(t)
	input := filepath.Join(root, "fixtures", "input.txt")
	if err := os.Chmod(input, 0); err != nil {
		t.Skipf("remove read permission: %v", err)
	}
	if probe, err := os.Open(input); err == nil {
		_ = probe.Close()
		t.Skip("host privileges bypass regular-file read permissions")
	}
	result, err := AdmitStructure(admissionRequest(root))
	requireStructuralFinding(t, result, err, FindingEntryUnreadable,
		FindingPolicyRefusal, "skill/fixtures/input.txt")
}

func TestAdmitStructureDoesNotFollowFileInsertedDuringSecondInventory(t *testing.T) {
	root := writeAdmissionFixture(t)
	input := filepath.Join(root, "fixtures", "input.txt")
	held := filepath.Join(root, "fixtures", "held.txt")
	var hookErr error
	result, err := admitStructureWithHooks(admissionRequest(root), structuralAdmissionHooks{
		skill: structuralCaptureHooks{beforeOpen: func(pass int, location string) {
			if pass != 2 || location != "fixtures/input.txt" || hookErr != nil {
				return
			}
			hookErr = os.Rename(input, held)
			if hookErr == nil {
				hookErr = os.Symlink("held.txt", input)
			}
		}},
	})
	if hookErr != nil {
		t.Skipf("symlink race fixture unavailable: %v", hookErr)
	}
	requireStructuralFinding(t, result, err, FindingEntryChanged,
		FindingSourceInstability, "skill/fixtures/input.txt")
}

func TestAdmitStructureDoesNotOpenFIFOInsertedImmediatelyBeforeRead(t *testing.T) {
	root := writeAdmissionFixture(t)
	input := filepath.Join(root, "fixtures", "input.txt")
	held := filepath.Join(root, "fixtures", "held.txt")
	replacement := filepath.Join(t.TempDir(), "replacement-fifo")
	if err := unix.Mkfifo(replacement, 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	watchFD, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Skipf("inotify unavailable: %v", err)
	}
	defer func() { _ = unix.Close(watchFD) }()
	if _, err := unix.InotifyAddWatch(watchFD, replacement, unix.IN_OPEN); err != nil {
		t.Skipf("watch FIFO open unavailable: %v", err)
	}
	var hookErr error
	result, err := admitStructureWithHooks(admissionRequest(root), structuralAdmissionHooks{
		skill: structuralCaptureHooks{beforeRead: func(pass int, location string) {
			if pass != 2 || location != "fixtures/input.txt" || hookErr != nil {
				return
			}
			hookErr = os.Rename(input, held)
			if hookErr == nil {
				hookErr = os.Rename(replacement, input)
			}
		}},
	})
	if hookErr != nil {
		t.Skipf("pre-read FIFO race fixture unavailable: %v", hookErr)
	}
	requireStructuralFinding(t, result, err, FindingEntryChanged,
		FindingSourceInstability, "skill/fixtures/input.txt")
	events := make([]byte, unix.SizeofInotifyEvent*2+256)
	if count, readErr := unix.Read(watchFD, events); count > 0 ||
		(readErr != nil && !errors.Is(readErr, unix.EAGAIN)) {
		t.Fatalf("raced FIFO received an open event: count=%d err=%v", count, readErr)
	}
}

func TestAdmitStructureDoesNotFollowDirectoryInsertedDuringSecondInventory(t *testing.T) {
	root := writeAdmissionFixture(t)
	fixtures := filepath.Join(root, "fixtures")
	held := filepath.Join(root, "held-fixtures")
	var hookErr error
	result, err := admitStructureWithHooks(admissionRequest(root), structuralAdmissionHooks{
		skill: structuralCaptureHooks{beforeOpen: func(pass int, location string) {
			if pass != 2 || location != "fixtures/input.txt" || hookErr != nil {
				return
			}
			hookErr = os.Rename(fixtures, held)
			if hookErr == nil {
				hookErr = os.Symlink("held-fixtures", fixtures)
			}
		}},
	})
	if hookErr != nil {
		t.Skipf("directory race fixture unavailable: %v", hookErr)
	}
	requireStructuralFinding(t, result, err, FindingEntryChanged,
		FindingSourceInstability, "skill/fixtures")
}

func TestAdmitStructureRefusesIntermediateRootSymlink(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	root := filepath.Join(real, "skill")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "SKILL.md"), admissionSkillDocument())
	writeFile(t, filepath.Join(root, "evals", "evals.json"), admissionEvalsDocument())
	writeFile(t, filepath.Join(root, "fixtures", "input.txt"), "synthetic\n")
	linkedParent := filepath.Join(parent, "linked")
	if err := os.Symlink("real", linkedParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := AdmitStructure(admissionRequest(filepath.Join(linkedParent, "skill")))
	requireStructuralFinding(t, result, err, FindingRootSymlink, FindingPolicyRefusal, "skill")
}

func TestAdmitStructureDetectsSelectedRootReplacement(t *testing.T) {
	root := writeAdmissionFixture(t)
	held := root + "-held"
	var hookErr error
	result, err := admitStructureWithHooks(admissionRequest(root), structuralAdmissionHooks{
		skill: structuralCaptureHooks{afterFirstInventory: func() {
			hookErr = os.Rename(root, held)
			if hookErr == nil {
				hookErr = os.Mkdir(root, 0o700)
			}
		}},
	})
	if hookErr != nil {
		t.Skipf("root replacement fixture unavailable: %v", hookErr)
	}
	requireStructuralFinding(t, result, err, FindingRootChanged,
		FindingSourceInstability, "skill")
}

func TestAdmitStructureRefusesInvalidFilesystemLocation(t *testing.T) {
	root := writeAdmissionFixture(t)
	name := string([]byte{'b', 'a', 'd', 0xff})
	if err := os.WriteFile(filepath.Join(root, name), []byte("synthetic\n"), 0o600); err != nil {
		t.Skipf("invalid UTF-8 filename unavailable: %v", err)
	}
	result, err := AdmitStructure(admissionRequest(root))
	requireStructuralFinding(t, result, err, FindingInvalidLocation,
		FindingPolicyRefusal, "skill")
}

func TestLinuxStructuralCaptureRefusesDescendantMountBoundary(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skipf("/proc is unavailable on this host: %v", err)
	}
	root, err := openLinuxRootNoFollow("/")
	if err != nil {
		t.Skipf("secure root open unavailable: %v", err)
	}
	defer func() { _ = root.Close() }()
	opened, err := openLinuxStructuralEntry(root, "proc", "proc")
	if err == nil {
		_ = opened.Close()
		t.Skip("/proc is not a distinct descendant mount on this host")
	}
	code, location := structuralCaptureError(err)
	if code != FindingMountBoundary || location != "proc" {
		t.Fatalf("mount finding = %q/%q, want %q/proc", code, location, FindingMountBoundary)
	}
}
