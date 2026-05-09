package main

import (
	"context"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	log.Println("Starting Smee instrumentation sidecar...")

	// Environment variables
	downstreamServiceURL = os.Getenv("DOWNSTREAM_SERVICE_URL")
	if downstreamServiceURL == "" {
		log.Fatal("FATAL: DOWNSTREAM_SERVICE_URL environment variable must be set.")
	}

	smeeChannelURL := os.Getenv("SMEE_CHANNEL_URL")
	if smeeChannelURL == "" {
		log.Fatal("FATAL: SMEE_CHANNEL_URL environment variable must be set.")
	}

	sharedPath := os.Getenv("SHARED_VOLUME_PATH")
	if sharedPath == "" {
		sharedPath = "/shared"
	}

	healthFilePath := os.Getenv("HEALTH_FILE_PATH")
	if healthFilePath == "" {
		healthFilePath = filepath.Join(sharedPath, "health-status.txt")
	}

	// Parse configuration
	healthCheckInterval := 30
	if intervalStr := os.Getenv("HEALTH_CHECK_INTERVAL_SECONDS"); intervalStr != "" {
		if val, err := strconv.Atoi(intervalStr); err == nil && val > 0 {
			healthCheckInterval = val
		}
	}

	healthCheckTimeout := 20
	if timeoutStr := os.Getenv("HEALTH_CHECK_TIMEOUT_SECONDS"); timeoutStr != "" {
		if val, err := strconv.Atoi(timeoutStr); err == nil && val > 0 {
			healthCheckTimeout = val
		}
	}

	// Check if pprof endpoints should be enabled (disabled by default for security)
	enablePprof := "true" == os.Getenv("ENABLE_PPROF")

	// HTTP clients will be initialized lazily when first needed

	// Write probe scripts to shared volume
	if err := writeScriptsToVolume(sharedPath); err != nil {
		log.Fatalf("FATAL: Failed to write probe scripts: %v", err)
	}

	// Register metrics with Prometheus.
	prometheus.MustRegister(forwardAttempts)
	prometheus.MustRegister(health_check)

	// Start background health checker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runHealthChecker(ctx, smeeChannelURL, healthFilePath, healthCheckInterval, healthCheckTimeout)

	// --- Relay Server (on port 8080) ---
	relayMux := http.NewServeMux()
	relayMux.HandleFunc("/", forwardHandler)

	// Configure relay server with timeouts to prevent goroutine leaks
	// while maintaining transparency (timeouts longer than any realistic client)
	relayServer := &http.Server{
		Addr:         ":8080",
		Handler:      relayMux,
		ReadTimeout:  180 * time.Second, // 3 min - longer than any client timeout
		WriteTimeout: 60 * time.Second,  // 1 min - safe response timeout
		IdleTimeout:  600 * time.Second, // 10 min - generous keep-alive cleanup
	}

	go func() {
		log.Printf("Relay server listening on %s with timeouts (read: %.0fs, write: %.0fs, idle: %.0fs)",
			relayServer.Addr,
			relayServer.ReadTimeout.Seconds(),
			relayServer.WriteTimeout.Seconds(),
			relayServer.IdleTimeout.Seconds())
		if err := relayServer.ListenAndServe(); err != nil {
			log.Fatalf("FATAL: Relay server failed: %v", err)
		}
	}()

	// --- Management Server (on port 9100) ---
	mgmtMux := http.NewServeMux()
	mgmtMux.Handle("/metrics", promhttp.Handler())

	// Add pprof endpoints for memory profiling
	if enablePprof {
		log.Println("Enabling pprof endpoints for debugging")
		mgmtMux.HandleFunc("/debug/pprof/", pprof.Index)
		mgmtMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mgmtMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mgmtMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mgmtMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		mgmtMux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
		mgmtMux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
		mgmtMux.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
		mgmtMux.Handle("/debug/pprof/block", pprof.Handler("block"))
		mgmtMux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	} else {
		log.Println("pprof endpoints disabled (set ENABLE_PPROF=true to enable)")
	}

	go func() {
		if enablePprof {
			log.Println("Management server (metrics & pprof) listening on :9100")
		} else {
			log.Println("Management server (metrics) listening on :9100")
		}
		if err := http.ListenAndServe(":9100", mgmtMux); err != nil {
			log.Fatalf("FATAL: Management server failed: %v", err)
		}
	}()

	select {}
}
