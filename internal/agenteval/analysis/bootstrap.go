package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"math/big"
	"sort"
)

const bootstrapMethod = "sha256_counter_percentile"

func bootstrapInterval(ctx context.Context, values []int64, samples uint32, confidence uint16, seed, domain string) (Interval, error) {
	sums := make([]*big.Int, samples)
	for sample := uint32(0); sample < samples; sample++ {
		if err := analysisContextError(ctx); err != nil {
			return Interval{}, err
		}
		sum := new(big.Int)
		// Values originate from manifest pairs capped at MaxPairBindings.
		for draw := uint32(0); draw < uint32(len(values)); draw++ { //nolint:gosec
			if draw&1023 == 0 {
				if err := analysisContextError(ctx); err != nil {
					return Interval{}, err
				}
			}
			index, err := deterministicIndex(seed, domain, sample, draw, uint64(len(values)))
			if err != nil {
				return Interval{}, err
			}
			sum.Add(sum, big.NewInt(values[index]))
		}
		sums[sample] = sum
	}
	sort.Slice(sums, func(left, right int) bool { return sums[left].Cmp(sums[right]) < 0 })
	tail := uint64(samples) * uint64(10000-confidence) / 20000
	if tail >= uint64(samples) {
		tail = uint64(samples - 1)
	}
	lower := sums[tail]
	upper := sums[uint64(samples)-1-tail]
	denominator := new(big.Int).SetUint64(uint64(len(values)))
	return Interval{
		Method: bootstrapMethod, ConfidenceBasisPoints: confidence, Samples: samples,
		Lower: rationalFromBig(lower, denominator), Upper: rationalFromBig(upper, denominator),
	}, nil
}

func deterministicIndex(seed, domain string, sample, draw uint32, bound uint64) (int, error) {
	limit := uint64(math.MaxUint64) - uint64(math.MaxUint64)%bound
	for counter := uint32(0); counter < 64; counter++ {
		hash := sha256.New()
		writeHashPart(hash, []byte("agent-eval/analysis/bootstrap/v1"))
		writeHashPart(hash, []byte(seed))
		writeHashPart(hash, []byte(domain))
		writeHashPart(hash, uint32Bytes(sample))
		writeHashPart(hash, uint32Bytes(draw))
		writeHashPart(hash, uint32Bytes(counter))
		value := binary.BigEndian.Uint64(hash.Sum(nil)[:8])
		if value < limit {
			// Bound is the positive length of a MaxPairBindings-bounded slice.
			return int(value % bound), nil //nolint:gosec
		}
	}
	return 0, contractError(ErrorLimitExceeded, errInvalidValue)
}
