package brokercontract

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-discovery-v2.schema.json
var discoverySchemaV2 []byte

func DiscoverySchemaV2() []byte { return append([]byte(nil), discoverySchemaV2...) }

func DiscoverySchemaSHA256V2() string {
	digest := sha256.Sum256(discoverySchemaV2)
	return hex.EncodeToString(digest[:])
}
