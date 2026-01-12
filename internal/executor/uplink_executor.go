package executor

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ethanadams/synthetics/internal/config"
	"github.com/ethanadams/synthetics/internal/jitter"
	"github.com/ethanadams/synthetics/internal/k6output"
	"github.com/ethanadams/synthetics/internal/logging"
	"github.com/ethanadams/synthetics/internal/metrics"
	"github.com/oklog/ulid/v2"
)

// UplinkExecutor runs Uplink tests via k6 with xk6-storj extension
type UplinkExecutor struct {
	k6Binary string
	config   *config.Config
	metrics  *metrics.Collector
}

// NewUplink creates a new Uplink executor
func NewUplink(cfg *config.Config, mc *metrics.Collector) *UplinkExecutor {
	return &UplinkExecutor{
		k6Binary: cfg.K6.BinaryPath,
		config:   cfg,
		metrics:  mc,
	}
}

// RunTest executes a synthetic test (handles single or multi-step)
func (e *UplinkExecutor) RunTest(ctx context.Context, test *config.Test) error {
	log.Printf("Running test: %s", test.Name)

	testStart := time.Now()

	// Generate ULID for this test run (for filename uniqueness)
	entropy := ulid.Monotonic(rand.Reader, 0)
	testULID := ulid.MustNew(ulid.Timestamp(testStart), entropy)
	sharedFilename := test.GetFilename(testULID.String())
	bucket := test.GetBucket(e.config.Satellite.Bucket)

	isSingleStep := test.IsSingleStep()

	if isSingleStep {
		log.Printf("Test %s using ULID: %s (filename: %s)", test.Name, testULID.String(), sharedFilename)
	} else {
		log.Printf("Test %s (%d steps) using ULID: %s (filename: %s)",
			test.Name, len(test.Steps), testULID.String(), sharedFilename)
	}

	// Run each step sequentially
	for i, step := range test.Steps {
		if !isSingleStep {
			log.Printf("  [%d/%d] Running: %s", i+1, len(test.Steps), step.Name)
		}

		if err := e.runStep(ctx, test.Name, &step, sharedFilename, testULID.String(), bucket, isSingleStep); err != nil {
			if !isSingleStep {
				log.Printf("  [%d/%d] Failed: %s - %v", i+1, len(test.Steps), step.Name, err)
			}
			e.metrics.RecordTestRun(test.Name, step.Name, "uplink", false, time.Since(testStart))
			return fmt.Errorf("test %s failed at step %s: %w", test.Name, step.Name, err)
		}

		if !isSingleStep {
			log.Printf("  [%d/%d] Completed: %s", i+1, len(test.Steps), step.Name)
		}
	}

	duration := time.Since(testStart)
	log.Printf("Test %s completed successfully in %v", test.Name, duration)
	// For overall test run, use empty action (represents entire test)
	e.metrics.RecordTestRun(test.Name, "", "uplink", true, duration)

	return nil
}

// runStep executes a single test step
func (e *UplinkExecutor) runStep(ctx context.Context, testName string, step *config.TestStep, sharedFilename, testULID, bucket string, isSingleStep bool) error {
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

	// Create temporary file for k6 output
	outputFile := filepath.Join(os.TempDir(), fmt.Sprintf("k6-output-%s-%s-%d.json", testName, step.Name, time.Now().Unix()))
	defer os.Remove(outputFile)

	// Set timeout
	timeout := step.TimeoutDuration()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build k6 command
	args := []string{
		"run",
		"--out", fmt.Sprintf("json=%s", outputFile),
		"--summary-mode=disabled", // Disable end-of-test summary
		"--no-usage-report",       // No usage reporting
		"--quiet",                 // Suppress verbose output
	}

	cmd := exec.CommandContext(ctx, e.k6Binary, append(args, step.Script)...)

	// Start with base environment - ALWAYS include test metadata
	env := append(os.Environ(),
		fmt.Sprintf("STORJ_ACCESS_GRANT=%s", e.config.Satellite.AccessGrant),
		fmt.Sprintf("STORJ_BUCKET=%s", bucket),
		fmt.Sprintf("TEST_NAME=%s", testName),
		fmt.Sprintf("SHARED_FILE=%s", sharedFilename),
		fmt.Sprintf("TEST_ULID=%s", testULID),
	)

	// Add step-specific configuration as environment variables
	if step.FileSize != nil {
		env = append(env, fmt.Sprintf("FILE_SIZE=%d", step.FileSize.Int64()))
	}
	if step.TTLSeconds != nil {
		env = append(env, fmt.Sprintf("TTL_SECONDS=%d", *step.TTLSeconds))
	}
	if step.FilePrefix != nil {
		env = append(env, fmt.Sprintf("FILE_PREFIX=%s", *step.FilePrefix))
	}
	if step.MaxAgeMinutes != nil {
		env = append(env, fmt.Sprintf("MAX_AGE_MINUTES=%d", *step.MaxAgeMinutes))
	}
	if step.MaxDelete != nil {
		env = append(env, fmt.Sprintf("MAX_DELETE=%d", *step.MaxDelete))
	}

	cmd.Env = env

	// Run the test
	output, err := cmd.CombinedOutput()
	duration := time.Since(stepStart)

	if err != nil {
		log.Printf("    Step %s failed: %v", step.Name, err)
		if len(output) > 0 {
			log.Printf("    Output: %s", string(output))
		}

		// Record metrics
		e.metrics.RecordTestRun(testName, step.Name, "uplink", false, duration)
		return fmt.Errorf("step execution failed: %w", err)
	}

	// Log k6 console output if present
	if len(output) > 0 {
		log.Printf("    k6 output: %s", string(output))
	}

	// Parse k6 output and update metrics
	if err := e.parseAndRecordMetrics(outputFile, testName, bucket, fileSizeLabel); err != nil {
		log.Printf("    Warning: failed to parse k6 output: %v", err)
	}

	e.metrics.RecordTestRun(testName, step.Name, "uplink", true, duration)

	return nil
}

