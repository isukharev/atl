package agenteval

// testATLCapabilityCatalogHandler is embedded in fake ATL executables so tests
// exercise the same selected-process boundary even when private-plan execution
// copies only the executable into an isolated runtime.
func testATLCapabilityCatalogHandler() string {
	return `if [ "$1" = "capabilities" ] && [ "$2" = "-o" ] && [ "$3" = "json" ]; then
/bin/cat <<'PINNED_CAPABILITY_CATALOG_V1'
` + string(pinnedCapabilityCatalogJSON) + `
PINNED_CAPABILITY_CATALOG_V1
exit 0
fi
`
}
