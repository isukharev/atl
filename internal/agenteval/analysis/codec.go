package analysis

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

const maxJSONDepth = 64

func EncodeReport(report Report, manifest experiment.Manifest) ([]byte, error) {
	if err := ValidateReportForManifest(manifest, report); err != nil {
		return nil, err
	}
	return encodeReportCanonical(report)
}

func encodeReportCanonical(report Report) ([]byte, error) {
	data, err := json.Marshal(report)
	if err != nil || len(data)+1 > MaxReportBytes {
		return nil, contractError(ErrorLimitExceeded, err)
	}
	return append(data, '\n'), nil
}

func DecodeReport(reader io.Reader, manifest experiment.Manifest) (Report, error) {
	if reader == nil {
		return Report{}, contractError(ErrorInvalidReport, errInvalidValue)
	}
	limited := &io.LimitedReader{R: reader, N: MaxReportBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || len(data) < 3 || len(data) > MaxReportBytes || limited.N == 0 || !utf8.Valid(data) ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 ||
		validateJSONMembers(data[:len(data)-1]) != nil {
		return Report{}, contractError(ErrorInvalidReport, err)
	}
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, contractError(ErrorInvalidReport, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Report{}, contractError(ErrorInvalidReport, err)
	}
	if err := ValidateReportForManifest(manifest, report); err != nil {
		return Report{}, err
	}
	canonical, err := encodeReportCanonical(report)
	if err != nil || !bytes.Equal(data, canonical) {
		return Report{}, contractError(ErrorInvalidReport, err)
	}
	return cloneReport(report), nil
}

func validateJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	limits := reportJSONLimits{}
	if err := validateJSONValue(decoder, 0, nil, &limits); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errInvalidValue
	}
	return nil
}

type reportJSONLimits struct {
	dimensions       uint64
	pairedRows       uint64
	trialProjections uint64
}

func validateJSONValue(decoder *json.Decoder, depth int, path []string, limits *reportJSONLimits) error {
	if depth > maxJSONDepth {
		return errInvalidValue
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	want, ok := reportJSONTypeAtPath(path)
	if !ok {
		return errInvalidValue
	}
	for want.Kind() == reflect.Pointer {
		want = want.Elem()
	}
	switch delimiter {
	case '{':
		if want.Kind() != reflect.Struct {
			return errInvalidValue
		}
		seen := map[string]bool{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok || seen[name] || !reportJSONStructHasMember(want, name) {
				return errInvalidValue
			}
			seen[name] = true
			if err := validateJSONValue(decoder, depth+1, append(path, name), limits); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errInvalidValue
		}
	case '[':
		if want.Kind() != reflect.Slice {
			return errInvalidValue
		}
		maximum, kind := reportJSONArrayLimit(path)
		count := uint64(0)
		for decoder.More() {
			count++
			if count > maximum {
				return errInvalidValue
			}
			switch kind {
			case reportJSONArrayDimensions:
				limits.dimensions++
				if limits.dimensions > MaxDimensionResults {
					return errInvalidValue
				}
			case reportJSONArrayPairedRows:
				limits.pairedRows++
				if limits.pairedRows > MaxPairedDeltas {
					return errInvalidValue
				}
			case reportJSONArrayTrialProjections:
				limits.trialProjections++
				if limits.trialProjections > MaxTrialProjections {
					return errInvalidValue
				}
			}
			if err := validateJSONValue(decoder, depth+1, append(path, "*"), limits); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errInvalidValue
		}
	default:
		return errInvalidValue
	}
	return nil
}

func reportJSONTypeAtPath(path []string) (reflect.Type, bool) {
	current := reflect.TypeOf(Report{})
	for _, segment := range path {
		for current.Kind() == reflect.Pointer {
			current = current.Elem()
		}
		if segment == "*" {
			if current.Kind() != reflect.Slice {
				return nil, false
			}
			current = current.Elem()
			continue
		}
		if current.Kind() != reflect.Struct {
			return nil, false
		}
		field, ok := reportJSONStructMember(current, segment)
		if !ok {
			return nil, false
		}
		current = field.Type
	}
	return current, true
}

func reportJSONStructHasMember(value reflect.Type, name string) bool {
	_, ok := reportJSONStructMember(value, name)
	return ok
}

func reportJSONStructMember(value reflect.Type, name string) (reflect.StructField, bool) {
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		tag := field.Tag.Get("json")
		if comma := bytes.IndexByte([]byte(tag), ','); comma >= 0 {
			tag = tag[:comma]
		}
		if tag == name {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

type reportJSONArrayKind uint8

const (
	reportJSONArrayOrdinary reportJSONArrayKind = iota
	reportJSONArrayDimensions
	reportJSONArrayPairedRows
	reportJSONArrayTrialProjections
)

func reportJSONArrayLimit(path []string) (uint64, reportJSONArrayKind) {
	maximum := uint64(MaxReportBytes)
	switch {
	case reportJSONPath(path, "comparisons"):
		return MaxStratifiedResults, reportJSONArrayOrdinary
	case reportJSONPath(path, "activation"):
		return experiment.MaxStrata, reportJSONArrayOrdinary
	case reportJSONPath(path, "funnels"):
		return uint64(experiment.MaxTreatments) * uint64(experiment.MaxStrata), reportJSONArrayOrdinary
	case reportJSONPath(path, "pass_at_k"):
		return MaxPassAtKResults, reportJSONArrayOrdinary
	case reportJSONPath(path, "coverage", "pairs"):
		return experiment.MaxPairBindings, reportJSONArrayOrdinary
	case reportJSONPath(path, "coverage", "members"):
		return experiment.MaxTrials, reportJSONArrayOrdinary
	case reportJSONPath(path, "coverage", "members", "*", "stages"):
		return experiment.MaxStages, reportJSONArrayTrialProjections
	case reportJSONPath(path, "coverage", "members", "*", "metrics"):
		return experiment.MaxMetrics, reportJSONArrayTrialProjections
	case reportJSONPath(path, "coverage", "reasons"), reportJSONPath(path, "coverage", "pairs", "*", "reasons"):
		return uint64(len(closedReasons)), reportJSONArrayOrdinary
	case reportJSONPath(path, "comparisons", "*", "binary"):
		return uint64(experiment.MaxStages + experiment.MaxMetrics), reportJSONArrayDimensions
	case reportJSONPath(path, "comparisons", "*", "continuous"):
		return experiment.MaxMetrics, reportJSONArrayDimensions
	case reportJSONPath(path, "comparisons", "*", "binary", "*", "pairs"),
		reportJSONPath(path, "comparisons", "*", "continuous", "*", "deltas"):
		return experiment.MaxBlocks, reportJSONArrayPairedRows
	case reportJSONPath(path, "funnels", "*", "stages"):
		return experiment.MaxStages, reportJSONArrayOrdinary
	default:
		return maximum, reportJSONArrayOrdinary
	}
}

func reportJSONPath(path []string, want ...string) bool {
	if len(path) != len(want) {
		return false
	}
	for index := range want {
		if path[index] != want[index] {
			return false
		}
	}
	return true
}
