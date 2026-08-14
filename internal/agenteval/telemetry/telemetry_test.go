package telemetry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func testDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validPlanSpan() Span {
	return Span{
		Name:     SpanPlan,
		IDSHA256: testDigest("plan-event"),
		Status:   SpanStarted,
		Attributes: []Attribute{
			{Key: AttributeStatus, Kind: AttributeEnumValue, Enum: StatusStarted},
			{Key: AttributePlanSHA256, Kind: AttributeDigestValue, SHA256: testDigest("plan")},
		},
	}
}

func validOutcomeSpan() Span {
	return Span{
		Name:     SpanOutcome,
		IDSHA256: testDigest("outcome-event"),
		Status:   SpanCompleted,
		Attributes: []Attribute{
			{Key: AttributeStatus, Kind: AttributeEnumValue, Enum: StatusCompleted},
			{Key: AttributeOutcome, Kind: AttributeEnumValue, Enum: OutcomeSucceeded},
		},
	}
}

func validPlanMetric(value uint64) Metric {
	return Metric{Name: MetricPlanTotal, Kind: MetricCounter, Value: value, Attributes: []Attribute{}}
}

func validOutcomeMetric(value uint64) Metric {
	return Metric{
		Name:  MetricOutcomeTotal,
		Kind:  MetricCounter,
		Value: value,
		Attributes: []Attribute{
			{Key: AttributeOutcome, Kind: AttributeEnumValue, Enum: OutcomeSucceeded},
		},
	}
}

func validProjectionInput() Projection {
	return Projection{
		Enabled: true,
		Spans: []Span{
			validOutcomeSpan(),
			validPlanSpan(),
		},
		Metrics: []Metric{
			validOutcomeMetric(1),
			validPlanMetric(1),
		},
	}
}

func requireCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q", want)
	}
	got, ok := CodeOf(err)
	if !ok || got != want {
		t.Fatalf("error code = %q, %v; want %q", got, ok, want)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error %q does not unwrap to ErrInvalid", got)
	}
}

func mustSeal(t *testing.T, input Projection) Projection {
	t.Helper()
	sealed, err := Seal(input)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	return sealed
}

func TestSealCanonicalizesAndCopiesInput(t *testing.T) {
	input := validProjectionInput()
	sealed := mustSeal(t, input)
	encoded, err := Encode(sealed)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if sealed.Schema != Schema || sealed.SchemaVersion != SchemaVersion || sealed.ContractVersion != ContractVersion {
		t.Fatalf("Seal() headers = %#v", sealed)
	}
	if sealed.Spans[0].Name != SpanPlan || sealed.Metrics[0].Name != MetricPlanTotal {
		t.Fatalf("Seal() did not use closed canonical ordering: %#v", sealed)
	}
	if sealed.Spans[0].Attributes[0].Key != AttributePlanSHA256 || sealed.Spans[1].Attributes[0].Key != AttributeOutcome {
		t.Fatalf("Seal() did not canonicalize attributes: %#v", sealed.Spans)
	}
	if err := Validate(sealed); err != nil {
		t.Fatalf("Validate(sealed) error = %v", err)
	}

	input.Spans[0].Attributes[0].Enum = "mutated"
	input.Metrics[0].Value = 99
	if sealed.Spans[1].Attributes[0].Enum == "mutated" || sealed.Metrics[1].Value == 99 {
		t.Fatal("Seal() retained caller-owned mutable state")
	}

	reversed := validProjectionInput()
	reversed.Spans[0], reversed.Spans[1] = reversed.Spans[1], reversed.Spans[0]
	reversed.Metrics[0], reversed.Metrics[1] = reversed.Metrics[1], reversed.Metrics[0]
	for index := range reversed.Spans {
		reversed.Spans[index].Attributes[0], reversed.Spans[index].Attributes[1] = reversed.Spans[index].Attributes[1], reversed.Spans[index].Attributes[0]
	}
	if other := mustSeal(t, reversed); !bytes.Equal(encoded, mustEncode(t, other)) {
		t.Fatal("equivalent arrival order produced different canonical bytes")
	}
}

func mustEncode(t *testing.T, projection Projection) []byte {
	t.Helper()
	encoded, err := Encode(projection)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return encoded
}

