// Package compatibility owns compiled protocol profiles and owner-only exact
// backend activations. Product identity is never inferred from a nearby build.
package compatibility

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// Product is a closed product identifier. Its representation is private so
// callers can only use the product values declared by this package.
type Product struct {
	name string
}

var (
	ProductConfluence = Product{name: "confluence"}
	ProductJira       = Product{name: "jira"}
)

// String returns the stable lower-case product name.
func (p Product) String() string { return p.name }

// Valid reports whether p is one of the package's closed product values.
func (p Product) Valid() bool {
	return p == ProductConfluence || p == ProductJira
}

// ParseProduct resolves one of the package's exact product names.
func ParseProduct(value string) (Product, error) {
	switch value {
	case ProductConfluence.name:
		return ProductConfluence, nil
	case ProductJira.name:
		return ProductJira, nil
	default:
		return Product{}, configError("invalid product")
	}
}

// Version is an exact three-component numeric backend version.
type Version string

// ParseVersion validates and returns an exact backend version.
func ParseVersion(value string) (Version, error) {
	version := Version(value)
	if err := version.Validate(); err != nil {
		return "", err
	}
	return version, nil
}

// Validate requires exactly three non-empty decimal components.
func (v Version) Validate() error {
	if len(v) < 5 || len(v) > 64 {
		return configError("version must have exactly three bounded numeric components")
	}
	parts := strings.Split(string(v), ".")
	if len(parts) != 3 {
		return configError("version must have exactly three numeric components")
	}
	for _, part := range parts {
		if part == "" || len(part) > 20 || !decimal(part) {
			return configError("version must have exactly three numeric components")
		}
	}
	return nil
}

// BuildNumber is the exact decimal build identifier reported by a backend.
// It remains a string so valid 20-digit values are not constrained by the
// machine word size and leading zeroes, if reported, retain their identity.
type BuildNumber string

// ParseBuildNumber validates and returns an exact backend build number.
func ParseBuildNumber(value string) (BuildNumber, error) {
	build := BuildNumber(value)
	if err := build.Validate(); err != nil {
		return "", err
	}
	return build, nil
}

// Validate requires between one and twenty ASCII decimal digits.
func (b BuildNumber) Validate() error {
	value := string(b)
	if len(value) < 1 || len(value) > 20 || !decimal(value) {
		return configError("build_number must contain 1..20 decimal digits")
	}
	return nil
}

// Pin identifies one exact product build.
type Pin struct {
	Version     Version     `json:"version"`
	BuildNumber BuildNumber `json:"build_number"`
}

// Validate checks both exact identity components.
func (p Pin) Validate() error {
	if err := p.Version.Validate(); err != nil {
		return err
	}
	return p.BuildNumber.Validate()
}

// Descriptor names one compiled protocol profile. Exact product identity is
// deliberately not embedded in the public binary: an owner binds the profile
// to one exact version/build in the owner-only settings file.
type Descriptor struct {
	ID      string
	Product Product
	Family  string
}

const (
	ConfluenceInlineCommentsDCProfileID = "confluence-inline-comments-dc-profile-1"
	ConfluenceInlineCommentsDCFamily    = "confluence-inline-comments-dc"
)

var catalog = [...]Descriptor{
	{
		ID:      ConfluenceInlineCommentsDCProfileID,
		Product: ProductConfluence,
		Family:  ConfluenceInlineCommentsDCFamily,
	},
}

// Descriptors returns a copy of the compile-time provider catalog.
func Descriptors() []Descriptor {
	return append([]Descriptor(nil), catalog[:]...)
}

// Catalog is an alias for Descriptors for callers that prefer catalog naming.
func Catalog() []Descriptor { return Descriptors() }

// Select returns a compiled descriptor by its exact closed ID. It intentionally
// implements no version inference, normalization, or fallback.
func Select(product Product, providerID string) (Descriptor, bool) {
	return selectFrom(catalog[:], product, providerID)
}

func selectFrom(descriptors []Descriptor, product Product, providerID string) (Descriptor, bool) {
	if !product.Valid() || providerID == "" {
		return Descriptor{}, false
	}
	for _, descriptor := range descriptors {
		if descriptor.Product == product && descriptor.ID == providerID {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

func decimal(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func configError(message string) error {
	return fmt.Errorf("%w: compatibility: %s", domain.ErrConfig, message)
}
