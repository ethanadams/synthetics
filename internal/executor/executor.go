package executor

import (
	"context"

	"github.com/ethanadams/synthetics/internal/config"
)

// TestExecutor defines the interface for test execution
type TestExecutor interface {
	RunTest(ctx context.Context, test *config.Test) error
}
