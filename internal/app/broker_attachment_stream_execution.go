package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type brokerAttachmentStreamState uint8

const (
	brokerAttachmentStreamStarting brokerAttachmentStreamState = iota + 1
	brokerAttachmentStreamReady
	brokerAttachmentStreamCandidate
	brokerAttachmentStreamAuthorized
	brokerAttachmentStreamComplete
	brokerAttachmentStreamClosed
)

type brokerAttachmentCandidateState struct {
	id     uint64
	length int
	digest [sha256.Size]byte
	eof    bool
}

type brokerAttachmentTailState struct {
	length int
	digest [sha256.Size]byte
}

type brokerAttachmentTentativeState struct {
	candidateID      uint64
	cumulativeBytes  int64
	cumulativeSHA256 string
	cumulativeState  []byte
	chunkCount       int
	tail             brokerAttachmentTailState
	lineSHA256s      []string
	releaseDecision  string
	deadlineMillis   int64
	terminal         bool
}

type BrokerJiraAttachmentStreamOperation struct {
	mu     sync.Mutex
	base   context.Context
	cancel context.CancelFunc
	now    func() time.Time

	startedAt      time.Time
	lastMillis     int64
	deadlineMillis int64

	authorizer       domain.BrokerAttachmentAuthorizerV3
	jira             domain.BrokerJiraAttachmentPortV3
	budgets          *BrokerAttachmentExecutionBudgets
	definition       domain.BrokerAttachmentOperationDefinitionV3
	scannerTailBytes int

	handle domain.BrokerJiraAttachmentOpenHandleV3
	body   io.ReadCloser
	state  brokerAttachmentStreamState

	anchor                domain.BrokerAttachmentStreamAnchorV3
	manifest              domain.BrokerAttachmentManifestCoreV3
	manifestSHA256        string
	snapshot              domain.BrokerJiraAttachmentSnapshotV3
	plan                  domain.BrokerAttachmentMetadataPlanV3
	priorReleaseSHA256    string
	committedBytes        int64
	committedSHA256       string
	committedHashState    []byte
	committedChunkCount   int
	committedReleaseCount int
	expectedTail          brokerAttachmentTailState
	sourceBytes           int64
	sourceEOF             bool
	nextCandidateID       uint64
	candidate             brokerAttachmentCandidateState
	tentative             brokerAttachmentTentativeState
	closeErr              error
}

type BrokerAttachmentReadCandidate struct {
	ID    uint64
	Bytes []byte
	EOF   bool
}

func (BrokerAttachmentReadCandidate) String() string   { return "Broker attachment read candidate" }
func (BrokerAttachmentReadCandidate) GoString() string { return "Broker attachment read candidate" }
func (BrokerAttachmentReadCandidate) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Broker attachment read candidate")
}

type BrokerAttachmentFreshAuthorization struct {
	Context         domain.BrokerVerifiedContext
	ReleaseDeadline time.Time
}

func (BrokerAttachmentFreshAuthorization) String() string {
	return "Broker attachment fresh authorization"
}
func (BrokerAttachmentFreshAuthorization) GoString() string {
	return "Broker attachment fresh authorization"
}
func (BrokerAttachmentFreshAuthorization) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Broker attachment fresh authorization")
}

type BrokerAttachmentReleaseSelection struct {
	CandidateID  uint64
	Payload      []byte
	RetainedTail []byte
}

func (BrokerAttachmentReleaseSelection) String() string { return "Broker attachment release selection" }
func (BrokerAttachmentReleaseSelection) GoString() string {
	return "Broker attachment release selection"
}
func (BrokerAttachmentReleaseSelection) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Broker attachment release selection")
}

type BrokerAttachmentAuthorizedRelease struct {
	CandidateID   uint64
	Lines         [][]byte
	FlushNotAfter time.Time
}

func (BrokerAttachmentAuthorizedRelease) String() string {
	return "Broker attachment authorized release"
}
func (BrokerAttachmentAuthorizedRelease) GoString() string {
	return "Broker attachment authorized release"
}
func (BrokerAttachmentAuthorizedRelease) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Broker attachment authorized release")
}

