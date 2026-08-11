// Package schemaregistry owns the closed standalone durable-schema inventory
// and the reviewed graph of one-step migrations. It contains declarations
// only; profile-owned decoders and migration implementations remain outside
// this leaf package.
package schemaregistry

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	Schema          = "agent-eval/schema-registry"
	SchemaVersion   = 1
	ContractVersion = "0.1.0-pre-release"
	MaxBytes        = 1 << 20
)

var (
	ErrInvalidRegistry = errors.New("schema_registry_invalid")
	ErrUnknownSchema   = errors.New("schema_registry_unknown_schema")
	ErrMigrationPath   = errors.New("schema_registry_migration_path")
)

//go:embed registry.v1.json
var builtInBytes []byte

type Registry struct {
	Schema          string       `json:"schema"`
	SchemaVersion   int          `json:"schema_version"`
	ContractVersion string       `json:"contract_version"`
	Entries         []Descriptor `json:"entries"`
}

type Descriptor struct {
	Namespace      string          `json:"namespace"`
	Kind           string          `json:"kind"`
	Owner          string          `json:"owner"`
	Current        int             `json:"current"`
	Readable       []int           `json:"readable"`
	Emitted        []int           `json:"emitted"`
	Executable     []int           `json:"executable"`
	Disposition    string          `json:"disposition"`
	Privacy        string          `json:"privacy"`
	Migration      string          `json:"migration"`
	MaxBytes       int64           `json:"max_bytes"`
	SchemaResource string          `json:"schema_resource"`
	MigrationEdges []MigrationEdge `json:"migration_edges"`
}

type MigrationEdge struct {
	ID             string `json:"id"`
	From           int    `json:"from"`
	To             int    `json:"to"`
	Implementation string `json:"implementation"`
}

type Inspection struct {
	Descriptor           Descriptor `json:"descriptor"`
	SchemaSHA256         string     `json:"schema_sha256"`
	MigrationSHA256      string     `json:"migration_graph_sha256"`
	RegistrySHA256       string     `json:"registry_sha256"`
	SupportedMigrations  int        `json:"supported_migrations"`
	MigrationUnavailable bool       `json:"migration_unavailable"`
}

// BuiltIn returns a newly decoded immutable-by-ownership snapshot. Mutating a
// returned slice cannot change a later caller's registry.
func BuiltIn() (Registry, error) {
	return Decode(bytes.NewReader(builtInBytes))
}

func BuiltInBytes() []byte {
	return slices.Clone(builtInBytes)
}

