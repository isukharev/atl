package brokerserver

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func TestAttachmentCredentialScannerOwnsDeduplicatedRepresentations(t *testing.T) {
	configured := []byte(`synthetic-secret\"<>&token`)
	overlapping := []byte("synthetic-secret")
	urlDistinct := []byte("token-aa>")
	held := []byte("held/workload+token")
	guard, err := NewCredentialGuard(configured, bytes.Clone(configured), overlapping, urlDistinct)
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := guard.NewAttachmentCredentialScanner(held)
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Close()

	expected := uniqueSyntheticPatterns(configured, overlapping, urlDistinct, held)
	if len(scanner.patterns) != len(expected) {
		t.Fatalf("patterns=%d want=%d", len(scanner.patterns), len(expected))
	}
	for _, pattern := range expected {
		if !scannerHasPattern(scanner, pattern) {
			t.Fatalf("missing synthetic representation %q", pattern)
		}
		window := append([]byte("prefix-"), pattern...)
		window = append(window, "-suffix"...)
		if err := scanner.CheckNativeWindow(window); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("representation %q err=%v", pattern, err)
		}
		if err := scanner.CheckSerializedLine(window); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("serialized representation %q err=%v", pattern, err)
		}
	}
	escaped := []byte(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(string(configured)))
	if !bytes.Contains(escaped, []byte("<>&")) || bytes.Contains(escaped, []byte(`\u003c`)) || !scannerHasPattern(scanner, escaped) {
		t.Fatalf("JSON representation is not HTML-unescaped: %q", escaped)
	}
	if !scannerHasPattern(scanner, configured) || !scannerHasPattern(scanner, overlapping) {
		t.Fatal("overlapping credentials were incorrectly collapsed")
	}
	standard, rawURL := []byte(base64.RawStdEncoding.EncodeToString(urlDistinct)), []byte(base64.RawURLEncoding.EncodeToString(urlDistinct))
	if bytes.Equal(standard, rawURL) || !scannerHasPattern(scanner, standard) || !scannerHasPattern(scanner, rawURL) {
		t.Fatal("standard and URL-safe base64 representations were not independently protected")
	}
}

func TestAttachmentCredentialScannerChecksWithheldTailAndEncodedAlignments(t *testing.T) {
	credential := []byte("cross-window-token")
	guard, err := NewCredentialGuard(credential)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	scanner, err := guard.NewAttachmentCredentialScanner([]byte("held-workload-token"))
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Close()
	lookahead, err := scanner.LookaheadBytes()
	if err != nil {
		t.Fatal(err)
	}
	frameBytes := int(brokercontract.MaxAttachmentDecodedFrameBytesV3)
	window := bytes.Repeat([]byte{'z'}, frameBytes+lookahead)
	start := frameBytes - 4
	copy(window[start:], credential)
	if err := scanner.CheckNativeWindow(window[:frameBytes]); err != nil {
		t.Fatalf("credential prefix alone rejected: %v", err)
	}
	if err := scanner.CheckNativeWindow(window); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("cross-boundary credential err=%v", err)
	}

	encoded := []byte(base64.StdEncoding.EncodeToString(credential))
	for alignment := 0; alignment < 3; alignment++ {
		candidate := append(bytes.Repeat([]byte{'z'}, alignment), encoded...)
		if err := scanner.CheckNativeWindow(candidate); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("base64 alignment=%d err=%v", alignment, err)
		}
	}
}

