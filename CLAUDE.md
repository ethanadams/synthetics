# Storj Synthetics Monitoring System

## Overview
Complete synthetic monitoring system for Storj satellites with multi-executor architecture supporting native Storj protocol and multiple S3-compatible gateway implementations.

**Version:** 1.0.0

## Architecture

### System Design
```
┌─────────────────────────────────────────────────────┐
│           Synthetics Service (Go)                   │
│                                                     │
│  Scheduler (cron) ──▶ Executor ──▶ k6 Binary       │
│                                     + xk6-storj     │
│                                     │               │
│                        ┌────────────▼────────────┐  │
│                        │  Metrics Collector      │  │
│                        └────────────┬────────────┘  │
│                                     │               │
│                        ┌────────────▼────────────┐  │
│                        │   /metrics (HTTP)       │  │
│                        └─────────────────────────┘  │
└─────────────────────────────────────────────────────┘
                                      │
                                      ▼
                         ┌────────────────────────┐
                         │      Prometheus        │
                         │   (scrapes metrics)    │
                         └────────────┬───────────┘
                                      │
                                      ▼
                         ┌────────────────────────┐
                         │       Grafana          │
                         │   (visualization)      │
                         └────────────────────────┘
```

### Multi-Executor Architecture
The system supports four execution modes for comprehensive testing:

| Executor | Implementation | Use Case |
|----------|---------------|----------|
| `uplink` | k6 + xk6-storj | Native Storj protocol (default, best performance) |
| `s3` | AWS SDK v2 | S3 gateway via official AWS SDK |
| `http-s3` | Go net/http | S3 gateway via raw HTTP (no SDK dependencies) |
| `curl-s3` | curl subprocess | S3 gateway via curl (useful for debugging) |

**1. UplinkExecutor** (Native Storj Protocol)
- Uses k6 + custom xk6-storj extension
- Tests native Storj protocol directly
- Wraps Storj uplink SDK
- Best performance and lowest overhead

**2. S3Executor** (S3-Compatible Gateway via AWS SDK)
- Uses AWS SDK v2 for S3 operations
- Pure Go implementation (no k6)
- Tests HTTP/S3 API compatibility
- Gateway performance monitoring

**3. HttpS3Executor** (S3-Compatible Gateway via raw HTTP)
- Uses Go's net/http with manual AWS Signature V4 signing
- No AWS SDK dependencies
- Granular HTTP timing metrics (DNS, TLS, TTFB, transfer)
- Full control over HTTP requests

**4. CurlS3Executor** (S3-Compatible Gateway via curl)
- Shells out to curl subprocess
- AWS Signature V4 headers passed via -H flags
- Granular HTTP timing metrics via curl's timing output
- Useful for debugging HTTP-level issues

All executors emit metrics with `executor` labels for direct comparison.

## Core Components

### 1. Custom xk6 Extension (`cmd/xk6-storj/`)
- **Purpose:** Enables k6 to test native Storj protocol
- **Operations:** Upload, Download, Delete, List, Stat
- **Features:** TTL support, custom metadata, error handling
- **Integration:** Registered as k6 module `k6/x/storj`

### 2. Synthetics Service (`cmd/synthetics/`)
- **HTTP Server:** Exposes `/metrics` and `/health` endpoints
- **Scheduler:** Cron-based test execution
- **Executor Manager:** Routes tests to appropriate executor
- **Lifecycle Management:** Graceful shutdown, signal handling

### 3. UplinkExecutor (`internal/executor/uplink_executor.go`)
- Executes tests via k6 subprocess
- Parses k6 JSON output for metrics
- Supports multi-step workflows
- Environment variable injection
- ULID-based filename generation

### 4. S3Executor (`internal/executor/s3_executor.go`)
- Native Go S3 operations
- Custom endpoint resolver for Storj gateway
- Operations determined by step name
- Direct AWS SDK v2 integration
- Streaming support for large files

### 5. HttpS3Executor (`internal/executor/http_s3_executor.go`)
- Raw HTTP requests using Go's net/http
- Manual AWS Signature V4 signing (`internal/executor/awsv4/`)
- HTTP timing via httptrace (DNS, TCP, TLS, TTFB, transfer)
- No external SDK dependencies

### 6. CurlS3Executor (`internal/executor/curl_s3_executor.go`)
- Shells out to curl command
- Reuses AWS Signature V4 signer for header generation
- Parses curl timing output for HTTP phases
- Writes upload data to temp files

