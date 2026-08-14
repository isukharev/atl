package telemetry

import "sync"

// Collector accumulates only validated closed records while explicitly
// enabled. It never starts an exporter or performs I/O. The mutex makes a
// collector safe for independent lifecycle producers without making arrival
// order part of the resulting projection.
type Collector struct {
	mu         sync.Mutex
	enabled    bool
	maxSpans   int
	maxMetrics int
	spans      []Span
	metrics    []Metric
	invalid    ErrorCode
}

// NewCollector creates a bounded collector. Config{} is disabled and has no
// accumulation side effect; limits of zero select the package defaults.
func NewCollector(config Config) (*Collector, error) {
	maxSpans, maxMetrics := config.MaxSpans, config.MaxMetrics
	if maxSpans == 0 {
		maxSpans = MaxSpans
	}
	if maxMetrics == 0 {
		maxMetrics = MaxMetrics
	}
	if maxSpans < 1 || maxSpans > MaxSpans || maxMetrics < 1 || maxMetrics > MaxMetrics {
		return nil, fail(ErrorInvalidConfig)
	}
	return &Collector{enabled: config.Enabled, maxSpans: maxSpans, maxMetrics: maxMetrics,
		spans: []Span{}, metrics: []Metric{}}, nil
}

// Enabled reports whether this collector is explicitly accumulating records.
func (collector *Collector) Enabled() bool {
	if collector == nil {
		return false
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return collector.enabled
}

// AddSpan admits one closed span. Disabled collectors intentionally ignore
// input before validation so default-off telemetry cannot inspect or retain
// source data.
func (collector *Collector) AddSpan(span Span) error {
	if collector == nil {
		return fail(ErrorInvalidConfig)
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if !collector.enabled {
		return nil
	}
	if collector.invalid != "" {
		return fail(collector.invalid)
	}
	if len(collector.spans) >= collector.maxSpans {
		collector.invalid = ErrorLimitExceeded
		return fail(collector.invalid)
	}
	if len(span.Attributes) > MaxAttributes {
		collector.invalid = ErrorLimitExceeded
		return fail(collector.invalid)
	}
	span.Attributes = canonicalAttributes(span.Attributes)
	if err := validateSpan(span, false); err != nil {
		collector.invalid = errorCode(err, ErrorInvalidSpan)
		return err
	}
	key := spanKey(span)
	for _, existing := range collector.spans {
		if spanKey(existing) == key {
			collector.invalid = ErrorInvalidSpan
			return fail(collector.invalid)
		}
	}
	collector.spans = append(collector.spans, span)
	return nil
}

// AddMetric admits one closed metric series. A repeated series is rejected so
// callers must aggregate explicitly rather than making arrival order
// observable.
func (collector *Collector) AddMetric(metric Metric) error {
	if collector == nil {
		return fail(ErrorInvalidConfig)
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if !collector.enabled {
		return nil
	}
	if collector.invalid != "" {
		return fail(collector.invalid)
	}
	if len(collector.metrics) >= collector.maxMetrics {
		collector.invalid = ErrorLimitExceeded
		return fail(collector.invalid)
	}
	if len(metric.Attributes) > MaxAttributes {
		collector.invalid = ErrorLimitExceeded
		return fail(collector.invalid)
	}
	metric.Attributes = canonicalAttributes(metric.Attributes)
	if err := validateMetric(metric, false); err != nil {
		collector.invalid = errorCode(err, ErrorInvalidMetric)
		return err
	}
	key := metricKey(metric)
	for _, existing := range collector.metrics {
		if metricKey(existing) == key {
			collector.invalid = ErrorInvalidMetric
			return fail(collector.invalid)
		}
	}
	collector.metrics = append(collector.metrics, metric)
	return nil
}

// Projection snapshots and seals the collector without retaining caller
// slices after the method returns.
func (collector *Collector) Projection() (Projection, error) {
	if collector == nil {
		return Projection{}, fail(ErrorInvalidConfig)
	}
	collector.mu.Lock()
	if collector.invalid != "" {
		invalid := collector.invalid
		collector.mu.Unlock()
		return Projection{}, fail(invalid)
	}
	projection := Projection{Schema: Schema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		Enabled: collector.enabled, Spans: cloneSpans(collector.spans), Metrics: cloneMetrics(collector.metrics)}
	collector.mu.Unlock()
	return Seal(projection)
}

func errorCode(err error, fallback ErrorCode) ErrorCode {
	if code, ok := CodeOf(err); ok {
		return code
	}
	return fallback
}

func spanKey(span Span) string { return string(span.Name) + "\x00" + span.IDSHA256 }

func metricKey(metric Metric) string { return metricSeriesKey(metric) }