func TestAttachmentCredentialScannerEnforcesDerivedAndInspectionBounds(t *testing.T) {
	configured := bytes.Repeat([]byte{'"'}, maxGuardCredentialBytes)
	held := bytes.Repeat([]byte{'\\'}, brokertransport.MaxWorkloadCredentialBytes)
	guard, err := NewCredentialGuard(configured)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	scanner, err := guard.NewAttachmentCredentialScanner(held)
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Close()
	lookahead, err := scanner.LookaheadBytes()
	if err != nil {
		t.Fatal(err)
	}
	if lookahead != maxAttachmentCredentialPatternBytes-1 {
		t.Fatalf("lookahead=%d", lookahead)
	}
	stored := 0
	for _, pattern := range scanner.patterns {
		stored += len(pattern)
		if len(pattern) > maxAttachmentCredentialPatternBytes {
			t.Fatalf("pattern bytes=%d", len(pattern))
		}
	}
	if stored > maxAttachmentCredentialPatternStore {
		t.Fatalf("stored bytes=%d", stored)
	}

	nativeMaximum := int(brokercontract.MaxAttachmentDecodedFrameBytesV3) + lookahead
	if err := scanner.CheckNativeWindow(bytes.Repeat([]byte{'z'}, nativeMaximum)); err != nil {
		t.Fatalf("exact native cap: %v", err)
	}
	if err := scanner.CheckNativeWindow(bytes.Repeat([]byte{'z'}, nativeMaximum+1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("native overflow err=%v", err)
	}
	if err := scanner.CheckSerializedLine(bytes.Repeat([]byte{'z'}, int(brokercontract.MaxAttachmentDataLineBytesV3))); err != nil {
		t.Fatalf("exact line cap: %v", err)
	}
	if err := scanner.CheckSerializedLine(bytes.Repeat([]byte{'z'}, int(brokercontract.MaxAttachmentDataLineBytesV3)+1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("line overflow err=%v", err)
	}
	if err := scanner.CheckNativeWindow(nil); err != nil {
		t.Fatalf("empty native window: %v", err)
	}
	if err := scanner.CheckSerializedLine(nil); err != nil {
		t.Fatalf("empty serialized line: %v", err)
	}
}

func TestAttachmentCredentialScannerRejectsInvalidDerivedStoresWithoutMutatingInputs(t *testing.T) {
	emptyGuard, err := NewCredentialGuard()
	if err != nil {
		t.Fatal(err)
	}
	emptyScanner, err := emptyGuard.NewAttachmentCredentialScanner(bytes.Repeat([]byte{'h'}, brokertransport.MinWorkloadCredentialBytes))
	if err != nil {
		t.Fatalf("empty configured set: %v", err)
	}
	emptyScanner.Close()
	emptyGuard.Close()

	for _, test := range []struct {
		name        string
		credentials [][]byte
		held        []byte
	}{
		{name: "empty held", held: nil},
		{name: "short held", held: []byte("short")},
		{name: "long held", held: bytes.Repeat([]byte{'h'}, brokertransport.MaxWorkloadCredentialBytes+1)},
		{name: "pattern overflow", credentials: [][]byte{bytes.Repeat([]byte{0}, maxGuardCredentialBytes)}, held: []byte("held-workload-token")},
		{name: "store overflow", credentials: [][]byte{bytes.Repeat([]byte{1}, 20<<10), bytes.Repeat([]byte{2}, 20<<10), bytes.Repeat([]byte{3}, 20<<10)}, held: []byte("held-workload-token")},
	} {
		t.Run(test.name, func(t *testing.T) {
			guard, err := NewCredentialGuard(test.credentials...)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			beforeCredentials := cloneByteSlices(test.credentials)
			beforeHeld := bytes.Clone(test.held)
			scanner, scanErr := guard.NewAttachmentCredentialScanner(test.held)
			if scanner != nil || !errors.Is(scanErr, domain.ErrUsage) {
				t.Fatalf("scanner=%v err=%v", scanner, scanErr)
			}
			if !equalByteSlices(test.credentials, beforeCredentials) || !bytes.Equal(test.held, beforeHeld) {
				t.Fatal("constructor modified borrowed input")
			}
			if bytes.Contains([]byte(scanErr.Error()), []byte("held-workload-token")) {
				t.Fatalf("error exposed credential: %v", scanErr)
			}
		})
	}
}

func TestAttachmentCredentialScannerClonesClearsClosesAndFormatsSafely(t *testing.T) {
	configured := []byte("configured-private-canary")
	held := []byte("held-private-canary")
	guard, err := NewCredentialGuard(configured)
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := guard.NewAttachmentCredentialScanner(held)
	if err != nil {
		t.Fatal(err)
	}
	retained := cloneByteSlices(scanner.patterns)
	owned := append([][]byte(nil), scanner.patterns...)
	clear(configured)
	clear(held)
	guard.Close()
	if err := scanner.CheckSerializedLine([]byte("prefix-configured-private-canary-suffix")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("independent scanner err=%v", err)
	}

	formatted := fmt.Sprintf("%v|%+v|%#v|%s|%q|%d", scanner, scanner, scanner, scanner, scanner, scanner)
	for _, private := range append(retained, []byte("configured-private-canary"), []byte("held-private-canary")) {
		if len(private) > 0 && bytes.Contains([]byte(formatted), private) {
			t.Fatalf("format exposed private pattern: %q", formatted)
		}
	}
	if !strings.Contains(formatted, attachmentCredentialScannerLabel) {
		t.Fatalf("format=%q", formatted)
	}

	scanner.Close()
	scanner.Close()
	for _, pattern := range owned {
		if !bytes.Equal(pattern, make([]byte, len(pattern))) {
			t.Fatal("Close did not clear an owned pattern")
		}
	}
	if _, err := scanner.LookaheadBytes(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("closed lookahead err=%v", err)
	}
	if err := scanner.CheckNativeWindow(nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("closed native check err=%v", err)
	}
	if err := scanner.CheckSerializedLine(nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("closed line check err=%v", err)
	}
	if _, err := guard.NewAttachmentCredentialScanner([]byte("held-workload-token")); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("closed parent err=%v", err)
	}
	var nilScanner *AttachmentCredentialScanner
	nilScanner.Close()
	if _, err := nilScanner.LookaheadBytes(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("nil lookahead err=%v", err)
	}
}

func TestCredentialRepresentationVisitorClearsRejectedOwnedPattern(t *testing.T) {
	stop := errors.New("synthetic stop")
	var accepted, rejected []byte
	calls := 0
	err := visitOwnedCredentialRepresentations([]byte("synthetic-private-canary"), func(pattern []byte) error {
		calls++
		if calls == 1 {
			accepted = pattern
			return nil
		}
		rejected = pattern
		return stop
	})
	defer clear(accepted)
	if !errors.Is(err, stop) || len(accepted) == 0 || len(rejected) == 0 {
		t.Fatalf("calls=%d accepted=%d rejected=%d err=%v", calls, len(accepted), len(rejected), err)
	}
	if !bytes.Equal(rejected, make([]byte, len(rejected))) {
		t.Fatal("visitor rejection did not clear its owned pattern")
	}
}

func TestAttachmentCredentialScannerZeroValueFailsClosed(t *testing.T) {
	var scanner AttachmentCredentialScanner
	if _, err := scanner.LookaheadBytes(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("zero-value lookahead err=%v", err)
	}
	if err := scanner.CheckNativeWindow([]byte("unprotected")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("zero-value native check err=%v", err)
	}
	if err := scanner.CheckSerializedLine([]byte("unprotected")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("zero-value line check err=%v", err)
	}
	scanner.Close()
}

func TestAttachmentCredentialScannerCheckAndCloseAreConcurrent(t *testing.T) {
	guard, err := NewCredentialGuard([]byte("configured-private-canary"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	scanner, err := guard.NewAttachmentCredentialScanner([]byte("held-private-canary"))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	const workerCount, iterations = 8, 100
	var workers sync.WaitGroup
	var checked sync.WaitGroup
	checked.Add(workerCount)
	errorsSeen := make(chan error, workerCount*(2+iterations*3))
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if err := scanner.CheckNativeWindow([]byte("clean")); err != nil {
				errorsSeen <- err
			}
			if err := scanner.CheckSerializedLine([]byte("configured-private-canary")); !errors.Is(err, domain.ErrCheckFailed) {
				errorsSeen <- fmt.Errorf("pre-close credential check did not reject: %v", err)
			}
			checked.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				for _, scanErr := range []error{scanner.CheckNativeWindow([]byte("clean")), scanner.CheckSerializedLine([]byte("configured-private-canary"))} {
					if scanErr != nil && !errors.Is(scanErr, domain.ErrCheckFailed) {
						errorsSeen <- scanErr
					}
				}
				if _, lookaheadErr := scanner.LookaheadBytes(); lookaheadErr != nil && !errors.Is(lookaheadErr, domain.ErrCheckFailed) {
					errorsSeen <- lookaheadErr
				}
			}
		}()
	}
	close(start)
	checked.Wait()
	scanner.Close()
	workers.Wait()
	close(errorsSeen)
	for scanErr := range errorsSeen {
		t.Fatalf("unexpected concurrent error: %v", scanErr)
	}
}

func uniqueSyntheticPatterns(credentials ...[]byte) [][]byte {
	var result [][]byte
	for _, credential := range credentials {
		escaped := []byte(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(string(credential)))
		for _, pattern := range [][]byte{
			bytes.Clone(credential),
			[]byte(base64.StdEncoding.EncodeToString(credential)),
			[]byte(base64.RawStdEncoding.EncodeToString(credential)),
			[]byte(base64.RawURLEncoding.EncodeToString(credential)),
			escaped,
		} {
			found := false
			for _, existing := range result {
				found = found || bytes.Equal(existing, pattern)
			}
			if !found {
				result = append(result, pattern)
			}
		}
	}
	return result
}

func scannerHasPattern(scanner *AttachmentCredentialScanner, pattern []byte) bool {
	for _, existing := range scanner.patterns {
		if bytes.Equal(existing, pattern) {
			return true
		}
	}
	return false
}

func cloneByteSlices(values [][]byte) [][]byte {
	result := make([][]byte, len(values))
	for index := range values {
		result[index] = bytes.Clone(values[index])
	}
	return result
}

func equalByteSlices(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !bytes.Equal(left[index], right[index]) {
			return false
		}
	}
	return true
}