### 7. Metrics Collector (`internal/metrics/collector.go`)
Prometheus metrics with `action`/`step_name` and `executor` labels:

**Test execution metrics:**
- `synthetics_test_runs_total{test_name, step_name, executor, status}`
- `synthetics_test_duration_seconds{test_name, step_name, executor}`

**Operation metrics:**
- `synth_duration_seconds{test_name, action, executor, bucket, file_size}` - duration histogram
- `synth_bytes_total{test_name, action, executor, bucket}` - bytes transferred
- `synth_operation_count_total{test_name, action, executor, bucket}` - operation count
- `synth_operation_success_total{test_name, action, executor, status}` - success/failure

**Live metrics (gauges for real-time visibility):**
- `synth_last_duration_seconds{test_name, action, executor}` - most recent operation duration
- `synth_last_http_phase_seconds{test_name, action, executor, phase}` - most recent HTTP phase timing

**HTTP timing metrics (S3 executors only):**
- `synth_http_timing_seconds{test_name, action, executor, phase}` - HTTP phase breakdown
  - Phases: dns, connect, tls, ttfb, transfer, sign, total

### 8. Logging (`internal/logging/`)
Configurable log levels: debug, info, warn, error

```yaml
logging:
  level: "debug"  # Enable detailed HTTP timing logs
```

### 9. Configuration System (`internal/config/config.go`)
- **YAML-based:** Human-readable configuration
- **Type-safe:** Structured fields with validation
- **Environment Variables:** `${VAR}` expansion for secrets
- **Human-readable Sizes:** "512KB", "5MB", "1GB" support
- **Per-test Overrides:** Bucket, filename, executor selection
- **Jitter Configuration:** Global, test-level, and step-level jitter support

### 10. Jitter System (`internal/jitter/`)
- Prevents thundering herd when tests share schedules
- Applies random delay (0 to max) before execution
- Supports duration (`"30s"`) or percentage (`"10%"`) formats
- Context-aware: respects cancellation for graceful shutdown
- Inheritance: step overrides test, test overrides global

### 11. Test Scripts (`scripts/tests/`)
Modular k6 JavaScript test scripts:
- `upload.js` - File upload with TTL support
- `download.js` - File download with verification
- `delete.js` - File deletion with batch cleanup
- `list_objects.js` - Bucket listing operations

### 12. Test Data Generation (`internal/testdata/`)
- Pre-generates test files on startup
- Caches files in `/tmp/test-data/`
- Avoids CPU overhead during tests
- Naming: `{test-name}-{size}.bin`

## Deployment Options

### Docker Deployment
**Multi-architecture Images:**
- `ghcr.io/ethanadams/synthetics:latest` (amd64, arm64)
- Automated builds via GitHub Actions
- Multi-stage builds for minimal image size (~50MB)

**Docker Compose Stack:**
- Synthetics service
- Prometheus for metrics storage
- Grafana for visualization
- Pre-configured scraping and dashboards

### Kubernetes Deployment
**Helm Chart Features:**
- Production-ready defaults
- S3 configuration support
- Secrets management
- Resource limits and requests
- Health checks and probes
- Optional persistent storage
- ServiceMonitor for Prometheus Operator
- Pod disruption budgets

**Install from OCI Registry:**
```bash
helm install synthetics oci://ghcr.io/ethanadams/charts/synthetics \
  --set storj.accessGrant="your-grant" \
  --set s3.accessKey="your-key" \
  --set s3.secretKey="your-secret"
```

**Install from Source:**
```bash
helm install synthetics ./deployments/helm/synthetics \
  --set storj.accessGrant="your-grant" \
  --set s3.accessKey="your-key" \
  --set s3.secretKey="your-secret"
```

## Monitoring & Visualization

### Grafana Dashboard
Pre-built dashboard with dynamic filtering:
- **Executor Filter:** Compare uplink vs S3 performance
- **Test Name Filter:** Focus on specific tests
- **Action Filter:** Filter by operation type (upload, download, delete, list)
- **Percentile Filter:** Select p50, p95, p99

**Visualizations:**
- Success rate gauge and trends
- Performance percentiles (p50, p95, p99) by executor
- Live response times (real-time gauge values)
- Throughput charts (bytes/sec)
- Performance degradation detection
- HTTP timing breakdown (S3 executors)

### Alerting
Pre-configured Prometheus alerts:
- High failure rates (>10%)
- Slow operations (p95 > thresholds)
- Service health checks
- Low throughput detection

## Configuration Examples

