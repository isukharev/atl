package agentskills

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	lifecycleSecurityMatcherRevision         = "matcher-v1-bounded-conjunctions"
	lifecycleSecurityPrecisionCorpusRevision = "precision-corpus-v1-2-positive-3-negative"
	lifecycleSecurityMaxDecodedCandidates    = 32
	lifecycleSecurityMaxDecodedBytes         = 64 << 10
	lifecycleSecurityMaxTokenBytes           = 4096
)

type lifecycleSecurityRuleSpec struct {
	id         LifecycleSecurityRuleID
	severity   LifecycleSecuritySeverity
	confidence LifecycleSecurityConfidence
	evidence   []LifecycleSecurityEvidence
	fileTypes  []LifecycleSecurityFileType
}

var lifecycleSecurityAllFileTypes = []LifecycleSecurityFileType{
	LifecycleSecurityFileMarkdown, LifecycleSecurityFileJSON, LifecycleSecurityFileYAML,
	LifecycleSecurityFileConfig, LifecycleSecurityFileShell, LifecycleSecurityFileSource,
	LifecycleSecurityFileText,
}

var lifecycleSecurityRuleSpecs = []lifecycleSecurityRuleSpec{
	{
		id: LifecycleSecurityRuleCredentialLike, severity: LifecycleSecuritySeverityCritical,
		confidence: LifecycleSecurityConfidenceHigh,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidencePrivateKeyHeader, LifecycleSecurityEvidenceTokenAssignment},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
	{
		id: LifecycleSecurityRulePromptCommandInjection, severity: LifecycleSecuritySeverityCritical,
		confidence: LifecycleSecurityConfidenceHigh,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidenceInstructionOverride, LifecycleSecurityEvidenceDynamicCommandEval},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
	{
		id: LifecycleSecurityRuleUnsafeInstallUpdate, severity: LifecycleSecuritySeverityHigh,
		confidence: LifecycleSecurityConfidenceHigh,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidenceDownloadToInstaller, LifecycleSecurityEvidenceUnboundedPackageUpdate},
		fileTypes:  []LifecycleSecurityFileType{LifecycleSecurityFileMarkdown, LifecycleSecurityFileJSON, LifecycleSecurityFileYAML, LifecycleSecurityFileConfig, LifecycleSecurityFileShell, LifecycleSecurityFileSource, LifecycleSecurityFileText},
	},
	{
		id: LifecycleSecurityRuleShellDownload, severity: LifecycleSecuritySeverityHigh,
		confidence: LifecycleSecurityConfidenceHigh,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidenceDownloadCommand, LifecycleSecurityEvidenceNetworkShellDevice},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
	{
		id: LifecycleSecurityRuleEscalation, severity: LifecycleSecuritySeverityHigh,
		confidence: LifecycleSecurityConfidenceHigh,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidencePrivilegeCommand, LifecycleSecurityEvidencePermissionWidening},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
	{
		id: LifecycleSecurityRulePersistence, severity: LifecycleSecuritySeverityHigh,
		confidence: LifecycleSecurityConfidenceHigh,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidenceStartupRegistration, LifecycleSecurityEvidenceSchedulerRegistration},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
	{
		id: LifecycleSecurityRuleDestructive, severity: LifecycleSecuritySeverityCritical,
		confidence: LifecycleSecurityConfidenceHigh,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidenceRecursiveRootDelete, LifecycleSecurityEvidenceDeviceOrStoreWipe},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
	{
		id: LifecycleSecurityRuleNetworkDestination, severity: LifecycleSecuritySeverityHigh,
		confidence: LifecycleSecurityConfidenceMedium,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidenceLiteralRemoteDestination, LifecycleSecurityEvidenceWildcardListener},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
	{
		id: LifecycleSecurityRuleUnboundedAuthority, severity: LifecycleSecuritySeverityCritical,
		confidence: LifecycleSecurityConfidenceHigh,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidenceToolWildcardOrBypass, LifecycleSecurityEvidencePrivilegedRootMount},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
	{
		id: LifecycleSecurityRuleEncodedPayload, severity: LifecycleSecuritySeverityCritical,
		confidence: LifecycleSecurityConfidenceMedium,
		evidence:   []LifecycleSecurityEvidence{LifecycleSecurityEvidenceDecodedHighRiskEvidence},
		fileTypes:  lifecycleSecurityAllFileTypes,
	},
}

