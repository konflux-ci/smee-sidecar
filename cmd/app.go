package main

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/prometheus/client_golang/prometheus"
)

//go:embed scripts/check-smee-health.sh
var smeeHealthScript []byte

//go:embed scripts/check-sidecar-health.sh
var sidecarHealthScript []byte

//go:embed scripts/check-file-age.sh
var fileAgeScript []byte

// HealthStatus represents the current health status
type HealthStatus struct {
	Status  string // "success" or "failure"
	Message string
}

var (
	forwardAttempts = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "smee_events_relayed_total",
			Help: "Total number of regular events relayed by the sidecar.",
		},
	)
	// Gauge metric to track the health check status.
	health_check = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "health_check",
			Help: "Indicates the outcome of the last completed health check (1 for OK, 0 for failure).",
		},
	)
	// The mutex protects a map where the KEY is the test ID
	// and the VALUE is a channel that the handler will wait on.
	healthChecks = make(map[string]chan bool)
	mutex        = &sync.Mutex{}
	// Global downstream service URL for per-request proxy creation
	downstreamServiceURL string

	// Shared HTTP clients to prevent resource accumulation
	healthCheckClient *http.Client
	proxyInstance     *httputil.ReverseProxy

	// Thread-safe initialization
	healthCheckOnce sync.Once
	proxyOnce       sync.Once
	proxyError      error
)

type HealthCheckPayload struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// createOptimizedTransport creates a transport with proper resource limits
func createOptimizedTransport() *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: "true" == os.Getenv("INSECURE_SKIP_VERIFY"),
		},
		DisableKeepAlives:     false,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
		MaxConnsPerHost:       10,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
	}
}

// getHealthCheckClient returns the shared health check client, creating it lazily if needed
func getHealthCheckClient() *http.Client {
	healthCheckOnce.Do(func() {
		healthCheckClient = &http.Client{
			Transport: createOptimizedTransport(),
			Timeout:   30 * time.Second,
		}
	})
	return healthCheckClient
}

// getProxyInstance returns the shared proxy instance, creating it lazily if needed
func getProxyInstance() (*httputil.ReverseProxy, error) {
	proxyOnce.Do(func() {
		parsedURL, err := url.Parse(downstreamServiceURL)
		if err != nil {
			proxyError = fmt.Errorf("could not parse downstream URL %s: %v", downstreamServiceURL, err)
			return
		}
		proxyInstance = httputil.NewSingleHostReverseProxy(parsedURL)
		proxyInstance.Transport = createOptimizedTransport()
	})
	return proxyInstance, proxyError
}

// forwardHandler needs to find the correct channel to signal success.
func forwardHandler(w http.ResponseWriter, r *http.Request) {
	// Check for health check header first (fast path)
	if healthCheckID := r.Header.Get("X-Health-Check-ID"); healthCheckID != "" {
		// Always drain request body to prevent connection reuse issues
		_, _ = io.Copy(io.Discard, r.Body)

		// Force connection closure for health checks to prevent connection pooling
		w.Header().Set("Connection", "close")

		mutex.Lock()
		resultChan, exists := healthChecks[healthCheckID]
		mutex.Unlock()

		if exists {
			// Signal that we received the health check event
			select {
			case resultChan <- true:
			default:
				// Channel is full or closed, ignore
			}
		}

		w.WriteHeader(http.StatusOK)
		return
	}

	// Forward real webhook events directly - no need to read body into memory

	// Use the shared proxy instance
	proxy, err := getProxyInstance()
	if err != nil {
		http.Error(w, "internal server error: failed to create proxy", http.StatusInternalServerError)
		return
	}

	// Only count actual forwarding attempts (after successful proxy creation)
	forwardAttempts.Inc()
	proxy.ServeHTTP(w, r)
}

