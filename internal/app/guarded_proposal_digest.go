package app

import (
	"crypto/sha256"
	"encoding/hex"
)

func guardedProposalDigest(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
