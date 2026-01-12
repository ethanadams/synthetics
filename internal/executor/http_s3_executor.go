package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/ethanadams/synthetics/internal/config"
	"github.com/ethanadams/synthetics/internal/executor/awsv4"
	"github.com/ethanadams/synthetics/internal/jitter"
	"github.com/ethanadams/synthetics/internal/logging"
	"github.com/ethanadams/synthetics/internal/metrics"
	"github.com/oklog/ulid/v2"
)

// httpTimingTracer captures detailed HTTP timing using httptrace
type httpTimingTracer struct {
	start            time.Time
	dnsStart         time.Time
	dnsDone          time.Time
	connectStart     time.Time
	connectDone      time.Time
	tlsStart         time.Time
	tlsDone          time.Time
	firstByteTime    time.Time
	wroteRequest     time.Time
}

func newHTTPTimingTracer() *httpTimingTracer {
	return &httpTimingTracer{start: time.Now()}
}

func (t *httpTimingTracer) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:             func(_ httptrace.DNSStartInfo) { t.dnsStart = time.Now() },
		DNSDone:              func(_ httptrace.DNSDoneInfo) { t.dnsDone = time.Now() },
		ConnectStart:         func(_, _ string) { t.connectStart = time.Now() },
		ConnectDone:          func(_, _ string, _ error) { t.connectDone = time.Now() },
		TLSHandshakeStart:    func() { t.tlsStart = time.Now() },
		TLSHandshakeDone:     func(_ tls.ConnectionState, _ error) { t.tlsDone = time.Now() },
		WroteRequest:         func(_ httptrace.WroteRequestInfo) { t.wroteRequest = time.Now() },
		GotFirstResponseByte: func() { t.firstByteTime = time.Now() },
	}
}

func (t *httpTimingTracer) toMetrics(transferDone time.Time) metrics.HTTPTimings {
	timings := metrics.HTTPTimings{
		Total: transferDone.Sub(t.start),
	}

	if !t.dnsStart.IsZero() && !t.dnsDone.IsZero() {
		timings.DNSLookup = t.dnsDone.Sub(t.dnsStart)
	}
	if !t.connectStart.IsZero() && !t.connectDone.IsZero() {
		timings.TCPConnect = t.connectDone.Sub(t.connectStart)
	}
	if !t.tlsStart.IsZero() && !t.tlsDone.IsZero() {
		timings.TLSHandshake = t.tlsDone.Sub(t.tlsStart)
	}
	if !t.wroteRequest.IsZero() && !t.firstByteTime.IsZero() {
		timings.TTFB = t.firstByteTime.Sub(t.wroteRequest)
	}
	if !t.firstByteTime.IsZero() {
		timings.Transfer = transferDone.Sub(t.firstByteTime)
	}

	return timings
}

const executorNameHttpS3 = "http-s3"

// HttpS3Executor runs S3 tests using raw HTTP requests (no AWS SDK).
type HttpS3Executor struct {
	client   *http.Client
	endpoint string
	signer   *awsv4.Signer // Cached signer for efficiency
	config   *config.Config
	metrics  *metrics.Collector
}

// NewHttpS3 creates a new HTTP-based S3 executor.
func NewHttpS3(cfg *config.Config, mc *metrics.Collector) (*HttpS3Executor, error) {
	if cfg.S3.Endpoint == "" {
		return nil, fmt.Errorf("S3 endpoint is required")
	}
	if cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		return nil, fmt.Errorf("S3 access key and secret key are required")
	}

	region := cfg.S3.Region
	if region == "" {
		region = "us-east-1" // Default region for S3 compatible services
	}

	creds := awsv4.Credentials{
		AccessKey: cfg.S3.AccessKey,
		SecretKey: cfg.S3.SecretKey,
		Region:    region,
	}

	return &HttpS3Executor{
		client: &http.Client{
			Timeout: 5 * time.Minute, // Default timeout, overridden per-request
		},
		endpoint: cfg.S3.Endpoint,
		signer:   awsv4.NewSigner(creds), // Cached signer
		config:   cfg,
		metrics:  mc,
	}, nil
}