func (*BrokerJiraAttachmentStreamOperation) String() string   { return brokerAttachmentStateLabel }
func (*BrokerJiraAttachmentStreamOperation) GoString() string { return brokerAttachmentStateLabel }
func (*BrokerJiraAttachmentStreamOperation) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, brokerAttachmentStateLabel)
}

func (o *BrokerJiraAttachmentStreamOperation) ReadCandidate(retainedTail []byte) (BrokerAttachmentReadCandidate, error) {
	if o == nil {
		return BrokerAttachmentReadCandidate{}, brokerAttachmentStreamError(domain.ErrCheckFailed)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state != brokerAttachmentStreamReady || !o.matchesExpectedTail(retainedTail) {
		return BrokerAttachmentReadCandidate{}, o.failLocked(domain.ErrCheckFailed)
	}
	window := append(make([]byte, 0, int(brokercontract.MaxAttachmentDecodedFrameBytesV3)+o.scannerTailBytes), retainedTail...)
	if o.sourceEOF {
		return o.installCandidate(window, true)
	}
	// Even a completely full final window must prove EOF before its last data
	// release. Otherwise a zero-lookahead caller needs an invalid extra terminal
	// release after already committing all declared bytes.
	for len(window) < cap(window) || o.sourceBytes == o.snapshot.DeclaredSize {
		remaining := o.snapshot.DeclaredSize - o.sourceBytes
		if remaining < 0 {
			clear(window)
			return BrokerAttachmentReadCandidate{}, o.failLocked(domain.ErrCheckFailed)
		}
		if remaining == 0 {
			var probe [1]byte
			n, readErr := o.body.Read(probe[:])
			if n < 0 || n > len(probe) || n > 0 {
				clear(window)
				return BrokerAttachmentReadCandidate{}, o.failLocked(domain.ErrCheckFailed)
			}
			if errors.Is(readErr, io.EOF) {
				o.sourceEOF = true
				break
			}
			if readErr != nil {
				clear(window)
				return BrokerAttachmentReadCandidate{}, o.failLocked(readErr)
			}
			clear(window)
			return BrokerAttachmentReadCandidate{}, o.failLocked(io.ErrNoProgress)
		}
		readBytes := min(int64(cap(window)-len(window)), remaining)
		start := len(window)
		window = window[:start+int(readBytes)]
		n, readErr := o.body.Read(window[start:])
		if n < 0 || n > int(readBytes) {
			clear(window)
			return BrokerAttachmentReadCandidate{}, o.failLocked(domain.ErrCheckFailed)
		}
		window = window[:start+n]
		o.sourceBytes += int64(n)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			clear(window)
			return BrokerAttachmentReadCandidate{}, o.failLocked(readErr)
		}
		if errors.Is(readErr, io.EOF) {
			if o.sourceBytes != o.snapshot.DeclaredSize {
				clear(window)
				return BrokerAttachmentReadCandidate{}, o.failLocked(io.ErrUnexpectedEOF)
			}
			o.sourceEOF = true
			break
		}
		if n == 0 {
			clear(window)
			return BrokerAttachmentReadCandidate{}, o.failLocked(io.ErrNoProgress)
		}
	}
	return o.installCandidate(window, o.sourceEOF)
}

func (o *BrokerJiraAttachmentStreamOperation) installCandidate(window []byte, eof bool) (BrokerAttachmentReadCandidate, error) {
	if err := o.decisionError(o.deadlineMillis); err != nil {
		clear(window)
		return BrokerAttachmentReadCandidate{}, o.failLocked(err)
	}
	if eof && o.sourceBytes != o.snapshot.DeclaredSize {
		clear(window)
		return BrokerAttachmentReadCandidate{}, o.failLocked(io.ErrUnexpectedEOF)
	}
	o.nextCandidateID++
	o.candidate = brokerAttachmentCandidateState{id: o.nextCandidateID, length: len(window), digest: sha256.Sum256(window), eof: eof}
	o.state = brokerAttachmentStreamCandidate
	return BrokerAttachmentReadCandidate{ID: o.candidate.id, Bytes: window, EOF: eof}, nil
}

