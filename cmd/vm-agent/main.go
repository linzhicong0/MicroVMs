// VM Agent — runs inside every Firecracker MicroVM.
// Listens for commands from the management plane (via vsock in production,
// HTTP in this POC) and executes them in the guest OS.
//
// Responsibilities:
// - Execute shell commands and stream output
// - Read/write files in the guest filesystem
// - Report health/readiness to the management plane
// - Read MMDS metadata for configuration
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultPort    = "9090"
	defaultWorkDir = "/workspace"
)

// ExecRequest represents a command execution request.
type ExecRequest struct {
	Cmd       string `json:"cmd"`
	Cwd       string `json:"cwd,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

// ExecResponse represents the result of command execution.
type ExecResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// WriteFileRequest represents a file write request.
type WriteFileRequest struct {
	Content string `json:"content"`
	Mode    int    `json:"mode,omitempty"`
}

// FileInfo represents file metadata.
type FileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

// Agent is the in-VM agent process.
type Agent struct {
	workDir string
}

func main() {
	port := os.Getenv("AGENT_PORT")
	if port == "" {
		port = defaultPort
	}

	workDir := os.Getenv("WORK_DIR")
	if workDir == "" {
		workDir = defaultWorkDir
	}

	// Ensure workspace exists
	os.MkdirAll(workDir, 0755)

	agent := &Agent{workDir: workDir}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /exec", agent.handleExec)
	mux.HandleFunc("GET /files/{path...}", agent.handleReadFile)
	mux.HandleFunc("PUT /files/{path...}", agent.handleWriteFile)
	mux.HandleFunc("GET /readdir/{path...}", agent.handleReadDir)
	mux.HandleFunc("POST /mkdir/{path...}", agent.handleMkdir)
	mux.HandleFunc("GET /stat/{path...}", agent.handleStat)
	mux.HandleFunc("GET /health", agent.handleHealth)

	log.Printf("🤖 VM Agent listening on :%s (workdir: %s)", port, workDir)

	// In production, this would listen on vsock (CID 3, port 52)
	// instead of TCP. Example:
	//   listener, _ := vsock.Listen(52, nil)
	//   http.Serve(listener, mux)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Agent error: %v", err)
	}
}

func (a *Agent) handleExec(w http.ResponseWriter, r *http.Request) {
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request: %v", err)
		return
	}

	cwd := req.Cwd
	if cwd == "" {
		cwd = a.workDir
	}

	// Execute the command.
	// NOTE: This is intentionally executing user-provided commands — the VM agent
	// runs inside an isolated Firecracker MicroVM with its own kernel, so command
	// execution is the core purpose of this service. The isolation boundary is the
	// MicroVM itself, not input validation. #nosec
	cmd := exec.Command("sh", "-c", req.Cmd) //nolint:gosec
	cmd.Dir = cwd

	// Set environment
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("HOME=%s", a.workDir),
		"TERM=xterm-256color",
	)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
			stderr.WriteString(err.Error())
		}
	}

	resp := ExecResponse{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}

	jsonResponse(w, http.StatusOK, resp)
}

func (a *Agent) handleReadFile(w http.ResponseWriter, r *http.Request) {
	path := a.resolvePath(r.PathValue("path"))

	content, err := os.ReadFile(path)
	if err != nil {
		httpError(w, http.StatusNotFound, "file not found: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(content)
}

func (a *Agent) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	path := a.resolvePath(r.PathValue("path"))

	var req WriteFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request: %v", err)
		return
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		httpError(w, http.StatusInternalServerError, "create parent dir: %v", err)
		return
	}

	mode := os.FileMode(0644)
	if req.Mode != 0 {
		mode = os.FileMode(req.Mode)
	}

	if err := os.WriteFile(path, []byte(req.Content), mode); err != nil {
		httpError(w, http.StatusInternalServerError, "write error: %v", err)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *Agent) handleReadDir(w http.ResponseWriter, r *http.Request) {
	path := a.resolvePath(r.PathValue("path"))

	entries, err := os.ReadDir(path)
	if err != nil {
		httpError(w, http.StatusNotFound, "directory not found: %v", err)
		return
	}

	result := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		info, _ := e.Info()
		fi := FileInfo{
			Name:  e.Name(),
			IsDir: e.IsDir(),
		}
		if info != nil {
			fi.Size = info.Size()
			fi.ModTime = info.ModTime()
		}
		result = append(result, fi)
	}

	jsonResponse(w, http.StatusOK, result)
}

func (a *Agent) handleMkdir(w http.ResponseWriter, r *http.Request) {
	path := a.resolvePath(r.PathValue("path"))

	if err := os.MkdirAll(path, 0755); err != nil {
		httpError(w, http.StatusInternalServerError, "mkdir error: %v", err)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *Agent) handleStat(w http.ResponseWriter, r *http.Request) {
	path := a.resolvePath(r.PathValue("path"))

	info, err := os.Stat(path)
	if err != nil {
		httpError(w, http.StatusNotFound, "not found: %v", err)
		return
	}

	fi := FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		IsDir:   info.IsDir(),
		ModTime: info.ModTime(),
	}

	jsonResponse(w, http.StatusOK, fi)
}

func (a *Agent) handleHealth(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status":   "healthy",
		"work_dir": a.workDir,
		"pid":      os.Getpid(),
		"hostname": getHostname(),
	})
}

func (a *Agent) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(a.workDir, path)
}

func getHostname() string {
	h, _ := os.Hostname()
	return h
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