type lifecycleSecurityMatch struct {
	ruleID   LifecycleSecurityRuleID
	evidence LifecycleSecurityEvidence
}

func lifecycleSecurityRuleDescriptor(ruleID LifecycleSecurityRuleID, evidence LifecycleSecurityEvidence) (lifecycleSecurityRuleSpec, bool) {
	for _, spec := range lifecycleSecurityRuleSpecs {
		if spec.id != ruleID {
			continue
		}
		for _, candidate := range spec.evidence {
			if candidate == evidence {
				return spec, true
			}
		}
	}
	return lifecycleSecurityRuleSpec{}, false
}

func scanLifecycleSecurityText(data []byte) []lifecycleSecurityMatch {
	matches, _ := scanLifecycleSecurityTextForTypeBounded(data, "")
	return matches
}

func scanLifecycleSecurityTextForTypeBounded(data []byte, fileType LifecycleSecurityFileType) ([]lifecycleSecurityMatch, bool) {
	if !utf8.Valid(data) {
		return nil, true
	}
	matches := make([]lifecycleSecurityMatch, 0, len(lifecycleSecurityRuleSpecs)*2)
	seen := make(map[lifecycleSecurityMatch]struct{})
	complete := true
	inFence := false
	lineCount := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), lifecycleSecurityMaxLineBytes)
	for scanner.Scan() {
		lineCount++
		if lineCount > lifecycleSecurityMaxLines {
			return matches, false
		}
		rawLine := scanner.Text()
		text := strings.ToLower(rawLine)
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if trimmed == "" {
			continue
		}
		for _, spec := range lifecycleSecurityRuleSpecs {
			if fileType != "" && !lifecycleSecurityRuleApplies(spec, fileType) {
				continue
			}
			for _, evidence := range spec.evidence {
				matched := false
				if evidence == LifecycleSecurityEvidenceDecodedHighRiskEvidence {
					decodedMatched, decodedComplete := hasDecodedHighRiskEvidenceBounded(rawLine)
					matched = decodedMatched
					complete = complete && decodedComplete
				} else {
					matched = matchLifecycleSecurityEvidence(evidence, text)
				}
				if matched && lifecycleSecurityCommandEvidence(evidence) &&
					!inFence && !lifecycleSecurityCommandLikeLine(text, fileType) {
					matched = false
				}
				if matched && lifecycleSecurityDocumentaryNegation(text) &&
					(lifecycleSecurityCommandEvidence(evidence) || evidence == LifecycleSecurityEvidenceToolWildcardOrBypass ||
						evidence == LifecycleSecurityEvidenceLiteralRemoteDestination) {
					matched = false
				}
				if matched {
					match := lifecycleSecurityMatch{ruleID: spec.id, evidence: evidence}
					if _, duplicate := seen[match]; duplicate {
						continue
					}
					seen[match] = struct{}{}
					matches = append(matches, match)
				}
			}
		}
	}
	if scanner.Err() != nil {
		return matches, false
	}
	return matches, complete
}

func lifecycleSecurityRuleApplies(spec lifecycleSecurityRuleSpec, fileType LifecycleSecurityFileType) bool {
	for _, candidate := range spec.fileTypes {
		if candidate == fileType {
			return true
		}
	}
	return false
}

