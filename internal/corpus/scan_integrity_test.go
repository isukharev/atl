//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package corpus

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyMissingExpectedMemberReportsMembershipIntegrity(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, generation := sealTestGeneration(t, store, "payload", "")
	if err := generation.Close(); err != nil {
		t.Fatal(err)
	}
	memberPath := filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "item")
	if err := os.Remove(memberPath); err != nil {
		t.Fatal(err)
	}

	_, err := store.Verify(context.Background(), stage.ID())
	assertReason(t, err, ReasonMembership)
}

func TestCopyMemberMissingExpectedMemberReportsMembershipIntegrity(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, generation := sealTestGeneration(t, store, "payload", "")
	defer func() { _ = generation.Close() }()
	memberPath := filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "item")
	if err := os.Remove(memberPath); err != nil {
		t.Fatal(err)
	}

	_, err := generation.CopyMember(context.Background(), ServiceJira, "one", RoleNative, io.Discard)
	assertReason(t, err, ReasonMembership)
}

func TestCopyMemberPreservesCancellationAndIOClassifications(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	_, generation := sealTestGeneration(t, store, "payload", "")
	defer func() { _ = generation.Close() }()

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := generation.CopyMember(ctx, ServiceJira, "one", RoleNative, io.Discard)
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrIntegrity) {
			t.Fatalf("copy error = %v", err)
		}
	})

	t.Run("destination IO", func(t *testing.T) {
		_, err := generation.CopyMember(context.Background(), ServiceJira, "one", RoleNative, closedWriter{})
		assertReason(t, err, ReasonIO)
	})
}

type closedWriter struct{}

func (closedWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
