// Sandbox API Server — the management plane that wraps Firecracker
// and exposes the Sandbox interface over HTTP/WebSocket.
//
// This is the central component of the Firecracker sandbox platform.
// It manages VM lifecycle, proxies exec/file operations to the in-VM agent,
// handles snapshot/resume, and manages TAP networking.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"syscall"
	"time"

	"github.com/linzhicong0/MicroVMs/pkg/firecracker"
	"github.com/linzhicong0/MicroVMs/pkg/network"
	"github.com/linzhicong0/MicroVMs/pkg/snapshot"
)

// SandboxInstance represents a running or paused MicroVM sandbox.
type SandboxInstance struct {
	ID           string             `json:"id"`
	Language     string             `json:"language"`
	Status       string             `json:"status"` // running, paused, stopped
	VCPUs        int                `json:"vcpus"`
	MemSizeMiB   int                `json:"mem_size_mib"`
	TAP          *network.TAPDevice `json:"tap"`
	CreatedAt    time.Time          `json:"created_at"`
	NetworkGroup string             `json:"network_group,omitempty"`
	EnvVars      map[string]string  `json:"env_vars,omitempty"`
	WorkDir      string             `json:"work_dir"`
	// KVM mode management fields (not serialized to JSON)
	process    *os.Process // Firecracker process handle
	socketPath string      // Firecracker Unix socket path
	agentURL   string      // In-VM agent base URL (http://{ip}:9090)
	rootfsPath string      // Per-VM rootfs image copy path
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
	// KVM / Firecracker mode (all empty = simulation mode)
	firecrackerBin string // path to firecracker binary
	kernelImage    string // path to guest kernel (vmlinux.bin)
	rootfsImage    string // path to base rootfs ext4 image
	socketDir      string // directory for Firecracker Unix sockets
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

	// KVM / Firecracker mode configuration
	firecrackerBin := os.Getenv("FIRECRACKER_BIN")
	kernelImage := os.Getenv("KERNEL_IMAGE")
	rootfsImage := os.Getenv("ROOTFS_IMAGE")
	socketDir := os.Getenv("SOCKET_DIR")
	if socketDir == "" {
		socketDir = "/tmp/firecracker-sockets"
	}

	if firecrackerBin != "" {
		if kernelImage == "" {
			log.Fatal("KERNEL_IMAGE must be set when FIRECRACKER_BIN is set")
		}
		if rootfsImage == "" {
			log.Fatal("ROOTFS_IMAGE must be set when FIRECRACKER_BIN is set")
		}
		if err := os.MkdirAll(socketDir, 0755); err != nil {
			log.Fatalf("Failed to create socket dir: %v", err)
		}
	}

	snapStore, err := snapshot.NewStore(snapshotDir)
	if err != nil {
		log.Fatalf("Failed to create snapshot store: %v", err)
	}

	srv := &Server{
		sandboxes:      make(map[string]*SandboxInstance),
		netManager:     network.NewManager("", "", ""),
		snapStore:      snapStore,
		port:           port,
		baseDomain:     baseDomain,
		firecrackerBin: firecrackerBin,
		kernelImage:    kernelImage,
		rootfsImage:    rootfsImage,
		socketDir:      socketDir,
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
	if firecrackerBin != "" {
		log.Printf("   Mode: KVM (Firecracker)")
		log.Printf("   Firecracker binary: %s", firecrackerBin)
		log.Printf("   Kernel image: %s", kernelImage)
		log.Printf("   Rootfs image: %s", rootfsImage)
		log.Printf("   Socket dir: %s", socketDir)
	} else {
		log.Printf("   Mode: simulation (set FIRECRACKER_BIN to enable KVM)")
	}

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
	sandboxID := generateID(8)
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

	// In KVM mode, start a real Firecracker VM.
	if s.firecrackerBin != "" {
		if err := s.startFirecrackerVM(r.Context(), instance); err != nil {
			_ = s.netManager.DestroyTAP(tap.Name)
			_ = os.RemoveAll(workDir)
			httpError(w, http.StatusInternalServerError, "start VM: %v", err)
			return
		}
	}

	s.mu.Lock()
	s.sandboxes[sandboxID] = instance
	s.mu.Unlock()

	log.Printf("Created sandbox %s [%s] on %s (%s)", sandboxID, req.Language, tap.Name, tap.IP)

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

	// In KVM mode: stop Firecracker process and clean up resources.
	if sb.process != nil {
		_ = sb.process.Kill()
	}
	if sb.socketPath != "" {
		_ = os.Remove(sb.socketPath)
	}
	if sb.rootfsPath != "" {
		_ = os.Remove(sb.rootfsPath)
	}
	if sb.TAP != nil {
		_ = s.netManager.DestroyTAP(sb.TAP.Name)
	}

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

	// In KVM mode, forward the command to the in-VM agent.
	if sb.agentURL != "" {
		log.Printf("[%s] exec (KVM): %s (cwd: %s)", id, req.Cmd, req.Cwd)
		body, err := json.Marshal(req)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "marshal request: %v", err)
			return
		}
		agentResp, err := http.Post(sb.agentURL+"/exec", "application/json", bytes.NewReader(body))
		if err != nil {
			httpError(w, http.StatusBadGateway, "agent unreachable: %v", err)
			return
		}
		defer agentResp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(agentResp.StatusCode)
		if _, err := io.Copy(w, agentResp.Body); err != nil {
			log.Printf("[%s] exec: copy agent response: %v", id, err)
		}
		return
	}

	// Simulation mode: echo back the command.
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

	// In KVM mode, forward to in-VM agent.
	if sb.agentURL != "" {
		agentResp, err := http.Get(sb.agentURL + "/files/" + path)
		if err != nil {
			httpError(w, http.StatusBadGateway, "agent unreachable: %v", err)
			return
		}
		defer agentResp.Body.Close()
		w.Header().Set("Content-Type", agentResp.Header.Get("Content-Type"))
		w.WriteHeader(agentResp.StatusCode)
		if _, err := io.Copy(w, agentResp.Body); err != nil {
			log.Printf("[%s] readfile: copy agent response: %v", id, err)
		}
		return
	}

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

	// In KVM mode, forward to in-VM agent.
	if sb.agentURL != "" {
		body, err := json.Marshal(req)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "marshal request: %v", err)
			return
		}
		httpReq, err := http.NewRequest(http.MethodPut, sb.agentURL+"/files/"+path, bytes.NewReader(body))
		if err != nil {
			httpError(w, http.StatusInternalServerError, "build agent request: %v", err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		agentResp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			httpError(w, http.StatusBadGateway, "agent unreachable: %v", err)
			return
		}
		defer agentResp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(agentResp.StatusCode)
		if _, err := io.Copy(w, agentResp.Body); err != nil {
			log.Printf("[%s] writefile: copy agent response: %v", id, err)
		}
		return
	}

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

	// In KVM mode, forward to in-VM agent.
	if sb.agentURL != "" {
		agentResp, err := http.Get(sb.agentURL + "/readdir/" + path)
		if err != nil {
			httpError(w, http.StatusBadGateway, "agent unreachable: %v", err)
			return
		}
		defer agentResp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(agentResp.StatusCode)
		if _, err := io.Copy(w, agentResp.Body); err != nil {
			log.Printf("[%s] readdir: copy agent response: %v", id, err)
		}
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

// startFirecrackerVM launches a Firecracker process, configures the VM via the
// Firecracker REST API, boots it, and waits for the in-VM agent to be ready.
//
// instance must not be visible to other goroutines yet (i.e. not yet added to
// s.sandboxes), so its fields can be set without holding s.mu.
func (s *Server) startFirecrackerVM(ctx context.Context, instance *SandboxInstance) error {
	tap := instance.TAP

	// Create the TAP device on the host (requires root / CAP_NET_ADMIN).
	if err := s.netManager.CreateTAP(tap); err != nil {
		return fmt.Errorf("create TAP device: %w", err)
	}

	// Create a per-VM copy of the base rootfs image.
	rootfsPath := fmt.Sprintf("%s/%s.ext4", instance.WorkDir, instance.ID)
	if err := copyFile(s.rootfsImage, rootfsPath); err != nil {
		return fmt.Errorf("copy rootfs: %w", err)
	}
	instance.rootfsPath = rootfsPath

	// Start the Firecracker process.
	socketPath := fmt.Sprintf("%s/%s.sock", s.socketDir, instance.ID)
	instance.socketPath = socketPath

	logFile, err := os.Create(fmt.Sprintf("%s/%s.log", s.socketDir, instance.ID))
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	defer logFile.Close()

	// #nosec G204 -- firecrackerBin is operator-supplied via FIRECRACKER_BIN env var
	cmd := exec.CommandContext(ctx, s.firecrackerBin,
		"--api-sock", socketPath,
		"--id", instance.ID,
		"--log-level", "Info",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start firecracker process: %w", err)
	}
	instance.process = cmd.Process

	// cleanup kills the VM on any subsequent error so we don't leak processes.
	cleanup := func() { _ = cmd.Process.Kill() }

	// Wait up to 5 s for the Unix socket to appear.
	if err := waitForSocket(socketPath, 5*time.Second); err != nil {
		cleanup()
		return fmt.Errorf("firecracker socket not ready: %w", err)
	}

	// Configure the VM via the Firecracker REST API.
	fc := firecracker.NewClient(socketPath)

	if err := fc.SetMachineConfig(ctx, firecracker.MachineConfig{
		VCPUCount:  instance.VCPUs,
		MemSizeMiB: instance.MemSizeMiB,
	}); err != nil {
		cleanup()
		return fmt.Errorf("set machine config: %w", err)
	}

	// Boot args: serial console + static IP via kernel cmdline.
	bootArgs := fmt.Sprintf(
		"console=ttyS0 reboot=k panic=1 pci=off nomodules ro "+
			"ip=%s::10.0.0.1:255.255.255.0::eth0:off",
		tap.IP,
	)
	if err := fc.SetBootSource(ctx, firecracker.BootSource{
		KernelImagePath: s.kernelImage,
		BootArgs:        bootArgs,
	}); err != nil {
		cleanup()
		return fmt.Errorf("set boot source: %w", err)
	}

	if err := fc.AddDrive(ctx, firecracker.Drive{
		DriveID:      "rootfs",
		PathOnHost:   rootfsPath,
		IsRootDevice: true,
		IsReadOnly:   false,
	}); err != nil {
		cleanup()
		return fmt.Errorf("add rootfs drive: %w", err)
	}

	if err := fc.AddNetworkInterface(ctx, firecracker.NetworkInterface{
		IfaceID:     "eth0",
		HostDevName: tap.Name,
		GuestMAC:    tap.MAC,
	}); err != nil {
		cleanup()
		return fmt.Errorf("add network interface: %w", err)
	}

	if err := fc.StartVM(ctx); err != nil {
		cleanup()
		return fmt.Errorf("start VM: %w", err)
	}

	// Wait up to 30 s for the in-VM agent (listening on port 9090) to become ready.
	agentURL := fmt.Sprintf("http://%s:9090", tap.IP)
	instance.agentURL = agentURL
	if err := waitForAgent(agentURL, 30*time.Second); err != nil {
		cleanup()
		return fmt.Errorf("in-VM agent not ready: %w", err)
	}

	log.Printf("[%s] VM booted -- agent at %s", instance.ID, agentURL)
	return nil
}

// waitForSocket polls until the Firecracker Unix socket exists or the timeout expires.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for socket %s", path)
}

// waitForAgent polls the agent's /health endpoint until it responds OK or the timeout expires.
func waitForAgent(agentURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 1 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(agentURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for agent at %s", agentURL)
}

// copyFile copies src to dst, creating any necessary parent directories.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

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

// generateID returns a random hex string of the given length.
func generateID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails.
		return fmt.Sprintf("%x", time.Now().UnixNano())[:n]
	}
	return hex.EncodeToString(b)[:n]
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}
