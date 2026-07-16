package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/isukharev/atl/internal/agenteval"
)

func runCommandBrokerProbe(errorOutput io.Writer) int {
	forbiddenAddress := os.Getenv("ATL_EVAL_FORBIDDEN_NETWORK_ADDRESS")
	if forbiddenAddress == "" {
		fmt.Fprintln(errorOutput, "atl evaluation confinement probe has no network sentinel")
		return 1
	}
	host, portText, splitErr := net.SplitHostPort(forbiddenAddress)
	port, portErr := strconv.Atoi(portText)
	if splitErr != nil || portErr != nil || port < 1 || port > 65535 || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(errorOutput, "atl evaluation confinement probe has an invalid network sentinel")
		return 1
	}
	// #nosec G704 -- the runner supplies and the probe validates a numeric
	// loopback sentinel specifically to prove that command networking is denied.
	connection, networkErr := net.DialTimeout("tcp", forbiddenAddress, 500*time.Millisecond)
	if networkErr == nil {
		_ = connection.Close()
		fmt.Fprintln(errorOutput, "atl evaluation confinement probe reached a forbidden network sentinel")
		return 1
	}
	manifestPath := os.Getenv("ATL_EVAL_COMMAND_BROKER_FILE")
	response, err := agenteval.CallCommandBroker(manifestPath, nil, true)
	if err != nil || response.Status != "ready" {
		fmt.Fprintln(errorOutput, "atl evaluation confinement probe could not reach its command broker")
		return 1
	}
	return 0
}
