package executor

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ethanadams/synthetics/internal/config"
	"github.com/ethanadams/synthetics/internal/executor/awsv4"
	"github.com/ethanadams/synthetics/internal/jitter"
	"github.com/ethanadams/synthetics/internal/logging"
	"github.com/ethanadams/synthetics/internal/metrics"
	"github.com/oklog/ulid/v2"
)

// curlWriteFormat is the format string for curl -w to get timing info
// Format: http_code|time_namelookup|time_connect|time_appconnect|time_starttransfer|time_total
const curlWriteFormat = "%{http_code}|%{time_namelookup}|%{time_connect}|%{time_appconnect}|%{time_starttransfer}|%{time_total}"

// parseCurlOutput parses curl -w output and returns status code and timings
func parseCurlOutput(output string) (statusCode string, timings metrics.HTTPTimings, err error) {
	parts := strings.Split(strings.TrimSpace(output), "|")
	if len(parts) != 6 {
		return "", metrics.HTTPTimings{}, fmt.Errorf("unexpected curl output format: %s", output)
	}

	statusCode = parts[0]

	parseSeconds := func(s string) time.Duration {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return time.Duration(f * float64(time.Second))
	}

	dnsLookup := parseSeconds(parts[1])
	tcpConnect := parseSeconds(parts[2])
	tlsHandshake := parseSeconds(parts[3])
	ttfb := parseSeconds(parts[4])
	total := parseSeconds(parts[5])

	// Curl times are cumulative, convert to individual phases
	timings = metrics.HTTPTimings{
		DNSLookup:    dnsLookup,
		TCPConnect:   tcpConnect - dnsLookup,
		TLSHandshake: tlsHandshake - tcpConnect,
		TTFB:         ttfb - tlsHandshake,
		Transfer:     total - ttfb,
		Total:        total,
	}

	return statusCode, timings, nil
}

const executorNameCurlS3 = "curl-s3"

// CurlS3Executor runs S3 tests using curl subprocess.
type CurlS3Executor struct {
	curlPath string
	endpoint string
	signer   *awsv4.Signer // Cached signer for efficiency
	config   *config.Config
	metrics  *metrics.Collector
}

// NewCurlS3 creates a new curl-based S3 executor.
func NewCurlS3(cfg *config.Config, mc *metrics.Collector) (*CurlS3Executor, error) {
	if cfg.S3.Endpoint == "" {
		return nil, fmt.Errorf("S3 endpoint is required")
	}
	if cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		return nil, fmt.Errorf("S3 access key and secret key are required")
	}

	// Find curl binary
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		return nil, fmt.Errorf("curl not found in PATH: %w", err)
	}

	region := cfg.S3.Region
	if region == "" {
		region = "us-east-1"
	}

	creds := awsv4.Credentials{
		AccessKey: cfg.S3.AccessKey,
		SecretKey: cfg.S3.SecretKey,
		Region:    region,
	}

	return &CurlS3Executor{
		curlPath: curlPath,
		endpoint: cfg.S3.Endpoint,
		signer:   awsv4.NewSigner(creds), // Cached signer
		config:   cfg,
		metrics:  mc,
	}, nil
}