func lifecycleSecurityCommandEvidence(evidence LifecycleSecurityEvidence) bool {
	switch evidence {
	case LifecycleSecurityEvidenceDownloadToInstaller, LifecycleSecurityEvidenceUnboundedPackageUpdate,
		LifecycleSecurityEvidenceDownloadCommand, LifecycleSecurityEvidenceNetworkShellDevice,
		LifecycleSecurityEvidencePrivilegeCommand, LifecycleSecurityEvidencePermissionWidening,
		LifecycleSecurityEvidenceStartupRegistration, LifecycleSecurityEvidenceSchedulerRegistration,
		LifecycleSecurityEvidenceRecursiveRootDelete, LifecycleSecurityEvidenceDeviceOrStoreWipe,
		LifecycleSecurityEvidenceWildcardListener, LifecycleSecurityEvidencePrivilegedRootMount,
		LifecycleSecurityEvidenceDynamicCommandEval:
		return true
	default:
		return false
	}
}

func lifecycleSecurityCommandLikeLine(line string, fileType LifecycleSecurityFileType) bool {
	trimmed := strings.TrimSpace(line)
	for _, prefix := range []string{"$ ", "> ", "# ", "| ", "; ", "&& ", "|| ", "run ", "execute ", "invoke ", "use ", "launch ", "write ", "copy "} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	commands := []string{
		"curl ", "wget ", "fetch ", "invoke-webrequest ", "iwr ", "pip ", "pip3 ", "npm ", "apt ", "apt-get ", "brew ", "cargo ",
		"eval(", "eval \"", "sh -c ", "bash -c ", "zsh -c ", "python -c ", "powershell -command ",
		"nc ", "ncat ", "netcat ", "socat ", "sudo ", "doas ", "pkexec ", "setcap ", "runas ", "chmod ", "chown ", "systemctl ",
		"launchctl ", "crontab ", "schtasks ", "register-scheduledtask ", "at ", "rm ", "find ", "mkfs.", "diskutil ", "dd ", "mount ",
		"docker ", "allowed-tools:", "allowed_tools:", "listen ", "bind ",
	}
	for _, command := range commands {
		if strings.HasPrefix(trimmed, command) || strings.Contains(trimmed, "`"+command) {
			return true
		}
	}
	if fileType == LifecycleSecurityFileJSON || fileType == LifecycleSecurityFileYAML ||
		fileType == LifecycleSecurityFileConfig || fileType == LifecycleSecurityFileSource {
		for _, command := range commands {
			if strings.Contains(trimmed, command) {
				return true
			}
		}
	}
	return false
}

func lifecycleSecurityDocumentaryNegation(line string) bool {
	return containsAnyPhrase(line, " is forbidden", " is prohibited", " is rejected", " is disabled", " is reviewed", " is documented",
		" are forbidden", " are prohibited", " must not", " not allowed", " without executing", " never run", " do not run")
}

