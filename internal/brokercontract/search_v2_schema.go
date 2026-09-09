package brokercontract

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-execution-v2.schema.json
var executionSchemaV2 []byte

func ExecutionSchemaV2() []byte { return append([]byte(nil), executionSchemaV2...) }

func ExecutionSchemaSHA256V2() string {
	digest := sha256.Sum256(executionSchemaV2)
	return hex.EncodeToString(digest[:])
}
