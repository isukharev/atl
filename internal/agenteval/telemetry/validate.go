package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type projectionCore struct {
	Schema          string   `json:"schema"`
	SchemaVersion   int      `json:"schema_version"`
	ContractVersion string   `json:"contract_version"`
	Enabled         bool     `json:"enabled"`
	Spans           []Span   `json:"spans"`
	Metrics         []Metric `json:"metrics"`
}

// Seal creates a canonical immutable projection. It sorts spans, metrics, and
// typed attributes so equal inputs produce equal bytes regardless of arrival
// timing or caller collection order.
func Seal(input Projection) (Projection, error) {
	if len(input.Spans) > MaxSpans || len(input.Metrics) > MaxMetrics {
		return Projection{}, fail(ErrorLimitExceeded)
	}
	for _, span := range input.Spans {
		if len(span.Attributes) > MaxAttributes {
			return Projection{}, fail(ErrorLimitExceeded)
		}
	}
	for _, metric := range input.Metrics {
		if len(metric.Attributes) > MaxAttributes {
			return Projection{}, fail(ErrorLimitExceeded)
		}
	}
	projection := cloneProjection(input)
	if projection.Schema != "" && projection.Schema != Schema ||
		projection.SchemaVersion != 0 && projection.SchemaVersion != SchemaVersion ||
		projection.ContractVersion != "" && projection.ContractVersion != ContractVersion {
		return Projection{}, fail(ErrorInvalidProjection)
	}
	projection.Schema = Schema
	projection.SchemaVersion = SchemaVersion
	projection.ContractVersion = ContractVersion
	if projection.Spans == nil {
		projection.Spans = []Span{}
	}
	if projection.Metrics == nil {
		projection.Metrics = []Metric{}
	}
	for index := range projection.Spans {
		span := &projection.Spans[index]
		span.Attributes = canonicalAttributes(span.Attributes)
		if err := validateSpan(*span, false); err != nil {
			return Projection{}, err
		}
	}
	for index := range projection.Metrics {
		metric := &projection.Metrics[index]
		metric.Attributes = canonicalAttributes(metric.Attributes)
		if err := validateMetric(*metric, false); err != nil {
			return Projection{}, err
		}
	}
	sort.SliceStable(projection.Spans, func(left, right int) bool {
		return spanBefore(projection.Spans[left], projection.Spans[right])
	})
	sort.SliceStable(projection.Metrics, func(left, right int) bool {
		return metricBefore(projection.Metrics[left], projection.Metrics[right])
	})
	providedDigest := projection.ProjectionSHA256
	projection.ProjectionSHA256 = ""
	digest, err := projectionDigest(projection)
	if err != nil {
		return Projection{}, fail(ErrorInvalidProjection)
	}
	if providedDigest != "" && providedDigest != digest {
		return Projection{}, fail(ErrorInvalidProjection)
	}
	projection.ProjectionSHA256 = digest
	if err := Validate(projection); err != nil {
		return Projection{}, err
	}
	return projection, nil
}

// Validate checks an already-sealed projection without mutating it.
func Validate(projection Projection) error {
	if err := validateProjectionShape(projection); err != nil {
		return err
	}
	digest, err := projectionDigest(projection)
	if err != nil || digest != projection.ProjectionSHA256 {
		return fail(ErrorInvalidProjection)
	}
	return nil
}

func validateProjectionShape(projection Projection) error {
	if projection.Schema != Schema || projection.SchemaVersion != SchemaVersion || projection.ContractVersion != ContractVersion ||
		projection.Spans == nil || projection.Metrics == nil || len(projection.Spans) > MaxSpans || len(projection.Metrics) > MaxMetrics ||
		!validDigest(projection.ProjectionSHA256) || (!projection.Enabled && (len(projection.Spans) != 0 || len(projection.Metrics) != 0)) {
		return fail(ErrorInvalidProjection)
	}
	seenSpans := make(map[string]bool, len(projection.Spans))
	var previousSpan Span
	for index, span := range projection.Spans {
		if err := validateSpan(span, true); err != nil {
			return err
		}
		key := spanKey(span)
		if seenSpans[key] || index > 0 && !spanBefore(previousSpan, span) {
			return fail(ErrorInvalidSpan)
		}
		seenSpans[key] = true
		previousSpan = span
	}
	seenMetrics := make(map[string]bool, len(projection.Metrics))
	var previousMetric Metric
	for index, metric := range projection.Metrics {
		if err := validateMetric(metric, true); err != nil {
			return err
		}
		key := metricSeriesKey(metric)
		if seenMetrics[key] || index > 0 && !metricBefore(previousMetric, metric) {
			return fail(ErrorInvalidMetric)
		}
		seenMetrics[key] = true
		previousMetric = metric
	}
	return nil
}

