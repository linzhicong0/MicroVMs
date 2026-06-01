// Package sandbox defines the Sandbox interface that mirrors the Open-Agents
// sandbox contract. Any implementation (Firecracker, Docker, mock) must satisfy this.
package sandbox

import "context"

// ExecResult represents the result of executing a command in the sandbox.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// FileInfo represents file/directory metadata.
type FileInfo struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// SnapshotResult contains the ID of a created snapshot.
type SnapshotResult struct {
	SnapshotID string `json:"snapshot_id"`
}

// Sandbox defines the interface that the agent runtime uses to interact
// with an execution environment. This mirrors the Open-Agents Sandbox interface.
type Sandbox interface {
	// Exec runs a shell command inside the sandbox.
	Exec(ctx context.Context, cmd string, cwd string, timeoutMs int) (*ExecResult, error)

	// ReadFile reads a file from the sandbox filesystem.
	ReadFile(ctx context.Context, path string) ([]byte, error)

	// WriteFile writes content to a file in the sandbox filesystem.
	WriteFile(ctx context.Context, path string, content []byte) error

	// ReadDir lists the contents of a directory.
	ReadDir(ctx context.Context, path string) ([]FileInfo, error)

	// Mkdir creates a directory (with parents if needed).
	Mkdir(ctx context.Context, path string) error

	// Stat returns metadata about a file or directory.
	Stat(ctx context.Context, path string) (*FileInfo, error)

	// Snapshot freezes the sandbox state and returns a snapshot ID.
	Snapshot(ctx context.Context) (*SnapshotResult, error)

	// Stop terminates the sandbox.
	Stop(ctx context.Context) error

	// Domain returns the public URL for a given port exposed by the sandbox.
	Domain(port int) string

	// ID returns the unique identifier of this sandbox.
	ID() string
}
