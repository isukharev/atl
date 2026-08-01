package compatibility

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	SettingsSchemaVersion = 1
	SettingsFileName      = "compatibility.json"
	MaxSettingsBytes      = 16 << 10
)

// Settings is the separate owner-controlled opt-in for exact compatibility
// providers. It is not inferred from a nearby backend version.
type Settings struct {
	SchemaVersion int         `json:"schema_version"`
	Confluence    *Activation `json:"confluence,omitempty"`
}

// Activation is an explicit owner authorization that binds one compiled
// protocol profile to one exact backend identity.
type Activation struct {
	ProviderID  string      `json:"provider_id"`
	Version     Version     `json:"version"`
	BuildNumber BuildNumber `json:"build_number"`
}

func (a Activation) Pin() Pin {
	return Pin{Version: a.Version, BuildNumber: a.BuildNumber}
}

func (a Activation) Validate(product Product) error {
	if _, ok := Select(product, a.ProviderID); !ok {
		return configError("unknown provider_id")
	}
	return a.Pin().Validate()
}

// DefaultSettings returns an empty current-schema settings value.
func DefaultSettings() Settings {
	return Settings{SchemaVersion: SettingsSchemaVersion}
}

// Validate rejects unknown schema generations and malformed exact pins.
func (s Settings) Validate() error {
	if s.SchemaVersion != SettingsSchemaVersion {
		return configError("unsupported settings schema_version")
	}
	if s.Confluence != nil {
		if err := s.Confluence.Validate(ProductConfluence); err != nil {
			return err
		}
	}
	return nil
}

// Path returns the dedicated compatibility settings path beneath dir.
func Path(dir string) string {
	return filepath.Join(dir, SettingsFileName)
}

// Load strictly reads owner-only settings. A missing file means that no
// compatibility provider is enabled and returns an empty schema-v1 value.
func Load(dir string) (Settings, error) {
	path := Path(dir)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, configError("settings file is unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Settings{}, configError("settings path must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Settings{}, configError("settings file must be owner-only")
	}
	if info.Size() > MaxSettingsBytes {
		return Settings{}, configError("settings file exceeds 16 KiB")
	}

	file, err := os.Open(path)
	if err != nil {
		return Settings{}, configError("settings file is unavailable")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, MaxSettingsBytes+1))
	if err != nil {
		return Settings{}, configError("settings file could not be read")
	}
	if len(data) > MaxSettingsBytes {
		return Settings{}, configError("settings file exceeds 16 KiB")
	}

	var settings Settings
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, configError("settings file is invalid JSON")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Settings{}, configError("settings file must contain one JSON object")
	}
	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// Save validates and atomically persists settings in an owner-only file. A new
// directory is created owner-only as well.
func Save(dir string, settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return configError("settings could not be encoded")
	}
	data = append(data, '\n')
	if len(data) > MaxSettingsBytes {
		return configError("settings file exceeds 16 KiB")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return configError("settings directory could not be created")
	}
	if err := safepath.WriteFileAtomic(Path(dir), data, 0o600); err != nil {
		return configError("settings file could not be saved")
	}
	return nil
}
