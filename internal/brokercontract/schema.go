package brokercontract

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-v1.schema.json
var schemaV1 []byte

func SchemaV1() []byte { return append([]byte(nil), schemaV1...) }

func SchemaSHA256() string {
	sum := sha256.Sum256(schemaV1)
	return hex.EncodeToString(sum[:])
}
