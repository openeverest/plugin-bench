package main

import (
	"embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

// The frontend bundle is copied here by the Docker build. CI creates a
// placeholder so the backend can be checked before the frontend is built.
//
//go:embed dist/main.js
var distFS embed.FS

type runSummary struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Profile   string    `json:"profile"`
	CreatedAt time.Time `json:"createdAt"`
}

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
		"status":      "scaffold",
		"database":    "postgresql",
		"runnerReady": false,
		"storage":     "ephemeral",
		"message":     "Benchmark orchestration is not implemented yet.",
	})
}

func handleRuns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"runs": []runSummary{}})
	case http.MethodPost:
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "benchmark execution is not implemented in the scaffold",
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /main.js", handleBundle)
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /api/status", handleStatus)
	mux.HandleFunc("/api/runs", handleRuns)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("plugin-bench backend listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