// parseAndRecordMetrics parses k6 JSON output and records metrics
func (e *UplinkExecutor) parseAndRecordMetrics(outputFile, testName, bucket, fileSizeLabel string) error {
	points, err := k6output.ParseJSONOutput(outputFile)
	if err != nil {
		return err
	}

	// Group metrics by name
	grouped := k6output.GroupMetricsByName(points)

	// Log what metrics were found
	logging.Debug("    Parsed %d metric points, found metric types: %v", len(points), func() []string {
		keys := make([]string, 0, len(grouped))
		for k := range grouped {
			keys = append(keys, k)
		}
		return keys
	}())

	// Collect upload metrics (duration and bytes) to combine in single call
	var uploadDuration time.Duration
	var uploadBytes int64
	var uploadSuccess = true

	if uploadPoints, ok := grouped["storj_upload_duration_ms"]; ok && len(uploadPoints) > 0 {
		uploadDuration = time.Duration(uploadPoints[0].Value) * time.Millisecond
		logging.Debug("    Uplink upload duration from k6: %v (raw value: %v)", uploadDuration, uploadPoints[0].Value)
	}
	if uploadBytesPoints, ok := grouped["storj_upload_bytes_total"]; ok && len(uploadBytesPoints) > 0 {
		uploadBytes = int64(uploadBytesPoints[0].Value)
	}
	if uploadSuccessPoints, ok := grouped["storj_upload_success"]; ok && len(uploadSuccessPoints) > 0 {
		uploadSuccess = uploadSuccessPoints[0].Value > 0
	}

	// Record upload metrics in single call (so histogram gets both duration and bytes-derived fileSize)
	if uploadDuration > 0 || uploadBytes > 0 {
		e.metrics.RecordStorjUpload(testName, "uplink", bucket, fileSizeLabel, uploadDuration, uploadBytes, uploadSuccess)
	}

	// Collect download metrics (duration and bytes) to combine in single call
	var downloadDuration time.Duration
	var downloadBytes int64
	var downloadSuccess = true

	if downloadPoints, ok := grouped["storj_download_duration_ms"]; ok && len(downloadPoints) > 0 {
		downloadDuration = time.Duration(downloadPoints[0].Value) * time.Millisecond
		logging.Debug("    Uplink download duration from k6: %v (raw value: %v)", downloadDuration, downloadPoints[0].Value)
	}
	if downloadBytesPoints, ok := grouped["storj_download_bytes_total"]; ok && len(downloadBytesPoints) > 0 {
		downloadBytes = int64(downloadBytesPoints[0].Value)
	}
	if downloadSuccessPoints, ok := grouped["storj_download_success"]; ok && len(downloadSuccessPoints) > 0 {
		downloadSuccess = downloadSuccessPoints[0].Value > 0
	}

	// Record download metrics in single call (so histogram gets both duration and bytes-derived fileSize)
	if downloadDuration > 0 || downloadBytes > 0 {
		e.metrics.RecordStorjDownload(testName, "uplink", bucket, fileSizeLabel, downloadDuration, downloadBytes, downloadSuccess)
	}

	// Process delete duration metrics
	if deletePoints, ok := grouped["storj_delete_duration_ms"]; ok {
		for _, point := range deletePoints {
			duration := time.Duration(point.Value) * time.Millisecond
			logging.Debug("    Uplink delete duration from k6: %v (raw value: %v)", duration, point.Value)
			e.metrics.RecordStorjDelete(testName, "uplink", bucket, fileSizeLabel, duration, 1, true)
		}
	}

	// Process delete success metrics
	if deleteSuccessPoints, ok := grouped["storj_delete_success"]; ok {
		for _, point := range deleteSuccessPoints {
			success := point.Value > 0
			count := 1 // Each point represents one delete attempt
			if !success {
				// Record failure (no duration, count=0)
				e.metrics.RecordStorjDelete(testName, "uplink", bucket, fileSizeLabel, 0, count, success)
			}
		}
	}

	// Process delete count metrics
	if deleteCountPoints, ok := grouped["storj_delete_count_total"]; ok {
		totalDeletes := 0
		for _, point := range deleteCountPoints {
			totalDeletes += int(point.Value)
		}
		if totalDeletes > 0 {
			// For count-only metrics, pass empty fileSize and 0 duration
			e.metrics.RecordStorjDelete(testName, "uplink", bucket, "", 0, totalDeletes, true)
		}
	}

	log.Printf("Parsed %d metric points from test %s", len(points), testName)

	return nil
}