// ensureBucket creates the bucket if it doesn't exist
func (e *HttpS3Executor) ensureBucket(ctx context.Context, bucket string) error {
	// Check if bucket exists by trying to HEAD it
	headURL := fmt.Sprintf("%s/%s", e.endpoint, bucket)
	headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, headURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create HEAD request: %w", err)
	}
	if err := e.signer.Sign(headReq); err != nil {
		return fmt.Errorf("failed to sign HEAD request: %w", err)
	}

	headResp, err := e.client.Do(headReq)
	if err == nil {
		headResp.Body.Close()
		if headResp.StatusCode == http.StatusOK {
			// Bucket exists
			return nil
		}
	}

	// Try to create the bucket with PUT
	putURL := fmt.Sprintf("%s/%s", e.endpoint, bucket)
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create PUT request: %w", err)
	}
	if err := e.signer.Sign(putReq); err != nil {
		return fmt.Errorf("failed to sign PUT request: %w", err)
	}

	putResp, err := e.client.Do(putReq)
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}
	putResp.Body.Close()

	if putResp.StatusCode == http.StatusOK || putResp.StatusCode == http.StatusCreated {
		log.Printf("    Created bucket: %s", bucket)
	} else if putResp.StatusCode != http.StatusConflict {
		// 409 Conflict usually means bucket already exists, which is fine
		log.Printf("    Note: CreateBucket returned status %d (may be ignorable if bucket exists)", putResp.StatusCode)
	}

	// Verify bucket is now accessible
	verifyReq, err := http.NewRequestWithContext(ctx, http.MethodHead, headURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create verify request: %w", err)
	}
	if err := e.signer.Sign(verifyReq); err != nil {
		return fmt.Errorf("failed to sign verify request: %w", err)
	}

	verifyResp, err := e.client.Do(verifyReq)
	if err != nil {
		return fmt.Errorf("bucket %s not accessible after creation attempt: %w", bucket, err)
	}
	verifyResp.Body.Close()

	if verifyResp.StatusCode != http.StatusOK {
		return fmt.Errorf("bucket %s not accessible after creation attempt: status %d", bucket, verifyResp.StatusCode)
	}

	return nil
}

// RunTest executes an HTTP S3 test (handles single or multi-step).
func (e *HttpS3Executor) RunTest(ctx context.Context, test *config.Test) error {
	log.Printf("Running HTTP S3 test: %s", test.Name)

	testStart := time.Now()

	// Generate ULID for this test run
	entropy := ulid.Monotonic(rand.Reader, 0)
	testULID := ulid.MustNew(ulid.Timestamp(testStart), entropy)
	sharedFilename := test.GetFilename(testULID.String())
	bucket := test.GetBucket(e.config.Satellite.Bucket)

	// Ensure bucket exists before running test
	if err := e.ensureBucket(ctx, bucket); err != nil {
		return fmt.Errorf("failed to ensure bucket %s exists: %w", bucket, err)
	}

	isSingleStep := test.IsSingleStep()

	if isSingleStep {
		log.Printf("HTTP S3 test %s using ULID: %s (filename: %s, bucket: %s)",
			test.Name, testULID.String(), sharedFilename, bucket)
	} else {
		log.Printf("HTTP S3 test %s (%d steps) using ULID: %s (filename: %s, bucket: %s)",
			test.Name, len(test.Steps), testULID.String(), sharedFilename, bucket)
	}

	// Run each step sequentially
	for i, step := range test.Steps {
		if !isSingleStep {
			log.Printf("  [%d/%d] Running: %s", i+1, len(test.Steps), step.Name)
		}

		if err := e.runStep(ctx, test.Name, &step, sharedFilename, bucket, isSingleStep); err != nil {
			if !isSingleStep {
				log.Printf("  [%d/%d] Failed: %s - %v", i+1, len(test.Steps), step.Name, err)
			}
			e.metrics.RecordTestRun(test.Name, step.Name, executorNameHttpS3, false, time.Since(testStart))
			return fmt.Errorf("HTTP S3 test %s failed at step %s: %w", test.Name, step.Name, err)
		}

		if !isSingleStep {
			log.Printf("  [%d/%d] Completed: %s", i+1, len(test.Steps), step.Name)
		}
	}

	duration := time.Since(testStart)
	log.Printf("HTTP S3 test %s completed successfully in %v", test.Name, duration)
	e.metrics.RecordTestRun(test.Name, "", executorNameHttpS3, true, duration)

	return nil
}