// ensureBucket creates the bucket if it doesn't exist
func (e *CurlS3Executor) ensureBucket(ctx context.Context, bucket string) error {
	bucketURL := fmt.Sprintf("%s/%s", e.endpoint, bucket)

	// Check if bucket exists by trying to HEAD it
	headHeaders, _, err := e.signAndGetHeaders(http.MethodHead, bucketURL, 0)
	if err != nil {
		return fmt.Errorf("failed to sign HEAD request: %w", err)
	}

	headArgs := []string{"-s", "-S", "-I", "-o", "/dev/null", "-w", "%{http_code}"}
	for _, h := range headHeaders {
		headArgs = append(headArgs, "-H", h)
	}
	headArgs = append(headArgs, bucketURL)

	headCmd := exec.CommandContext(ctx, e.curlPath, headArgs...)
	headOutput, err := headCmd.Output()
	if err == nil && strings.TrimSpace(string(headOutput)) == "200" {
		// Bucket exists
		return nil
	}

	// Try to create the bucket with PUT
	putHeaders, _, err := e.signAndGetHeaders(http.MethodPut, bucketURL, 0)
	if err != nil {
		return fmt.Errorf("failed to sign PUT request: %w", err)
	}

	putArgs := []string{"-s", "-S", "-X", "PUT", "-o", "/dev/null", "-w", "%{http_code}"}
	for _, h := range putHeaders {
		putArgs = append(putArgs, "-H", h)
	}
	putArgs = append(putArgs, bucketURL)

	putCmd := exec.CommandContext(ctx, e.curlPath, putArgs...)
	putOutput, err := putCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}

	putStatus := strings.TrimSpace(string(putOutput))
	if putStatus == "200" || putStatus == "201" {
		log.Printf("    Created bucket: %s", bucket)
	} else if putStatus != "409" {
		// 409 Conflict usually means bucket already exists
		log.Printf("    Note: CreateBucket returned status %s (may be ignorable if bucket exists)", putStatus)
	}

	// Verify bucket is now accessible
	verifyHeaders, _, err := e.signAndGetHeaders(http.MethodHead, bucketURL, 0)
	if err != nil {
		return fmt.Errorf("failed to sign verify request: %w", err)
	}

	verifyArgs := []string{"-s", "-S", "-I", "-o", "/dev/null", "-w", "%{http_code}"}
	for _, h := range verifyHeaders {
		verifyArgs = append(verifyArgs, "-H", h)
	}
	verifyArgs = append(verifyArgs, bucketURL)

	verifyCmd := exec.CommandContext(ctx, e.curlPath, verifyArgs...)
	verifyOutput, err := verifyCmd.Output()
	if err != nil {
		return fmt.Errorf("bucket %s not accessible after creation attempt: %w", bucket, err)
	}

	verifyStatus := strings.TrimSpace(string(verifyOutput))
	if verifyStatus != "200" {
		return fmt.Errorf("bucket %s not accessible after creation attempt: status %s", bucket, verifyStatus)
	}

	return nil
}

// RunTest executes a curl S3 test (handles single or multi-step).
func (e *CurlS3Executor) RunTest(ctx context.Context, test *config.Test) error {
	log.Printf("Running Curl S3 test: %s", test.Name)

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
		log.Printf("Curl S3 test %s using ULID: %s (filename: %s, bucket: %s)",
			test.Name, testULID.String(), sharedFilename, bucket)
	} else {
		log.Printf("Curl S3 test %s (%d steps) using ULID: %s (filename: %s, bucket: %s)",
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
			e.metrics.RecordTestRun(test.Name, step.Name, executorNameCurlS3, false, time.Since(testStart))
			return fmt.Errorf("Curl S3 test %s failed at step %s: %w", test.Name, step.Name, err)
		}

		if !isSingleStep {
			log.Printf("  [%d/%d] Completed: %s", i+1, len(test.Steps), step.Name)
		}
	}

	duration := time.Since(testStart)
	log.Printf("Curl S3 test %s completed successfully in %v", test.Name, duration)
	e.metrics.RecordTestRun(test.Name, "", executorNameCurlS3, true, duration)

	return nil
}

// runStep executes a single curl S3 test step.
func (e *CurlS3Executor) runStep(ctx context.Context, testName string, step *config.TestStep, filename, bucket string, isSingleStep bool) error {
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
		err = fmt.Errorf("unknown Curl S3 operation: %s", step.Name)
	}

	duration := time.Since(stepStart)

	if err != nil {
		log.Printf("    Curl S3 step %s failed: %v", step.Name, err)
		e.metrics.RecordTestRun(testName, step.Name, executorNameCurlS3, false, duration)
		return fmt.Errorf("step execution failed: %w", err)
	}

	e.metrics.RecordTestRun(testName, step.Name, executorNameCurlS3, true, duration)
	return nil
}

// buildURL constructs the S3 object URL using path-style addressing.
func (e *CurlS3Executor) buildURL(bucket, key string) string {
	return fmt.Sprintf("%s/%s/%s", e.endpoint, bucket, key)
}

