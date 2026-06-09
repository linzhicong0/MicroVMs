// Simple Go HTTP API for Firecracker MicroVM POC.
//
// Demonstrates inter-VM microservice communication by calling a Python
// service running in a separate Firecracker VM via the TAP network.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

var pythonURL string

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	pythonURL = os.Getenv("PYTHON_SERVICE_URL")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /call-python", handleCallPython)
	mux.HandleFunc("GET /greeting", handleGreeting)

	log.Printf("[go-service] Listening on :%s (python_url=%s)", port, pythonURL)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{
		"service": "go",
		"message": "Hello from Go!",
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "healthy"})
}

func handleCallPython(w http.ResponseWriter, r *http.Request) {
	if pythonURL == "" {
		writeJSON(w, 500, map[string]string{
			"error": "PYTHON_SERVICE_URL not set",
		})
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(pythonURL)
	if err != nil {
		writeJSON(w, 502, map[string]string{
			"error":   "failed to call python service",
			"details": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSON(w, 502, map[string]string{
			"error":   "failed to read python response",
			"details": err.Error(),
		})
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"service":        "go",
		"python_status":  resp.StatusCode,
		"python_response": json.RawMessage(body),
	})
}

func handleGreeting(w http.ResponseWriter, r *http.Request) {
	if pythonURL == "" {
		writeJSON(w, 500, map[string]string{
			"error": "PYTHON_SERVICE_URL not set",
		})
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(pythonURL + "/hello")
	if err != nil {
		writeJSON(w, 502, map[string]string{
			"error":   "failed to call python /hello",
			"details": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSON(w, 502, map[string]string{
			"error":   "failed to read python response",
			"details": err.Error(),
		})
		return
	}

	var pyResp struct {
		Message string `json:"message"`
	}
	json.Unmarshal(body, &pyResp)

	greeting := fmt.Sprintf("Go says: the Python service told me '%s'", pyResp.Message)
	log.Printf("[go-service] %s", greeting)

	writeJSON(w, 200, map[string]interface{}{
		"service":         "go",
		"greeting":        greeting,
		"python_response": json.RawMessage(body),
	})
}

func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}
