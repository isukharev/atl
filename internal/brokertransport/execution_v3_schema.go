package brokertransport

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-execution-http-v3.schema.json
var executionHTTPV3 []byte

func ExecutionSchemaV3() []byte { return append([]byte(nil), executionHTTPV3...) }

func ExecutionSchemaSHA256V3() string {
	digest := sha256.Sum256(executionHTTPV3)
	return hex.EncodeToString(digest[:])
}
