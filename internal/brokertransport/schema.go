package brokertransport

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-http-v1.schema.json
var schemaV1 []byte

func SchemaV1() []byte { return append([]byte(nil), schemaV1...) }

func SchemaSHA256() string {
	digest := sha256.Sum256(schemaV1)
	return hex.EncodeToString(digest[:])
}
