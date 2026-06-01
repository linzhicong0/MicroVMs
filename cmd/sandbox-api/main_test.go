package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/linzhicong0/MicroVMs/pkg/network"
	"github.com/linzhicong0/MicroVMs/pkg/snapshot"
)

func setupTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	snapStore, err := snapshot.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		sandboxes:  make(map[string]*SandboxInstance),
		netManager: network.NewManager("", "", ""),
		snapStore:  snapStore,
		port:       "8080",
		baseDomain: "sandbox.localhost",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sandboxes", srv.handleCreateSandbox)
	mux.HandleFunc("GET /sandboxes", srv.handleListSandboxes)
	mux.HandleFunc("GET /sandboxes/{id}", srv.handleGetSandbox)
	mux.HandleFunc("DELETE /sandboxes/{id}", srv.handleStopSandbox)
	mux.HandleFunc("POST /sandboxes/{id}/exec", srv.handleExec)
	mux.HandleFunc("GET /sandboxes/{id}/files/{path...}", srv.handleReadFile)
	mux.HandleFunc("PUT /sandboxes/{id}/files/{path...}", srv.handleWriteFile)
	mux.HandleFunc("POST /sandboxes/{id}/snapshot", srv.handleSnapshot)
	mux.HandleFunc("GET /health", srv.handleHealth)

	return httptest.NewServer(mux)
}

func TestCreateAndExec(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()

	// Create sandbox
	body := `{"language":"node","vcpus":2,"mem_size_mib":512}`
	resp, err := http.Post(ts.URL+"/sandboxes", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var sandbox map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&sandbox)

	sandboxID, ok := sandbox["id"].(string)
	if !ok || sandboxID == "" {
		t.Fatal("sandbox ID not returned")
	}

	// Exec command
	execBody := `{"cmd":"echo hello","cwd":"/workspace"}`
	resp2, err := http.Post(
		fmt.Sprintf("%s/sandboxes/%s/exec", ts.URL, sandboxID),
		"application/json",
		bytes.NewBufferString(execBody),
	)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("exec expected 200, got %d", resp2.StatusCode)
	}

	var execResult map[string]interface{}
	json.NewDecoder(resp2.Body).Decode(&execResult)

	if execResult["exit_code"].(float64) != 0 {
		t.Fatalf("expected exit code 0, got %v", execResult["exit_code"])
	}

	t.Logf("Sandbox %s exec result: %s", sandboxID, execResult["stdout"])
}

func TestHealth(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var health map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&health)

	if health["status"] != "healthy" {
		t.Fatalf("expected healthy, got %v", health["status"])
	}
}