func (o *BrokerJiraAttachmentStreamOperation) AuthorizeRelease(fresh BrokerAttachmentFreshAuthorization, selection BrokerAttachmentReleaseSelection) (BrokerAttachmentAuthorizedRelease, error) {
	if o == nil {
		return BrokerAttachmentAuthorizedRelease{}, brokerAttachmentStreamError(domain.ErrCheckFailed)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state != brokerAttachmentStreamCandidate || selection.CandidateID != o.candidate.id || !o.validSelection(selection) {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(domain.ErrCheckFailed)
	}
	if o.budgets.authentication.Usage().Attempts != o.committedReleaseCount+2 {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(domain.ErrCheckFailed)
	}
	authenticationDeadline := o.startedAt.Add(fresh.ReleaseDeadline.Sub(o.startedAt))
	authenticationDeadlineMillis := authenticationDeadline.UnixMilli()
	if err := brokercontract.MatchAttachmentReleaseContextV3(o.anchor, fresh.Context, o.currentMillis(), authenticationDeadlineMillis); err != nil {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}

	terminal := o.candidate.eof && len(selection.RetainedTail) == 0
	coordinate := domain.BrokerAttachmentReleaseCoordinateV3{Kind: domain.BrokerAttachmentReleaseData, Index: o.committedChunkCount, Offset: o.committedBytes}
	if len(selection.Payload) == 0 {
		coordinate = domain.BrokerAttachmentReleaseCoordinateV3{Kind: domain.BrokerAttachmentReleaseTerminal, Index: 0, Offset: 0}
	}
	anchorSHA256, err := brokercontract.AttachmentStreamAnchorSHA256V3(o.anchor)
	if err != nil {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}
	qualification := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationRelease, Release: &domain.BrokerAttachmentReleaseQualificationV3{
		Context: fresh.Context, AnchorSHA256: anchorSHA256, PriorReleaseSHA256: o.priorReleaseSHA256, Coordinate: coordinate, Plan: o.plan,
	}}
	if err := brokercontract.ValidateAttachmentReleaseQualificationV3(qualification, o.anchor, o.priorReleaseSHA256, o.currentMillis(), authenticationDeadlineMillis); err != nil {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}
	qualificationStarted := o.currentMillis()
	decisionCtx, decisionCancel, err := o.categoryContext(o.budgets.decision, authenticationDeadlineMillis)
	if err != nil {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}
	qualificationDecision, callErr := o.authorizer.AuthorizeAttachmentQualification(decisionCtx, qualification)
	if callErr == nil {
		callErr = decisionCtx.Err()
	}
	decisionCancel()
	validationErr := brokercontract.ValidateAttachmentQualificationDecisionV3(qualificationDecision, qualification, o.currentMillis(), o.deadlineMillis)
	if callErr != nil || validationErr != nil {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(firstBrokerReadError(callErr, validationErr))
	}
	qualificationDeadline := min(brokerLocalDecisionDeadline(qualificationDecision.BrokerDecisionCore, qualificationStarted), authenticationDeadlineMillis)

	metadataCtx, metadataCancel, err := o.categoryContext(o.budgets.metadata, qualificationDeadline)
	if err != nil {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}
	snapshot, callErr := o.jira.QualifyBrokerJiraAttachment(metadataCtx, o.snapshot.IssueKey, o.snapshot.AttachmentID)
	if callErr == nil {
		callErr = metadataCtx.Err()
	}
	metadataCancel()
	if callErr != nil || !reflect.DeepEqual(snapshot, o.snapshot) {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(firstBrokerReadError(callErr, domain.ErrCheckFailed))
	}
	if err := brokercontract.ValidateAttachmentQualificationDecisionV3(qualificationDecision, qualification, o.currentMillis(), o.deadlineMillis); err != nil {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}

	facts, cumulativeState, cumulativeSHA256, cumulativeBytes, chunkCount, err := o.releaseFacts(selection.Payload, terminal)
	if err != nil {
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}
	releaseOperation := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationRelease, Release: &domain.BrokerAttachmentReleaseOperationV3{
		QualificationRequest: qualification, QualificationDecision: qualificationDecision, AnchorSHA256: anchorSHA256, PriorReleaseSHA256: o.priorReleaseSHA256,
		SnapshotSHA256: o.anchor.SnapshotSHA256, ManifestCoreSHA256: o.manifestSHA256, Facts: facts,
	}}
	operationStarted := o.currentMillis()
	decisionCtx, decisionCancel, err = o.categoryContext(o.budgets.decision, qualificationDeadline)
	if err != nil {
		clear(cumulativeState)
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}
	decision, callErr := o.authorizer.AuthorizeAttachmentOperation(decisionCtx, releaseOperation)
	if callErr == nil {
		callErr = decisionCtx.Err()
	}
	decisionCancel()
	validationErr = brokercontract.ValidateAttachmentOperationDecisionV3(decision, releaseOperation, o.currentMillis(), o.deadlineMillis)
	if callErr != nil || validationErr != nil {
		clear(cumulativeState)
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(firstBrokerReadError(callErr, validationErr))
	}
	operationDeadline := min(brokerLocalDecisionDeadline(decision.BrokerDecisionCore, operationStarted), qualificationDeadline)
	if err := o.decisionError(operationDeadline); err != nil {
		clear(cumulativeState)
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}
	lines, lineSHA256s, err := o.releaseLines(selection.Payload, facts, decision.DecisionSHA256, terminal)
	if err != nil {
		clear(cumulativeState)
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(err)
	}
	if !o.budgets.validUsage(o.committedReleaseCount+2, 5+2*(o.committedReleaseCount+1), 2+(o.committedReleaseCount+1), 1) {
		clear(cumulativeState)
		clearLines(lines)
		return BrokerAttachmentAuthorizedRelease{}, o.failLocked(domain.ErrCheckFailed)
	}
	o.tentative = brokerAttachmentTentativeState{
		candidateID: selection.CandidateID, cumulativeBytes: cumulativeBytes, cumulativeSHA256: cumulativeSHA256, cumulativeState: cumulativeState,
		chunkCount: chunkCount, tail: brokerAttachmentTailState{length: len(selection.RetainedTail), digest: sha256.Sum256(selection.RetainedTail)},
		lineSHA256s: append([]string(nil), lineSHA256s...), releaseDecision: decision.DecisionSHA256, deadlineMillis: operationDeadline, terminal: terminal,
	}
	o.state = brokerAttachmentStreamAuthorized
	return BrokerAttachmentAuthorizedRelease{CandidateID: selection.CandidateID, Lines: lines, FlushNotAfter: o.timeForMillis(operationDeadline)}, nil
}

