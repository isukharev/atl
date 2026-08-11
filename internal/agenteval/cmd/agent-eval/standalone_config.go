package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/agenteval"
)

const standaloneProjectConfigRelativePath = ".agent-eval/config.json"

type standaloneFlagSpec struct {
	takesValue bool
	repeatable bool
}

type standaloneParsedFlags struct {
	values      map[string][]string
	booleans    map[string]bool
	positionals []string
}

func parseStandaloneFlags(args []string, specs map[string]standaloneFlagSpec) (standaloneParsedFlags, *standaloneFailure) {
	parsed := standaloneParsedFlags{values: make(map[string][]string), booleans: make(map[string]bool)}
	positional := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if positional {
			parsed.positionals = append(parsed.positionals, argument)
			continue
		}
		if argument == "--" {
			positional = true
			continue
		}
		if !strings.HasPrefix(argument, "--") {
			parsed.positionals = append(parsed.positionals, argument)
			continue
		}
		nameValue := strings.TrimPrefix(argument, "--")
		name, inlineValue, hasInline := strings.Cut(nameValue, "=")
		spec, ok := specs[name]
		if !ok || name == "" {
			return standaloneParsedFlags{}, standaloneFail(standaloneUsageError, "unknown_flag")
		}
		if !spec.takesValue {
			if hasInline || parsed.booleans[name] {
				return standaloneParsedFlags{}, standaloneFail(standaloneUsageError, "invalid_flag")
			}
			parsed.booleans[name] = true
			continue
		}
		value := inlineValue
		if !hasInline {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
				return standaloneParsedFlags{}, standaloneFail(standaloneUsageError, "missing_flag_value")
			}
			index++
			value = args[index]
		}
		if value == "" || (!spec.repeatable && len(parsed.values[name]) != 0) {
			return standaloneParsedFlags{}, standaloneFail(standaloneUsageError, "invalid_flag")
		}
		parsed.values[name] = append(parsed.values[name], value)
	}
	if output := parsed.one("output"); output != "" && output != "json" && output != "text" {
		return standaloneParsedFlags{}, standaloneFail(standaloneUsageError, "invalid_output_mode")
	}
	if environment := parsed.one("environment"); environment != "" && environment != "none" && environment != "portable-v1" {
		return standaloneParsedFlags{}, standaloneFail(standaloneConfigurationError, "unknown_environment_projection")
	}
	return parsed, nil
}

