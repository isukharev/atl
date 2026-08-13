// Command check-skill-safety validates shell safety examples in skills-src
// without executing them.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const readOnlyShellMarker = "<!-- atl:read-only-shell -->"

var requiredReadOnlySkillBlocks = map[string]int{
	"skills-src/meeting-tasks/SKILL.md":    2,
	"skills-src/spec-to-backlog/SKILL.md":  1,
	"skills-src/sprint-dashboard/SKILL.md": 2,
	"skills-src/status-report/SKILL.md":    4,
	"skills-src/search-knowledge/SKILL.md": 5,
	"skills-src/triage-issue/SKILL.md":     2,
}

type skillSafetyReport struct {
	Files       int
	Blocks      int
	JQPipelines int
}

var pipefailStatement = regexp.MustCompile(`(?:^|[;&]\s)set\s+-o\s+pipefail(?:\s|;|$)`)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	report, err := validateReadOnlySkillBlocks(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Skill safety: %d read-only shell blocks and %d failure-visible atl-to-jq pipelines in %d files\n",
		report.Blocks, report.JQPipelines, report.Files)
}

func validateReadOnlySkillBlocks(root string) (skillSafetyReport, error) {
	counts := map[string]int{}
	coveredFiles := map[string]bool{}
	report := skillSafetyReport{}
	var problems []string
	skillsRoot := filepath.Join(root, "skills-src")
	skillFiles, err := os.OpenRoot(skillsRoot)
	if err != nil {
		return skillSafetyReport{}, err
	}
	defer func() { _ = skillFiles.Close() }()
	err = fs.WalkDir(skillFiles.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			return nil
		}
		data, err := skillFiles.ReadFile(path)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(filepath.Join("skills-src", path))
		count, fileProblems := validateReadOnlyShellFile(rel, string(data))
		pipelines, pipelineProblems := validateJQPipelines(rel, string(data))
		counts[rel] = count
		if count > 0 || pipelines > 0 {
			coveredFiles[rel] = true
		}
		problems = append(problems, fileProblems...)
		problems = append(problems, pipelineProblems...)
		report.JQPipelines += pipelines
		return nil
	})
	if err != nil {
		return skillSafetyReport{}, err
	}
	for path, minimum := range requiredReadOnlySkillBlocks {
		if counts[path] < minimum {
			problems = append(problems, fmt.Sprintf("%s has %d designated read-only shell blocks; require at least %d", path, counts[path], minimum))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return skillSafetyReport{}, fmt.Errorf("skill shell safety contract failed:\n- %s", strings.Join(problems, "\n- "))
	}
	for _, count := range counts {
		if count > 0 {
			report.Blocks += count
		}
	}
	report.Files = len(coveredFiles)
	return report, nil
}

// validateJQPipelines requires a Bash fence to enable pipefail before any jq
// pipeline whose upstream commands include atl. This keeps an atl failure from
// becoming a successful pipeline, though Bash may report a different failing
// stage's status. POSIX sh does not define pipefail, so an example that depends
// on it must name Bash.
func validateJQPipelines(path, content string) (int, []string) {
	lines := strings.Split(content, "\n")
	count := 0
	var problems []string
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "```") {
			continue
		}
		language := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		if language != "sh" && language != "bash" && language != "shell" {
			continue
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
			end++
		}
		if end >= len(lines) {
			break
		}
		statements := shellStatements(lines[i+1 : end])
		pipefailEnabled := false
		for _, statement := range statements {
			pipefailAt := pipefailStatement.FindStringIndex(statement.text)
			pipelineAt, hasPipeline := atlToJQPipelinePosition(statement.text)
			if !hasPipeline {
				if pipefailAt != nil {
					pipefailEnabled = true
				}
				continue
			}
			count++
			if language != "bash" {
				problems = append(problems, fmt.Sprintf("%s:%d atl-to-jq pipeline must use a bash fence because pipefail is not portable sh", path, i+2+statement.line))
				continue
			}
			if !pipefailEnabled && (pipefailAt == nil || pipefailAt[0] > pipelineAt) {
				problems = append(problems, fmt.Sprintf("%s:%d atl-to-jq pipeline must be preceded by set -o pipefail in the same shell fence", path, i+2+statement.line))
			}
			if pipefailAt != nil {
				pipefailEnabled = true
			}
		}
		i = end
	}
	return count, problems
}

