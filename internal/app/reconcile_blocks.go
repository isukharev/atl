package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

const nativeReconcileMaxAlignmentCells = 1_000_000

type nativeSemanticBlock struct {
	kind string
	hash string
}

type NativeReconcileBlock struct {
	State         string `json:"state"`
	Kind          string `json:"kind"`
	BaseStart     *int   `json:"base_start,omitempty"`
	BaseCount     int    `json:"base_count"`
	OursStart     *int   `json:"ours_start,omitempty"`
	OursCount     int    `json:"ours_count"`
	TheirsStart   *int   `json:"theirs_start,omitempty"`
	TheirsCount   int    `json:"theirs_count"`
	BaseSHA256    string `json:"base_sha256,omitempty"`
	OursSHA256    string `json:"ours_sha256,omitempty"`
	TheirsSHA256  string `json:"theirs_sha256,omitempty"`
	OursChanged   bool   `json:"ours_changed"`
	TheirsChanged bool   `json:"theirs_changed"`
	Converged     bool   `json:"converged,omitempty"`
}

type NativeReconcileBlockSummary struct {
	Total      int `json:"total"`
	Unchanged  int `json:"unchanged"`
	LocalOnly  int `json:"local_only"`
	RemoteOnly int `json:"remote_only"`
	Diverged   int `json:"diverged"`
}

func classifyNativeReconcileBlocks(base, ours, theirs []nativeSemanticBlock) ([]NativeReconcileBlock, NativeReconcileBlockSummary, error) {
	baseCells := uint64(len(base)) + 1
	cells := baseCells*(uint64(len(ours))+1) + baseCells*(uint64(len(theirs))+1)
	if cells > nativeReconcileMaxAlignmentCells {
		return nil, NativeReconcileBlockSummary{}, fmt.Errorf("%w: reconcile exceeds the %d-cell aggregate alignment safety bound", domain.ErrCheckFailed, nativeReconcileMaxAlignmentCells)
	}
	matchO := reconcileBlockLCS(base, ours)
	matchT := reconcileBlockLCS(base, theirs)
	var rows []NativeReconcileBlock
	prevB, prevO, prevT := -1, -1, -1
	emitRegion := func(nextB, nextO, nextT int) {
		bs, os, ts := base[prevB+1:nextB], ours[prevO+1:nextO], theirs[prevT+1:nextT]
		if len(bs)+len(os)+len(ts) == 0 {
			return
		}
		rows = append(rows, reconcileBlockRegion(prevB+1, prevO+1, prevT+1, bs, os, ts))
	}
	for bi := range base {
		if matchO[bi] < 0 || matchT[bi] < 0 {
			continue
		}
		emitRegion(bi, matchO[bi], matchT[bi])
		b, o, th := bi, matchO[bi], matchT[bi]
		rows = append(rows, NativeReconcileBlock{
			State: "unchanged", Kind: base[bi].kind, BaseStart: &b, BaseCount: 1, OursStart: &o, OursCount: 1, TheirsStart: &th, TheirsCount: 1,
			BaseSHA256: base[bi].hash, OursSHA256: ours[o].hash, TheirsSHA256: theirs[th].hash,
		})
		prevB, prevO, prevT = bi, o, th
	}
	emitRegion(len(base), len(ours), len(theirs))

	summary := NativeReconcileBlockSummary{Total: len(rows)}
	for _, row := range rows {
		switch row.State {
		case "unchanged":
			summary.Unchanged++
		case "local_only":
			summary.LocalOnly++
		case "remote_only":
			summary.RemoteOnly++
		case "diverged":
			summary.Diverged++
		}
	}
	return rows, summary, nil
}

func checkNativeReconcileLocalAlignment(base, ours uint64) error {
	// Charge the base/ours matrix and the irreducible one-column base/theirs
	// matrix before any remote read. The final exact total is checked again once
	// the remote block count is known.
	cells := (base+1)*(ours+1) + (base + 1)
	if cells > nativeReconcileMaxAlignmentCells {
		return fmt.Errorf("%w: local reconcile exceeds the %d-cell aggregate alignment safety bound", domain.ErrCheckFailed, nativeReconcileMaxAlignmentCells)
	}
	return nil
}

func reconcileBlockRegion(baseStart, oursStart, theirsStart int, base, ours, theirs []nativeSemanticBlock) NativeReconcileBlock {
	baseHash, oursHash, theirsHash := reconcileBlockSequenceHash(base), reconcileBlockSequenceHash(ours), reconcileBlockSequenceHash(theirs)
	classification := classifyReconcileHashes(baseHash, oursHash, theirsHash)
	row := NativeReconcileBlock{
		State: classification.State, Kind: reconcileBlockRegionKind(base, ours, theirs), BaseCount: len(base), OursCount: len(ours), TheirsCount: len(theirs),
		BaseSHA256: baseHash, OursSHA256: oursHash, TheirsSHA256: theirsHash,
		OursChanged: classification.OursChanged, TheirsChanged: classification.TheirsChanged, Converged: classification.Converged,
	}
	if len(base) > 0 {
		row.BaseStart = &baseStart
	}
	if len(ours) > 0 {
		row.OursStart = &oursStart
	}
	if len(theirs) > 0 {
		row.TheirsStart = &theirsStart
	}
	return row
}

func classifyReconcileHashes(baseHash, oursHash, theirsHash string) NativeReconcileClassification {
	oursChanged, theirsChanged := oursHash != baseHash, theirsHash != baseHash
	result := NativeReconcileClassification{OursChanged: oursChanged, TheirsChanged: theirsChanged}
	switch {
	case oursHash == theirsHash:
		result.State, result.Converged = "unchanged", oursChanged
	case !oursChanged:
		result.State = "remote_only"
	case !theirsChanged:
		result.State = "local_only"
	default:
		result.State, result.Conflict = "diverged", true
	}
	return result
}

func reconcileBlockSequenceHash(blocks []nativeSemanticBlock) string {
	h := sha256.New()
	for _, block := range blocks {
		_, _ = h.Write([]byte(block.kind))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(block.hash))
		_, _ = h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func reconcileBlockRegionKind(groups ...[]nativeSemanticBlock) string {
	kind := ""
	for _, blocks := range groups {
		for _, block := range blocks {
			if kind == "" {
				kind = block.kind
			} else if kind != block.kind {
				return "block_group"
			}
		}
	}
	if kind == "" {
		return "block_group"
	}
	return kind
}

func reconcileBlockLCS(base, side []nativeSemanticBlock) []int {
	match := make([]int, len(base))
	for i := range match {
		match[i] = -1
	}
	if len(base) == 0 || len(side) == 0 {
		return match
	}
	dp := make([][]int32, len(base)+1)
	for i := range dp {
		dp[i] = make([]int32, len(side)+1)
	}
	for i := len(base) - 1; i >= 0; i-- {
		for j := len(side) - 1; j >= 0; j-- {
			if base[i] == side[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	for i, j := 0, 0; i < len(base) && j < len(side); {
		switch {
		case base[i] == side[j] && dp[i][j] == dp[i+1][j+1]+1:
			match[i] = j
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return match
}
