package brokercontract

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-execution-v3.schema.json
var executionSchemaV3 []byte

func ExecutionSchemaV3() []byte { return append([]byte(nil), executionSchemaV3...) }

func ExecutionSchemaSHA256V3() string {
	digest := sha256.Sum256(executionSchemaV3)
	return hex.EncodeToString(digest[:])
}
