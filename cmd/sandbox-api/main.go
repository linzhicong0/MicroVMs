// Sandbox API Server — the management plane that wraps Firecracker
// and exposes the Sandbox interface over HTTP/WebSocket.
//
// This is the central component of the Firecracker sandbox platform.
// It manages VM lifecycle, proxies exec/file operations to the in-VM agent,
// handles snapshot/resume, and manages TAP networking.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/linzhicong0/MicroVMs/pkg/network"
	"github.com/linzhicong0/MicroVMs/pkg/snapshot"
)

// SandboxInstance represents a running or paused MicroVM sandbox.
type SandboxInstance struct {
	ID          string            `json:"id"`
	Language    string            `json:"language"`
	Status      string            `json:"status"` // running, paused, stopped
	VCPUs       int               `json:"vcpus"`
	MemSizeMiB  int               `json:"mem_size_mib"`
	TAP         *network.TAPDevice `json:"tap"`
	CreatedAt   time.Time         `json:"created_at"`
	NetworkGroup string           `json:"network_group,omitempty"`
	EnvVars     map[string]string `json:"env_vars,omitempty"`
	WorkDir     string            `json:"work_dir"`
}

// CreateSandboxRequest is the request body for creating a new sandbox.
type CreateSandboxRequest struct {
	Language     string            `json:"language"`
	VCPUs        int               `json:"vcpus,omitempty"`
	MemSizeMiB   int               `json:"mem_size_mib,omitempty"`
	NetworkGroup string            `json:"network_group,omitempty"`
	EnvVars      map[string]string `json:"env_vars,omitempty"`
	SnapshotID   string            `json:"snapshot_id,omitempty"`
}