func matchLifecycleSecurityEvidence(evidence LifecycleSecurityEvidence, text string) bool {
	switch evidence {
	case LifecycleSecurityEvidencePrivateKeyHeader:
		return strings.Contains(text, "-----begin ") && strings.Contains(text, " private key-----")
	case LifecycleSecurityEvidenceTokenAssignment:
		return hasSensitiveAssignment(text)
	case LifecycleSecurityEvidenceInstructionOverride:
		return containsAnyPhrase(text,
			"ignore previous instructions", "disregard previous instructions",
			"override system instructions", "override safety instructions",
			"you are now the system prompt")
	case LifecycleSecurityEvidenceDynamicCommandEval:
		return strings.Contains(text, "eval(") || strings.Contains(text, "eval \"") ||
			containsAnyPhrase(text, "sh -c", "bash -c", "zsh -c", "python -c", "powershell -command")
	case LifecycleSecurityEvidenceDownloadToInstaller:
		return hasDownloadTool(text) && (hasPipeToShell(text) ||
			containsAnyPhrase(text, "chmod +x", "chmod 755", "-o /tmp/", "-o /var/tmp/"))
	case LifecycleSecurityEvidenceUnboundedPackageUpdate:
		return containsAnyPhrase(text, "pip install -u", "pip3 install -u", "npm install -g", "npm i -g",
			"apt upgrade", "apt-get upgrade", "brew upgrade", "cargo install --force")
	case LifecycleSecurityEvidenceDownloadCommand:
		return hasDownloadTool(text) && hasRemoteDestination(text)
	case LifecycleSecurityEvidenceNetworkShellDevice:
		return containsAnyPhrase(text, "nc -", "ncat ", "netcat ", "socat ") ||
			(strings.Contains(text, "listen") && strings.Contains(text, "-e"))
	case LifecycleSecurityEvidencePrivilegeCommand:
		return containsAnyPhrase(text, "sudo ", "doas ", "pkexec ", "setcap ", "runas ")
	case LifecycleSecurityEvidencePermissionWidening:
		return containsAnyPhrase(text, "chmod 777", "chmod a+rwx", "chmod +s", "chmod u+s", "chown root")
	case LifecycleSecurityEvidenceStartupRegistration:
		return containsAnyPhrase(text, "/etc/cron", "crontab", "systemctl enable", "launchctl load",
			".config/autostart", "registry\\run", "registry/run")
	case LifecycleSecurityEvidenceSchedulerRegistration:
		return containsAnyPhrase(text, "schtasks", "register-scheduledtask", "at 0", "at 1", "at 2", "at 3", "at 4", "at 5", "at 6", "at 7", "at 8", "at 9")
	case LifecycleSecurityEvidenceRecursiveRootDelete:
		return containsAnyPhrase(text, "rm -rf /", "rm -fr /", "rm --recursive --force /", "find / -delete")
	case LifecycleSecurityEvidenceDeviceOrStoreWipe:
		return containsAnyPhrase(text, "mkfs.", "diskutil erase", "drop database", "format c:") ||
			(strings.Contains(text, "dd ") && strings.Contains(text, "of=/dev/"))
	case LifecycleSecurityEvidenceLiteralRemoteDestination:
		return hasRemoteDestination(text) && containsAnyWord(text, "endpoint", "destination", "webhook", "connect", "send", "post", "remote")
	case LifecycleSecurityEvidenceWildcardListener:
		return strings.Contains(text, "0.0.0.0") || strings.Contains(text, "listen *:") || strings.Contains(text, "bind *:")
	case LifecycleSecurityEvidenceToolWildcardOrBypass:
		return containsAnyPhrase(text, "allowed-tools: *", "allowed_tools: *", "all tools allowed", "disable confirmation", "--no-verify", "--no-confirm")
	case LifecycleSecurityEvidencePrivilegedRootMount:
		return containsAnyPhrase(text, "--privileged", "mount --bind /", "mount / ", "mount /\n", "mount /;")
	case LifecycleSecurityEvidenceDecodedHighRiskEvidence:
		return hasDecodedHighRiskEvidence(text)
	default:
		return false
	}
}

func hasSensitiveAssignment(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if containsAnyWord(line, "api_key", "apikey", "access_token", "secret", "password", "token") &&
			(strings.Contains(line, "=") || strings.Contains(line, ":")) &&
			!containsAnyPhrase(line, "example", "placeholder", "your-token", "<token>", "replace-me") {
			for _, marker := range []string{"=", ":"} {
				index := strings.Index(line, marker)
				if index >= 0 && len(strings.TrimSpace(line[index+1:])) >= 8 {
					return true
				}
			}
		}
	}
	return false
}

func hasDownloadTool(text string) bool {
	return containsAnyWord(text, "curl", "wget", "fetch", "invoke-webrequest", "iwr")
}

func hasPipeToShell(text string) bool {
	return strings.Contains(text, "| sh") || strings.Contains(text, "|sh") ||
		strings.Contains(text, "| bash") || strings.Contains(text, "|bash") || strings.Contains(text, "| zsh")
}

