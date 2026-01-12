package testdata

import (
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ethanadams/synthetics/internal/config"
)

const dataDir = "/tmp/test-data"

// EnsureTestDataFiles generates test data files for all configured tests
// if they don't already exist. This is called once at startup.
func EnsureTestDataFiles(cfg *config.Config) error {
	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create test data directory: %w", err)
	}

	log.Printf("Ensuring test data files in %s...", dataDir)

	// Collect unique (testName, fileSize) combinations from config
	fileSizes := make(map[string]int64)

	for _, test := range cfg.Tests {
		for _, step := range test.Steps {
			// Only care about upload steps
			if filepath.Base(step.Script) != "upload.js" {
				continue
			}

			// Get file size from step config
			if step.FileSize != nil && step.FileSize.Int64() > 0 {
				size := step.FileSize.Int64()
				key := fmt.Sprintf("%s-%d", test.Name, size)
				fileSizes[key] = size
			}
		}
	}

	if len(fileSizes) == 0 {
		log.Printf("No upload tests found in config, skipping test data generation")
		return nil
	}

	// Generate each unique file
	for key, size := range fileSizes {
		filename := filepath.Join(dataDir, key+".bin")
		if err := ensureFile(filename, size); err != nil {
			log.Printf("Warning: failed to generate %s: %v", filename, err)
		}
	}

	// List generated files
	entries, err := os.ReadDir(dataDir)
	if err == nil {
		log.Printf("Test data files ready (%d files):", len(entries))
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil {
				log.Printf("  - %s (%s)", entry.Name(), formatBytes(info.Size()))
			}
		}
	}

	return nil
}

// ensureFile creates a test data file if it doesn't exist or is wrong size
func ensureFile(filename string, size int64) error {
	// Check if file exists with correct size
	if info, err := os.Stat(filename); err == nil {
		if info.Size() == size {
			log.Printf("  Using existing: %s", filepath.Base(filename))
			return nil
		}
		// Wrong size, regenerate
		log.Printf("  Regenerating: %s (wrong size: %d vs %d)", filepath.Base(filename), info.Size(), size)
		os.Remove(filename)
	}

	// Generate new file
	log.Printf("  Generating: %s (%s)", filepath.Base(filename), formatBytes(size))

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	// Generate random data in chunks to avoid memory issues with large files
	const chunkSize = 1024 * 1024 // 1MB chunks
	buf := make([]byte, chunkSize)
	remaining := size

	for remaining > 0 {
		toWrite := chunkSize
		if remaining < int64(chunkSize) {
			toWrite = int(remaining)
		}

		if _, err := rand.Read(buf[:toWrite]); err != nil {
			return fmt.Errorf("failed to generate random data: %w", err)
		}

		if _, err := f.Write(buf[:toWrite]); err != nil {
			return fmt.Errorf("failed to write data: %w", err)
		}

		remaining -= int64(toWrite)
	}

	return nil
}

// formatBytes formats bytes for human-readable output
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