// runStep executes a single HTTP S3 test step.
func (e *HttpS3Executor) runStep(ctx context.Context, testName string, step *config.TestStep, filename, bucket string, isSingleStep bool) error {
	// Apply step-level jitter if configured
	if step.Jitter != nil && step.Jitter.IsEnabled() {
		maxJitter, _ := step.Jitter.ParseMaxJitter(0) // Steps use duration only, not percentage
		if maxJitter > 0 {
			if err := jitter.Apply(ctx, maxJitter, fmt.Sprintf("step %s/%s", testName, step.Name)); err != nil {
				return fmt.Errorf("step jitter interrupted: %w", err)
			}
		}
	}

	stepStart := time.Now()

	// Get file size label if configured
	fileSizeLabel := ""
	if step.FileSize != nil {
		fileSizeLabel = step.FileSize.String()
	}

	// Set timeout
	timeout := step.TimeoutDuration()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Determine operation from step name
	var err error
	switch step.Name {
	case "upload":
		err = e.uploadObject(ctx, testName, bucket, filename, step)
	case "download":
		err = e.downloadObject(ctx, testName, bucket, filename)
	case "delete":
		err = e.deleteObject(ctx, testName, bucket, filename, fileSizeLabel)
	default:
		err = fmt.Errorf("unknown HTTP S3 operation: %s", step.Name)
	}

	duration := time.Since(stepStart)

	if err != nil {
		log.Printf("    HTTP S3 step %s failed: %v", step.Name, err)
		e.metrics.RecordTestRun(testName, step.Name, executorNameHttpS3, false, duration)
		return fmt.Errorf("step execution failed: %w", err)
	}

	e.metrics.RecordTestRun(testName, step.Name, executorNameHttpS3, true, duration)
	return nil
}

// buildURL constructs the S3 object URL using path-style addressing.
func (e *HttpS3Executor) buildURL(bucket, key string) string {
	return fmt.Sprintf("%s/%s/%s", e.endpoint, bucket, key)
}

// uploadObject uploads a file to S3 using HTTP PUT.
func (e *HttpS3Executor) uploadObject(ctx context.Context, testName, bucket, filename string, step *config.TestStep) error {
	var fileSize int64 = 1024 * 1024 // Default 1MB
	fileSizeLabel := "1MB"
	if step.FileSize != nil {
		fileSize = step.FileSize.Int64()
		fileSizeLabel = step.FileSize.String()
	}

	// Generate random data
	data := make([]byte, fileSize)
	if _, err := rand.Read(data); err != nil {
		return fmt.Errorf("failed to generate random data: %w", err)
	}

	// Build request
	url := e.buildURL(bucket, filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.ContentLength = fileSize
	req.Header.Set("Content-Type", "application/octet-stream")

	// Add TTL metadata if specified
	if step.TTLSeconds != nil && *step.TTLSeconds > 0 {
		req.Header.Set("X-Amz-Meta-Ttl-Seconds", fmt.Sprintf("%d", *step.TTLSeconds))
	}

	// Sign the request (uses cached signing key) - measure signing time
	signStart := time.Now()
	if err := e.signer.Sign(req); err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}
	signDuration := time.Since(signStart)

	// Add timing tracer
	tracer := newHTTPTimingTracer()
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), tracer.trace()))

	// Execute request
	resp, err := e.client.Do(req)
	if err != nil {
		e.metrics.RecordStorjUpload(testName, executorNameHttpS3, bucket, fileSizeLabel, time.Since(tracer.start), fileSize, false)
		return fmt.Errorf("HTTP PUT failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body to complete timing
	io.Copy(io.Discard, resp.Body)
	transferDone := time.Now()

	// Record granular timing metrics
	timings := tracer.toMetrics(transferDone)
	e.metrics.RecordHTTPTiming(testName, "upload", executorNameHttpS3, timings)
	e.metrics.RecordHTTPTimingPhase(testName, "upload", executorNameHttpS3, "sign", signDuration)

	// Check response
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		e.metrics.RecordStorjUpload(testName, executorNameHttpS3, bucket, fileSizeLabel, timings.Total, fileSize, false)
		return fmt.Errorf("HTTP PUT returned status %d", resp.StatusCode)
	}

	// Log with TTL info if specified
	if step.TTLSeconds != nil && *step.TTLSeconds > 0 {
		logging.Debug("    HTTP S3 uploaded %s (%d bytes) with TTL %ds in %v (sign=%v, dns=%v, tls=%v, ttfb=%v)",
			filename, fileSize, *step.TTLSeconds, timings.Total, signDuration, timings.DNSLookup, timings.TLSHandshake, timings.TTFB)
	} else {
		logging.Debug("    HTTP S3 uploaded %s (%d bytes) in %v (sign=%v, dns=%v, tls=%v, ttfb=%v)",
			filename, fileSize, timings.Total, signDuration, timings.DNSLookup, timings.TLSHandshake, timings.TTFB)
	}
	e.metrics.RecordStorjUpload(testName, executorNameHttpS3, bucket, fileSizeLabel, timings.Total, fileSize, true)

	return nil
}

