package brokerconfig

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/strictjson"
)

func TestPublishedBrokerLocalExampleMatchesClosedConfigAndServiceProfile(t *testing.T) {
	configBody, err := os.ReadFile("../../examples/broker-local/broker.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if strictjson.DecodeExact(configBody, brokercontract.MaxCanonicalDepth, &cfg) != nil || validate(cfg) != nil {
		t.Fatal("published Broker example does not match the closed configuration")
	}
	unit, err := os.ReadFile("../../examples/broker-local/atl-broker.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"User=atl-broker", "Group=atl-broker", "NoNewPrivileges=true", "CapabilityBoundingSet=", "AmbientCapabilities=",
		"PrivateDevices=true", "ProtectSystem=strict", "ProtectHome=true", "RestrictAddressFamilies=AF_INET AF_INET6",
		"MemoryMax=2G", "CPUQuota=100%", "TasksMax=64", "LimitCORE=0", "KillMode=mixed", "TimeoutStopSec=7s",
		"ExecStart=/usr/local/bin/atl broker serve --config /etc/atl-broker/broker.json",
	} {
		if bytes.Count(unit, []byte(required)) != 1 {
			t.Fatalf("service directive %q count=%d", required, bytes.Count(unit, []byte(required)))
		}
	}
	for _, forbidden := range []string{"Environment=", "EnvironmentFile=", "0.0.0.0", "--verbose", "ATL_ALLOW_INSECURE"} {
		if strings.Contains(string(unit), forbidden) {
			t.Fatalf("service contains forbidden expansion %q", forbidden)
		}
	}
}
