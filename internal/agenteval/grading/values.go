package grading

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var closedModes = []Mode{ModeDeterministic, ModeJudgeAssessment, ModeScriptDSL}

func contractError(detail string) error {
	return newError(ErrorContract, fmt.Errorf("%w: %s", ErrContract, detail))
}

func policyError(detail string) error {
	return newError(ErrorPolicy, fmt.Errorf("%w: %s", ErrPolicy, detail))
}

func evidenceError(detail string) error {
	return newError(ErrorEvidence, fmt.Errorf("%w: %s", ErrEvidence, detail))
}

func interruptedError(err error) error {
	return newError(ErrorInterrupted, fmt.Errorf("%w: %w", ErrInterrupted, err))
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > MaxIdentifierBytes || !utf8.ValidString(value) || strings.Contains(value, "//") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for index, r := range segment {
			if unicode.IsControl(r) || unicode.IsSpace(r) || !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-", r) ||
				index == 0 && strings.ContainsRune("._-", r) {
				return false
			}
		}
	}
	return true
}

func validVersion(value string) bool {
	if value == "" || len(value) > MaxIdentifierBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) || !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(".+_-", r) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validText(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\t' {
			return false
		}
	}
	return true
}

func validRelativePath(value string) bool {
	if value == "" || len(value) > MaxRelativePathBytes || !utf8.ValidString(value) || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || !validText(part, MaxRelativePathBytes) {
			return false
		}
	}
	return true
}

func validJSONPointer(value string) bool {
	if len(value) > MaxRelativePathBytes || !utf8.ValidString(value) || value != "" && !strings.HasPrefix(value, "/") {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] == '~' {
			if index+1 >= len(value) || value[index+1] != '0' && value[index+1] != '1' {
				return false
			}
			index++
		}
	}
	return true
}

func validEmbeddedJSON(data json.RawMessage) bool {
	if len(data) == 0 || len(data) > MaxExpectedJSONBytes || !utf8.Valid(data) || validateJSONShape(data) != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	canonical, err := json.Marshal(value)
	return err == nil && bytes.Equal(data, canonical)
}

func hashDomain(domain string, parts ...[]byte) string {
	hash := sha256.New()
	for _, part := range append([][]byte{[]byte(domain)}, parts...) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		hash.Write(length[:])
		hash.Write(part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (s Support) valid() bool {
	return s == SupportNotApplicable || s == SupportSupported || s == SupportUnknown || s == SupportUnsupported
}

func (m Mode) valid() bool { return slices.Contains(closedModes, m) }

func (c ExecutionClass) valid() bool {
	return c == ExecutionInProcess || c == ExecutionHermeticVerifier || c == ExecutionOfflineAssessment
}

func (p Presence) valid() bool {
	return p == PresenceNotApplicable || p == PresenceObserved || p == PresenceUnknown || p == PresenceUnsupported
}

func (v Visibility) valid() bool { return v == VisibilityPublic || v == VisibilityHidden }

func (t JSONType) valid() bool {
	return t == JSONTypeArray || t == JSONTypeBoolean || t == JSONTypeInteger || t == JSONTypeNull || t == JSONTypeNumber ||
		t == JSONTypeObject || t == JSONTypeString
}

func (s OutputStream) valid() bool { return s == OutputStdout || s == OutputStderr }

func (k TreeChangeKind) valid() bool { return k == TreeAdded || k == TreeModified || k == TreeRemoved }

func (k ReviewerKind) valid() bool { return k == ReviewerHuman || k == ReviewerModel }

func (a Authority) valid() bool {
	return a == AuthorityDeterministic || a == AuthorityScript || a == AuthorityJudge
}

func (k EvidenceKind) valid() bool {
	return k == EvidenceFile || k == EvidenceCommand || k == EvidenceTree || k == EvidenceSequence || k == EvidenceCounter
}

func (k DisagreementKind) valid() bool {
	return k == DisagreementReviewers || k == DisagreementDeterministicJudge
}

func (s ReceiptStatus) valid() bool {
	return s == ReceiptComplete || s == ReceiptIncomplete
}

func validateJSONShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	values := 0
	var consume func(int) error
	consume = func(depth int) error {
		if depth > MaxJSONDepth || values >= MaxJSONValues {
			return contractError("json_bounds")
		}
		values++
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
			seen := map[string]struct{}{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return contractError("json_object")
				}
				if _, duplicate := seen[name]; duplicate {
					return contractError("json_duplicate")
				}
				seen[name] = struct{}{}
				if err := consume(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return contractError("json_object")
			}
		case '[':
			for decoder.More() {
				if err := consume(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return contractError("json_array")
			}
		default:
			return contractError("json_delimiter")
		}
		return nil
	}
	if err := consume(1); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return contractError("json_trailing")
	}
	return nil
}
