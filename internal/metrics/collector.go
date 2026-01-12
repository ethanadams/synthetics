package metrics

import (
	"fmt"
	"time"

	"github.com/ethanadams/synthetics/internal/logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Collector manages Prometheus metrics for synthetic tests
type Collector struct {
	// Test execution metrics
	testRunsTotal   *prometheus.CounterVec
	testRunDuration *prometheus.HistogramVec

	// Unified Storj operation metrics
	storjDuration         *prometheus.HistogramVec
	storjBytes            *prometheus.CounterVec
	storjOperationCount   *prometheus.CounterVec
	storjOperationSuccess *prometheus.CounterVec

	// Granular HTTP timing metrics (for S3 executors)
	httpTiming *prometheus.HistogramVec

	// Live/instant metrics (Gauges for real-time visibility)
	lastDuration  *prometheus.GaugeVec
	lastHTTPPhase *prometheus.GaugeVec
}

// HTTPTimings holds detailed HTTP timing breakdown
type HTTPTimings struct {
	DNSLookup    time.Duration
	TCPConnect   time.Duration
	TLSHandshake time.Duration
	TTFB         time.Duration // Time to first byte (from request sent to first response byte)
	Transfer     time.Duration // Data transfer time
	Total        time.Duration
}

// NewCollector creates a new metrics collector
func NewCollector() *Collector {
	return &Collector{
		testRunsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "synthetics_test_runs_total",
				Help: "Total number of synthetic test runs",
			},
			[]string{"test_name", "step_name", "executor", "status"},
		),
		testRunDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "synthetics_test_duration_seconds",
				Help:    "Duration of synthetic test runs",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"test_name", "step_name", "executor"},
		),
		storjDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "synth_duration_seconds",
				Help:    "Duration of Storj operations (upload, download, etc.)",
				Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0},
			},
			[]string{"test_name", "action", "executor", "bucket", "file_size"},
		),
		storjBytes: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "synth_bytes_total",
				Help: "Total bytes transferred (uploaded/downloaded) to/from Storj",
			},
			[]string{"test_name", "action", "executor", "bucket"},
		),
		storjOperationCount: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "synth_operation_count_total",
				Help: "Total count of Storj operations",
			},
			[]string{"test_name", "action", "executor", "bucket"},
		),
		storjOperationSuccess: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "synth_operation_success_total",
				Help: "Total successful Storj operations",
			},
			[]string{"test_name", "action", "executor", "status"},
		),
		httpTiming: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "synth_http_timing_seconds",
				Help:    "Granular HTTP timing breakdown (dns, connect, tls, ttfb, transfer)",
				Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
			},
			[]string{"test_name", "action", "executor", "phase"},
		),
		lastDuration: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "synth_last_duration_seconds",
				Help: "Duration of the most recent operation (live/instant value)",
			},
			[]string{"test_name", "action", "executor"},
		),
		lastHTTPPhase: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "synth_last_http_phase_seconds",
				Help: "Most recent HTTP phase timing (live/instant value)",
			},
			[]string{"test_name", "action", "executor", "phase"},
		),
	}
}

// RecordTestRun records a test execution
func (c *Collector) RecordTestRun(testName, stepName, executor string, success bool, duration time.Duration) {
	status := "success"
	if !success {
		status = "failure"
	}
	c.testRunsTotal.WithLabelValues(testName, stepName, executor, status).Inc()
	c.testRunDuration.WithLabelValues(testName, stepName, executor).Observe(duration.Seconds())
}

// RecordStorjUpload records a Storj upload operation
func (c *Collector) RecordStorjUpload(testName, executor, bucket, fileSize string, duration time.Duration, bytes int64, success bool) {
	const action = "upload"
	if fileSize != "" && duration > 0 {
		c.storjDuration.WithLabelValues(testName, action, executor, bucket, fileSize).Observe(duration.Seconds())
		logging.Debug("    RecordStorjUpload histogram: test=%s executor=%s fileSize=%s duration=%v", testName, executor, fileSize, duration)
	}
	// Update live duration gauge only when duration is provided
	if duration > 0 {
		c.lastDuration.WithLabelValues(testName, action, executor).Set(duration.Seconds())
		logging.Debug("    RecordStorjUpload gauge: test=%s executor=%s duration=%v", testName, executor, duration)
	}
	if success {
		c.storjBytes.WithLabelValues(testName, action, executor, bucket).Add(float64(bytes))
		c.storjOperationCount.WithLabelValues(testName, action, executor, bucket).Inc()
		c.storjOperationSuccess.WithLabelValues(testName, action, executor, "success").Inc()
	} else {
		c.storjOperationSuccess.WithLabelValues(testName, action, executor, "failure").Inc()
	}
}