// downloadObject downloads a file from S3 using HTTP GET.
func (e *HttpS3Executor) downloadObject(ctx context.Context, testName, bucket, filename string) error {
	// Build request
	url := e.buildURL(bucket, filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Sign the request (uses cached signing key) - measure signing time
	signStart := time.Now()
	if err := e.signer.Sign(req); err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}
	signDuration := time.Since(signStart)

	// Add timing tracer
	tracer := newHTTPTimingTracer()
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), tracer.trace()))

	// Execute request
	resp, err := e.client.Do(req)
	if err != nil {
		e.metrics.RecordStorjDownload(testName, executorNameHttpS3, bucket, "", time.Since(tracer.start), 0, false)
		return fmt.Errorf("HTTP GET failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		e.metrics.RecordStorjDownload(testName, executorNameHttpS3, bucket, "", time.Since(tracer.start), 0, false)
		return fmt.Errorf("HTTP GET returned status %d: %s", resp.StatusCode, string(body))
	}

	// Read the data to measure actual download time
	bytesRead, err := io.Copy(io.Discard, resp.Body)
	transferDone := time.Now()

	// Record granular timing metrics
	timings := tracer.toMetrics(transferDone)
	e.metrics.RecordHTTPTiming(testName, "download", executorNameHttpS3, timings)
	e.metrics.RecordHTTPTimingPhase(testName, "download", executorNameHttpS3, "sign", signDuration)

	if err != nil {
		e.metrics.RecordStorjDownload(testName, executorNameHttpS3, bucket, "", timings.Total, bytesRead, false)
		return fmt.Errorf("failed to read HTTP response: %w", err)
	}

	logging.Debug("    HTTP S3 downloaded %s (%d bytes) in %v (sign=%v, dns=%v, tls=%v, ttfb=%v, transfer=%v)",
		filename, bytesRead, timings.Total, signDuration, timings.DNSLookup, timings.TLSHandshake, timings.TTFB, timings.Transfer)
	e.metrics.RecordStorjDownload(testName, executorNameHttpS3, bucket, "", timings.Total, bytesRead, true)

	return nil
}

// deleteObject deletes a file from S3 using HTTP DELETE.
func (e *HttpS3Executor) deleteObject(ctx context.Context, testName, bucket, filename, fileSizeLabel string) error {
	// Build request
	url := e.buildURL(bucket, filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Sign the request (uses cached signing key) - measure signing time
	signStart := time.Now()
	if err := e.signer.Sign(req); err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}
	signDuration := time.Since(signStart)

	// Add timing tracer
	tracer := newHTTPTimingTracer()
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), tracer.trace()))

	// Execute request
	resp, err := e.client.Do(req)
	if err != nil {
		e.metrics.RecordStorjDelete(testName, executorNameHttpS3, bucket, fileSizeLabel, 0, 0, false)
		return fmt.Errorf("HTTP DELETE failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body to complete timing
	io.Copy(io.Discard, resp.Body)
	transferDone := time.Now()

	// Record granular timing metrics
	timings := tracer.toMetrics(transferDone)
	e.metrics.RecordHTTPTiming(testName, "delete", executorNameHttpS3, timings)
	e.metrics.RecordHTTPTimingPhase(testName, "delete", executorNameHttpS3, "sign", signDuration)

	// Check response (204 No Content is the expected success response for DELETE)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		e.metrics.RecordStorjDelete(testName, executorNameHttpS3, bucket, fileSizeLabel, 0, 0, false)
		return fmt.Errorf("HTTP DELETE returned status %d", resp.StatusCode)
	}

	logging.Debug("    HTTP S3 deleted %s in %v (sign=%v, dns=%v, tls=%v, ttfb=%v)",
		filename, timings.Total, signDuration, timings.DNSLookup, timings.TLSHandshake, timings.TTFB)
	e.metrics.RecordStorjDelete(testName, executorNameHttpS3, bucket, fileSizeLabel, timings.Total, 1, true)

	return nil
}
