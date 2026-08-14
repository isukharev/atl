// Package telemetry owns a provider-free, content-minimized telemetry
// projection. It has no exporter, endpoint discovery, network, credential,
// filesystem, SDK, or provider authority. Callers explicitly supply a local
// writer only after making that authority decision.
package telemetry

import "errors"

const (
	Schema          = "agent-eval/telemetry-projection"
	SchemaVersion   = 1
	ContractVersion = "0.1.0-pre-release"

	MaxProjectionBytes = 1 << 20
	MaxSpans           = 64
	MaxMetrics         = 128
	MaxAttributes      = 16
	MaxJSONDepth       = 64
)

var ErrInvalid = errors.New("telemetry_projection_invalid")

type ErrorCode string

const (
	ErrorInvalidProjection ErrorCode = "invalid_telemetry_projection"
	ErrorInvalidSpan       ErrorCode = "invalid_telemetry_span"
	ErrorInvalidMetric     ErrorCode = "invalid_telemetry_metric"
	ErrorInvalidAttribute  ErrorCode = "invalid_telemetry_attribute"
	ErrorInvalidConfig     ErrorCode = "invalid_telemetry_config"
	ErrorInvalidSpool      ErrorCode = "invalid_telemetry_spool"
	ErrorLimitExceeded     ErrorCode = "telemetry_limit_exceeded"
)

type Error struct{ code ErrorCode }

func (e *Error) Error() string   { return string(e.code) }
func (e *Error) Unwrap() error   { return ErrInvalid }
func (e *Error) Code() ErrorCode { return e.code }

func fail(code ErrorCode) error { return &Error{code: code} }

// CodeOf returns a stable, content-free telemetry validation class.
func CodeOf(err error) (ErrorCode, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Code(), true
}

// SpanName identifies one closed evaluator lifecycle span. There are no
// arbitrary span names because names are a high-cardinality data-egress path.
type SpanName string

const (
	SpanPlan      SpanName = "plan"
	SpanQueue     SpanName = "queue"
	SpanAttempt   SpanName = "attempt"
	SpanAdapter   SpanName = "adapter"
	SpanBackend   SpanName = "backend"
	SpanGrader    SpanName = "grader"
	SpanOutcome   SpanName = "outcome"
	SpanBounds    SpanName = "bounds"
	SpanCoverage  SpanName = "coverage"
	SpanAggregate SpanName = "aggregate"
)

var closedSpanNames = [...]SpanName{
	SpanPlan,
	SpanQueue,
	SpanAttempt,
	SpanAdapter,
	SpanBackend,
	SpanGrader,
	SpanOutcome,
	SpanBounds,
	SpanCoverage,
	SpanAggregate,
}

// SpanNames returns the closed span vocabulary in canonical order.
func SpanNames() []SpanName {
	values := make([]SpanName, len(closedSpanNames))
	copy(values, closedSpanNames[:])
	return values
}

type SpanStatus string

const (
	SpanStarted   SpanStatus = "started"
	SpanCompleted SpanStatus = "completed"
	SpanRefused   SpanStatus = "refused"
	SpanErrored   SpanStatus = "errored"
	SpanSkipped   SpanStatus = "skipped"
	SpanUnknown   SpanStatus = "unknown"
)

var closedSpanStatuses = [...]SpanStatus{
	SpanStarted, SpanCompleted, SpanRefused, SpanErrored, SpanSkipped, SpanUnknown,
}

// MetricName identifies one closed evaluator metric series.
type MetricName string

const (
	MetricPlanTotal         MetricName = "plan_total"
	MetricQueueDepth        MetricName = "queue_depth"
	MetricAttemptTotal      MetricName = "attempt_total"
	MetricAdapterTotal      MetricName = "adapter_total"
	MetricBackendTotal      MetricName = "backend_total"
	MetricGraderTotal       MetricName = "grader_total"
	MetricOutcomeTotal      MetricName = "outcome_total"
	MetricBoundRefusalTotal MetricName = "bound_refusal_total"
	MetricCoverageTotal     MetricName = "coverage_total"
	MetricAggregateTotal    MetricName = "aggregate_total"
)

var closedMetricNames = [...]MetricName{
	MetricPlanTotal,
	MetricQueueDepth,
	MetricAttemptTotal,
	MetricAdapterTotal,
	MetricBackendTotal,
	MetricGraderTotal,
	MetricOutcomeTotal,
	MetricBoundRefusalTotal,
	MetricCoverageTotal,
	MetricAggregateTotal,
}

// MetricNames returns the closed metric vocabulary in canonical order.
func MetricNames() []MetricName {
	values := make([]MetricName, len(closedMetricNames))
	copy(values, closedMetricNames[:])
	return values
}

type MetricKind string

const (
	MetricCounter MetricKind = "counter"
	MetricGauge   MetricKind = "gauge"
)

