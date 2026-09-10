package brokerserver

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

const (
	maxAttachmentCredentialPatternBytes = 128 << 10
	maxAttachmentCredentialPatternStore = 512 << 10
	attachmentCredentialScannerLabel    = "Broker attachment credential scanner"
)

// AttachmentCredentialScanner performs bounded checks for the credential
// representations protected by one attachment stream. It does not retain its
// input buffers and may be checked and closed concurrently.
type AttachmentCredentialScanner struct {
	mu        sync.RWMutex
	patterns  [][]byte
	lookahead int
	closed    bool
}

// NewAttachmentCredentialScanner clones the configured credentials and the
// held workload credential. The parent guard and all input buffers must remain
// open, stable, and unmodified for the duration of this call. The returned
// scanner is independent, so the inputs and parent may be cleared afterward.
// CredentialGuard.Close must not run concurrently with this constructor.
func (g *CredentialGuard) NewAttachmentCredentialScanner(heldWorkload []byte) (*AttachmentCredentialScanner, error) {
	if g == nil || g.credentials == nil || len(g.credentials) > maxGuardCredentials || len(heldWorkload) < brokertransport.MinWorkloadCredentialBytes || len(heldWorkload) > brokertransport.MaxWorkloadCredentialBytes {
		return nil, attachmentCredentialScannerUsageError()
	}
	scanner := &AttachmentCredentialScanner{patterns: make([][]byte, 0, 5*(len(g.credentials)+1))}
	valid := false
	defer func() {
		if !valid {
			scanner.clear()
		}
	}()
	totalRaw := 0
	for _, credential := range g.credentials {
		if len(credential) < minGuardCredentialBytes || len(credential) > maxGuardCredentialBytes || totalRaw > maxGuardCredentialBytes-len(credential) {
			return nil, attachmentCredentialScannerUsageError()
		}
		totalRaw += len(credential)
		if err := scanner.addCredential(credential); err != nil {
			return nil, err
		}
	}
	if err := scanner.addCredential(heldWorkload); err != nil {
		return nil, err
	}
	if len(scanner.patterns) == 0 {
		return nil, attachmentCredentialScannerUsageError()
	}
	valid = true
	return scanner, nil
}

// LookaheadBytes returns the longest protected pattern minus one. The future
// stream owner retains this many native bytes before publishing any frame.
func (s *AttachmentCredentialScanner) LookaheadBytes() (int, error) {
	if s == nil {
		return 0, attachmentCredentialScannerCheckError()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || len(s.patterns) == 0 {
		return 0, attachmentCredentialScannerCheckError()
	}
	return s.lookahead, nil
}

// CheckNativeWindow synchronously inspects a borrowed native-byte window. The
// caller owns retention and must never pass more than one frame plus lookahead.
func (s *AttachmentCredentialScanner) CheckNativeWindow(window []byte) error {
	if s == nil {
		return attachmentCredentialScannerCheckError()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || len(s.patterns) == 0 {
		return attachmentCredentialScannerCheckError()
	}
	maximum := int(brokercontract.MaxAttachmentDecodedFrameBytesV3) + s.lookahead
	if len(window) > maximum {
		return attachmentCredentialScannerUsageError()
	}
	return s.check(window)
}

// CheckSerializedLine synchronously inspects a borrowed, already serialized
// line. Protocol-specific codecs retain responsibility for their smaller caps.
func (s *AttachmentCredentialScanner) CheckSerializedLine(line []byte) error {
	if s == nil {
		return attachmentCredentialScannerCheckError()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || len(s.patterns) == 0 {
		return attachmentCredentialScannerCheckError()
	}
	// The codec caps JSON bytes; publication adds one NDJSON delimiter.
	if int64(len(line)) > brokercontract.MaxAttachmentDataLineBytesV3+1 {
		return attachmentCredentialScannerUsageError()
	}
	return s.check(line)
}

// Close waits for active checks, clears owned patterns, and rejects later use.
func (s *AttachmentCredentialScanner) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.clear()
	s.closed = true
}

func (*AttachmentCredentialScanner) String() string   { return attachmentCredentialScannerLabel }
func (*AttachmentCredentialScanner) GoString() string { return attachmentCredentialScannerLabel }

// Format prevents alternate formatting verbs from traversing private pattern
// storage. Type and pointer formatting may still expose only type/address.
func (*AttachmentCredentialScanner) Format(state fmt.State, verb rune) {
	value := attachmentCredentialScannerLabel
	if verb == 'q' {
		value = strconv.Quote(value)
	}
	_, _ = io.WriteString(state, value)
}

func (s *AttachmentCredentialScanner) addCredential(credential []byte) error {
	if err := visitOwnedCredentialRepresentations(credential, s.addPattern); err != nil {
		return attachmentCredentialScannerUsageError()
	}
	return nil
}

func (s *AttachmentCredentialScanner) addPattern(pattern []byte) error {
	if len(pattern) == 0 || len(pattern) > maxAttachmentCredentialPatternBytes {
		return attachmentCredentialScannerUsageError()
	}
	for _, existing := range s.patterns {
		if bytes.Equal(existing, pattern) {
			clear(pattern)
			return nil
		}
	}
	stored := 0
	for _, existing := range s.patterns {
		stored += len(existing)
	}
	if stored > maxAttachmentCredentialPatternStore-len(pattern) {
		return attachmentCredentialScannerUsageError()
	}
	s.patterns = append(s.patterns, pattern)
	if len(pattern)-1 > s.lookahead {
		s.lookahead = len(pattern) - 1
	}
	return nil
}

func (s *AttachmentCredentialScanner) check(value []byte) error {
	for _, pattern := range s.patterns {
		if bytes.Contains(value, pattern) {
			return attachmentCredentialScannerCheckError()
		}
	}
	return nil
}

func (s *AttachmentCredentialScanner) clear() {
	clearCredentialPatterns(s.patterns)
	s.patterns = nil
	s.lookahead = 0
}

func attachmentCredentialScannerUsageError() error {
	return fmt.Errorf("%w: invalid Broker attachment credential scanner input", domain.ErrUsage)
}

func attachmentCredentialScannerCheckError() error {
	return fmt.Errorf("%w: Broker attachment credential scan failed", domain.ErrCheckFailed)
}