// signAndGetHeaders creates a signed request and extracts headers for curl.
// Uses cached signer for efficiency. Returns headers and sign duration.
func (e *CurlS3Executor) signAndGetHeaders(method, url string, contentLength int64) ([]string, time.Duration, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	if contentLength > 0 {
		req.ContentLength = contentLength
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	// Sign with cached signer - measure signing time
	signStart := time.Now()
	if err := e.signer.Sign(req); err != nil {
		return nil, 0, fmt.Errorf("failed to sign request: %w", err)
	}
	signDuration := time.Since(signStart)

	// Extract headers for curl
	var headers []string
	for name, values := range req.Header {
		for _, value := range values {
			headers = append(headers, fmt.Sprintf("%s: %s", name, value))
		}
	}

	return headers, signDuration, nil
}

// uploadObject uploads a file to S3 using curl.
func (e *CurlS3Executor) uploadObject(ctx context.Context, testName, bucket, filename string, step *config.TestStep) error {
	var fileSize int64 = 1024 * 1024 // Default 1MB
	fileSizeLabel := "1MB"
	if step.FileSize != nil {
		fileSize = step.FileSize.Int64()
		fileSizeLabel = step.FileSize.String()
	}

	// Generate random data and write to temp file
	data := make([]byte, fileSize)
	if _, err := rand.Read(data); err != nil {
		return fmt.Errorf("failed to generate random data: %w", err)
	}

	// Write to temp file for curl to upload
	tmpFile, err := os.CreateTemp("", "curl-upload-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	url := e.buildURL(bucket, filename)

	// Get signed headers (uses UNSIGNED-PAYLOAD for efficiency)
	headers, signDuration, err := e.signAndGetHeaders(http.MethodPut, url, fileSize)
	if err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	// Add TTL metadata if specified
	if step.TTLSeconds != nil && *step.TTLSeconds > 0 {
		headers = append(headers, fmt.Sprintf("X-Amz-Meta-Ttl-Seconds: %d", *step.TTLSeconds))
	}

	// Build curl command with timing output
	args := []string{
		"-s", "-S", // Silent but show errors
		"-X", "PUT",
		"--data-binary", "@" + tmpPath,
		"-w", curlWriteFormat,
		"-o", "/dev/null", // Discard response body
	}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	args = append(args, url)

	cmd := exec.CommandContext(ctx, e.curlPath, args...)
	output, err := cmd.Output()

	if err != nil {
		e.metrics.RecordStorjUpload(testName, executorNameCurlS3, bucket, fileSizeLabel, 0, fileSize, false)
		return fmt.Errorf("curl PUT failed: %w", err)
	}

	// Parse output for status code and timings
	statusCode, timings, err := parseCurlOutput(string(output))
	if err != nil {
		e.metrics.RecordStorjUpload(testName, executorNameCurlS3, bucket, fileSizeLabel, 0, fileSize, false)
		return fmt.Errorf("failed to parse curl output: %w", err)
	}

	// Record granular timing metrics
	e.metrics.RecordHTTPTiming(testName, "upload", executorNameCurlS3, timings)
	e.metrics.RecordHTTPTimingPhase(testName, "upload", executorNameCurlS3, "sign", signDuration)

	if statusCode != "200" && statusCode != "201" {
		e.metrics.RecordStorjUpload(testName, executorNameCurlS3, bucket, fileSizeLabel, timings.Total, fileSize, false)
		return fmt.Errorf("curl PUT returned status %s", statusCode)
	}

	if step.TTLSeconds != nil && *step.TTLSeconds > 0 {
		logging.Debug("    Curl S3 uploaded %s (%d bytes) with TTL %ds in %v (sign=%v, dns=%v, tls=%v, ttfb=%v)",
			filename, fileSize, *step.TTLSeconds, timings.Total, signDuration, timings.DNSLookup, timings.TLSHandshake, timings.TTFB)
	} else {
		logging.Debug("    Curl S3 uploaded %s (%d bytes) in %v (sign=%v, dns=%v, tls=%v, ttfb=%v)",
			filename, fileSize, timings.Total, signDuration, timings.DNSLookup, timings.TLSHandshake, timings.TTFB)
	}
	e.metrics.RecordStorjUpload(testName, executorNameCurlS3, bucket, fileSizeLabel, timings.Total, fileSize, true)

	return nil
}

// downloadObject downloads a file from S3 using curl.
func (e *CurlS3Executor) downloadObject(ctx context.Context, testName, bucket, filename string) error {
	url := e.buildURL(bucket, filename)

	// Get signed headers
	headers, signDuration, err := e.signAndGetHeaders(http.MethodGet, url, 0)
	if err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	// Create temp file for download
	tmpFile, err := os.CreateTemp("", "curl-download-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Build curl command with timing output
	args := []string{
		"-s", "-S",
		"-X", "GET",
		"-o", tmpPath,
		"-w", curlWriteFormat,
	}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	args = append(args, url)

	cmd := exec.CommandContext(ctx, e.curlPath, args...)
	output, err := cmd.Output()

	if err != nil {
		e.metrics.RecordStorjDownload(testName, executorNameCurlS3, bucket, "", 0, 0, false)
		return fmt.Errorf("curl GET failed: %w", err)
	}

	// Parse output for status code and timings
	statusCode, timings, err := parseCurlOutput(string(output))
	if err != nil {
		e.metrics.RecordStorjDownload(testName, executorNameCurlS3, bucket, "", 0, 0, false)
		return fmt.Errorf("failed to parse curl output: %w", err)
	}

	// Record granular timing metrics
	e.metrics.RecordHTTPTiming(testName, "download", executorNameCurlS3, timings)
	e.metrics.RecordHTTPTimingPhase(testName, "download", executorNameCurlS3, "sign", signDuration)

	if statusCode != "200" {
		e.metrics.RecordStorjDownload(testName, executorNameCurlS3, bucket, "", timings.Total, 0, false)
		return fmt.Errorf("curl GET returned status %s", statusCode)
	}

	// Get downloaded file size
	fileInfo, err := os.Stat(tmpPath)
	if err != nil {
		e.metrics.RecordStorjDownload(testName, executorNameCurlS3, bucket, "", timings.Total, 0, false)
		return fmt.Errorf("failed to stat downloaded file: %w", err)
	}
	bytesRead := fileInfo.Size()

	logging.Debug("    Curl S3 downloaded %s (%d bytes) in %v (sign=%v, dns=%v, tls=%v, ttfb=%v, transfer=%v)",
		filename, bytesRead, timings.Total, signDuration, timings.DNSLookup, timings.TLSHandshake, timings.TTFB, timings.Transfer)
	e.metrics.RecordStorjDownload(testName, executorNameCurlS3, bucket, "", timings.Total, bytesRead, true)

	return nil
}

// deleteObject deletes a file from S3 using curl.
func (e *CurlS3Executor) deleteObject(ctx context.Context, testName, bucket, filename, fileSizeLabel string) error {
	url := e.buildURL(bucket, filename)

	// Get signed headers
	headers, signDuration, err := e.signAndGetHeaders(http.MethodDelete, url, 0)
	if err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	// Build curl command with timing output
	args := []string{
		"-s", "-S",
		"-X", "DELETE",
		"-w", curlWriteFormat,
		"-o", "/dev/null",
	}
	for _, h := range headers {
		args = append(args, "-H", h)
	}
	args = append(args, url)

	cmd := exec.CommandContext(ctx, e.curlPath, args...)
	output, err := cmd.Output()

	if err != nil {
		e.metrics.RecordStorjDelete(testName, executorNameCurlS3, bucket, fileSizeLabel, 0, 0, false)
		return fmt.Errorf("curl DELETE failed: %w", err)
	}

	// Parse output for status code and timings
	statusCode, timings, err := parseCurlOutput(string(output))
	if err != nil {
		e.metrics.RecordStorjDelete(testName, executorNameCurlS3, bucket, fileSizeLabel, 0, 0, false)
		return fmt.Errorf("failed to parse curl output: %w", err)
	}

	// Record granular timing metrics
	e.metrics.RecordHTTPTiming(testName, "delete", executorNameCurlS3, timings)
	e.metrics.RecordHTTPTimingPhase(testName, "delete", executorNameCurlS3, "sign", signDuration)

	// Check HTTP status code (204 No Content is expected for DELETE)
	if statusCode != "200" && statusCode != "204" {
		e.metrics.RecordStorjDelete(testName, executorNameCurlS3, bucket, fileSizeLabel, 0, 0, false)
		return fmt.Errorf("curl DELETE returned status %s", statusCode)
	}

	logging.Debug("    Curl S3 deleted %s in %v (sign=%v, dns=%v, tls=%v, ttfb=%v)",
		filename, timings.Total, signDuration, timings.DNSLookup, timings.TLSHandshake, timings.TTFB)
	e.metrics.RecordStorjDelete(testName, executorNameCurlS3, bucket, fileSizeLabel, timings.Total, 1, true)

	return nil
}

// Ensure CurlS3Executor implements TestExecutor
var _ TestExecutor = (*CurlS3Executor)(nil)