// writeScriptsToVolume writes the embedded probe scripts to the shared volume
func writeScriptsToVolume(sharedPath string) error {
	scripts := map[string][]byte{
		"check-smee-health.sh":    smeeHealthScript,
		"check-sidecar-health.sh": sidecarHealthScript,
		"check-file-age.sh":       fileAgeScript,
	}

	for filename, content := range scripts {
		scriptPath := filepath.Join(sharedPath, filename)

		// Check if file exists and make it writable before overwriting
		// This handles container restarts where the volume persists with read-only files
		if _, err := os.Stat(scriptPath); err == nil {
			if err := os.Chmod(scriptPath, 0755); err != nil {
				return fmt.Errorf("failed to make %s writable: %v", filename, err)
			}
		}

		if err := os.WriteFile(scriptPath, content, 0755); err != nil {
			return fmt.Errorf("failed to write %s: %v", filename, err)
		}

		// Make script read-only to prevent accidental modification
		if err := os.Chmod(scriptPath, 0555); err != nil {
			return fmt.Errorf("failed to make %s read-only: %v", filename, err)
		}

		log.Printf("Wrote read-only probe script: %s", scriptPath)
	}
	return nil
}

// writeHealthStatus writes health status to file atomically
func writeHealthStatus(status *HealthStatus, filePath string) error {
	// Simple format with only fields used by probe scripts
	content := fmt.Sprintf("status=%s\nmessage=%s\n",
		status.Status,
		status.Message,
	)

	// Atomic write: write to temp file, then rename
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %v", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("failed to rename temp file: %v", err)
	}

	return nil
}

// performHealthCheck executes a single end-to-end health check
func performHealthCheck(smeeChannelURL string, timeoutSeconds int) *HealthStatus {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	testID := uuid.New().String()
	status := &HealthStatus{
		Status:  "failure",
		Message: "Health check failed",
	}

	payload := HealthCheckPayload{Type: "health-check", ID: testID}
	payloadBytes, _ := json.Marshal(payload)

	// Create a channel for this specific request and register it.
	resultChan := make(chan bool, 1)
	mutex.Lock()
	healthChecks[testID] = resultChan
	mutex.Unlock()

	// Ensure we always clean up the map entry for this ID.
	defer func() {
		mutex.Lock()
		delete(healthChecks, testID)
		mutex.Unlock()
	}()

	// Create and send the POST request.
	req, err := http.NewRequestWithContext(ctx, "POST", smeeChannelURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		status.Message = fmt.Sprintf("Failed to create request: %v", err)
		return status
	}

	// Send health check ID in header for fast detection AND JSON body for server compatibility
	req.Header.Set("X-Health-Check-ID", testID)
	req.Header.Set("Content-Type", "application/json")

	// Ensure connection is closed after use
	req.Close = true

	// Use the shared HTTP client
	client := getHealthCheckClient()

	resp, err := client.Do(req)
	if err != nil {
		status.Message = fmt.Sprintf("Failed to POST to smee server: %v", err)
		return status
	}

	// Always close response body to prevent resource leaks
	defer func() {
		if resp != nil && resp.Body != nil {
			// Drain and close the body to ensure resources are freed
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	// Wait for the forwardHandler to receive the event, or for the timeout.
	select {
	case <-resultChan:
		status.Status = "success"
		status.Message = "Health check completed successfully"
	case <-ctx.Done():
		status.Message = "Health check timed out waiting for event round-trip"
	}

	return status
}

// runHealthChecker runs the background health checker
func runHealthChecker(ctx context.Context, smeeChannelURL, healthFilePath string, intervalSeconds, timeoutSeconds int) {
	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
	defer ticker.Stop()

	log.Printf("Starting background health checker (interval: %ds, timeout: %ds)", intervalSeconds, timeoutSeconds)

	for {
		select {
		case <-ctx.Done():
			log.Println("Health checker stopped")
			return
		case <-ticker.C:
			status := performHealthCheck(smeeChannelURL, timeoutSeconds)

			if err := writeHealthStatus(status, healthFilePath); err != nil {
				log.Printf("Failed to write health status: %v", err)
			} else {
				log.Printf("Health check completed: %s (%s)", status.Status, status.Message)
			}

			// Update Prometheus metric
			if status.Status == "success" {
				health_check.Set(1)
			} else {
				health_check.Set(0)
			}
		}
	}
}
