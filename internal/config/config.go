package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	Satellite SatelliteConfig `yaml:"satellite"`
	S3        S3Config        `yaml:"s3"`
	Tests     []Test          `yaml:"tests"`
	K6        K6Config        `yaml:"k6"`
	Metrics   MetricsConfig   `yaml:"metrics"`
	Logging   LoggingConfig   `yaml:"logging"`
	Jitter    JitterConfig    `yaml:"jitter"` // Global jitter config (default: disabled)
}

// JitterConfig holds jitter configuration
type JitterConfig struct {
	Enabled *bool  `yaml:"enabled,omitempty"` // nil = inherit from parent, false = disabled
	Max     string `yaml:"max,omitempty"`     // Max jitter: duration ("30s") or percentage ("10%")
}

// SatelliteConfig holds Storj satellite configuration
type SatelliteConfig struct {
	AccessGrant string `yaml:"access_grant"`
	Bucket      string `yaml:"bucket"`
}

// S3Config holds S3 gateway configuration
type S3Config struct {
	Endpoint  string `yaml:"endpoint"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	Region    string `yaml:"region"`
}

// Test defines a synthetic test (1+ sequential steps)
type Test struct {
	Name     string        `yaml:"name"`
	Schedule string        `yaml:"schedule"`
	Enabled  bool          `yaml:"enabled"`
	Executor string        `yaml:"executor"`         // Executor type: "uplink" or "s3" (default: "uplink")
	Bucket   *string       `yaml:"bucket,omitempty"` // Optional: override global bucket
	Filename *string       `yaml:"filename"`         // Optional: custom filename
	Jitter   *JitterConfig `yaml:"jitter,omitempty"` // Optional: test-level jitter override
	Steps    []TestStep    `yaml:"steps"`            // Required: 1+ steps
}

// ByteSize represents a file size that can be specified as bytes or human-readable format
type ByteSize int64

// UnmarshalYAML implements custom YAML unmarshaling for human-readable sizes
func (bs *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	// Try to unmarshal as int64 first (backward compatibility)
	var intVal int64
	if err := value.Decode(&intVal); err == nil {
		*bs = ByteSize(intVal)
		return nil
	}

	// Try to unmarshal as string (human-readable format)
	var strVal string
	if err := value.Decode(&strVal); err != nil {
		return fmt.Errorf("file_size must be a number or string like '5MB': %w", err)
	}

	// Parse human-readable size
	size, err := parseByteSize(strVal)
	if err != nil {
		return err
	}
	*bs = ByteSize(size)
	return nil
}

// Int64 returns the byte size as int64
func (bs ByteSize) Int64() int64 {
	return int64(bs)
}

// String returns the byte size in human-readable format
func (bs ByteSize) String() string {
	bytes := int64(bs)
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

// parseByteSize converts human-readable sizes to bytes
// Supports: B, KB, MB, GB (case-insensitive)
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Find where the number ends and unit begins
	var numStr string
	var unitStr string
	for i, c := range s {
		if c >= '0' && c <= '9' || c == '.' {
			continue
		}
		numStr = s[:i]
		unitStr = s[i:]
		break
	}

	// If no unit found, assume it's just a number in bytes
	if unitStr == "" {
		numStr = s
		unitStr = "B"
	}

	// Parse the number
	num, err := strconv.ParseFloat(strings.TrimSpace(numStr), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in size '%s': %w", s, err)
	}

	// Parse the unit (case-insensitive)
	unitStr = strings.TrimSpace(strings.ToUpper(unitStr))
	var multiplier int64
	switch unitStr {
	case "B", "":
		multiplier = 1
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit '%s' (supported: B, KB, MB, GB)", unitStr)
	}

	return int64(num * float64(multiplier)), nil
}

// TestStep defines a single step within a test
type TestStep struct {
	Name    string `yaml:"name"`
	Script  string `yaml:"script"`
	Timeout string `yaml:"timeout"`

	// Upload options
	FileSize   *ByteSize `yaml:"file_size,omitempty"`   // Size (e.g., "5MB", "512KB", or bytes)
	TTLSeconds *int      `yaml:"ttl_seconds,omitempty"` // Time-to-live in seconds

	// Download/Delete options
	FilePrefix *string `yaml:"file_prefix,omitempty"` // File prefix filter

	// Delete options
	MaxAgeMinutes *int `yaml:"max_age_minutes,omitempty"` // Max age for deletion
	MaxDelete     *int `yaml:"max_delete,omitempty"`      // Max files to delete

	// Jitter options
	Jitter *JitterConfig `yaml:"jitter,omitempty"` // Optional: step-level jitter
}

// GetExecutor returns the executor type (with default "uplink")
func (t *Test) GetExecutor() string {
	if t.Executor == "" {
		return "uplink"
	}
	return t.Executor
}

// GetBucket returns the bucket for this test (test-specific or global)
func (t *Test) GetBucket(globalBucket string) string {
	if t.Bucket != nil && *t.Bucket != "" {
		return *t.Bucket
	}
	return globalBucket
}

// GetFilename returns the filename for this test run
func (t *Test) GetFilename(ulid string) string {
	if t.Filename != nil && *t.Filename != "" {
		return *t.Filename
	}
	return fmt.Sprintf("%s-%s.bin", t.Name, ulid)
}

// IsSingleStep returns true if test has exactly one step
func (t *Test) IsSingleStep() bool {
	return len(t.Steps) == 1
}

// TimeoutDuration returns the timeout as a time.Duration
func (t *TestStep) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(t.Timeout)
	if err != nil {
		return 2 * time.Minute // default
	}
	return d
}

// K6Config holds k6 binary configuration
type K6Config struct {
	BinaryPath   string `yaml:"binary_path"`
	OutputFormat string `yaml:"output_format"`
}

// MetricsConfig holds metrics server configuration
type MetricsConfig struct {
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// IsEnabled returns whether jitter is enabled
func (j *JitterConfig) IsEnabled() bool {
	if j == nil || j.Enabled == nil {
		return false
	}
	return *j.Enabled
}

// GetEffectiveJitter returns the effective jitter config, merging with parent
func (j *JitterConfig) GetEffectiveJitter(parent *JitterConfig) JitterConfig {
	result := JitterConfig{}

	// Start with parent values
	if parent != nil {
		result.Enabled = parent.Enabled
		result.Max = parent.Max
	}

	// Override with current values if set
	if j != nil {
		if j.Enabled != nil {
			result.Enabled = j.Enabled
		}
		if j.Max != "" {
			result.Max = j.Max
		}
	}

	return result
}

// ParseMaxJitter parses the max jitter value and returns the duration
// For percentages, scheduleInterval is used to calculate the actual duration
func (j *JitterConfig) ParseMaxJitter(scheduleInterval time.Duration) (time.Duration, error) {
	if j == nil || j.Max == "" {
		return 0, nil
	}

	max := strings.TrimSpace(j.Max)

	// Check if it's a percentage
	if strings.HasSuffix(max, "%") {
		percentStr := strings.TrimSuffix(max, "%")
		percent, err := strconv.ParseFloat(percentStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid jitter percentage '%s': %w", max, err)
		}
		if percent < 0 || percent > 100 {
			return 0, fmt.Errorf("jitter percentage must be between 0 and 100, got %v", percent)
		}
		if scheduleInterval <= 0 {
			return 0, fmt.Errorf("cannot use percentage jitter without schedule interval")
		}
		return time.Duration(float64(scheduleInterval) * percent / 100), nil
	}

	// Parse as duration
	return time.ParseDuration(max)
}

// ParseCronInterval estimates the interval between cron executions
// Supports common patterns like "*/5 * * * *" (every 5 min), "0 * * * *" (hourly), etc.
func ParseCronInterval(schedule string) (time.Duration, error) {
	parts := strings.Fields(schedule)
	if len(parts) < 5 {
		return 0, fmt.Errorf("invalid cron schedule: %s", schedule)
	}

	minute := parts[0]
	hour := parts[1]

	// Check for "*/N" pattern in minutes
	if strings.HasPrefix(minute, "*/") {
		n, err := strconv.Atoi(strings.TrimPrefix(minute, "*/"))
		if err == nil && n > 0 {
			return time.Duration(n) * time.Minute, nil
		}
	}

	// Check for "*/N" pattern in hours
	if minute == "0" && strings.HasPrefix(hour, "*/") {
		n, err := strconv.Atoi(strings.TrimPrefix(hour, "*/"))
		if err == nil && n > 0 {
			return time.Duration(n) * time.Hour, nil
		}
	}

	// Fixed minute, any hour = hourly
	if _, err := strconv.Atoi(minute); err == nil && hour == "*" {
		return time.Hour, nil
	}

	// Fixed minute and hour = daily
	if _, err := strconv.Atoi(minute); err == nil {
		if _, err := strconv.Atoi(hour); err == nil {
			return 24 * time.Hour, nil
		}
	}

	// Default: assume 1 minute if we can't determine
	return time.Minute, nil
}

// GetTestJitter returns the effective jitter config for a test
func (t *Test) GetTestJitter(global JitterConfig) JitterConfig {
	return t.Jitter.GetEffectiveJitter(&global)
}

// GetStepJitter returns the effective jitter config for a step
func (s *TestStep) GetStepJitter(testJitter *JitterConfig) JitterConfig {
	return s.Jitter.GetEffectiveJitter(testJitter)
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand environment variables
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, err
	}

	// Set defaults
	if cfg.K6.BinaryPath == "" {
		cfg.K6.BinaryPath = "/usr/local/bin/k6"
	}
	if cfg.K6.OutputFormat == "" {
		cfg.K6.OutputFormat = "json"
	}
	if cfg.S3.Region == "" {
		cfg.S3.Region = "us-east-1"
	}
	if cfg.Metrics.Port == 0 {
		cfg.Metrics.Port = 8080
	}
	if cfg.Metrics.Path == "" {
		cfg.Metrics.Path = "/metrics"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}

	return &cfg, nil
}
