package httpx

import (
	"crypto/sha256"
	"encoding/hex"
)

// QualifiedTLSOptionsBytes binds an exclusive trust pool to already-qualified
// CA bytes. The caller retains ownership of bundle and may clear it after this
// function returns.
func QualifiedTLSOptionsBytes(bundle []byte) (TLSOptions, string, error) {
	pool, err := exclusiveCertPool(bundle)
	if err != nil {
		return TLSOptions{}, "", err
	}
	digest := sha256.Sum256(bundle)
	return TLSOptions{rootCAs: pool}, hex.EncodeToString(digest[:]), nil
}
