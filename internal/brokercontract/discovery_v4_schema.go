package brokercontract

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed schema/broker-discovery-v4.schema.json
var discoverySchemaV4 []byte

func DiscoverySchemaV4() []byte { return append([]byte(nil), discoverySchemaV4...) }

func DiscoverySchemaSHA256V4() string {
	digest := sha256.Sum256(discoverySchemaV4)
	return hex.EncodeToString(digest[:])
}
