// Package network manages TAP devices and Linux bridge configuration
// for Firecracker MicroVM networking.
package network

import (
	"fmt"
	"net"
	"os/exec"
	"sync"
)

const (
	DefaultBridge  = "br0"
	DefaultSubnet  = "10.0.0.0/24"
	DefaultGateway = "10.0.0.1"
)

// Manager handles TAP device creation and IP allocation for MicroVMs.
type Manager struct {
	bridge    string
	subnet    string
	gateway   string
	nextIP    byte // next available last octet
	allocated map[string]string // tapName -> IP
	mu        sync.Mutex
}

// NewManager creates a new network manager.
func NewManager(bridge, subnet, gateway string) *Manager {
	if bridge == "" {
		bridge = DefaultBridge
	}
	if subnet == "" {
		subnet = DefaultSubnet
	}
	if gateway == "" {
		gateway = DefaultGateway
	}
	return &Manager{
		bridge:    bridge,
		subnet:    subnet,
		gateway:   gateway,
		nextIP:    2, // .1 is the gateway
		allocated: make(map[string]string),
	}
}

// TAPDevice represents an allocated TAP interface for a VM.
type TAPDevice struct {
	Name    string `json:"name"`
	IP      string `json:"ip"`
	MAC     string `json:"mac"`
	Gateway string `json:"gateway"`
}

// AllocateTAP creates a new TAP device and assigns it an IP.
// In production, this runs `ip` commands. For POC, it simulates allocation.
func (m *Manager) AllocateTAP(sandboxID string) (*TAPDevice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.nextIP > 254 {
		return nil, fmt.Errorf("no more IPs available in subnet %s", m.subnet)
	}

	tapName := fmt.Sprintf("tap%d", m.nextIP-2)
	ip := fmt.Sprintf("10.0.0.%d", m.nextIP)
	mac := fmt.Sprintf("AA:FC:00:00:00:%02X", m.nextIP)

	m.allocated[tapName] = ip
	m.nextIP++

	return &TAPDevice{
		Name:    tapName,
		IP:      ip,
		MAC:     mac,
		Gateway: m.gateway,
	}, nil
}

// CreateTAP actually creates the TAP device on the host (requires root).
func (m *Manager) CreateTAP(tap *TAPDevice) error {
	commands := [][]string{
		{"ip", "tuntap", "add", tap.Name, "mode", "tap"},
		{"ip", "link", "set", tap.Name, "master", m.bridge},
		{"ip", "link", "set", tap.Name, "up"},
	}

	for _, cmd := range commands {
		if err := exec.Command(cmd[0], cmd[1:]...).Run(); err != nil {
			return fmt.Errorf("failed to run %v: %w", cmd, err)
		}
	}
	return nil
}

// DestroyTAP removes a TAP device from the host.
func (m *Manager) DestroyTAP(tapName string) error {
	m.mu.Lock()
	delete(m.allocated, tapName)
	m.mu.Unlock()

	return exec.Command("ip", "link", "del", tapName).Run()
}

// GetAllocatedDevices returns all currently allocated TAP devices.
func (m *Manager) GetAllocatedDevices() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make(map[string]string, len(m.allocated))
	for k, v := range m.allocated {
		result[k] = v
	}
	return result
}

// GenerateMAC generates a deterministic MAC address for a VM.
func GenerateMAC(vmIndex int) net.HardwareAddr {
	return net.HardwareAddr{
		0xAA, 0xFC, 0x00, 0x00,
		byte(vmIndex >> 8), byte(vmIndex & 0xFF),
	}
}