// ExecRequest is the request body for executing a command.
type ExecRequest struct {
	Cmd       string `json:"cmd"`
	Cwd       string `json:"cwd,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

// ExecResponse is the response from command execution.
type ExecResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// WriteFileRequest is the request body for writing a file.
type WriteFileRequest struct {
	Content string `json:"content"`
}

// Server is the Sandbox API management plane server.
type Server struct {
	sandboxes    map[string]*SandboxInstance
	mu           sync.RWMutex
	netManager   *network.Manager
	snapStore    *snapshot.Store
	port         string
	baseDomain   string
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	baseDomain := os.Getenv("BASE_DOMAIN")
	if baseDomain == "" {
		baseDomain = "sandbox.localhost"
	}

	snapshotDir := os.Getenv("SNAPSHOT_DIR")
	if snapshotDir == "" {
		snapshotDir = "/tmp/firecracker-snapshots"
	}

	snapStore, err := snapshot.NewStore(snapshotDir)
	if err != nil {
		log.Fatalf("Failed to create snapshot store: %v", err)
	}

	srv := &Server{
		sandboxes:  make(map[string]*SandboxInstance),
		netManager: network.NewManager("", "", ""),
		snapStore:  snapStore,
		port:       port,
		baseDomain: baseDomain,
	}

	mux := http.NewServeMux()

	// Sandbox CRUD
	mux.HandleFunc("POST /sandboxes", srv.handleCreateSandbox)
	mux.HandleFunc("GET /sandboxes", srv.handleListSandboxes)
	mux.HandleFunc("GET /sandboxes/{id}", srv.handleGetSandbox)
	mux.HandleFunc("DELETE /sandboxes/{id}", srv.handleStopSandbox)

	// Exec
	mux.HandleFunc("POST /sandboxes/{id}/exec", srv.handleExec)

	// File operations
	mux.HandleFunc("GET /sandboxes/{id}/files/{path...}", srv.handleReadFile)
	mux.HandleFunc("PUT /sandboxes/{id}/files/{path...}", srv.handleWriteFile)
	mux.HandleFunc("GET /sandboxes/{id}/readdir/{path...}", srv.handleReadDir)

	// Snapshot
	mux.HandleFunc("POST /sandboxes/{id}/snapshot", srv.handleSnapshot)

	// Health
	mux.HandleFunc("GET /health", srv.handleHealth)

	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(ctx)
	}()

	log.Printf("🔥 Sandbox API Server listening on :%s", port)
	log.Printf("   Base domain: %s", baseDomain)
	log.Printf("   Snapshot dir: %s", snapshotDir)

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

func (s *Server) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	var req CreateSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	// Defaults
	if req.VCPUs == 0 {
		req.VCPUs = 2
	}
	if req.MemSizeMiB == 0 {
		req.MemSizeMiB = 512
	}
	if req.Language == "" {
		req.Language = "universal"
	}

	// Allocate network
	sandboxID := uuid.New().String()[:8]
	tap, err := s.netManager.AllocateTAP(sandboxID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "allocate network: %v", err)
		return
	}

	// Create workspace directory (simulates workspace block device)
	workDir := fmt.Sprintf("/tmp/sandbox-workspaces/%s", sandboxID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		httpError(w, http.StatusInternalServerError, "create workspace: %v", err)
		return
	}

	instance := &SandboxInstance{
		ID:           sandboxID,
		Language:     req.Language,
		Status:       "running",
		VCPUs:        req.VCPUs,
		MemSizeMiB:   req.MemSizeMiB,
		TAP:          tap,
		CreatedAt:    time.Now(),
		NetworkGroup: req.NetworkGroup,
		EnvVars:      req.EnvVars,
		WorkDir:      workDir,
	}

	s.mu.Lock()
	s.sandboxes[sandboxID] = instance
	s.mu.Unlock()

	log.Printf("Created sandbox %s [%s] on %s (%s)", sandboxID, req.Language, tap.Name, tap.IP)

	// In production, this would:
	// 1. Create TAP device on host
	// 2. Start Firecracker process with Unix socket
	// 3. Configure VM via Firecracker API (machine-config, boot-source, drives, network, vsock)
	// 4. Inject metadata via MMDS
	// 5. Start the VM
	// 6. Wait for in-VM agent to report ready

	jsonResponse(w, http.StatusCreated, instance)
}

func (s *Server) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sandboxes := make([]*SandboxInstance, 0, len(s.sandboxes))
	for _, sb := range s.sandboxes {
		sandboxes = append(sandboxes, sb)
	}
	jsonResponse(w, http.StatusOK, sandboxes)
}

func (s *Server) handleGetSandbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sb, ok := s.getSandbox(id)
	if !ok {
		httpError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}
	jsonResponse(w, http.StatusOK, sb)
}

func (s *Server) handleStopSandbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mu.Lock()
	sb, ok := s.sandboxes[id]
	if !ok {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}
	sb.Status = "stopped"
	delete(s.sandboxes, id)
	s.mu.Unlock()

	// In production: stop Firecracker process, destroy TAP device, clean up workspace
	log.Printf("Stopped sandbox %s", id)
	jsonResponse(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sb, ok := s.getSandbox(id)
	if !ok {
		httpError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}

	if sb.Status != "running" {
		httpError(w, http.StatusConflict, "sandbox is %s, not running", sb.Status)
		return
	}

	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request: %v", err)
		return
	}

	// In production, this would forward the command to the in-VM agent
	// via vsock connection. For the POC, we simulate execution.
	log.Printf("[%s] exec: %s (cwd: %s)", id, req.Cmd, req.Cwd)

	// POC simulation: execute locally in the workspace directory
	resp := &ExecResponse{
		Stdout:   fmt.Sprintf("[sandbox %s] simulated output of: %s\n", id, req.Cmd),
		Stderr:   "",
		ExitCode: 0,
	}

	jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := r.PathValue("path")

	sb, ok := s.getSandbox(id)
	if !ok {
		httpError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}

	// In production: forward to in-VM agent via vsock
	fullPath := fmt.Sprintf("%s/%s", sb.WorkDir, path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		httpError(w, http.StatusNotFound, "file not found: %s", path)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(content)
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := r.PathValue("path")

	sb, ok := s.getSandbox(id)
	if !ok {
		httpError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}

	var req WriteFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request: %v", err)
		return
	}

	// In production: forward to in-VM agent via vsock
	fullPath := fmt.Sprintf("%s/%s", sb.WorkDir, path)
	if err := os.MkdirAll(fmt.Sprintf("%s/%s", sb.WorkDir, dirOf(path)), 0755); err != nil {
		httpError(w, http.StatusInternalServerError, "create parent dir: %v", err)
		return
	}

	if err := os.WriteFile(fullPath, []byte(req.Content), 0644); err != nil {
		httpError(w, http.StatusInternalServerError, "write file: %v", err)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadDir(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := r.PathValue("path")

	sb, ok := s.getSandbox(id)
	if !ok {
		httpError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}

	fullPath := fmt.Sprintf("%s/%s", sb.WorkDir, path)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		httpError(w, http.StatusNotFound, "directory not found: %s", path)
		return
	}

	type DirEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
	}

	result := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, DirEntry{Name: e.Name(), IsDir: e.IsDir()})
	}

	jsonResponse(w, http.StatusOK, result)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	sb, ok := s.getSandbox(id)
	if !ok {
		httpError(w, http.StatusNotFound, "sandbox %s not found", id)
		return
	}

	snapshotID := fmt.Sprintf("snap-%s-%d", id, time.Now().Unix())

	// In production:
	// 1. Pause the VM: PUT /vm {state: "Paused"}
	// 2. Create snapshot: PUT /snapshot/create {snapshot_path, mem_file_path}
	// 3. Upload files to S3/NFS
	// 4. Store metadata

	meta := snapshot.Metadata{
		SnapshotID:   snapshotID,
		SandboxID:    id,
		Language:     sb.Language,
		CreatedAt:    time.Now(),
		MemFilePath:  fmt.Sprintf("/tmp/firecracker-snapshots/%s/mem", snapshotID),
		SnapFilePath: fmt.Sprintf("/tmp/firecracker-snapshots/%s/vmstate", snapshotID),
	}

	if err := s.snapStore.Save(meta); err != nil {
		httpError(w, http.StatusInternalServerError, "save snapshot: %v", err)
		return
	}

	sb.Status = "paused"
	log.Printf("[%s] snapshot created: %s", id, snapshotID)

	jsonResponse(w, http.StatusOK, map[string]string{
		"snapshot_id": snapshotID,
		"status":      "paused",
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	count := len(s.sandboxes)
	s.mu.RUnlock()

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status":          "healthy",
		"active_sandboxes": count,
		"version":         "0.1.0-poc",
	})
}

// Helper functions

func (s *Server) getSandbox(id string) (*SandboxInstance, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sb, ok := s.sandboxes[id]
	return sb, ok
}

func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func httpError(w http.ResponseWriter, status int, format string, args ...interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": fmt.Sprintf(format, args...),
	})
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}
