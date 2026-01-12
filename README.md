# Storj Synthetics Monitoring

Synthetic monitoring system for Storj satellites using k6, Go, and Prometheus metrics. This system generates synthetic traffic to test Storj satellite availability and performance via native Storj protocol and multiple S3-compatible gateway implementations, exposing detailed metrics for alerting and visualization.

## Features

- **Multi-executor architecture** with 4 executor types for comprehensive testing:
  - `uplink`: Native Storj protocol via k6 + xk6-storj extension
  - `s3`: S3 gateway via AWS SDK v2
  - `http-s3`: S3 gateway via raw HTTP (Go net/http, no SDK dependencies)
  - `curl-s3`: S3 gateway via curl subprocess (useful for debugging)
- **Custom k6 extension** wrapping Storj uplink SDK for native Storj operations
- **Scheduled synthetic tests** for upload/download operations with configurable intervals
- **Human-readable file sizes** in configuration (e.g., "5MB", "512KB")
- **Per-test bucket overrides** to test different buckets independently
- **Prometheus metrics** with executor labels for monitoring latency, throughput, and success rates
- **Docker Compose** stack with Prometheus and Grafana for complete observability
- **Comprehensive alerting** with pre-configured Prometheus alert rules
- **Flexible configuration** via YAML with environment variable support

## Architecture

```
┌───────────────────────────────────────────────────┐
│              Synthetics Service (Go)              │
│                                                   │
│  ┌────────────────┐                               │
│  │   Scheduler    │───────┐                       │
│  │  (cron-based)  │       │                       │
│  └────────────────┘       │                       │
│                           │                       │
│            ┌──────────────┴──────────────┐        │
│            │      Executor Router        │        │
│            └──────────────┬──────────────┘        │
│    ┌───────────┬──────────┼──────────┐            │
│    ▼           ▼          ▼          ▼            │
│ ┌──────┐  ┌────────┐  ┌────────┐  ┌──────────┐    │
│ │uplink│  │  s3    │  │http-s3 │  │curl-s3   │    │
│ │ k6+  │  │AWS SDK │  │net/http│  │  curl    │    │
│ │ xk6  │  │  v2    │  │  only  │  │subprocess│    │
│ └──┬───┘  └───┬────┘  └───┬────┘  └────┬─────┘    │
│    │          │           │           │           │
│    └──────────┴───────────┴───────────┘           │
│                           │                       │
│              ┌────────────▼─────────────┐         │
│              │   Metrics Collector      │         │
│              │ (Prometheus Go client)   │         │
│              │  Labels: executor, test  │         │
│              └────────────┬─────────────┘         │
│                           │                       │
│              ┌────────────▼─────────────┐         │
│              │   HTTP Server (:8080)    │         │
│              │   /metrics - Prometheus  │         │
│              │   /health  - Health      │         │
│              └──────────────────────────┘         │
└───────────────────────────────────────────────────┘
                            │
                            ▼
               ┌────────────────────────┐
               │     Prometheus         │
               │  (metrics storage)     │
               └────────────┬───────────┘
                            │
                            ▼
               ┌────────────────────────┐
               │      Grafana           │
               │  (visualization)       │
               │  Filter by executor    │
               └────────────────────────┘
```

## Quick Start

### Prerequisites

