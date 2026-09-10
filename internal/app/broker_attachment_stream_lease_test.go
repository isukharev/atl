package app

import (
	"bytes"
	"testing"
	"time"
)

func TestBrokerAttachmentStreamWallRollbackCannotRenewReleaseAuthority(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
	operation := fixture.start(t, 7)
	candidate, err := operation.ReadCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(4 * time.Second)
	operation.mu.Lock()
	observed := operation.currentMillis()
	operation.mu.Unlock()
	fresh := fixture.fresh(t)
	fixture.clock.Advance(-10 * time.Second)
	if _, err := operation.AuthorizeRelease(fresh, BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes}); err == nil {
		t.Fatal("backwards wall clock renewed release authority")
	}
	if operation.lastMillis != observed || operation.state != brokerAttachmentStreamClosed {
		t.Fatalf("last=%d want=%d state=%d", operation.lastMillis, observed, operation.state)
	}
}

func TestBrokerAttachmentStreamExpiredFlushCannotCommitOrClearTransferredLines(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
	operation := fixture.start(t, 7)
	candidate, err := operation.ReadCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := operation.AuthorizeRelease(fixture.fresh(t), BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes})
	if err != nil {
		t.Fatal(err)
	}
	lines := cloneAttachmentLines(authorized.Lines)
	prior := operation.priorReleaseSHA256
	fixture.clock.Advance(6 * time.Second)
	if complete, err := operation.CommitRelease(candidate.ID, attachmentAppLineHashes(t, authorized.Lines)); err == nil || complete {
		t.Fatalf("expired commit complete=%t err=%v", complete, err)
	}
	if operation.priorReleaseSHA256 != prior || operation.committedReleaseCount != 0 || operation.committedBytes != 0 || operation.state != brokerAttachmentStreamClosed {
		t.Fatal("expired commit advanced state")
	}
	for index := range lines {
		if !bytes.Equal(lines[index], authorized.Lines[index]) {
			t.Fatal("close cleared a line already transferred to the server")
		}
	}
}