### Multi-Step Workflow (Uplink)
```yaml
tests:
  - name: "uplink-workflow"
    schedule: "*/5 * * * *"
    enabled: true
    executor: "uplink"
    steps:
      - name: "upload"
        script: "/app/scripts/tests/upload.js"
        timeout: "1m"
        file_size: "512KB"
        ttl_seconds: 3600
      - name: "download"
        script: "/app/scripts/tests/download.js"
        timeout: "30s"
      - name: "delete"
        script: "/app/scripts/tests/delete.js"
        timeout: "30s"
```

### S3 Gateway Test (AWS SDK)
```yaml
tests:
  - name: "s3-workflow"
    schedule: "*/5 * * * *"
    enabled: true
    executor: "s3"
    bucket: "s3-test-bucket"
    steps:
      - name: "upload"
        timeout: "1m"
        file_size: "5MB"
        ttl_seconds: 7200
      - name: "download"
        timeout: "30s"
      - name: "delete"
        timeout: "30s"
```

### HTTP S3 Gateway Test (Raw HTTP)
```yaml
tests:
  - name: "http-s3-workflow"
    schedule: "*/5 * * * *"
    enabled: true
    executor: "http-s3"
    steps:
      - name: "upload"
        timeout: "1m"
        file_size: "512KB"
      - name: "download"
        timeout: "30s"
      - name: "delete"
        timeout: "30s"
```

### Curl S3 Gateway Test
```yaml
tests:
  - name: "curl-s3-workflow"
    schedule: "*/5 * * * *"
    enabled: true
    executor: "curl-s3"
    steps:
      - name: "upload"
        timeout: "1m"
        file_size: "512KB"
      - name: "download"
        timeout: "30s"
      - name: "delete"
        timeout: "30s"
```

## Key Features

### Human-Readable Configuration
All file sizes support readable formats:
- "100KB" = 102,400 bytes
- "5MB" = 5,242,880 bytes
- "1GB" = 1,073,741,824 bytes

### ULID-Based Filenames
Auto-generated unique filenames per test run:
- Format: `{test-name}-{ULID}.bin`
- Example: `uplink-workflow-01HQZX4VWXY7Z8A9B0C1D2E3F4.bin`
- Lexicographically sortable

### TTL Support
Automatic file expiration:
```yaml
steps:
  - name: "upload"
    file_size: "512KB"
    ttl_seconds: 3600  # Expires in 1 hour
```

### Per-Test Bucket Override
```yaml
tests:
  - name: "large-file-test"
    bucket: "large-files-bucket"
    steps:
      - name: "upload"
        file_size: "10MB"
```

### Jitter Support
Prevents thundering herd when multiple tests run on the same schedule by adding random delays before execution.

**Configuration levels (inheritance: step → test → global):**
```yaml
# Global jitter (disabled by default)
jitter:
  enabled: false
  max: "30s"  # Duration or percentage

# Test-level override
tests:
  - name: "my-test"
    schedule: "*/5 * * * *"
    jitter:
      enabled: true
      max: "10%"  # 10% of 5 min = up to 30s delay
    steps:
      - name: "upload"
        jitter:
          enabled: true
          max: "5s"  # Step-level override (duration only)
      - name: "download"
        # Inherits test-level jitter
      - name: "delete"
        jitter:
          enabled: false  # Disable for this step
```

**Format options:**
- Duration: `"30s"`, `"1m"`, `"2m30s"` (fixed maximum)
- Percentage: `"10%"` (of cron schedule interval, test-level only)

## Development

### Building Locally
```bash
make deps
make install-xk6
make build-xk6
make build
make run
```

### Testing
```bash
make test
make docker-build
make docker-up
```

## Technical Specifications

**Languages:**
- Go 1.24+ (service, xk6 extension, S3 executors)
- JavaScript (k6 test scripts)

**Dependencies:**
- storj.io/uplink - Native Storj SDK
- github.com/robfig/cron/v3 - Cron scheduling
- github.com/prometheus/client_golang - Metrics
- gopkg.in/yaml.v3 - Configuration parsing
- github.com/aws/aws-sdk-go-v2 - S3 operations
- github.com/oklog/ulid/v2 - ULID generation

**External Tools:**
- xk6 - Custom k6 builder
- k6 - Load testing framework
- curl - HTTP client (for curl-s3 executor)
- Prometheus - Metrics storage
- Grafana - Visualization

## Repository

**GitHub:** https://github.com/ethanadams/synthetics
**Container Registry:** https://ghcr.io/ethanadams/synthetics
