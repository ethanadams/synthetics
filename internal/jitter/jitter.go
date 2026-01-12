package jitter

import (
	"context"
	"log"
	"math/rand"
	"time"
)

// Apply sleeps for a random duration between 0 and maxJitter
// Returns immediately if maxJitter <= 0 or context is cancelled
func Apply(ctx context.Context, maxJitter time.Duration, label string) error {
	if maxJitter <= 0 {
		return nil
	}

	// Generate random jitter between 0 and maxJitter
	jitterDuration := time.Duration(rand.Int63n(int64(maxJitter)))

	if jitterDuration > 0 {
		log.Printf("Applying jitter: %v (max: %v) for %s", jitterDuration, maxJitter, label)
	}

	select {
	case <-time.After(jitterDuration):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