func TestDecodeIsStrictAndRoundTripsCanonicalBytes(t *testing.T) {
	sealed := mustSeal(t, validProjectionInput())
	encoded := mustEncode(t, sealed)
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got := mustEncode(t, decoded); !bytes.Equal(got, encoded) {
		t.Fatalf("Decode/Encode changed canonical bytes")
	}

	cases := map[string][]byte{
		"missing final LF":        encoded[:len(encoded)-1],
		"leading whitespace":      append([]byte{' '}, encoded...),
		"trailing bytes":          append(append([]byte{}, encoded...), 'x'),
		"unknown member":          bytes.Replace(encoded, []byte(`,"enabled"`), []byte(`,"private":"redacted","enabled"`), 1),
		"duplicate member":        bytes.Replace(encoded, []byte(`,"enabled"`), []byte(`,"enabled":true,"enabled"`), 1),
		"future schema version":   bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"stale projection digest": bytes.Replace(encoded, []byte(`"projection_sha256":"`+sealed.ProjectionSHA256+`"`), []byte(`"projection_sha256":"`+testDigest("stale")+`"`), 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			requireCode(t, decodeBytes(data), ErrorInvalidProjection)
		})
	}
}

func decodeBytes(data []byte) error {
	_, err := Decode(bytes.NewReader(data))
	return err
}

func TestCollectorIsExplicitlyEnabledAndBounded(t *testing.T) {
	disabled, err := NewCollector(Config{})
	if err != nil {
		t.Fatalf("NewCollector(Config{}) error = %v", err)
	}
	if disabled.Enabled() {
		t.Fatal("zero Config unexpectedly enabled telemetry")
	}
	if err := disabled.AddSpan(Span{Name: SpanName("prompt")}); err != nil {
		t.Fatalf("disabled AddSpan() error = %v", err)
	}
	if err := disabled.AddMetric(Metric{Name: MetricName("secret")}); err != nil {
		t.Fatalf("disabled AddMetric() error = %v", err)
	}
	disabledProjection, err := disabled.Projection()
	if err != nil {
		t.Fatalf("disabled Projection() error = %v", err)
	}
	if disabledProjection.Enabled || len(disabledProjection.Spans) != 0 || len(disabledProjection.Metrics) != 0 {
		t.Fatalf("disabled collector retained data: %#v", disabledProjection)
	}

	collector, err := NewCollector(Config{Enabled: true, MaxSpans: 2, MaxMetrics: 2})
	if err != nil {
		t.Fatalf("NewCollector(enabled) error = %v", err)
	}
	if err := collector.AddSpan(validPlanSpan()); err != nil {
		t.Fatalf("AddSpan(plan) error = %v", err)
	}
	requireCode(t, collector.AddSpan(validPlanSpan()), ErrorInvalidSpan)
	requireCode(t, projectionError(collector), ErrorInvalidSpan)

	collector, err = NewCollector(Config{Enabled: true, MaxSpans: 2, MaxMetrics: 2})
	if err != nil {
		t.Fatalf("NewCollector(enabled) error = %v", err)
	}
	if err := collector.AddSpan(validPlanSpan()); err != nil {
		t.Fatalf("AddSpan(plan) error = %v", err)
	}
	if err := collector.AddSpan(validOutcomeSpan()); err != nil {
		t.Fatalf("AddSpan(outcome) error = %v", err)
	}
	requireCode(t, collector.AddSpan(Span{
		Name: SpanGrader, IDSHA256: testDigest("grader-event"), Status: SpanCompleted,
	}), ErrorLimitExceeded)
	requireCode(t, projectionError(collector), ErrorLimitExceeded)

	collector, err = NewCollector(Config{Enabled: true, MaxSpans: 2, MaxMetrics: 2})
	if err != nil {
		t.Fatalf("NewCollector(metrics) error = %v", err)
	}
	if err := collector.AddMetric(validOutcomeMetric(1)); err != nil {
		t.Fatalf("AddMetric(outcome) error = %v", err)
	}
	requireCode(t, collector.AddMetric(validOutcomeMetric(2)), ErrorInvalidMetric)
	requireCode(t, projectionError(collector), ErrorInvalidMetric)

	collector, err = NewCollector(Config{Enabled: true, MaxSpans: 2, MaxMetrics: 2})
	if err != nil {
		t.Fatalf("NewCollector(metric bounds) error = %v", err)
	}
	if err := collector.AddMetric(validOutcomeMetric(1)); err != nil {
		t.Fatalf("AddMetric(outcome) error = %v", err)
	}
	if err := collector.AddMetric(validPlanMetric(1)); err != nil {
		t.Fatalf("AddMetric(plan) error = %v", err)
	}
	requireCode(t, collector.AddMetric(validPlanMetric(3)), ErrorLimitExceeded)
	requireCode(t, projectionError(collector), ErrorLimitExceeded)

	collector, err = NewCollector(Config{Enabled: true, MaxSpans: 2, MaxMetrics: 2})
	if err != nil {
		t.Fatalf("NewCollector(preflight) error = %v", err)
	}
	oversized := validPlanSpan()
	oversized.Attributes = make([]Attribute, MaxAttributes+1)
	requireCode(t, collector.AddSpan(oversized), ErrorLimitExceeded)
	requireCode(t, projectionError(collector), ErrorLimitExceeded)

	collector, err = NewCollector(Config{Enabled: true, MaxSpans: 2, MaxMetrics: 2})
	if err != nil {
		t.Fatalf("NewCollector(valid) error = %v", err)
	}
	if err := collector.AddSpan(validPlanSpan()); err != nil {
		t.Fatalf("AddSpan(plan) error = %v", err)
	}
	if err := collector.AddMetric(validOutcomeMetric(1)); err != nil {
		t.Fatalf("AddMetric(outcome) error = %v", err)
	}
	projection, err := collector.Projection()
	if err != nil {
		t.Fatalf("collector Projection() error = %v", err)
	}
	if err := Validate(projection); err != nil {
		t.Fatalf("Validate(collector projection) error = %v", err)
	}
}