func Decode(reader io.Reader) (Registry, error) {
	if reader == nil {
		return Registry{}, ErrInvalidRegistry
	}
	limited := &io.LimitedReader{R: reader, N: MaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) == 0 || len(data) > MaxBytes || !utf8.Valid(data) ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || validateJSONShape(data) != nil {
		return Registry{}, ErrInvalidRegistry
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var registry Registry
	if err := decoder.Decode(&registry); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validateRegistry(registry) != nil {
		return Registry{}, ErrInvalidRegistry
	}
	encoded, err := Encode(registry)
	if err != nil || !bytes.Equal(data, encoded) {
		return Registry{}, ErrInvalidRegistry
	}
	return cloneRegistry(registry), nil
}

func Encode(registry Registry) ([]byte, error) {
	if validateRegistry(registry) != nil {
		return nil, ErrInvalidRegistry
	}
	data, err := json.Marshal(registry)
	if err != nil || len(data)+1 > MaxBytes {
		return nil, ErrInvalidRegistry
	}
	data = append(data, '\n')
	if !bytes.Equal(data, builtInBytes) {
		return nil, ErrInvalidRegistry
	}
	return data, nil
}

func (registry Registry) Lookup(namespace, kind string) (Descriptor, error) {
	key := namespace + "/" + kind
	index, found := slices.BinarySearchFunc(registry.Entries, key, func(entry Descriptor, wanted string) int {
		return strings.Compare(entry.Namespace+"/"+entry.Kind, wanted)
	})
	if !found {
		return Descriptor{}, ErrUnknownSchema
	}
	return cloneDescriptor(registry.Entries[index]), nil
}

func (registry Registry) Inspect(namespace, kind string) (Inspection, error) {
	descriptor, err := registry.Lookup(namespace, kind)
	if err != nil {
		return Inspection{}, err
	}
	registryBytes, err := Encode(registry)
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{
		Descriptor: descriptor, SchemaSHA256: descriptorSHA256(descriptor),
		MigrationSHA256: migrationGraphSHA256(descriptor), RegistrySHA256: sha256Hex(registryBytes),
		SupportedMigrations: len(descriptor.MigrationEdges), MigrationUnavailable: len(descriptor.MigrationEdges) == 0,
	}, nil
}

// MigrationPath returns the one and only reviewed ascending path. It rejects
// zero-length, downgrade, missing, cyclic, and ambiguous paths.
func (registry Registry) MigrationPath(namespace, kind string, from, to int) ([]MigrationEdge, error) {
	descriptor, err := registry.Lookup(namespace, kind)
	if err != nil || from < 1 || to < 1 || from >= to || to > descriptor.Current {
		return nil, ErrMigrationPath
	}
	paths := make([][]MigrationEdge, 0, 2)
	var visit func(int, map[int]bool, []MigrationEdge)
	visit = func(version int, seen map[int]bool, path []MigrationEdge) {
		if len(paths) > 1 || version > to || seen[version] {
			return
		}
		if version == to {
			paths = append(paths, slices.Clone(path))
			return
		}
		nextSeen := make(map[int]bool, len(seen)+1)
		for value := range seen {
			nextSeen[value] = true
		}
		nextSeen[version] = true
		for _, edge := range descriptor.MigrationEdges {
			if edge.From == version {
				visit(edge.To, nextSeen, append(path, edge))
			}
		}
	}
	visit(from, map[int]bool{}, nil)
	if len(paths) != 1 {
		return nil, ErrMigrationPath
	}
	return slices.Clone(paths[0]), nil
}

func ImplementationSHA256(edge MigrationEdge) string {
	return sha256Hex([]byte("agent-eval/migration-implementation/v1\x00" + edge.ID + "\x00" + edge.Implementation))
}

func validateRegistry(registry Registry) error {
	if registry.Schema != Schema || registry.SchemaVersion != SchemaVersion || registry.ContractVersion != ContractVersion || registry.Entries == nil || len(registry.Entries) == 0 {
		return ErrInvalidRegistry
	}
	owners := map[string]bool{"atl-profile": true, "extension": true, "lifecycle": true, "standalone": true}
	dispositions := map[string]bool{"preserve": true, "write_only_projection": true}
	privacy := map[string]bool{"content_minimized": true, "owner_private": true, "public": true, "public_or_private": true}
	migrations := map[string]bool{"compare_only": true, "explicit": true, "partial_explicit": true}
	previous := ""
	for _, descriptor := range registry.Entries {
		key := descriptor.Namespace + "/" + descriptor.Kind
		if !validID(descriptor.Namespace) || !validID(descriptor.Kind) || !owners[descriptor.Owner] || descriptor.Current < 1 ||
			!dispositions[descriptor.Disposition] || !privacy[descriptor.Privacy] || !migrations[descriptor.Migration] ||
			descriptor.MaxBytes < 1 || descriptor.MaxBytes > 1<<30 || descriptor.SchemaResource != "agent-eval/schema/"+key+"@"+fmt.Sprint(descriptor.Current) ||
			descriptor.Readable == nil || descriptor.Emitted == nil || descriptor.Executable == nil || descriptor.MigrationEdges == nil || key <= previous {
			return ErrInvalidRegistry
		}
		previous = key
		if !validVersions(descriptor.Readable, descriptor.Current) || !validVersions(descriptor.Emitted, descriptor.Current) || !validVersions(descriptor.Executable, descriptor.Current) {
			return ErrInvalidRegistry
		}
		for _, executable := range descriptor.Executable {
			if !slices.Contains(descriptor.Readable, executable) {
				return ErrInvalidRegistry
			}
		}
		if err := validateEdges(descriptor); err != nil {
			return err
		}
	}
	return nil
}

func validateEdges(descriptor Descriptor) error {
	seenID := make(map[string]bool, len(descriptor.MigrationEdges))
	seenPair := make(map[[2]int]bool, len(descriptor.MigrationEdges))
	for index, edge := range descriptor.MigrationEdges {
		pair := [2]int{edge.From, edge.To}
		if !validResourceID(edge.ID) || !validResourceID(edge.Implementation) || edge.From < 1 || edge.From >= edge.To || edge.To > descriptor.Current ||
			!slices.Contains(descriptor.Readable, edge.From) || !slices.Contains(descriptor.Readable, edge.To) || seenID[edge.ID] || seenPair[pair] ||
			(index > 0 && (descriptor.MigrationEdges[index-1].From > edge.From ||
				(descriptor.MigrationEdges[index-1].From == edge.From && descriptor.MigrationEdges[index-1].To >= edge.To))) {
			return ErrInvalidRegistry
		}
		seenID[edge.ID], seenPair[pair] = true, true
	}
	for _, start := range descriptor.Readable {
		seen := map[int]bool{}
		var walk func(int) bool
		walk = func(version int) bool {
			if seen[version] {
				return false
			}
			seen[version] = true
			defer delete(seen, version)
			for _, edge := range descriptor.MigrationEdges {
				if edge.From == version && !walk(edge.To) {
					return false
				}
			}
			return true
		}
		if !walk(start) {
			return ErrInvalidRegistry
		}
		for _, target := range descriptor.Readable {
			if target <= start {
				continue
			}
			if migrationPathCount(descriptor.MigrationEdges, start, target, map[int]bool{}) > 1 {
				return ErrInvalidRegistry
			}
		}
	}
	return nil
}

func migrationPathCount(edges []MigrationEdge, from, to int, seen map[int]bool) int {
	if from == to {
		return 1
	}
	if from > to || seen[from] {
		return 0
	}
	nextSeen := make(map[int]bool, len(seen)+1)
	for version := range seen {
		nextSeen[version] = true
	}
	nextSeen[from] = true
	count := 0
	for _, edge := range edges {
		if edge.From != from {
			continue
		}
		count += migrationPathCount(edges, edge.To, to, nextSeen)
		if count > 1 {
			return count
		}
	}
	return count
}

func validVersions(versions []int, current int) bool {
	for index, version := range versions {
		if version < 1 || version > current || (index > 0 && versions[index-1] >= version) {
			return false
		}
	}
	return true
}

func validID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func validResourceID(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func descriptorSHA256(descriptor Descriptor) string {
	copy := cloneDescriptor(descriptor)
	copy.MigrationEdges = []MigrationEdge{}
	data, _ := json.Marshal(copy)
	return sha256Hex(append([]byte("agent-eval/schema-resource/v1\x00"), data...))
}

func migrationGraphSHA256(descriptor Descriptor) string {
	data, _ := json.Marshal(descriptor.MigrationEdges)
	return sha256Hex(append([]byte("agent-eval/migration-graph/v1\x00"), data...))
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func cloneRegistry(registry Registry) Registry {
	result := registry
	result.Entries = make([]Descriptor, len(registry.Entries))
	for index := range registry.Entries {
		result.Entries[index] = cloneDescriptor(registry.Entries[index])
	}
	return result
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Readable = slices.Clone(descriptor.Readable)
	descriptor.Emitted = slices.Clone(descriptor.Emitted)
	descriptor.Executable = slices.Clone(descriptor.Executable)
	descriptor.MigrationEdges = slices.Clone(descriptor.MigrationEdges)
	return descriptor
}

func validateJSONShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return consumeJSON(decoder, 0)
}

func consumeJSON(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrInvalidRegistry
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			name, ok := nameToken.(string)
			if err != nil || !ok || seen[name] {
				return ErrInvalidRegistry
			}
			seen[name] = true
			if err := consumeJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidRegistry
	}
	closing, err := decoder.Token()
	if err != nil || closing != map[json.Delim]json.Delim{'{': '}', '[': ']'}[delimiter] {
		return ErrInvalidRegistry
	}
	if depth == 0 {
		if _, err := decoder.Token(); err != io.EOF {
			return ErrInvalidRegistry
		}
	}
	return nil
}