func validateSpan(span Span, requireCanonical bool) error {
	if spanOrdinal(span.Name) < 0 || !validDigest(span.IDSHA256) || spanStatusOrdinal(span.Status) < 0 ||
		span.Attributes == nil || len(span.Attributes) > MaxAttributes {
		return fail(ErrorInvalidSpan)
	}
	if err := validateAttributes(span.Attributes, func(key AttributeKey) bool { return spanAttributeAllowed(span.Name, key) }, requireCanonical); err != nil {
		return fail(ErrorInvalidSpan)
	}
	for _, attribute := range span.Attributes {
		if attribute.Key == AttributeStatus && attribute.Enum != AttributeEnum(span.Status) {
			return fail(ErrorInvalidSpan)
		}
	}
	return nil
}

func validateMetric(metric Metric, requireCanonical bool) error {
	if metricOrdinal(metric.Name) < 0 || (metric.Name == MetricQueueDepth && metric.Kind != MetricGauge) ||
		(metric.Name != MetricQueueDepth && metric.Kind != MetricCounter) || metric.Attributes == nil || len(metric.Attributes) > MaxAttributes {
		return fail(ErrorInvalidMetric)
	}
	if err := validateAttributes(metric.Attributes, func(key AttributeKey) bool { return metricAttributeAllowed(metric.Name, key) }, requireCanonical); err != nil {
		return fail(ErrorInvalidMetric)
	}
	return nil
}

func validateAttributes(attributes []Attribute, allowed func(AttributeKey) bool, requireCanonical bool) error {
	previous := -1
	seen := make(map[AttributeKey]bool, len(attributes))
	for _, attribute := range attributes {
		if !validAttribute(attribute) || !allowed(attribute.Key) || seen[attribute.Key] ||
			requireCanonical && attrOrdinal(attribute.Key) <= previous {
			return fail(ErrorInvalidAttribute)
		}
		seen[attribute.Key] = true
		previous = attrOrdinal(attribute.Key)
	}
	return nil
}

func canonicalAttributes(input []Attribute) []Attribute {
	if input == nil {
		return []Attribute{}
	}
	output := append([]Attribute{}, input...)
	sort.SliceStable(output, func(left, right int) bool {
		return attrOrdinal(output[left].Key) < attrOrdinal(output[right].Key)
	})
	return output
}

func validAttribute(attribute Attribute) bool {
	key := attrOrdinal(attribute.Key)
	if key < 0 {
		return false
	}
	switch attribute.Key {
	case AttributePlanSHA256, AttributeAttemptSHA256, AttributeAdapterSHA256, AttributeBackendSHA256, AttributeGraderSHA256:
		return attribute.Kind == AttributeDigestValue && validDigest(attribute.SHA256) && attribute.Enum == "" && attribute.Uint64 == 0
	case AttributeOutcome:
		return attribute.Kind == AttributeEnumValue && validOutcome(attribute.Enum) && attribute.SHA256 == "" && attribute.Uint64 == 0
	case AttributeStatus:
		return attribute.Kind == AttributeEnumValue && validAttributeStatus(attribute.Enum) && attribute.SHA256 == "" && attribute.Uint64 == 0
	case AttributeBoundKind:
		return attribute.Kind == AttributeEnumValue && validBoundKind(attribute.Enum) && attribute.SHA256 == "" && attribute.Uint64 == 0
	case AttributeCoverageKind:
		return attribute.Kind == AttributeEnumValue && validCoverageKind(attribute.Enum) && attribute.SHA256 == "" && attribute.Uint64 == 0
	case AttributeBoundLimit, AttributeCount:
		return attribute.Kind == AttributeUint64Value && attribute.Enum == "" && attribute.SHA256 == ""
	default:
		return false
	}
}