func (parsed standaloneParsedFlags) one(name string) string {
	values := parsed.values[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (parsed standaloneParsedFlags) many(name string) []string {
	return append([]string(nil), parsed.values[name]...)
}

func (parsed standaloneParsedFlags) boolean(name string) bool {
	return parsed.booleans[name]
}

func (parsed standaloneParsedFlags) outputMode() (string, *standaloneFailure) {
	return parsed.outputModeValue(), nil
}

func (parsed standaloneParsedFlags) outputModeValue() string {
	if output := parsed.one("output"); output != "" {
		return output
	}
	return "json"
}

func standaloneCommandFlagSpecs(command map[string]standaloneFlagSpec) map[string]standaloneFlagSpec {
	result := make(map[string]standaloneFlagSpec, len(command)+12)
	for name, spec := range command {
		result[name] = spec
	}
	for name, spec := range map[string]standaloneFlagSpec{
		"config":      {takesValue: true},
		"project":     {takesValue: true},
		"environment": {takesValue: true},
		"profile":     {takesValue: true},
		"model":       {takesValue: true},
		"repetitions": {takesValue: true},
		"dry-run":     {},
		"explain":     {},
		"output":      {takesValue: true},
	} {
		result[name] = spec
	}
	return result
}

type standaloneResolvedValue struct {
	value  string
	source string
}

type standaloneProvenance struct {
	Key        string `json:"key"`
	Source     string `json:"source"`
	ValueClass string `json:"value_class"`
}

type standaloneResolvedConfig struct {
	values                map[string]standaloneResolvedValue
	configSource          string
	environmentProjection string
	localRead             bool
}

type standaloneConfigurationSummary struct {
	Precedence            []string               `json:"precedence"`
	ConfigSource          string                 `json:"config_source"`
	EnvironmentProjection string                 `json:"environment_projection"`
	Provenance            []standaloneProvenance `json:"provenance"`
}

func (resolved standaloneResolvedConfig) summary() standaloneConfigurationSummary {
	classes := map[string]string{
		"profile": "opaque_identifier", "model": "opaque_identifier", "repetitions": "count",
	}
	ordered := []string{"profile", "model", "repetitions"}
	provenance := make([]standaloneProvenance, 0, len(resolved.values))
	for _, key := range ordered {
		value, ok := resolved.values[key]
		if !ok {
			continue
		}
		provenance = append(provenance, standaloneProvenance{Key: key, Source: value.source, ValueClass: classes[key]})
	}
	configSource := resolved.configSource
	if configSource == "" {
		configSource = "none"
	}
	environment := resolved.environmentProjection
	if environment == "" {
		environment = "none"
	}
	return standaloneConfigurationSummary{
		Precedence:            []string{"flags", "project_file", "opt_in_environment"},
		ConfigSource:          configSource,
		EnvironmentProjection: environment,
		Provenance:            provenance,
	}
}

func resolveStandaloneConfig(parsed standaloneParsedFlags) (standaloneResolvedConfig, *standaloneFailure) {
	if parsed.one("config") != "" && parsed.one("project") != "" {
		return standaloneResolvedConfig{}, standaloneFail(standaloneConfigurationError, "conflicting_config_sources")
	}
	resolved := standaloneResolvedConfig{values: make(map[string]standaloneResolvedValue)}
	environment := parsed.one("environment")
	if environment == "" {
		environment = "none"
	}
	resolved.environmentProjection = environment
	if environment == "portable-v1" {
		values, failure := standaloneEnvironmentProjection(os.Environ())
		if failure != nil {
			return standaloneResolvedConfig{}, failure
		}
		environmentValues := make(map[string]standaloneResolvedValue, len(values))
		for key, value := range values {
			environmentValues[key] = standaloneResolvedValue{value: value, source: "opt_in_environment"}
		}
		if failure := validateStandaloneConfigValues(environmentValues); failure != nil {
			return standaloneResolvedConfig{}, failure
		}
		standaloneOverlayConfigValues(resolved.values, environmentValues)
	}

	var (
		config  agenteval.StandaloneProjectConfig
		failure *standaloneFailure
	)
	if parsed.one("config") != "" {
		config, failure = readStandaloneProjectConfig(parsed.one("config"))
		resolved.configSource = "explicit_file"
	} else if parsed.one("project") != "" {
		config, failure = readStandaloneProjectConfigFromRoot(parsed.one("project"))
		resolved.configSource = "project_file"
	}
	if resolved.configSource != "" {
		if failure != nil {
			return standaloneResolvedConfig{}, failure
		}
		resolved.localRead = true
		standaloneOverlayConfigValues(resolved.values, standaloneProjectConfigValues(config))
	}

	flagValues := make(map[string]standaloneResolvedValue)
	for _, flagKey := range []struct {
		flag string
		key  string
	}{
		{"profile", "profile"},
		{"model", "model"},
		{"repetitions", "repetitions"},
	} {
		value := parsed.one(flagKey.flag)
		if value == "" {
			continue
		}
		if strings.TrimSpace(value) == "" || strings.IndexByte(value, 0) >= 0 {
			return standaloneResolvedConfig{}, standaloneFail(standaloneConfigurationError, "empty_config_value")
		}
		flagValues[flagKey.key] = standaloneResolvedValue{value: value, source: "flags"}
	}
	if failure := validateStandaloneConfigValues(flagValues); failure != nil {
		return standaloneResolvedConfig{}, failure
	}
	standaloneOverlayConfigValues(resolved.values, flagValues)
	return resolved, nil
}

func standaloneOverlayConfigValues(destination, source map[string]standaloneResolvedValue) {
	for key, value := range source {
		destination[key] = value
	}
}

func standaloneEnvironmentProjection(environ []string) (map[string]string, *standaloneFailure) {
	allowed := map[string]string{
		"AGENT_EVAL_PROFILE":     "profile",
		"AGENT_EVAL_MODEL":       "model",
		"AGENT_EVAL_REPETITIONS": "repetitions",
	}
	values := make(map[string]string)
	seen := make(map[string]struct{})
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, "AGENT_EVAL_") {
			continue
		}
		key, allowedName := allowed[name]
		if !allowedName || value == "" {
			return nil, standaloneFail(standaloneConfigurationError, "unknown_environment_key")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, standaloneFail(standaloneConfigurationError, "duplicate_environment_key")
		}
		seen[name] = struct{}{}
		values[key] = value
	}
	return values, nil
}

