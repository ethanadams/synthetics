package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethanadams/synthetics/internal/config"
	"github.com/ethanadams/synthetics/internal/executor"
	"github.com/ethanadams/synthetics/internal/logging"
	"github.com/ethanadams/synthetics/internal/metrics"
	"github.com/ethanadams/synthetics/internal/scheduler"
	"github.com/ethanadams/synthetics/internal/testdata"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	// Load configuration
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/config.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize logging level from config
	logging.SetLevel(cfg.Logging.Level)

	log.Printf("Starting Storj Synthetics Monitor")
	log.Printf("Config: bucket=%s, tests=%d",
		cfg.Satellite.Bucket, len(cfg.Tests))

	// Generate test data files for all configured tests
	if err := testdata.EnsureTestDataFiles(cfg); err != nil {
		log.Printf("Warning: failed to ensure test data files: %v", err)
	}

	// Initialize metrics collector
	metricsCollector := metrics.NewCollector()
	log.Printf("Initialized metrics collector")

	// Initialize executors
	executors := make(map[string]executor.TestExecutor)

	// Uplink executor (k6 + xk6-storj)
	uplinkExec := executor.NewUplink(cfg, metricsCollector)
	executors["uplink"] = uplinkExec
	log.Printf("Initialized Uplink executor")

	// S3 executor (AWS SDK)
	if cfg.S3.Endpoint != "" && cfg.S3.AccessKey != "" {
		s3Exec, err := executor.NewS3(cfg, metricsCollector)
		if err != nil {
			log.Printf("Warning: Failed to initialize S3 executor: %v", err)
		} else {
			executors["s3"] = s3Exec
			log.Printf("Initialized S3 executor (endpoint: %s)", cfg.S3.Endpoint)
		}
	} else {
		log.Printf("S3 executor disabled (no credentials configured)")
	}

	// HTTP S3 executor (standard library only, no AWS SDK)
	if cfg.S3.Endpoint != "" && cfg.S3.AccessKey != "" {
		httpS3Exec, err := executor.NewHttpS3(cfg, metricsCollector)
		if err != nil {
			log.Printf("Warning: Failed to initialize HTTP S3 executor: %v", err)
		} else {
			executors["http-s3"] = httpS3Exec
			log.Printf("Initialized HTTP S3 executor (endpoint: %s)", cfg.S3.Endpoint)
		}
	}

	// Curl S3 executor (uses curl subprocess)
	if cfg.S3.Endpoint != "" && cfg.S3.AccessKey != "" {
		curlS3Exec, err := executor.NewCurlS3(cfg, metricsCollector)
		if err != nil {
			log.Printf("Warning: Failed to initialize Curl S3 executor: %v", err)
		} else {
			executors["curl-s3"] = curlS3Exec
			log.Printf("Initialized Curl S3 executor (endpoint: %s)", cfg.S3.Endpoint)
		}
	}

	// Initialize and start scheduler
	sched := scheduler.New(cfg, executors)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		log.Fatalf("Failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Set up HTTP server
	mux := http.NewServeMux()

	// Metrics endpoint for Prometheus
	mux.Handle(cfg.Metrics.Path, promhttp.Handler())

	// Health check endpoint
	mux.HandleFunc("/health", healthHandler)

	// Root handler with info
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "Storj Synthetics Monitor\n\n")
		fmt.Fprintf(w, "Endpoints:\n")
		fmt.Fprintf(w, "  %s - Prometheus metrics\n", cfg.Metrics.Path)
		fmt.Fprintf(w, "  /health - Health check\n")
	})

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Metrics.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start HTTP server in a goroutine
	go func() {
		log.Printf("Starting HTTP server on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start HTTP server: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Received shutdown signal, shutting down gracefully...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("Shutdown complete")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK\n")
}
