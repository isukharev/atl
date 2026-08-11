package agentskills

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
)

type digestBuilder struct {
	hash interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
}

func newDigestBuilder(domain string) *digestBuilder {
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/agentskills/" + domain + "/v1\x00"))
	return &digestBuilder{hash: hash}
}

func (builder *digestBuilder) add(value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = builder.hash.Write(length[:])
	_, _ = builder.hash.Write(value)
}

func (builder *digestBuilder) addString(value string) { builder.add([]byte(value)) }

func (builder *digestBuilder) sum() string {
	return hex.EncodeToString(builder.hash.Sum(nil))
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validDigest(value string) bool {
	if len(value) != SHA256HexCharacters {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// Counts only flow here after their package bounds have been validated.
func countSlice[T any](values []T) uint32 {
	var count uint32
	for range values {
		count++
	}
	return count
}

func countMap[K comparable, V any](values map[K]V) uint32 {
	var count uint32
	for range values {
		count++
	}
	return count
}

func digestSnapshotFiles(domain string, files []SnapshotFile) string {
	ordered := append([]SnapshotFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	builder := newDigestBuilder(domain)
	for _, file := range ordered {
		builder.addString(file.Path)
		builder.add(file.Data)
	}
	return builder.sum()
}

type reportAccumulator struct {
	entries map[reportKey]uint32
}

type reportKey struct {
	code        ReportCode
	scope       string
	disposition Disposition
	blocking    bool
}

func (accumulator *reportAccumulator) add(code ReportCode, scope string, disposition Disposition, blocking bool) {
	accumulator.addCount(code, scope, disposition, blocking, 1)
}

func (accumulator *reportAccumulator) addCount(code ReportCode, scope string, disposition Disposition, blocking bool, count uint32) {
	if count == 0 {
		return
	}
	if accumulator.entries == nil {
		accumulator.entries = make(map[reportKey]uint32)
	}
	key := reportKey{code: code, scope: scope, disposition: disposition, blocking: blocking}
	accumulator.entries[key] += count
}

func (accumulator *reportAccumulator) report() Report {
	entries := make([]ReportEntry, 0, len(accumulator.entries))
	for key, count := range accumulator.entries {
		entries = append(entries, ReportEntry{
			Code: key.code, Scope: key.scope, Disposition: key.disposition,
			Count: count, BlocksExecution: key.blocking,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.Scope != right.Scope {
			return left.Scope < right.Scope
		}
		if left.Disposition != right.Disposition {
			return left.Disposition < right.Disposition
		}
		if left.BlocksExecution != right.BlocksExecution {
			return !left.BlocksExecution
		}
		return false
	})
	return Report{Entries: entries}
}