func readStandaloneProjectConfig(path string) (agenteval.StandaloneProjectConfig, *standaloneFailure) {
	before, err := os.Lstat(path)
	if !standaloneValidConfigFileInfo(before, err) {
		return agenteval.StandaloneProjectConfig{}, standaloneFail(standaloneConfigurationError, "invalid_config_file")
	}
	file, err := os.Open(path)
	if err != nil {
		return agenteval.StandaloneProjectConfig{}, standaloneFail(standaloneConfigurationError, "invalid_config_file")
	}
	return readStandaloneProjectConfigFile(file, before)
}

func readStandaloneProjectConfigFromRoot(project string) (agenteval.StandaloneProjectConfig, *standaloneFailure) {
	root, err := os.OpenRoot(project)
	if err != nil {
		return agenteval.StandaloneProjectConfig{}, standaloneFail(standaloneConfigurationError, "invalid_config_file")
	}
	before, statErr := root.Lstat(standaloneProjectConfigRelativePath)
	if !standaloneValidConfigFileInfo(before, statErr) {
		_ = root.Close()
		return agenteval.StandaloneProjectConfig{}, standaloneFail(standaloneConfigurationError, "invalid_config_file")
	}
	file, openErr := root.Open(standaloneProjectConfigRelativePath)
	if openErr != nil {
		_ = root.Close()
		return agenteval.StandaloneProjectConfig{}, standaloneFail(standaloneConfigurationError, "invalid_config_file")
	}
	config, failure := readStandaloneProjectConfigFile(file, before)
	if closeErr := root.Close(); closeErr != nil && failure == nil {
		return agenteval.StandaloneProjectConfig{}, standaloneFail(standaloneConfigurationError, "unstable_config_file")
	}
	return config, failure
}

func standaloneValidConfigFileInfo(info os.FileInfo, err error) bool {
	return err == nil && info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 &&
		info.Size() <= agenteval.StandaloneProjectConfigMaxBytes
}

func readStandaloneProjectConfigFile(file *os.File, before os.FileInfo) (agenteval.StandaloneProjectConfig, *standaloneFailure) {
	opened, openedStatErr := file.Stat()
	primary, primaryReadErr := io.ReadAll(io.LimitReader(file, agenteval.StandaloneProjectConfigMaxBytes+1))
	afterPrimary, primaryStatErr := file.Stat()
	_, seekErr := file.Seek(0, io.SeekStart)
	verification, verificationReadErr := io.ReadAll(io.LimitReader(file, agenteval.StandaloneProjectConfigMaxBytes+1))
	afterVerification, verificationStatErr := file.Stat()
	closeErr := file.Close()
	if openedStatErr != nil || primaryReadErr != nil || primaryStatErr != nil || seekErr != nil ||
		verificationReadErr != nil || verificationStatErr != nil || closeErr != nil ||
		len(primary) > agenteval.StandaloneProjectConfigMaxBytes || len(verification) > agenteval.StandaloneProjectConfigMaxBytes ||
		!bytes.Equal(primary, verification) ||
		!standaloneStableConfigSnapshots(before, opened, afterPrimary, afterVerification) {
		return agenteval.StandaloneProjectConfig{}, standaloneFail(standaloneConfigurationError, "unstable_config_file")
	}
	config, err := agenteval.DecodeStandaloneProjectConfig(bytes.NewReader(primary))
	if err != nil {
		return agenteval.StandaloneProjectConfig{}, standaloneFail(standaloneConfigurationError, "invalid_config_json")
	}
	return config, nil
}

