package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ethanadams/synthetics/internal/config"
	"github.com/ethanadams/synthetics/internal/jitter"
	"github.com/ethanadams/synthetics/internal/metrics"
	"github.com/oklog/ulid/v2"
)

// S3Executor runs S3 gateway tests using AWS SDK
type S3Executor struct {
	s3Client *s3.Client
	config   *config.Config
	metrics  *metrics.Collector
}

// NewS3 creates a new S3 executor
func NewS3(cfg *config.Config, mc *metrics.Collector) (*S3Executor, error) {
	// Create AWS config with custom endpoint
	awsCfg, err := awsConfig(cfg.S3.Endpoint, cfg.S3.AccessKey, cfg.S3.SecretKey, cfg.S3.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS config: %w", err)
	}

	// Create S3 client
	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true // Required for custom endpoints
	})

	return &S3Executor{
		s3Client: s3Client,
		config:   cfg,
		metrics:  mc,
	}, nil
}

// awsConfig creates AWS config with custom credentials and endpoint
func awsConfig(endpoint, accessKey, secretKey, region string) (aws.Config, error) {
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, regionID string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL:               endpoint,
			HostnameImmutable: true,
			Source:            aws.EndpointSourceCustom,
		}, nil
	})

	return awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		awsconfig.WithEndpointResolverWithOptions(customResolver),
		// Disable automatic checksum calculation for Storj compatibility
		// AWS SDK v2 1.73.0+ calculates CRC32 checksums by default which breaks compatibility with Storj
		// See: https://github.com/aws/aws-sdk-go-v2/discussions/2960
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)
}

// ensureBucket creates the bucket if it doesn't exist
func (e *S3Executor) ensureBucket(ctx context.Context, bucket string) error {
	// Check if bucket exists by trying to head it
	_, err := e.s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err == nil {
		// Bucket exists
		return nil
	}

	// Try to create the bucket
	_, err = e.s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		// Ignore "bucket already exists" errors (race condition or different error format)
		// Some S3-compatible services return different error codes
		log.Printf("    Note: CreateBucket returned: %v (may be ignorable if bucket exists)", err)
	} else {
		log.Printf("    Created bucket: %s", bucket)
	}

	// Verify bucket is now accessible
	_, err = e.s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return fmt.Errorf("bucket %s not accessible after creation attempt: %w", bucket, err)
	}

	return nil
}

// RunTest executes an S3 test (handles single or multi-step)
func (e *S3Executor) RunTest(ctx context.Context, test *config.Test) error {
	log.Printf("Running S3 test: %s", test.Name)

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
		log.Printf("S3 test %s using ULID: %s (filename: %s, bucket: %s)",
			test.Name, testULID.String(), sharedFilename, bucket)
	} else {
		log.Printf("S3 test %s (%d steps) using ULID: %s (filename: %s, bucket: %s)",
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
			e.metrics.RecordTestRun(test.Name, step.Name, "s3", false, time.Since(testStart))
			return fmt.Errorf("S3 test %s failed at step %s: %w", test.Name, step.Name, err)
		}

		if !isSingleStep {
			log.Printf("  [%d/%d] Completed: %s", i+1, len(test.Steps), step.Name)
		}
	}

	duration := time.Since(testStart)
	log.Printf("S3 test %s completed successfully in %v", test.Name, duration)
	// For overall test run, use empty action (represents entire test)
	e.metrics.RecordTestRun(test.Name, "", "s3", true, duration)

	return nil
}

// runStep executes a single S3 test step
func (e *S3Executor) runStep(ctx context.Context, testName string, step *config.TestStep, filename, bucket string, isSingleStep bool) error {
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

	// Determine operation from script name
	var err error
	switch step.Name {
	case "upload":
		err = e.uploadObject(ctx, testName, bucket, filename, step)
	case "download":
		err = e.downloadObject(ctx, testName, bucket, filename)
	case "delete":
		err = e.deleteObject(ctx, testName, bucket, filename, fileSizeLabel)
	default:
		err = fmt.Errorf("unknown S3 operation: %s", step.Name)
	}

	duration := time.Since(stepStart)

	if err != nil {
		log.Printf("    S3 step %s failed: %v", step.Name, err)
		e.metrics.RecordTestRun(testName, step.Name, "s3", false, duration)
		return fmt.Errorf("step execution failed: %w", err)
	}

	e.metrics.RecordTestRun(testName, step.Name, "s3", true, duration)
	return nil
}

