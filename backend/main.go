package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/api"
	"github.com/openeverest/plugin-bench/backend/internal/coordinator"
	"github.com/openeverest/plugin-bench/backend/internal/everest"
)

// The frontend bundle is copied here by the Docker build. CI creates a
// placeholder so the backend can be checked before the frontend is built.
//
//go:embed dist/main.js
var distFS embed.FS

//go:embed dist/icon.png
var iconData []byte

const shutdownTimeout = 30 * time.Second

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}

func handleBundle(w http.ResponseWriter, _ *http.Request) {
	bundle, err := distFS.ReadFile("dist/main.js")
	if err != nil {
		http.Error(w, "frontend bundle not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(bundle)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ready",
		"database":    "postgresql",
		"runnerReady": true,
		"storage":     "ephemeral",
		"message":     "Benchmark runs execute as Kubernetes Jobs.",
	})
}

func handleIcon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(iconData)
}

func coordinatorConfigFromEnv() (coordinator.Config, error) {
	executionTimeout, err := time.ParseDuration(envOrDefault("BENCHMARK_EXECUTION_TIMEOUT", "5m"))
	if err != nil {
		return coordinator.Config{}, fmt.Errorf("parse BENCHMARK_EXECUTION_TIMEOUT: %w", err)
	}
	return coordinator.Config{
		WorkloadNamespace:    os.Getenv("WORKLOAD_NAMESPACE"),
		RunnerImage:          os.Getenv("RUNNER_IMAGE"),
		ImagePullPolicy:      envOrDefault("RUNNER_IMAGE_PULL_POLICY", "IfNotPresent"),
		RunnerServiceAccount: os.Getenv("RUNNER_SERVICE_ACCOUNT_NAME"),
		ExecutionTimeout:     executionTimeout,
		Resources: coordinator.Resources{
			CPURequest:    os.Getenv("RUNNER_CPU_REQUEST"),
			MemoryRequest: os.Getenv("RUNNER_MEMORY_REQUEST"),
			CPULimit:      os.Getenv("RUNNER_CPU_LIMIT"),
			MemoryLimit:   os.Getenv("RUNNER_MEMORY_LIMIT"),
		},
	}, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func run() error {
	config, err := coordinatorConfigFromEnv()
	if err != nil {
		return err
	}
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid benchmark coordinator configuration: %w", err)
	}
	benchmarkCoordinator, err := coordinator.NewInCluster(config)
	if err != nil {
		return fmt.Errorf("initialize benchmark coordinator: %w", err)
	}

	lifecycleCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	benchmarkAPI := api.NewAPI(everest.GetCredentials, everest.CheckInstanceAccess, benchmarkCoordinator, api.NewRunStore(), lifecycleCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /main.js", handleBundle)
	mux.HandleFunc("GET /icon.png", handleIcon)
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /api/status", handleStatus)
	benchmarkAPI.RegisterRoutes(mux)

	port := envOrDefault("PORT", "8080")
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("PORT must be a number: %w", err)
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("plugin-bench backend listening on :%s", port)
		serverErrors <- server.ListenAndServe()
	}()

	var serveErr error
	select {
	case <-lifecycleCtx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = fmt.Errorf("serve HTTP: %w", err)
			stop()
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var shutdownErr error
	if err := server.Shutdown(shutdownCtx); err != nil {
		shutdownErr = fmt.Errorf("shut down HTTP server: %w", err)
	}
	var waitErr error
	if err := benchmarkAPI.Shutdown(shutdownCtx); err != nil {
		waitErr = fmt.Errorf("wait for benchmark runs to finish: %w", err)
	}
	return errors.Join(serveErr, shutdownErr, waitErr)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
