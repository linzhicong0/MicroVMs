// Package snapshot manages VM snapshot storage and retrieval.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store manages snapshot files on disk (or S3 in production).
type Store struct {
	basePath string
}

// NewStore creates a snapshot store at the given path.
func NewStore(basePath string) (*Store, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}
	return &Store{basePath: basePath}, nil
}

// Metadata holds information about a stored snapshot.
type Metadata struct {
	SnapshotID  string    `json:"snapshot_id"`
	SandboxID   string    `json:"sandbox_id"`
	Language    string    `json:"language"`
	CreatedAt   time.Time `json:"created_at"`
	MemFilePath string    `json:"mem_file_path"`
	SnapFilePath string   `json:"snap_file_path"`
	DiskPaths   []string  `json:"disk_paths"`
}

// Save stores snapshot metadata. In production, this would also
// upload the actual memory/disk files to S3.
func (s *Store) Save(meta Metadata) error {
	dir := filepath.Join(s.basePath, meta.SnapshotID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0644)
}

// Load retrieves snapshot metadata by ID.
func (s *Store) Load(snapshotID string) (*Metadata, error) {
	path := filepath.Join(s.basePath, snapshotID, "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s not found: %w", snapshotID, err)
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// List returns all stored snapshots.
func (s *Store) List() ([]Metadata, error) {
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		return nil, err
	}

	var snapshots []Metadata
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := s.Load(entry.Name())
		if err != nil {
			continue
		}
		snapshots = append(snapshots, *meta)
	}
	return snapshots, nil
}

// Delete removes a snapshot from the store.
func (s *Store) Delete(snapshotID string) error {
	dir := filepath.Join(s.basePath, snapshotID)
	return os.RemoveAll(dir)
}