func projectionError(collector *Collector) error {
	_, err := collector.Projection()
	return err
}

func TestCollectorConflictAndOverflowAreOrderIndependent(t *testing.T) {
	t.Run("span conflict", func(t *testing.T) {
		first := validPlanSpan()
		second := first
		second.Status = SpanCompleted
		second.Attributes = []Attribute{{Key: AttributeStatus, Kind: AttributeEnumValue, Enum: StatusCompleted}, {Key: AttributePlanSHA256, Kind: AttributeDigestValue, SHA256: testDigest("plan")}}
		for _, ordered := range [][2]Span{{first, second}, {second, first}} {
			collector, err := NewCollector(Config{Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := collector.AddSpan(ordered[0]); err != nil {
				t.Fatal(err)
			}
			requireCode(t, collector.AddSpan(ordered[1]), ErrorInvalidSpan)
			requireCode(t, projectionError(collector), ErrorInvalidSpan)
		}
	})
	t.Run("metric conflict", func(t *testing.T) {
		for _, values := range [][2]uint64{{1, 2}, {2, 1}} {
			collector, err := NewCollector(Config{Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := collector.AddMetric(validPlanMetric(values[0])); err != nil {
				t.Fatal(err)
			}
			requireCode(t, collector.AddMetric(validPlanMetric(values[1])), ErrorInvalidMetric)
			requireCode(t, projectionError(collector), ErrorInvalidMetric)
		}
	})
	t.Run("span overflow", func(t *testing.T) {
		for _, ordered := range [][2]Span{{validPlanSpan(), validOutcomeSpan()}, {validOutcomeSpan(), validPlanSpan()}} {
			collector, err := NewCollector(Config{Enabled: true, MaxSpans: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := collector.AddSpan(ordered[0]); err != nil {
				t.Fatal(err)
			}
			requireCode(t, collector.AddSpan(ordered[1]), ErrorLimitExceeded)
			requireCode(t, projectionError(collector), ErrorLimitExceeded)
		}
	})
}

func TestClosedVocabularyAndPrivacyBoundary(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Projection)
		code ErrorCode
	}{
		{name: "unknown span", edit: func(projection *Projection) { projection.Spans[0].Name = SpanName("prompt") }, code: ErrorInvalidSpan},
		{name: "unknown status", edit: func(projection *Projection) { projection.Spans[0].Status = SpanStatus("arbitrary") }, code: ErrorInvalidSpan},
		{name: "unknown attribute", edit: func(projection *Projection) { projection.Spans[0].Attributes[0].Key = AttributeKey("secret") }, code: ErrorInvalidSpan},
		{name: "wrong attribute kind", edit: func(projection *Projection) {
			projection.Spans[0].Attributes[0].Kind = AttributeDigestValue
			projection.Spans[0].Attributes[0].Enum = ""
			projection.Spans[0].Attributes[0].SHA256 = testDigest("wrong-kind")
		}, code: ErrorInvalidSpan},
		{name: "wrong attribute context", edit: func(projection *Projection) {
			projection.Spans[0].Attributes = append(projection.Spans[0].Attributes, Attribute{Key: AttributeOutcome, Kind: AttributeEnumValue, Enum: OutcomeSucceeded})
		}, code: ErrorInvalidSpan},
		{name: "unknown metric", edit: func(projection *Projection) { projection.Metrics[0].Name = MetricName("provider_message") }, code: ErrorInvalidMetric},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := validProjectionInput()
			test.edit(&input)
			requireCode(t, sealError(input), test.code)
		})
	}

	encoded := mustEncode(t, mustSeal(t, validProjectionInput()))
	for _, forbidden := range []string{"prompt", "secret", "provider_message", "https://", "Authorization"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("encoded projection contains forbidden content %q: %s", forbidden, encoded)
		}
	}
}

func sealError(input Projection) error {
	_, err := Seal(input)
	return err
}

func TestSealRejectsOversizedCollectionsBeforeTraversal(t *testing.T) {
	tooManyAttributes := validPlanSpan()
	tooManyAttributes.Attributes = make([]Attribute, MaxAttributes+1)
	input := validProjectionInput()
	input.Spans = append(input.Spans, tooManyAttributes)
	requireCode(t, sealError(input), ErrorLimitExceeded)

	tooManySpans := validProjectionInput()
	tooManySpans.Spans = make([]Span, MaxSpans+1)
	requireCode(t, sealError(tooManySpans), ErrorLimitExceeded)

	tooManyMetrics := validProjectionInput()
	tooManyMetrics.Metrics = make([]Metric, MaxMetrics+1)
	requireCode(t, sealError(tooManyMetrics), ErrorLimitExceeded)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWriteLocalSpoolRequiresExplicitWriter(t *testing.T) {
	sealed := mustSeal(t, validProjectionInput())
	want := mustEncode(t, sealed)
	var got bytes.Buffer
	if err := WriteLocalSpool(&got, sealed); err != nil {
		t.Fatalf("WriteLocalSpool() error = %v", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("spool bytes differ from Encode(): got %q want %q", got.Bytes(), want)
	}
	requireCode(t, WriteLocalSpool(nil, sealed), ErrorInvalidSpool)
	requireCode(t, WriteLocalSpool(zeroWriter{}, sealed), ErrorInvalidSpool)
	requireCode(t, WriteLocalSpool(errorWriter{}, sealed), ErrorInvalidSpool)
}

func TestValidateRejectsTampering(t *testing.T) {
	sealed := mustSeal(t, validProjectionInput())
	sealed.Spans[0].Status = SpanCompleted
	requireCode(t, Validate(sealed), ErrorInvalidSpan)
	_, err := Encode(sealed)
	requireCode(t, err, ErrorInvalidSpan)
}

func TestSpanStatusAttributeMustMatchSpanStatus(t *testing.T) {
	input := validProjectionInput()
	input.Spans[0].Status = SpanStarted
	requireCode(t, sealError(input), ErrorInvalidSpan)

	withoutStatus := validPlanSpan()
	withoutStatus.Attributes = []Attribute{{Key: AttributePlanSHA256, Kind: AttributeDigestValue, SHA256: testDigest("plan")}}
	if err := validateSpan(withoutStatus, false); err != nil {
		t.Fatalf("missing optional status attribute was rejected: %v", err)
	}
}

func FuzzDecodeRejectsUntrustedBytes(f *testing.F) {
	sealed, err := Seal(validProjectionInput())
	if err != nil {
		f.Fatalf("Seal() error = %v", err)
	}
	seed, err := Encode(sealed)
	if err != nil {
		f.Fatalf("Encode() error = %v", err)
	}
	f.Add(seed)
	f.Add([]byte(`{"schema":"agent-eval/telemetry-projection","schema_version":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		projection, err := Decode(bytes.NewReader(data))
		if err != nil {
			return
		}
		canonical, encodeErr := Encode(projection)
		if encodeErr != nil || !bytes.Equal(data, canonical) {
			t.Fatalf("accepted bytes are not canonical: err=%v", encodeErr)
		}
	})
}

func TestCanonicalOutputHasNoUnboundedWhitespace(t *testing.T) {
	encoded := mustEncode(t, mustSeal(t, validProjectionInput()))
	if !strings.HasSuffix(string(encoded), "\n") || strings.Contains(string(encoded[:len(encoded)-1]), "\n") {
		t.Fatalf("canonical output has invalid line framing: %q", encoded)
	}
}