func hasRemoteDestination(text string) bool {
	return strings.Contains(text, "https://") || strings.Contains(text, "http://") ||
		strings.Contains(text, "ssh://") || strings.Contains(text, "tcp://")
}

func containsAnyPhrase(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func containsAnyWord(text string, words ...string) bool {
	for _, word := range words {
		if containsWord(text, word) {
			return true
		}
	}
	return false
}

func containsWord(text, word string) bool {
	start := 0
	for {
		index := strings.Index(text[start:], word)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !lifecycleSecurityWordRune(text[index-1])
		end := index + len(word)
		afterOK := end == len(text) || !lifecycleSecurityWordRune(text[end])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
		if start >= len(text) {
			return false
		}
	}
}

func lifecycleSecurityWordRune(value byte) bool {
	return value == '_' || value >= '0' && value <= '9' || value >= 'a' && value <= 'z' || unicode.IsLetter(rune(value))
}

func hasDecodedHighRiskEvidence(text string) bool {
	matched, _ := hasDecodedHighRiskEvidenceBounded(text)
	return matched
}

func hasDecodedHighRiskEvidenceBounded(text string) (bool, bool) {
	if !containsAnyPhrase(text, "base64 -d", "base64 --decode", "base64.decode", "xxd -r -p", "openssl enc -d") {
		return false, true
	}
	decodedBytes := 0
	candidates := 0
	complete, found := visitLifecycleSecurityTokens(text, func(token string) lifecycleSecurityTokenDecision {
		if len(token) < 16 || len(token) > lifecycleSecurityMaxTokenBytes {
			return lifecycleSecurityTokenContinue
		}
		if candidates >= lifecycleSecurityMaxDecodedCandidates {
			return lifecycleSecurityTokenLimit
		}
		candidates++
		var decoded []byte
		if value, err := base64.StdEncoding.DecodeString(token); err == nil {
			decoded = value
		} else if value, err := base64.RawStdEncoding.DecodeString(token); err == nil {
			decoded = value
		} else if value, err := hex.DecodeString(token); err == nil {
			decoded = value
		} else {
			return lifecycleSecurityTokenContinue
		}
		if decodedBytes+len(decoded) > lifecycleSecurityMaxDecodedBytes {
			return lifecycleSecurityTokenLimit
		}
		decodedBytes += len(decoded)
		lower := strings.ToLower(string(decoded))
		if containsAnyPhrase(lower, "curl ", "wget ", "rm -rf", "sudo ", "private key", "base64 -d", "chmod 777") {
			return lifecycleSecurityTokenFound
		}
		return lifecycleSecurityTokenContinue
	})
	if found {
		return true, true
	}
	return false, complete
}

type lifecycleSecurityTokenDecision uint8

const (
	lifecycleSecurityTokenContinue lifecycleSecurityTokenDecision = iota
	lifecycleSecurityTokenFound
	lifecycleSecurityTokenLimit
)

func visitLifecycleSecurityTokens(text string, visit func(string) lifecycleSecurityTokenDecision) (complete, found bool) {
	tokenCount := 0
	start := -1
	flush := func(end int) bool {
		if start < 0 {
			return true
		}
		if tokenCount >= lifecycleSecurityMaxTokens {
			return false
		}
		tokenCount++
		decision := visit(text[start:end])
		start = -1
		switch decision {
		case lifecycleSecurityTokenFound:
			found = true
			return false
		case lifecycleSecurityTokenLimit:
			return false
		default:
			return true
		}
	}
	for index, value := range text {
		if lifecycleSecurityTokenDelimiter(value) {
			if !flush(index) {
				return false, found
			}
			continue
		}
		if start < 0 {
			start = index
		}
	}
	if !flush(len(text)) {
		return false, found
	}
	return true, found
}

func lifecycleSecurityTokenDelimiter(value rune) bool {
	return !unicode.IsLetter(value) && !unicode.IsDigit(value) && !strings.ContainsRune("+/=_-", value)
}
