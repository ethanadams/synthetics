package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ethanadams/synthetics/internal/config"
	"github.com/ethanadams/synthetics/internal/executor"
	"github.com/ethanadams/synthetics/internal/jitter"
	"github.com/robfig/cron/v3"
)

// Scheduler manages scheduled test execution
type Scheduler struct {
	cron      *cron.Cron
	executors map[string]executor.TestExecutor
	config    *config.Config
}

// New creates a new scheduler
func New(cfg *config.Config, executors map[string]executor.TestExecutor) *Scheduler {
	return &Scheduler{
		cron:      cron.New(),
		executors: executors,
		config:    cfg,
	}
}

// Start begins scheduling tests
func (s *Scheduler) Start(ctx context.Context) error {
	enabledCount := 0

	// Schedule all tests (single-step and multi-step)
	for _, test := range s.config.Tests {
		if !test.Enabled {
			log.Printf("Skipping disabled test: %s", test.Name)
			continue
		}

		// Capture loop variable
		testCopy := test

		// Get the executor for this test
		executorType := testCopy.GetExecutor()
		exec, ok := s.executors[executorType]
		if !ok {
			log.Printf("Skipping test %s: unknown executor type '%s'", testCopy.Name, executorType)
			continue
		}

		// Determine test type for logging
		testType := "single-step"
		if len(testCopy.Steps) > 1 {
			testType = fmt.Sprintf("%d-step", len(testCopy.Steps))
		}

		// Calculate effective jitter for this test
		effectiveJitter := testCopy.GetTestJitter(s.config.Jitter)
		var maxJitter time.Duration
		if effectiveJitter.IsEnabled() {
			scheduleInterval, _ := config.ParseCronInterval(testCopy.Schedule)
			maxJitter, _ = effectiveJitter.ParseMaxJitter(scheduleInterval)
		}

		// Capture maxJitter for closure
		testMaxJitter := maxJitter

		// Schedule the test
		entryID, err := s.cron.AddFunc(test.Schedule, func() {
			// Apply test-level jitter if configured
			if testMaxJitter > 0 {
				if err := jitter.Apply(ctx, testMaxJitter, fmt.Sprintf("test %s", testCopy.Name)); err != nil {
					log.Printf("Test %s jitter interrupted: %v", testCopy.Name, err)
					return
				}
			}

			log.Printf("Scheduled execution: %s (executor: %s)", testCopy.Name, executorType)
			if err := exec.RunTest(ctx, &testCopy); err != nil {
				log.Printf("Test %s failed: %v", testCopy.Name, err)
			}
		})

		if err != nil {
			return err
		}

		enabledCount++
		if testMaxJitter > 0 {
			log.Printf("Scheduled test: %s (%s, executor: %s, schedule: %s, jitter: max %v, entry ID: %d)",
				test.Name, testType, executorType, test.Schedule, testMaxJitter, entryID)
		} else {
			log.Printf("Scheduled test: %s (%s, executor: %s, schedule: %s, entry ID: %d)",
				test.Name, testType, executorType, test.Schedule, entryID)
		}
	}

	if enabledCount == 0 {
		log.Println("Warning: No tests enabled in configuration")
	} else {
		log.Printf("Successfully scheduled %d test(s)", enabledCount)
	}

	// Start the cron scheduler
	s.cron.Start()
	log.Println("Scheduler started")

	return nil
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	log.Println("Stopping scheduler...")
	ctx := s.cron.Stop()
	<-ctx.Done()
	log.Println("Scheduler stopped")
}

// RunNow immediately runs a specific test (useful for testing)
func (s *Scheduler) RunNow(ctx context.Context, testName string) error {
	for _, test := range s.config.Tests {
		if test.Name == testName {
			executorType := test.GetExecutor()
			exec, ok := s.executors[executorType]
			if !ok {
				return fmt.Errorf("unknown executor type '%s' for test %s", executorType, testName)
			}
			log.Printf("Running test on demand: %s (executor: %s)", testName, executorType)
			return exec.RunTest(ctx, &test)
		}
	}
	return fmt.Errorf("test not found: %s", testName)
}