type shellStatement struct {
	line int
	text string
}

func shellStatements(lines []string) []shellStatement {
	var statements []shellStatement
	var current []string
	start := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(stripShellComment(line))
		if trimmed == "" {
			continue
		}
		if len(current) == 0 {
			start = i
		}
		continued := shellLineContinues(trimmed)
		if strings.HasSuffix(trimmed, "\\") {
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "\\"))
		}
		current = append(current, trimmed)
		if continued {
			continue
		}
		statements = append(statements, shellStatement{line: start, text: strings.Join(strings.Fields(strings.Join(current, " ")), " ")})
		current = nil
	}
	if len(current) > 0 {
		statements = append(statements, shellStatement{line: start, text: strings.Join(strings.Fields(strings.Join(current, " ")), " ")})
	}
	return statements
}

func shellLineContinues(line string) bool {
	return strings.HasSuffix(line, "\\") || strings.HasSuffix(line, "|") ||
		strings.HasSuffix(line, "&&") || strings.HasSuffix(line, "||")
}

// stripShellComment removes a shell comment that begins at a word boundary,
// while preserving hashes inside quotes or ordinary words.
func stripShellComment(line string) string {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if quote == '"' {
			switch ch {
			case '\\':
				escaped = true
			case '"':
				quote = 0
			}
			continue
		}
		switch ch {
		case '\\':
			escaped = true
		case '\'', '"':
			quote = ch
		case '#':
			if i == 0 || isShellWordBoundary(line[i-1]) {
				return line[:i]
			}
		}
	}
	return line
}

func isShellWordBoundary(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '|' || ch == '&' || ch == ';' || ch == '(' || ch == ')'
}

type shellTokenKind uint8

const (
	shellWord shellTokenKind = iota
	shellPipe
	shellSeparator
)

type shellToken struct {
	kind  shellTokenKind
	text  string
	start int
}

// atlToJQPipelinePosition recognizes atl only as the command word of a pipeline
// segment. Merely passing the string "atl" to another producer is not an ATL
// pipeline. Operators and comments inside quotes remain data.
func atlToJQPipelinePosition(statement string) (int, bool) {
	tokens := shellTokens(statement)
	var command []shellToken
	upstreamATL := false
	for i := 0; i <= len(tokens); i++ {
		if i < len(tokens) && tokens[i].kind == shellWord {
			command = append(command, tokens[i])
			continue
		}
		name, position := shellCommand(command)
		if name == "jq" && upstreamATL {
			return position, true
		}
		if i == len(tokens) {
			break
		}
		if tokens[i].kind == shellPipe {
			upstreamATL = upstreamATL || name == "atl"
		} else {
			upstreamATL = false
		}
		command = nil
	}
	return 0, false
}

func shellTokens(statement string) []shellToken {
	var tokens []shellToken
	var word strings.Builder
	wordStart := -1
	var quote byte
	escaped := false
	flushWord := func() {
		if wordStart < 0 {
			return
		}
		tokens = append(tokens, shellToken{kind: shellWord, text: word.String(), start: wordStart})
		word.Reset()
		wordStart = -1
	}
	startWord := func(position int) {
		if wordStart < 0 {
			wordStart = position
		}
	}
	for i := 0; i < len(statement); i++ {
		ch := statement[i]
		if escaped {
			startWord(i - 1)
			word.WriteByte(ch)
			escaped = false
			continue
		}
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			} else {
				word.WriteByte(ch)
			}
			continue
		}
		if quote == '"' {
			switch ch {
			case '\\':
				escaped = true
			case '"':
				quote = 0
			default:
				word.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\\':
			startWord(i)
			escaped = true
		case '\'', '"':
			startWord(i)
			quote = ch
		case ' ', '\t', '\r', '\n':
			flushWord()
		case '|':
			flushWord()
			if i+1 < len(statement) && statement[i+1] == '|' {
				tokens = append(tokens, shellToken{kind: shellSeparator, text: "||", start: i})
				i++
			} else if i+1 < len(statement) && statement[i+1] == '&' {
				tokens = append(tokens, shellToken{kind: shellPipe, text: "|&", start: i})
				i++
			} else {
				tokens = append(tokens, shellToken{kind: shellPipe, text: "|", start: i})
			}
		case '&':
			if (i > 0 && (statement[i-1] == '>' || statement[i-1] == '<')) ||
				(i+1 < len(statement) && statement[i+1] == '>') {
				startWord(i)
				word.WriteByte(ch)
				continue
			}
			flushWord()
			if i+1 < len(statement) && statement[i+1] == '&' {
				i++
			}
			tokens = append(tokens, shellToken{kind: shellSeparator, text: "&", start: i})
		case ';':
			flushWord()
			tokens = append(tokens, shellToken{kind: shellSeparator, text: ";", start: i})
		default:
			startWord(i)
			word.WriteByte(ch)
		}
	}
	flushWord()
	return tokens
}