func (o *BrokerJiraAttachmentStreamOperation) CommitRelease(candidateID uint64, exactEmittedLineSHA256s []string) (bool, error) {
	if o == nil {
		return false, brokerAttachmentStreamError(domain.ErrCheckFailed)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state != brokerAttachmentStreamAuthorized || candidateID != o.tentative.candidateID || !slices.Equal(exactEmittedLineSHA256s, o.tentative.lineSHA256s) {
		return false, o.failLocked(domain.ErrCheckFailed)
	}
	if err := o.decisionError(o.tentative.deadlineMillis); err != nil {
		return false, o.failLocked(err)
	}
	receipt, err := brokercontract.AttachmentReleaseReceiptSHA256V3(o.priorReleaseSHA256, o.tentative.releaseDecision, exactEmittedLineSHA256s)
	if err != nil {
		return false, o.failLocked(err)
	}
	clear(o.committedHashState)
	o.committedHashState = o.tentative.cumulativeState
	o.tentative.cumulativeState = nil
	o.committedBytes = o.tentative.cumulativeBytes
	o.committedSHA256 = o.tentative.cumulativeSHA256
	o.committedChunkCount = o.tentative.chunkCount
	o.expectedTail = o.tentative.tail
	o.priorReleaseSHA256 = receipt
	o.committedReleaseCount++
	complete := o.tentative.terminal
	o.candidate = brokerAttachmentCandidateState{}
	o.tentative = brokerAttachmentTentativeState{}
	if complete {
		o.state = brokerAttachmentStreamComplete
	} else {
		o.state = brokerAttachmentStreamReady
	}
	return complete, nil
}

func (o *BrokerJiraAttachmentStreamOperation) Close() error {
	if o == nil {
		return nil
	}
	if o.cancel != nil {
		o.cancel()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state == brokerAttachmentStreamClosed {
		return o.closeErr
	}
	o.closeLocked()
	return o.closeErr
}

func (o *BrokerJiraAttachmentStreamOperation) validSelection(selection BrokerAttachmentReleaseSelection) bool {
	if len(selection.Payload)+len(selection.RetainedTail) != o.candidate.length {
		return false
	}
	hasher := sha256.New()
	_, _ = hasher.Write(selection.Payload)
	_, _ = hasher.Write(selection.RetainedTail)
	if !bytes.Equal(hasher.Sum(nil), o.candidate.digest[:]) {
		return false
	}
	frame := int(brokercontract.MaxAttachmentDecodedFrameBytesV3)
	if !o.candidate.eof {
		return len(selection.Payload) == frame && len(selection.RetainedTail) == o.scannerTailBytes
	}
	if o.candidate.length > frame {
		return len(selection.Payload) == frame && len(selection.RetainedTail) == o.candidate.length-frame
	}
	return len(selection.Payload) == o.candidate.length && len(selection.RetainedTail) == 0
}

func (o *BrokerJiraAttachmentStreamOperation) releaseFacts(payload []byte, terminal bool) (domain.BrokerAttachmentReleaseFactsV3, []byte, string, int64, int, error) {
	if len(payload) == 0 {
		if !terminal || o.committedBytes != 0 || o.committedChunkCount != 0 || o.snapshot.DeclaredSize != 0 {
			return domain.BrokerAttachmentReleaseFactsV3{}, nil, "", 0, 0, domain.ErrCheckFailed
		}
		facts := domain.BrokerAttachmentReleaseTerminalFactsV3{ChunkCount: 0, TotalBytes: 0, WholeSHA256: brokercontract.AttachmentEmptyBodySHA256V3, EOFProven: true, Complete: true}
		return domain.BrokerAttachmentReleaseFactsV3{Kind: domain.BrokerAttachmentReleaseTerminal, Terminal: &facts}, append([]byte(nil), o.committedHashState...), o.committedSHA256, 0, 0, nil
	}
	cumulativeBytes := o.committedBytes + int64(len(payload))
	if cumulativeBytes > o.snapshot.DeclaredSize || cumulativeBytes > brokercontract.MaxAttachmentNativeBodyBytesV3 || o.committedChunkCount >= brokercontract.MaxAttachmentDataFramesV3 {
		return domain.BrokerAttachmentReleaseFactsV3{}, nil, "", 0, 0, domain.ErrCheckFailed
	}
	state, digest, err := extendBrokerAttachmentHash(o.committedHashState, payload)
	if err != nil {
		return domain.BrokerAttachmentReleaseFactsV3{}, nil, "", 0, 0, err
	}
	payloadDigest := sha256.Sum256(payload)
	chunkCount := o.committedChunkCount + 1
	data := domain.BrokerAttachmentDataReleaseV3{
		Index: o.committedChunkCount, Offset: o.committedBytes, DecodedBytes: int64(len(payload)), PayloadSHA256: hex.EncodeToString(payloadDigest[:]),
		CumulativeBytes: cumulativeBytes, CumulativeSHA256: digest, ResourceSHA256: o.anchor.ResourcesSHA256,
	}
	if terminal {
		if cumulativeBytes != o.snapshot.DeclaredSize {
			clear(state)
			return domain.BrokerAttachmentReleaseFactsV3{}, nil, "", 0, 0, domain.ErrCheckFailed
		}
		data.Terminal = &domain.BrokerAttachmentReleaseTerminalFactsV3{ChunkCount: chunkCount, TotalBytes: cumulativeBytes, WholeSHA256: digest, EOFProven: true, Complete: true}
	}
	return domain.BrokerAttachmentReleaseFactsV3{Kind: domain.BrokerAttachmentReleaseData, Data: &data}, state, digest, cumulativeBytes, chunkCount, nil
}

func (o *BrokerJiraAttachmentStreamOperation) releaseLines(payload []byte, facts domain.BrokerAttachmentReleaseFactsV3, decisionSHA256 string, terminal bool) ([][]byte, []string, error) {
	lines := make([][]byte, 0, 3)
	if o.committedReleaseCount == 0 {
		line, err := brokercontract.EncodeAttachmentManifestLineV3(domain.BrokerAttachmentManifestLineV3{Core: o.manifest, ManifestCoreSHA256: o.manifestSHA256, ReleaseDecisionSHA256: decisionSHA256})
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, line)
	}
	if len(payload) > 0 {
		data := facts.Data
		line, err := brokercontract.EncodeAttachmentDataLineV3(domain.BrokerAttachmentDataLineV3{
			SchemaVersion: brokercontract.ExecutionSchemaVersionV3, FrameVersion: brokercontract.AttachmentFrameVersionV3, Kind: domain.BrokerAttachmentReleaseData,
			StreamID: o.anchor.StreamID, Index: data.Index, Offset: data.Offset, DecodedBytes: data.DecodedBytes, PayloadBase64: base64.StdEncoding.EncodeToString(payload),
			PayloadSHA256: data.PayloadSHA256, CumulativeBytes: data.CumulativeBytes, CumulativeSHA256: data.CumulativeSHA256, ResourceSHA256: data.ResourceSHA256,
			PriorReleaseSHA256: o.priorReleaseSHA256, ReleaseDecisionSHA256: decisionSHA256,
		})
		if err != nil {
			clearLines(lines)
			return nil, nil, err
		}
		lines = append(lines, line)
	}
	if terminal {
		terminalFacts := facts.Terminal
		if facts.Data != nil {
			terminalFacts = facts.Data.Terminal
		}
		lineValue := domain.BrokerAttachmentTerminalLineV3{
			SchemaVersion: brokercontract.ExecutionSchemaVersionV3, FrameVersion: brokercontract.AttachmentFrameVersionV3, Kind: domain.BrokerAttachmentReleaseTerminal,
			StreamID: o.anchor.StreamID, ChunkCount: terminalFacts.ChunkCount, TotalBytes: terminalFacts.TotalBytes, DeclaredSize: o.snapshot.DeclaredSize,
			WholeSHA256: terminalFacts.WholeSHA256, PriorReleaseSHA256: o.priorReleaseSHA256, ReleaseDecisionSHA256: decisionSHA256, EOFProven: true, Complete: true,
		}
		if err := brokercontract.ValidateAttachmentTerminalLineForReleaseV3(lineValue, facts, o.snapshot.DeclaredSize, o.anchor.StreamID, o.priorReleaseSHA256, decisionSHA256); err != nil {
			clearLines(lines)
			return nil, nil, err
		}
		line, err := brokercontract.EncodeAttachmentTerminalLineV3(lineValue)
		if err != nil {
			clearLines(lines)
			return nil, nil, err
		}
		lines = append(lines, line)
	}
	lineSHA256s := make([]string, len(lines))
	for index, line := range lines {
		exact := make([]byte, len(line)+1)
		copy(exact, line)
		exact[len(line)] = '\n'
		digest, err := brokercontract.AttachmentExactLineSHA256V3(exact)
		clear(exact)
		if err != nil {
			clearLines(lines)
			return nil, nil, err
		}
		lineSHA256s[index] = digest
	}
	return lines, lineSHA256s, nil
}

func (o *BrokerJiraAttachmentStreamOperation) matchesExpectedTail(value []byte) bool {
	if o.expectedTail.length == 0 {
		return len(value) == 0
	}
	return len(value) == o.expectedTail.length && sha256.Sum256(value) == o.expectedTail.digest
}

func (o *BrokerJiraAttachmentStreamOperation) currentMillis() int64 {
	elapsed := o.now().Sub(o.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	o.lastMillis = max(o.lastMillis, o.startedAt.UnixMilli()+elapsed.Milliseconds())
	return o.lastMillis
}

func (o *BrokerJiraAttachmentStreamOperation) timeForMillis(value int64) time.Time {
	return o.startedAt.Add(time.Duration(value-o.startedAt.UnixMilli()) * time.Millisecond)
}

func (o *BrokerJiraAttachmentStreamOperation) decisionError(deadlineMillis int64) error {
	if err := o.base.Err(); err != nil {
		return err
	}
	if o.currentMillis() >= deadlineMillis || o.currentMillis() >= o.deadlineMillis {
		return context.DeadlineExceeded
	}
	return nil
}

func (o *BrokerJiraAttachmentStreamOperation) categoryContext(budget *domain.ReadBudget, deadlineMillis int64) (context.Context, context.CancelFunc, error) {
	if budget == nil {
		return nil, nil, domain.ErrCheckFailed
	}
	if err := o.decisionError(deadlineMillis); err != nil {
		return nil, nil, err
	}
	deadline := o.timeForMillis(min(deadlineMillis, o.deadlineMillis))
	ctx, cancel := context.WithDeadline(o.base, deadline)
	return domain.WithReadBudget(ctx, budget), cancel, nil
}

func (o *BrokerJiraAttachmentStreamOperation) fail(err error) error {
	return brokerAttachmentStreamError(err)
}

func (o *BrokerJiraAttachmentStreamOperation) failLocked(err error) error {
	o.closeLocked()
	return brokerAttachmentStreamError(err)
}

func (o *BrokerJiraAttachmentStreamOperation) closeUnlocked() {
	if o == nil {
		return
	}
	if o.cancel != nil {
		o.cancel()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closeLocked()
}

func (o *BrokerJiraAttachmentStreamOperation) closeLocked() {
	if o.state == brokerAttachmentStreamClosed {
		return
	}
	if o.cancel != nil {
		o.cancel()
	}
	var errs []error
	if o.body != nil {
		errs = append(errs, o.body.Close())
		o.body = nil
	}
	if o.handle != nil {
		errs = append(errs, o.handle.Close())
		o.handle = nil
	}
	clear(o.committedHashState)
	clear(o.tentative.cumulativeState)
	o.committedHashState = nil
	o.tentative = brokerAttachmentTentativeState{}
	o.candidate = brokerAttachmentCandidateState{}
	o.expectedTail = brokerAttachmentTailState{}
	o.state = brokerAttachmentStreamClosed
	for _, err := range errs {
		if err != nil && o.closeErr == nil {
			o.closeErr = brokerAttachmentStreamError(err)
		}
	}
}

func newBrokerAttachmentHashState() ([]byte, error) {
	hasher := sha256.New()
	marshaler, ok := hasher.(encoding.BinaryMarshaler)
	if !ok {
		return nil, domain.ErrCheckFailed
	}
	return marshaler.MarshalBinary()
}

func extendBrokerAttachmentHash(state, value []byte) ([]byte, string, error) {
	hasher := sha256.New()
	unmarshaler, ok := hasher.(encoding.BinaryUnmarshaler)
	if !ok || unmarshaler.UnmarshalBinary(state) != nil {
		return nil, "", domain.ErrCheckFailed
	}
	_, _ = hasher.Write(value)
	digest := hex.EncodeToString(hasher.Sum(nil))
	marshaler, ok := hasher.(encoding.BinaryMarshaler)
	if !ok {
		return nil, "", domain.ErrCheckFailed
	}
	next, err := marshaler.MarshalBinary()
	if err != nil {
		return nil, "", domain.ErrCheckFailed
	}
	return next, digest, nil
}

func clearLines(lines [][]byte) {
	for _, line := range lines {
		clear(line)
	}
}