// AttributeKey is a closed, content-minimized key vocabulary. Values are
// typed below and each span/metric name admits only a reviewed subset.
type AttributeKey string

const (
	AttributePlanSHA256    AttributeKey = "plan_sha256"
	AttributeAttemptSHA256 AttributeKey = "attempt_sha256"
	AttributeAdapterSHA256 AttributeKey = "adapter_sha256"
	AttributeBackendSHA256 AttributeKey = "backend_sha256"
	AttributeGraderSHA256  AttributeKey = "grader_sha256"
	AttributeOutcome       AttributeKey = "outcome"
	AttributeStatus        AttributeKey = "status"
	AttributeBoundKind     AttributeKey = "bound_kind"
	AttributeBoundLimit    AttributeKey = "bound_limit"
	AttributeCoverageKind  AttributeKey = "coverage_kind"
	AttributeCount         AttributeKey = "count"
)

var closedAttributeKeys = [...]AttributeKey{
	AttributePlanSHA256,
	AttributeAttemptSHA256,
	AttributeAdapterSHA256,
	AttributeBackendSHA256,
	AttributeGraderSHA256,
	AttributeOutcome,
	AttributeStatus,
	AttributeBoundKind,
	AttributeBoundLimit,
	AttributeCoverageKind,
	AttributeCount,
}

// AttributeKeys returns the complete closed attribute vocabulary.
func AttributeKeys() []AttributeKey {
	values := make([]AttributeKey, len(closedAttributeKeys))
	copy(values, closedAttributeKeys[:])
	return values
}

type AttributeValueKind string

const (
	AttributeEnumValue   AttributeValueKind = "enum"
	AttributeDigestValue AttributeValueKind = "digest"
	AttributeUint64Value AttributeValueKind = "uint64"
)

type AttributeEnum string

const (
	OutcomeSucceeded AttributeEnum = "succeeded"
	OutcomeFailed    AttributeEnum = "failed"
	OutcomeCanceled  AttributeEnum = "canceled"
	OutcomeUnknown   AttributeEnum = "unknown"

	StatusStarted   AttributeEnum = "started"
	StatusCompleted AttributeEnum = "completed"
	StatusRefused   AttributeEnum = "refused"
	StatusErrored   AttributeEnum = "errored"
	StatusSkipped   AttributeEnum = "skipped"
	StatusUnknown   AttributeEnum = "unknown"

	BoundRequests AttributeEnum = "requests"
	BoundBytes    AttributeEnum = "bytes"
	BoundTime     AttributeEnum = "time"
	BoundTokens   AttributeEnum = "tokens"
	BoundCost     AttributeEnum = "cost"
	BoundChildren AttributeEnum = "children"

	CoverageComplete   AttributeEnum = "complete"
	CoveragePartial    AttributeEnum = "partial"
	CoverageIncomplete AttributeEnum = "incomplete"
	CoverageUnknown    AttributeEnum = "unknown"
)

// Attribute is a typed closed value. Exactly one value member is meaningful
// according to Kind; arbitrary strings and JSON values are impossible.
type Attribute struct {
	Key    AttributeKey       `json:"key"`
	Kind   AttributeValueKind `json:"kind"`
	Enum   AttributeEnum      `json:"enum"`
	SHA256 string             `json:"sha256"`
	Uint64 uint64             `json:"uint64"`
}

// Span contains no timestamp, duration, path, URL, argument, result, or
// provider message. IDSHA256 is the caller-provided opaque event identity
// used to make canonical ordering independent of arrival timing.
type Span struct {
	Name       SpanName    `json:"name"`
	IDSHA256   string      `json:"id_sha256"`
	Status     SpanStatus  `json:"status"`
	Attributes []Attribute `json:"attributes"`
}

// Metric contains one bounded numeric series. Dimensions reuse the closed
// attribute type and are restricted by metric name during validation.
type Metric struct {
	Name       MetricName  `json:"name"`
	Kind       MetricKind  `json:"kind"`
	Value      uint64      `json:"value"`
	Attributes []Attribute `json:"attributes"`
}

// Projection is a closed canonical local snapshot. Enabled=false is the
// default and requires empty spans and metrics, so no ambient telemetry is
// emitted or accumulated.
type Projection struct {
	Schema           string   `json:"schema"`
	SchemaVersion    int      `json:"schema_version"`
	ContractVersion  string   `json:"contract_version"`
	Enabled          bool     `json:"enabled"`
	Spans            []Span   `json:"spans"`
	Metrics          []Metric `json:"metrics"`
	ProjectionSHA256 string   `json:"projection_sha256"`
}

// Config controls the explicit collector boundary. A zero Config is disabled
// and has no side effects. Non-zero limits are upper bounds for this collector
// within the package-wide maxima.
type Config struct {
	Enabled    bool
	MaxSpans   int
	MaxMetrics int
}