// RecordStorjDownload records a Storj download operation
func (c *Collector) RecordStorjDownload(testName, executor, bucket, fileSize string, duration time.Duration, bytes int64, success bool) {
	const action = "download"
	// If no file size provided, derive from bytes (for downloads without config)
	if fileSize == "" && bytes > 0 {
		fileSize = formatBytesLabel(bytes)
	}
	// Fallback to "unknown" if we still don't have a file size (ensures histogram is always recorded)
	if fileSize == "" {
		fileSize = "unknown"
		logging.Debug("    RecordStorjDownload: no file size available (bytes=%d), using 'unknown' label", bytes)
	}

	if duration > 0 {
		c.storjDuration.WithLabelValues(testName, action, executor, bucket, fileSize).Observe(duration.Seconds())
		logging.Debug("    RecordStorjDownload histogram: test=%s executor=%s fileSize=%s duration=%v", testName, executor, fileSize, duration)
	}
	// Update live duration gauge only when duration is provided
	if duration > 0 {
		c.lastDuration.WithLabelValues(testName, action, executor).Set(duration.Seconds())
		logging.Debug("    RecordStorjDownload gauge: test=%s executor=%s duration=%v", testName, executor, duration)
	}
	if success {
		c.storjBytes.WithLabelValues(testName, action, executor, bucket).Add(float64(bytes))
		c.storjOperationCount.WithLabelValues(testName, action, executor, bucket).Inc()
		c.storjOperationSuccess.WithLabelValues(testName, action, executor, "success").Inc()
	} else {
		c.storjOperationSuccess.WithLabelValues(testName, action, executor, "failure").Inc()
	}
}


// formatBytesLabel converts bytes to human-readable label matching configured sizes
func formatBytesLabel(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes >= GB && bytes%(GB) == 0:
		return fmt.Sprintf("%dGB", bytes/GB)
	case bytes >= MB && bytes%(MB) == 0:
		return fmt.Sprintf("%dMB", bytes/MB)
	case bytes >= KB && bytes%(KB) == 0:
		return fmt.Sprintf("%dKB", bytes/KB)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// RecordStorjList records a Storj list operation
func (c *Collector) RecordStorjList(testName, executor, bucket string, success bool) {
	const action = "list"
	status := "success"
	if !success {
		status = "failure"
	}
	c.storjOperationSuccess.WithLabelValues(testName, action, executor, status).Inc()
	if success {
		c.storjOperationCount.WithLabelValues(testName, action, executor, bucket).Inc()
	}
}

// RecordHTTPTiming records granular HTTP timing breakdown
func (c *Collector) RecordHTTPTiming(testName, action, executor string, timings HTTPTimings) {
	if timings.DNSLookup > 0 {
		c.httpTiming.WithLabelValues(testName, action, executor, "dns").Observe(timings.DNSLookup.Seconds())
		c.lastHTTPPhase.WithLabelValues(testName, action, executor, "dns").Set(timings.DNSLookup.Seconds())
	}
	if timings.TCPConnect > 0 {
		c.httpTiming.WithLabelValues(testName, action, executor, "connect").Observe(timings.TCPConnect.Seconds())
		c.lastHTTPPhase.WithLabelValues(testName, action, executor, "connect").Set(timings.TCPConnect.Seconds())
	}
	if timings.TLSHandshake > 0 {
		c.httpTiming.WithLabelValues(testName, action, executor, "tls").Observe(timings.TLSHandshake.Seconds())
		c.lastHTTPPhase.WithLabelValues(testName, action, executor, "tls").Set(timings.TLSHandshake.Seconds())
	}
	if timings.TTFB > 0 {
		c.httpTiming.WithLabelValues(testName, action, executor, "ttfb").Observe(timings.TTFB.Seconds())
		c.lastHTTPPhase.WithLabelValues(testName, action, executor, "ttfb").Set(timings.TTFB.Seconds())
	}
	if timings.Transfer > 0 {
		c.httpTiming.WithLabelValues(testName, action, executor, "transfer").Observe(timings.Transfer.Seconds())
		c.lastHTTPPhase.WithLabelValues(testName, action, executor, "transfer").Set(timings.Transfer.Seconds())
	}
	if timings.Total > 0 {
		c.httpTiming.WithLabelValues(testName, action, executor, "total").Observe(timings.Total.Seconds())
		c.lastHTTPPhase.WithLabelValues(testName, action, executor, "total").Set(timings.Total.Seconds())
	}
}

// RecordHTTPTimingPhase records a single timing phase (e.g., "sign")
func (c *Collector) RecordHTTPTimingPhase(testName, action, executor, phase string, duration time.Duration) {
	if duration > 0 {
		c.httpTiming.WithLabelValues(testName, action, executor, phase).Observe(duration.Seconds())
		c.lastHTTPPhase.WithLabelValues(testName, action, executor, phase).Set(duration.Seconds())
	}
}

// RecordStorjDelete records a Storj delete operation
func (c *Collector) RecordStorjDelete(testName, executor, bucket, fileSize string, duration time.Duration, count int, success bool) {
	const action = "delete"

	// Record duration histogram (if file size label provided)
	if fileSize != "" && duration > 0 {
		c.storjDuration.WithLabelValues(testName, action, executor, bucket, fileSize).Observe(duration.Seconds())
	}

	// Always update the live duration gauge
	if duration > 0 {
		c.lastDuration.WithLabelValues(testName, action, executor).Set(duration.Seconds())
	}

	// Record success/failure status
	status := "success"
	if !success {
		status = "failure"
	}
	c.storjOperationSuccess.WithLabelValues(testName, action, executor, status).Inc()

	// Record operation count
	if success && count > 0 {
		c.storjOperationCount.WithLabelValues(testName, action, executor, bucket).Add(float64(count))
	}
}
