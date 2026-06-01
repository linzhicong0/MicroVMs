// Package firecracker provides a client for interacting with the Firecracker
// REST API via its Unix domain socket.
package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
)

// Client communicates with a Firecracker process via its Unix socket API.
type Client struct {
	socketPath string
	httpClient *http.Client
}

// NewClient creates a new Firecracker API client.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

// MachineConfig represents the VM's CPU and memory configuration.
type MachineConfig struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	SMT        bool `json:"smt,omitempty"`
}

// BootSource defines the kernel and boot arguments.
type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args,omitempty"`
}

// Drive represents a block device attached to the VM.
type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

// NetworkInterface represents a network device attached to the VM.
type NetworkInterface struct {
	IfaceID     string       `json:"iface_id"`
	HostDevName string       `json:"host_dev_name"`
	GuestMAC    string       `json:"guest_mac,omitempty"`
	RxRateLimit *RateLimiter `json:"rx_rate_limiter,omitempty"`
	TxRateLimit *RateLimiter `json:"tx_rate_limiter,omitempty"`
}

// RateLimiter defines bandwidth limits using a token bucket.
type RateLimiter struct {
	Bandwidth *TokenBucket `json:"bandwidth,omitempty"`
}

// TokenBucket defines rate limiting parameters.
type TokenBucket struct {
	Size       int64 `json:"size"`
	RefillTime int64 `json:"refill_time"`
}

// VsockConfig defines the virtio-vsock device.
type VsockConfig struct {
	GuestCID int    `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

// SnapshotCreate defines the snapshot creation request.
type SnapshotCreate struct {
	SnapshotType string `json:"snapshot_type"`
	SnapshotPath string `json:"snapshot_path"`
	MemFilePath  string `json:"mem_file_path"`
}

// SnapshotLoad defines the snapshot load request.
type SnapshotLoad struct {
	SnapshotPath       string `json:"snapshot_path"`
	MemBackend         MemBackend `json:"mem_backend"`
	EnableDiffSnapshots bool   `json:"enable_diff_snapshots,omitempty"`
	ResumeVM           bool   `json:"resume_vm,omitempty"`
}

// MemBackend defines the memory backend for snapshot loading.
type MemBackend struct {
	BackendType string `json:"backend_type"`
	BackendPath string `json:"backend_path"`
}

// Action represents a VM action (start, pause, resume).
type Action struct {
	ActionType string `json:"action_type"`
}

// SetMachineConfig configures the VM's CPU and memory.
func (c *Client) SetMachineConfig(ctx context.Context, cfg MachineConfig) error {
	return c.put(ctx, "/machine-config", cfg)
}

// SetBootSource sets the kernel image and boot arguments.
func (c *Client) SetBootSource(ctx context.Context, src BootSource) error {
	return c.put(ctx, "/boot-source", src)
}

// AddDrive attaches a block device to the VM.
func (c *Client) AddDrive(ctx context.Context, drive Drive) error {
	return c.put(ctx, fmt.Sprintf("/drives/%s", drive.DriveID), drive)
}

// AddNetworkInterface attaches a network interface to the VM.
func (c *Client) AddNetworkInterface(ctx context.Context, iface NetworkInterface) error {
	return c.put(ctx, fmt.Sprintf("/network-interfaces/%s", iface.IfaceID), iface)
}

// SetVsock configures the virtio-vsock device.
func (c *Client) SetVsock(ctx context.Context, vsock VsockConfig) error {
	return c.put(ctx, "/vsock", vsock)
}

// StartVM boots the MicroVM.
func (c *Client) StartVM(ctx context.Context) error {
	return c.put(ctx, "/actions", Action{ActionType: "InstanceStart"})
}

// PauseVM pauses the MicroVM.
func (c *Client) PauseVM(ctx context.Context) error {
	return c.put(ctx, "/vm", map[string]string{"state": "Paused"})
}

// ResumeVM resumes a paused MicroVM.
func (c *Client) ResumeVM(ctx context.Context) error {
	return c.put(ctx, "/vm", map[string]string{"state": "Resumed"})
}

// CreateSnapshot creates a full VM snapshot.
func (c *Client) CreateSnapshot(ctx context.Context, snap SnapshotCreate) error {
	return c.put(ctx, "/snapshot/create", snap)
}

// LoadSnapshot restores a VM from a snapshot.
func (c *Client) LoadSnapshot(ctx context.Context, snap SnapshotLoad) error {
	return c.put(ctx, "/snapshot/load", snap)
}

// SetMMDS sets the MMDS metadata for the VM.
func (c *Client) SetMMDS(ctx context.Context, data interface{}) error {
	return c.put(ctx, "/mmds", data)
}

func (c *Client) put(ctx context.Context, path string, body interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost"+path, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errResp map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			return fmt.Errorf("firecracker API error %d (could not decode response)", resp.StatusCode)
		}
		msg := errResp["fault_message"]
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("firecracker API error %d: %s", resp.StatusCode, msg)
	}

	return nil
}
