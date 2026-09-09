package brokercontract

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-cache-v2.schema.json
var cacheSchemaV2 []byte

func CacheSchemaV2() []byte { return append([]byte(nil), cacheSchemaV2...) }
func CacheSchemaSHA256V2() string {
	digest := sha256.Sum256(cacheSchemaV2)
	return hex.EncodeToString(digest[:])
}
