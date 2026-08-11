package agentskills

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type skillMetadata struct {
	name             string
	hasCompatibility bool
	hasAllowedTools  bool
}

func parseSkillMetadata(data []byte) (skillMetadata, error) {
	if len(data) == 0 || len(data) > MaxFileBytes || !utf8.Valid(data) {
		return skillMetadata{}, contractError(ErrorInvalidSkill, nil)
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return skillMetadata{}, contractError(ErrorInvalidSkill, fmt.Errorf("frontmatter start"))
	}
	fields := make(map[string]string)
	closed := false
	for _, line := range lines[1:] {
		if line == "---" {
			closed = true
			break
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, raw, found := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return skillMetadata{}, contractError(ErrorInvalidSkill, fmt.Errorf("frontmatter field"))
		}
		if _, duplicate := fields[key]; duplicate {
			return skillMetadata{}, contractError(ErrorInvalidSkill, fmt.Errorf("duplicate frontmatter field"))
		}
		value, err := parseYAMLScalar(raw)
		if err != nil {
			return skillMetadata{}, contractError(ErrorInvalidSkill, err)
		}
		fields[key] = value
	}
	if !closed {
		return skillMetadata{}, contractError(ErrorInvalidSkill, fmt.Errorf("frontmatter close"))
	}
	name, hasName := fields["name"]
	description, hasDescription := fields["description"]
	if !hasName || !validSkillName(name) || !hasDescription || description == "" || len(description) > 1024 {
		return skillMetadata{}, contractError(ErrorInvalidSkill, fmt.Errorf("required frontmatter"))
	}
	return skillMetadata{
		name: name, hasCompatibility: fields["compatibility"] != "",
		hasAllowedTools: fields["allowed-tools"] != "",
	}, nil
}

func parseYAMLScalar(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "\"") {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("quoted scalar")
		}
		return decoded, nil
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", fmt.Errorf("quoted scalar")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	if marker := strings.Index(value, " #"); marker >= 0 {
		value = strings.TrimSpace(value[:marker])
	}
	if strings.ContainsAny(value, "\r\n") || value == "|" || value == ">" {
		return "", fmt.Errorf("unsupported scalar")
	}
	return value, nil
}
