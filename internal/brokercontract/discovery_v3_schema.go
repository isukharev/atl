package brokercontract

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-discovery-v3.schema.json
var discoverySchemaV3 []byte

func DiscoverySchemaV3() []byte { return append([]byte(nil), discoverySchemaV3...) }

func DiscoverySchemaSHA256V3() string {
	digest := sha256.Sum256(discoverySchemaV3)
	return hex.EncodeToString(digest[:])
}
