package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"strings"
)

func rationalFromInt64(numerator, denominator int64) Rational {
	return rationalFromBig(big.NewInt(numerator), big.NewInt(denominator))
}

func rationalFromUint64(numerator, denominator uint64) Rational {
	left := new(big.Int).SetUint64(numerator)
	right := new(big.Int).SetUint64(denominator)
	return rationalFromBig(left, right)
}

func rationalFromBig(numerator, denominator *big.Int) Rational {
	value := new(big.Rat).SetFrac(new(big.Int).Set(numerator), new(big.Int).Set(denominator))
	return Rational{Numerator: value.Num().String(), Denominator: value.Denom().String()}
}

func parseRational(value Rational) (*big.Rat, bool) {
	if len(value.Numerator) == 0 || len(value.Denominator) == 0 ||
		len(value.Numerator) > MaxProbabilityDigits || len(value.Denominator) > MaxProbabilityDigits ||
		(value.Numerator != "0" && strings.HasPrefix(value.Numerator, "0")) ||
		strings.HasPrefix(value.Numerator, "-0") || strings.HasPrefix(value.Denominator, "0") ||
		strings.HasPrefix(value.Denominator, "-") || strings.HasPrefix(value.Numerator, "+") || strings.HasPrefix(value.Denominator, "+") {
		return nil, false
	}
	numerator, ok := new(big.Int).SetString(value.Numerator, 10)
	if !ok {
		return nil, false
	}
	denominator, ok := new(big.Int).SetString(value.Denominator, 10)
	if !ok || denominator.Sign() <= 0 {
		return nil, false
	}
	parsed := new(big.Rat).SetFrac(numerator, denominator)
	return parsed, parsed.Num().String() == value.Numerator && parsed.Denom().String() == value.Denominator
}

func exactTwoSidedBinomial(left, right uint32) Rational {
	result, _ := exactTwoSidedBinomialContext(context.Background(), left, right)
	return result
}

func exactTwoSidedBinomialContext(ctx context.Context, left, right uint32) (Rational, error) {
	if err := analysisContextError(ctx); err != nil {
		return Rational{}, err
	}
	total := left + right
	if total == 0 {
		return rationalFromInt64(1, 1), nil
	}
	tail := left
	if right < tail {
		tail = right
	}
	term := big.NewInt(1)
	numerator := new(big.Int).Set(term)
	for value := uint32(0); value < tail; value++ {
		if value&127 == 0 {
			if err := analysisContextError(ctx); err != nil {
				return Rational{}, err
			}
		}
		term.Mul(term, new(big.Int).SetUint64(uint64(total-value)))
		term.Quo(term, new(big.Int).SetUint64(uint64(value+1)))
		numerator.Add(numerator, term)
	}
	numerator.Lsh(numerator, 1)
	denominator := new(big.Int).Lsh(big.NewInt(1), uint(total))
	if numerator.Cmp(denominator) > 0 {
		numerator.Set(denominator)
	}
	if err := analysisContextError(ctx); err != nil {
		return Rational{}, err
	}
	return rationalFromBig(numerator, denominator), nil
}

func passEstimators(attempts, passed, k uint32) (Rational, Rational, bool) {
	if attempts == 0 || k == 0 || k > attempts || passed > attempts {
		return Rational{}, Rational{}, false
	}
	denominator := combination(attempts, k)
	failedChoice := new(big.Int)
	if attempts-passed >= k {
		failedChoice.Set(combination(attempts-passed, k))
	}
	passNumerator := new(big.Int).Sub(new(big.Int).Set(denominator), failedChoice)
	allPassNumerator := new(big.Int)
	if passed >= k {
		allPassNumerator.Set(combination(passed, k))
	}
	return rationalFromBig(passNumerator, denominator), rationalFromBig(allPassNumerator, denominator), true
}

func combination(n, k uint32) *big.Int {
	if k > n {
		return new(big.Int)
	}
	if k > n-k {
		k = n - k
	}
	result := big.NewInt(1)
	for index := uint32(1); index <= k; index++ {
		result.Mul(result, new(big.Int).SetUint64(uint64(n-k+index)))
		result.Quo(result, new(big.Int).SetUint64(uint64(index)))
	}
	return result
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validDerivedID(prefix, value string) bool {
	return len(value) == len(prefix)+1+sha256.Size*2 && strings.HasPrefix(value, prefix+"-") && validDigest(value[len(prefix)+1:])
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashPart(writer hashWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func hashParts(domain string, parts ...[]byte) string {
	hash := sha256.New()
	writeHashPart(hash, []byte("agent-eval/analysis/v1"))
	writeHashPart(hash, []byte(domain))
	for _, part := range parts {
		writeHashPart(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func uint32Bytes(value uint32) []byte {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	return encoded[:]
}