func spanAttributeAllowed(span SpanName, key AttributeKey) bool {
	switch key {
	case AttributeStatus:
		return true
	case AttributePlanSHA256:
		return span == SpanPlan
	case AttributeAttemptSHA256:
		return span == SpanAttempt
	case AttributeAdapterSHA256:
		return span == SpanAdapter
	case AttributeBackendSHA256:
		return span == SpanBackend
	case AttributeGraderSHA256:
		return span == SpanGrader
	case AttributeOutcome:
		return span == SpanOutcome
	case AttributeBoundKind, AttributeBoundLimit:
		return span == SpanQueue || span == SpanBounds
	case AttributeCoverageKind, AttributeCount:
		return span == SpanCoverage
	default:
		return false
	}
}

func metricAttributeAllowed(metric MetricName, key AttributeKey) bool {
	switch metric {
	case MetricOutcomeTotal:
		return key == AttributeOutcome
	case MetricBoundRefusalTotal:
		return key == AttributeBoundKind
	case MetricCoverageTotal:
		return key == AttributeCoverageKind
	default:
		return false
	}
}

func metricSortKey(metric Metric) string {
	data, _ := json.Marshal(struct {
		Name       MetricName  `json:"name"`
		Kind       MetricKind  `json:"kind"`
		Value      uint64      `json:"value"`
		Attributes []Attribute `json:"attributes"`
	}{metric.Name, metric.Kind, metric.Value, metric.Attributes})
	return string(data)
}

func metricSeriesKey(metric Metric) string {
	data, _ := json.Marshal(struct {
		Name       MetricName  `json:"name"`
		Kind       MetricKind  `json:"kind"`
		Attributes []Attribute `json:"attributes"`
	}{metric.Name, metric.Kind, metric.Attributes})
	return string(data)
}

func spanBefore(left, right Span) bool {
	leftName, rightName := spanOrdinal(left.Name), spanOrdinal(right.Name)
	if leftName != rightName {
		return leftName < rightName
	}
	return left.IDSHA256 < right.IDSHA256
}

func metricBefore(left, right Metric) bool {
	leftName, rightName := metricOrdinal(left.Name), metricOrdinal(right.Name)
	if leftName != rightName {
		return leftName < rightName
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return metricSortKey(left) < metricSortKey(right)
}

func projectionDigest(projection Projection) (string, error) {
	data, err := json.Marshal(projectionCore{Schema: projection.Schema, SchemaVersion: projection.SchemaVersion,
		ContractVersion: projection.ContractVersion, Enabled: projection.Enabled, Spans: cloneSpans(projection.Spans), Metrics: cloneMetrics(projection.Metrics)})
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/telemetry/v1\x00"))
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func spanOrdinal(name SpanName) int {
	for index, value := range closedSpanNames {
		if value == name {
			return index
		}
	}
	return -1
}

func metricOrdinal(name MetricName) int {
	for index, value := range closedMetricNames {
		if value == name {
			return index
		}
	}
	return -1
}

func attrOrdinal(key AttributeKey) int {
	for index, value := range closedAttributeKeys {
		if value == key {
			return index
		}
	}
	return -1
}

func spanStatusOrdinal(status SpanStatus) int {
	for index, value := range closedSpanStatuses {
		if value == status {
			return index
		}
	}
	return -1
}

func validOutcome(value AttributeEnum) bool {
	return value == OutcomeSucceeded || value == OutcomeFailed || value == OutcomeCanceled || value == OutcomeUnknown
}

func validAttributeStatus(value AttributeEnum) bool {
	return value == StatusStarted || value == StatusCompleted || value == StatusRefused || value == StatusErrored || value == StatusSkipped || value == StatusUnknown
}

func validBoundKind(value AttributeEnum) bool {
	return value == BoundRequests || value == BoundBytes || value == BoundTime || value == BoundTokens || value == BoundCost || value == BoundChildren
}

func validCoverageKind(value AttributeEnum) bool {
	return value == CoverageComplete || value == CoveragePartial || value == CoverageIncomplete || value == CoverageUnknown
}

func cloneProjection(input Projection) Projection {
	output := input
	output.Spans = cloneSpans(input.Spans)
	output.Metrics = cloneMetrics(input.Metrics)
	return output
}

func cloneSpans(input []Span) []Span {
	if input == nil {
		return nil
	}
	output := make([]Span, len(input))
	for index, span := range input {
		output[index] = span
		output[index].Attributes = append([]Attribute{}, span.Attributes...)
	}
	return output
}

func cloneMetrics(input []Metric) []Metric {
	if input == nil {
		return nil
	}
	output := make([]Metric, len(input))
	for index, metric := range input {
		output[index] = metric
		output[index].Attributes = append([]Attribute{}, metric.Attributes...)
	}
	return output
}