- Go 1.24+ (for local development)
- Docker and Docker Compose
- Storj access grant ([get one here](https://www.storj.io/))
- xk6 (will be installed automatically by build script)

### Option 1: Docker Deployment (Recommended)

1. Clone the repository:
```bash
git clone https://github.com/ethanadams/synthetics.git
cd synthetics
```

2. Create configuration:
```bash
cp configs/config.yaml.example configs/config.yaml
```

3. Set your Storj access grant:
```bash
export STORJ_ACCESS_GRANT="your-access-grant-here"
```

4. Start the stack:
```bash
make docker-up
```

5. Access the services:
- **Synthetics metrics**: http://localhost:8080/metrics
- **Prometheus**: http://localhost:9090
- **Grafana**: http://localhost:3000 (username: `admin`, password: `admin`)

### Option 2: Local Development

1. Install dependencies:
```bash
make deps
make install-xk6
```

2. Build the custom k6 binary:
```bash
make build-xk6
```

3. Create and edit configuration:
```bash
cp configs/config.yaml.example configs/config.yaml
# Edit configs/config.yaml and set your access grant
```

4. Run the service:
```bash
make run
```

5. Check metrics in another terminal:
```bash
curl http://localhost:8080/metrics
```

## Docker Images

**GitHub Container Registry:** [ghcr.io/ethanadams/synthetics](https://ghcr.io/ethanadams/synthetics)

Multi-architecture images are available for `linux/amd64` and `linux/arm64`.

### Pull and Run

```bash
docker pull ghcr.io/ethanadams/synthetics:latest

docker run -d \
  --name synthetics \
  -p 8080:8080 \
  -e STORJ_ACCESS_GRANT="your-access-grant" \
  -v $(pwd)/configs/config.yaml:/app/configs/config.yaml:ro \
  ghcr.io/ethanadams/synthetics:latest
```

### Available Tags

- `latest` - Latest build from main branch
- `1.0.0`, `1.0`, `1` - Semantic version tags
- `main-abc1234` - Commit-specific builds

### Build and Push

```bash
# Build for both amd64 and arm64
make docker-buildx-push VERSION=1.0.0

# Test build locally
make docker-buildx-dry VERSION=1.0.0
```

## Configuration

Edit `configs/config.yaml` to configure satellites, tests, and schedules. All tests use a unified structure with 1+ sequential steps.

### Basic Example

```yaml
satellite:
  access_grant: "${STORJ_ACCESS_GRANT}"  # Use env var for security
  bucket: "synthetics-test"              # Default bucket for all tests

tests:
  # Multi-step workflow with ULID-based filename
  - name: "quick-workflow"
    schedule: "*/5 * * * *"  # Every 5 minutes
    enabled: true
    executor: "uplink"       # Options: "uplink", "s3", "http-s3", "curl-s3"
    steps:
      - name: "upload"
        script: "/app/scripts/tests/upload.js"
        timeout: "1m"
        file_size: "512KB"   # Human-readable: 5MB, 1GB, etc.
        ttl_seconds: 3600    # Optional: 1 hour expiration
      - name: "download"
        script: "/app/scripts/tests/download.js"
        timeout: "30s"
      - name: "delete"
        script: "/app/scripts/tests/delete.js"
        timeout: "30s"

  # S3 gateway test with custom bucket
  - name: "s3-gateway-test"
    schedule: "*/5 * * * *"
    enabled: true
    executor: "s3"           # Use S3 gateway instead of native
    bucket: "s3-test-bucket" # Optional: override global bucket
    steps:
      - name: "upload"
        timeout: "1m"
        file_size: "5MB"
        ttl_seconds: 7200    # TTL supported on S3 executor too!

  # Single-step test with custom filename
  - name: "canary-check"
    schedule: "*/5 * * * *"
    enabled: true
    filename: "canary-file.bin"  # Optional: custom filename
    steps:
      - name: "download"
        script: "/app/scripts/tests/download.js"
        timeout: "1m"
```

### Test Structure

- **All tests have 1+ steps** that run sequentially
- **Executor selection**: Choose `uplink` (native Storj) or `s3` (S3 gateway) per test
- **Automatic filename generation** using ULID (e.g., `quick-workflow-01HQZX4VWXY7Z8A9B0C1D2E3F4.bin`)
- **Custom filenames** available via `filename` field
- **Per-test bucket overrides** via `bucket` field
- **Human-readable file sizes**: "512KB", "5MB", "1GB", etc. (also accepts raw bytes)
- **Shared state** across steps via `SHARED_FILE`, `TEST_NAME`, and `TEST_ULID` environment variables

### Filename Behavior

- **Default (no `filename` field)**: Auto-generates ULID-based filenames for each run
- **Custom (`filename: "name.bin"`)**: Uses the same filename for every run (useful for canary files)

### Executor Types

| Executor | Implementation | Use Case |
|----------|---------------|----------|
| `uplink` | k6 + xk6-storj extension | Native Storj protocol (default, best performance) |
| `s3` | AWS SDK v2 | S3 gateway via official AWS SDK |
| `http-s3` | Go net/http + AWS Sig V4 | S3 gateway via raw HTTP (no SDK dependencies) |
| `curl-s3` | curl subprocess | S3 gateway via curl (useful for debugging) |

### Schedule Format

Uses standard cron format:
```
* * * * *
│ │ │ │ │
│ │ │ │ └─── Day of week (0-6, Sunday=0)
│ │ │ └───── Month (1-12)
│ │ └─────── Day of month (1-31)
│ └───────── Hour (0-23)
└─────────── Minute (0-59)
```

Examples:
- `*/5 * * * *` - Every 5 minutes
- `0 * * * *` - Every hour
- `0 0 * * *` - Every day at midnight
- `0 9-17 * * 1-5` - Every hour from 9 AM to 5 PM, Monday through Friday

### S3 Configuration (Optional)

To enable S3 gateway testing, add S3 configuration to your config.yaml:

```yaml
satellite:
  access_grant: "${STORJ_ACCESS_GRANT}"
  bucket: "synthetics-test"

s3:
  endpoint: "https://gateway.storjshare.io"  # Storj S3 gateway
  access_key: "${S3_ACCESS_KEY}"             # S3 access key
  secret_key: "${S3_SECRET_KEY}"             # S3 secret key
  region: "us-east-1"                        # AWS region

tests:
  - name: "s3-upload-test"
    schedule: "*/5 * * * *"
    enabled: true
    executor: "s3"  # Use S3 executor
    steps:
      - name: "upload"
        timeout: "1m"
        file_size: "5MB"
        ttl_seconds: 3600  # TTL works on S3 executor too!
```

**Notes:**
- S3 configuration is only required if you have tests with `executor: "s3"`
- Tests with `executor: "uplink"` (or no executor specified) only need the `satellite` configuration
- Use environment variables for credentials: `S3_ACCESS_KEY` and `S3_SECRET_KEY`
- S3 executor doesn't require script files - operations are determined by step name (upload, download, delete)
- TTL (time-to-live) is supported on both uplink and S3 executors

## Metrics

All metrics are exposed at the `/metrics` endpoint in Prometheus format.

### Test Execution Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `synthetics_test_runs_total` | Counter | `test_name`, `step_name`, `executor`, `status` | Total number of test runs |
| `synthetics_test_duration_seconds` | Histogram | `test_name`, `step_name`, `executor` | Test execution duration |

**Note:** `step_name` is the user-defined name from config (e.g., "upload", "my-custom-step").

### Storj Operation Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `synth_duration_seconds` | Histogram | `test_name`, `action`, `executor`, `bucket`, `file_size` | Operation latency (upload, download, etc.) |
| `synth_bytes_total` | Counter | `test_name`, `action`, `executor`, `bucket` | Total bytes transferred (upload/download) |
| `synth_operation_count_total` | Counter | `test_name`, `action`, `executor`, `bucket` | Count of operations performed |
| `synth_operation_success_total` | Counter | `test_name`, `action`, `executor`, `status` | Operation success/failure counts |

**Note:** `action` is automatically determined by executor: "upload", "download", "delete", or "list" (not user-configurable).

### Live Metrics (Gauges)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `synth_last_duration_seconds` | Gauge | `test_name`, `action`, `executor` | Most recent operation duration |
| `synth_last_http_phase_seconds` | Gauge | `test_name`, `action`, `executor`, `phase` | Most recent HTTP phase timing |

### HTTP Timing Metrics (S3 Executors Only)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `synth_http_timing_seconds` | Histogram | `test_name`, `action`, `executor`, `phase` | HTTP phase breakdown |

**Phases:** dns, connect, tls, ttfb (time to first byte), transfer, sign, total

### Example Prometheus Queries

```promql
# 95th percentile upload latency
histogram_quantile(0.95, rate(synth_duration_seconds_bucket{action="upload"}[5m]))

# Upload failure rate
rate(synth_operation_success_total{action="upload",status="failure"}[5m])

# Upload throughput (bytes/sec)
rate(synth_bytes_total{action="upload"}[5m])

# Test success rate over time
rate(synthetics_test_runs_total{status="success"}[5m]) / rate(synthetics_test_runs_total[5m])

# Compare upload vs download latency
histogram_quantile(0.95, rate(synth_duration_seconds_bucket[5m])) by (action)
```

## Grafana Dashboard

A pre-built Grafana dashboard is available at `deployments/grafana/storj-synthetics-dashboard.json` with:

**Time-Range Aware Filtering:**
- Filter by test name (only shows tests that ran in selected time window)
- Filter by executor type (uplink, s3) - **compare native vs S3 gateway performance**
- Filter by action (upload, download, delete, list)
- Filter by file size (only shows sizes tested in selected time window)
- **Dropdowns automatically update when you change the time range picker**

All variables use `query_result()` with `$__range` for true time-awareness.

**Visualizations:**
- Success rate gauge and trends
- Performance percentiles (p50, p95, p99) by executor
- Throughput charts (bytes/sec)
- Test distribution and summary table
- Duration breakdowns by operation and executor

**Access:**
- URL: http://localhost:3000
- Default credentials: admin/admin (change after first login)
- Dashboard auto-provisions on startup

See `deployments/grafana/README.md` for detailed usage instructions.

## Alerting

Pre-configured Prometheus alerts are available in `deployments/prometheus/alerts.yml`:

- **High failure rates** (>10% failures)
- **Slow operations** (p95 latency > thresholds)
- **Service health** (service down, no recent tests)
- **Low throughput** detection

Customize thresholds by editing the alerts file.

## Kubernetes Deployment

### Helm Chart

Deploy to Kubernetes using the Helm chart from OCI registry:

```bash
# Install from OCI registry (recommended)
helm install synthetics oci://ghcr.io/ethanadams/charts/synthetics \
  --set storj.accessGrant="your-access-grant" \
  --namespace monitoring \
  --create-namespace

# Or install from source
helm install synthetics ./deployments/helm/synthetics \
  --set storj.accessGrant="your-access-grant" \
  --namespace monitoring \
  --create-namespace

# Production install with custom values
helm install synthetics oci://ghcr.io/ethanadams/charts/synthetics \
  --set storj.existingSecret="storj-credentials" \
  --namespace monitoring
```

**Features:**
- ServiceMonitor for Prometheus Operator integration
- Persistent storage for test data
- Horizontal Pod Autoscaling support
- Configurable resource limits
- Pod Disruption Budget
- Health checks and probes

**Configuration Options:**
- `storj.accessGrant` - Storj access grant (for uplink executor)
- `storj.bucket` - Default bucket name
- `s3.endpoint` - S3 gateway endpoint (for s3 executor)
- `s3.accessKey` - S3 access key (for s3 executor)
- `s3.secretKey` - S3 secret key (for s3 executor)
- `config.tests` - Test definitions (supports both uplink and s3 executors)
- `persistence.enabled` - Enable persistent storage
- `serviceMonitor.enabled` - Create ServiceMonitor
- `resources` - CPU/memory limits

See `deployments/helm/synthetics/README.md` for complete documentation.

### Example Values

**Development:**
```bash
# Download values file for customization
helm show values oci://ghcr.io/ethanadams/charts/synthetics > values-dev.yaml
# Edit values-dev.yaml, then install
helm install synthetics oci://ghcr.io/ethanadams/charts/synthetics \
  -f values-dev.yaml \
  --set storj.accessGrant="dev-grant"
```

**Production:**
```bash
# Create secret first
kubectl create secret generic storj-credentials \
  --from-literal=access-grant="prod-grant" \
  -n monitoring

# Install chart
helm install synthetics oci://ghcr.io/ethanadams/charts/synthetics \
  --set storj.existingSecret="storj-credentials" \
  --namespace monitoring
```

## Available Commands

```bash
make help              # Show all available commands
make build             # Build the synthetics service
make build-xk6         # Build custom k6 binary
make run               # Run service locally
make test              # Run tests
make docker-build      # Build Docker images
make docker-up         # Start Docker stack
make docker-down       # Stop Docker stack
make docker-logs       # View logs
make clean             # Clean build artifacts
```

## Writing Custom Tests

Create new test scripts in `scripts/tests/`:

```javascript
import storj from 'k6/x/storj';
import { check } from 'k6';
import { Trend } from 'k6/metrics';

const myMetric = new Trend('my_custom_metric');

export default function () {
    const client = storj.newClient(__ENV.STORJ_ACCESS_GRANT);

    try {
        // Your test logic here
        const start = Date.now();
        client.upload(bucket, key, data);
        myMetric.add(Date.now() - start);
    } finally {
        client.close();
    }
}
```

Add the test to your configuration and enable it.

## Troubleshooting

### k6 binary not found
```bash
make build-xk6  # Build the custom k6 binary
```

### Tests not running
Check logs:
```bash
docker-compose -f deployments/docker-compose.yml logs synthetics
```

### No metrics in Prometheus
1. Verify service is running: `curl http://localhost:8080/health`
2. Check metrics are exposed: `curl http://localhost:8080/metrics`
3. Verify Prometheus scrape config in `deployments/prometheus/prometheus.yml`

### Access grant issues
Ensure your access grant has:
- Read/write permissions
- Access to the configured bucket
- Valid expiration date

## Development

### Project Structure

```
synthetics/
├── cmd/
│   ├── synthetics/          # Main service
│   └── xk6-storj/           # k6 extension
├── internal/
│   ├── config/              # Configuration
│   ├── executor/            # Test executor
│   ├── k6output/            # Output parser
│   ├── metrics/             # Prometheus metrics
│   └── scheduler/           # Cron scheduler
├── scripts/
│   └── tests/               # k6 test scripts
├── configs/                 # Configuration files
└── deployments/             # Docker & Prometheus configs
```

### Running Tests

```bash
go test -v ./...
```

### Building Locally

```bash
# Build service
go build -o synthetics ./cmd/synthetics

# Build k6 with extension
./scripts/build-xk6.sh

# Run
./synthetics
```

## Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request

## Support

- GitHub Issues: https://github.com/ethanadams/synthetics/issues
- Storj Documentation: https://docs.storj.io/
- k6 Documentation: https://k6.io/docs/
