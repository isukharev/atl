// Package plugincontract owns the generated plugin-to-binary startup contract.
// It deliberately does not identify an MCP peer or verify a plugin package;
// markers are only claims carried by the local process invocation.
package plugincontract

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// InterfaceVersion changes only when generated plugin startup is no longer
	// compatible with the binary's accepted marker contract.
	InterfaceVersion = 1

	InterfaceFlagName = "plugin-interface-contract"
	ProductFlagName   = "plugin-product-version"

	maxProductVersionBytes = 128
)

type InterfaceStatus string

const (
	InterfaceUnverified InterfaceStatus = "unverified"
	InterfaceCompatible InterfaceStatus = "compatible"
)

type ProductStatus string

const (
	ProductUnverified ProductStatus = "unverified"
	ProductMatch      ProductStatus = "match"
	ProductMismatch   ProductStatus = "mismatch"
)

// StartupStatus keeps interface compatibility separate from informational
// product-version skew. An unmarked invocation is indistinguishable from a
// supported standalone invocation and therefore remains unverified.
type StartupStatus struct {
	InterfaceContract InterfaceStatus
	ProductVersion    ProductStatus
}

// Markers retains every occurrence so repeated generated-only flags cannot
// silently acquire pflag's usual last-value-wins behavior.
type Markers struct {
	InterfaceContracts []string
	ProductVersions    []string
}

var (
	ErrIncompleteMarkers     = errors.New("incomplete plugin startup markers")
	ErrRepeatedMarkers       = errors.New("repeated plugin startup markers")
	ErrIncompatibleInterface = errors.New("incompatible plugin interface contract")
	ErrInvalidProductVersion = errors.New("invalid plugin product version marker")
)

// Evaluate validates generated invocation markers. Product-version mismatch is
// informational and never makes an otherwise compatible interface fail.
func Evaluate(markers Markers, binaryProductVersion string) (StartupStatus, error) {
	status := StartupStatus{
		InterfaceContract: InterfaceUnverified,
		ProductVersion:    ProductUnverified,
	}
	if len(markers.InterfaceContracts) == 0 && len(markers.ProductVersions) == 0 {
		return status, nil
	}
	if len(markers.InterfaceContracts) != 1 || len(markers.ProductVersions) != 1 {
		if len(markers.InterfaceContracts) > 1 || len(markers.ProductVersions) > 1 {
			return StartupStatus{}, ErrRepeatedMarkers
		}
		return StartupStatus{}, ErrIncompleteMarkers
	}
	if markers.InterfaceContracts[0] != strconv.Itoa(InterfaceVersion) {
		return StartupStatus{}, ErrIncompatibleInterface
	}
	pluginProductVersion := markers.ProductVersions[0]
	if !ValidProductVersion(pluginProductVersion) {
		return StartupStatus{}, ErrInvalidProductVersion
	}
	binaryProductVersion = strings.TrimSpace(binaryProductVersion)
	if binaryProductVersion == "" {
		binaryProductVersion = "dev"
	}
	status.InterfaceContract = InterfaceCompatible
	status.ProductVersion = ProductMismatch
	if pluginProductVersion == binaryProductVersion {
		status.ProductVersion = ProductMatch
	}
	return status, nil
}

// ValidProductVersion accepts the bounded opaque token used by plugin
// manifests. Compatibility is defined by InterfaceVersion, never by parsing or
// ordering product versions.
func ValidProductVersion(value string) bool {
	if value == "" || len(value) > maxProductVersionBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for index, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || index > 0 && (r == '.' || r == '+' || r == '-') {
			continue
		}
		return false
	}
	return true
}