func standaloneStableConfigSnapshots(snapshots ...os.FileInfo) bool {
	if len(snapshots) == 0 || snapshots[0] == nil || !snapshots[0].Mode().IsRegular() {
		return false
	}
	reference := snapshots[0]
	for _, snapshot := range snapshots[1:] {
		if snapshot == nil || !snapshot.Mode().IsRegular() || !os.SameFile(reference, snapshot) ||
			reference.Size() != snapshot.Size() || reference.Mode() != snapshot.Mode() ||
			!reference.ModTime().Equal(snapshot.ModTime()) {
			return false
		}
	}
	return true
}

func standaloneProjectConfigValues(config agenteval.StandaloneProjectConfig) map[string]standaloneResolvedValue {
	values := make(map[string]standaloneResolvedValue)
	apply := func(key string, value *string) {
		if value == nil {
			return
		}
		values[key] = standaloneResolvedValue{value: *value, source: "project_file"}
	}
	apply("profile", config.Profile)
	apply("model", config.Model)
	if config.Repetitions != nil {
		values["repetitions"] = standaloneResolvedValue{value: strconv.Itoa(*config.Repetitions), source: "project_file"}
	}
	return values
}

func validateStandaloneConfigValues(values map[string]standaloneResolvedValue) *standaloneFailure {
	for key, value := range values {
		if strings.TrimSpace(value.value) == "" || strings.IndexByte(value.value, 0) >= 0 {
			return standaloneFail(standaloneConfigurationError, "empty_config_value")
		}
		if (key == "profile" || key == "model") &&
			(!utf8.ValidString(value.value) || len(value.value) > agenteval.StandaloneProjectConfigIdentifierMaxBytes) {
			return standaloneFail(standaloneConfigurationError, "invalid_config_value")
		}
		if key == "repetitions" {
			number, err := strconv.Atoi(value.value)
			if err != nil || number < 1 || number > 1000 {
				return standaloneFail(standaloneConfigurationError, "invalid_repetitions")
			}
		}
	}
	return nil
}

func standaloneDecodeClosedJSON(data []byte, target any, maxDepth, maxCollection int) error {
	if len(data) == 0 || !utf8.Valid(data) || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return errors.New("invalid_json_encoding")
	}
	if err := standaloneValidateJSONShape(data, maxDepth, maxCollection); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing_json")
		}
		return err
	}
	return nil
}

func standaloneValidateJSONShape(data []byte, maxDepth, maxCollection int) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := standaloneConsumeJSONValue(decoder, 1, maxDepth, maxCollection); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing_json")
		}
		return err
	}
	return nil
}

func standaloneConsumeJSONValue(decoder *json.Decoder, depth, maxDepth, maxCollection int) error {
	if depth > maxDepth {
		return errors.New("json_depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		count := 0
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object_key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate_member")
			}
			seen[key] = struct{}{}
			count++
			if count > maxCollection {
				return errors.New("object_size")
			}
			if err := standaloneConsumeJSONValue(decoder, depth+1, maxDepth, maxCollection); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("object_close")
		}
	case '[':
		count := 0
		for decoder.More() {
			count++
			if count > maxCollection {
				return errors.New("array_size")
			}
			if err := standaloneConsumeJSONValue(decoder, depth+1, maxDepth, maxCollection); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("array_close")
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	return nil
}