// uploadObject uploads a file to S3
func (e *S3Executor) uploadObject(ctx context.Context, testName, bucket, filename string, step *config.TestStep) error {
	var fileSize int64 = 1024 * 1024 // Default 1MB
	fileSizeLabel := "1MB"            // Default label
	if step.FileSize != nil {
		fileSize = step.FileSize.Int64()
		fileSizeLabel = step.FileSize.String()
	}

	// Generate random data
	data := make([]byte, fileSize)
	if _, err := rand.Read(data); err != nil {
		return fmt.Errorf("failed to generate random data: %w", err)
	}

	start := time.Now()

	// Prepare PutObject input
	putInput := &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(filename),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(fileSize),
	}

	// Add TTL via metadata if specified
	// Note: Storj S3 gateway doesn't support Expires header for object deletion
	// TTL must be set at upload time via uplink SDK, not S3 API
	if step.TTLSeconds != nil && *step.TTLSeconds > 0 {
		// Store TTL in metadata for reference (actual TTL only works with uplink executor)
		if putInput.Metadata == nil {
			putInput.Metadata = make(map[string]string)
		}
		putInput.Metadata["ttl-seconds"] = fmt.Sprintf("%d", *step.TTLSeconds)
	}

	// Upload to S3
	_, err := e.s3Client.PutObject(ctx, putInput)

	duration := time.Since(start)

	if err != nil {
		e.metrics.RecordStorjUpload(testName, "s3", bucket, fileSizeLabel, duration, fileSize, false)
		return fmt.Errorf("S3 PutObject failed: %w", err)
	}

	// Log with TTL info if specified
	if step.TTLSeconds != nil && *step.TTLSeconds > 0 {
		log.Printf("    S3 uploaded %s (%d bytes) with TTL %ds in %v", filename, fileSize, *step.TTLSeconds, duration)
	} else {
		log.Printf("    S3 uploaded %s (%d bytes) in %v", filename, fileSize, duration)
	}
	e.metrics.RecordStorjUpload(testName, "s3", bucket, fileSizeLabel, duration, fileSize, true)

	return nil
}

// downloadObject downloads a file from S3
func (e *S3Executor) downloadObject(ctx context.Context, testName, bucket, filename string) error {
	start := time.Now()

	// Download from S3
	result, err := e.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(filename),
	})

	if err != nil {
		e.metrics.RecordStorjDownload(testName, "s3", bucket, "", time.Since(start), 0, false)
		return fmt.Errorf("S3 GetObject failed: %w", err)
	}
	defer result.Body.Close()

	// Log content length from response headers for debugging
	var expectedSize int64
	if result.ContentLength != nil {
		expectedSize = *result.ContentLength
	}

	// Read the data to measure actual download time
	bytesRead, err := io.Copy(io.Discard, result.Body)
	duration := time.Since(start)

	if err != nil {
		e.metrics.RecordStorjDownload(testName, "s3", bucket, "", duration, bytesRead, false)
		return fmt.Errorf("failed to read S3 object: %w", err)
	}

	// Warn if bytes read doesn't match expected size
	if expectedSize > 0 && bytesRead != expectedSize {
		log.Printf("    WARNING: S3 download size mismatch for %s: expected %d bytes, got %d bytes", filename, expectedSize, bytesRead)
	}

	log.Printf("    S3 downloaded %s (%d bytes, expected %d) in %v", filename, bytesRead, expectedSize, duration)
	e.metrics.RecordStorjDownload(testName, "s3", bucket, "", duration, bytesRead, true)

	return nil
}

// deleteObject deletes a file from S3
func (e *S3Executor) deleteObject(ctx context.Context, testName, bucket, filename, fileSizeLabel string) error {
	start := time.Now()

	// Delete from S3
	_, err := e.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(filename),
	})

	duration := time.Since(start)

	if err != nil {
		e.metrics.RecordStorjDelete(testName, "s3", bucket, fileSizeLabel, 0, 0, false)
		return fmt.Errorf("S3 DeleteObject failed: %w", err)
	}

	log.Printf("    S3 deleted %s in %v", filename, duration)
	e.metrics.RecordStorjDelete(testName, "s3", bucket, fileSizeLabel, duration, 1, true)

	return nil
}