func shellCommand(tokens []shellToken) (string, int) {
	skipRedirectionTarget := false
	for index, token := range tokens {
		if skipRedirectionTarget {
			skipRedirectionTarget = false
			continue
		}
		if redirection, consumesNext := shellRedirection(token.text); redirection {
			skipRedirectionTarget = consumesNext
			continue
		}
		if token.text == "" || isShellAssignment(token.text) {
			continue
		}
		name := filepath.Base(token.text)
		if name != "env" {
			return name, token.start
		}
		return envCommand(tokens[index+1:])
	}
	return "", 0
}

func envCommand(tokens []shellToken) (string, int) {
	options := true
	skipOptionValue := false
	skipRedirectionTarget := false
	for _, token := range tokens {
		word := token.text
		if word == "" {
			continue
		}
		if skipRedirectionTarget {
			skipRedirectionTarget = false
			continue
		}
		if redirection, consumesNext := shellRedirection(word); redirection {
			skipRedirectionTarget = consumesNext
			continue
		}
		if skipOptionValue {
			skipOptionValue = false
			continue
		}
		if options && word == "--" {
			options = false
			continue
		}
		if options && envOptionTakesValue(word) {
			skipOptionValue = true
			continue
		}
		if options && strings.HasPrefix(word, "-") {
			continue
		}
		if isShellAssignment(word) {
			continue
		}
		return filepath.Base(word), token.start
	}
	return "", 0
}

func envOptionTakesValue(word string) bool {
	return word == "-u" || word == "--unset" || word == "-C" ||
		word == "--chdir" || word == "-S" || word == "--split-string"
}

func shellRedirection(word string) (bool, bool) {
	rest := word
	for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		rest = rest[1:]
	}
	operators := []string{"&>>", "<<<", "&>", ">>", "<<", "<>", ">|", ">&", "<&", ">", "<"}
	for _, operator := range operators {
		if strings.HasPrefix(rest, operator) {
			return true, len(rest) == len(operator)
		}
	}
	return false, false
}

func isShellAssignment(word string) bool {
	equals := strings.IndexByte(word, '=')
	if equals <= 0 {
		return false
	}
	for i := 0; i < equals; i++ {
		ch := word[i]
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && ch != '_' && (i == 0 || ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func validateReadOnlyShellFile(path, content string) (int, []string) {
	lines := strings.Split(content, "\n")
	count := 0
	var problems []string
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != readOnlyShellMarker {
			continue
		}
		count++
		markerLine := i + 1
		fence := i + 1
		for fence < len(lines) && strings.TrimSpace(lines[fence]) == "" {
			fence++
		}
		if fence >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[fence]), "```") {
			problems = append(problems, fmt.Sprintf("%s:%d marker must be followed by a shell fence", path, markerLine))
			continue
		}
		language := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[fence]), "```"))
		if language != "sh" && language != "bash" && language != "shell" {
			problems = append(problems, fmt.Sprintf("%s:%d marker fence language %q is not sh/bash/shell", path, markerLine, language))
			continue
		}
		end := fence + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
			end++
		}
		if end >= len(lines) {
			problems = append(problems, fmt.Sprintf("%s:%d read-only shell fence is not closed", path, markerLine))
			continue
		}
		first := ""
		for _, line := range lines[fence+1 : end] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			first = trimmed
			break
		}
		if first != "export ATL_READ_ONLY=1" {
			problems = append(problems, fmt.Sprintf("%s:%d first executable statement is %q; require export ATL_READ_ONLY=1", path, markerLine, first))
		}
		i = end
	}
	return count, problems
}
