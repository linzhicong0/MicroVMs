# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Firecracker Sandbox Platform POC — a self-hosted, polyglot MicroVM platform serving as a drop-in replacement for the Vercel cloud sandbox (Open-Agents). Uses Firecracker MicroVMs with KVM for isolation, TAP networking for inter-VM communication, and vsock for host↔VM control.

## Build & Run Commands

```bash
make build          # Build both sandbox-api and vm-agent binaries into bin/
make build-api      # Build sandbox-api only
make build-agent    # Build vm-agent only
make test           # Run all Go tests
make clean          # Remove bin/
make demo           # Start sandbox-api, create a sandbox via curl, requires pkill to stop
```

Run a single test:
```bash
go test ./cmd/sandbox-api/ -run TestCreateAndExec -v
```

Run with KVM (requires root, Linux host with KVM):
```bash
sudo -E ./bin/sandbox-api  # Needs FIRECRACKER_BIN, KERNEL_IMAGE, ROOTFS_IMAGE env vars
```

Docker demo (no KVM needed):
```bash
cd demo && docker-compose up --build
```

Generate rootfs images:
```bash
make rootfs-go       # Non-interactive: sudo ./rootfs/generate-rootfs.sh --language go
make rootfs-java     # Non-interactive: sudo ./rootfs/generate-rootfs.sh --language java
make generate-rootfs # Interactive prompt
```

## Architecture

Three-tier architecture:

1. **Agent Runtime (external)** — connects to the management plane via HTTP
2. **Sandbox API Server** (`cmd/sandbox-api/`) — the management plane. Creates/destroys MicroVMs, proxies exec/file operations to in-VM agents, manages snapshots. In simulation mode (no `FIRECRACKER_BIN`), it echoes commands; in KVM mode, it spawns Firecracker processes and communicates with in-VM agents over TCP (vsock CID 3 port 52 in production).
3. **Firecracker MicroVMs** — each runs `vm-agent` (`cmd/vm-agent/`) which listens for exec/file/mkdir/stat commands on port 9090.

Key packages:
- `pkg/firecracker/` — Go client for Firecracker's Unix-socket REST API (machine config, boot, drives, network, snapshots, MMDS)
- `pkg/network/` — TAP device allocation, IP assignment (10.0.0.0/24 subnet), Linux bridge management
- `pkg/sandbox/` — `Sandbox` interface definition mirroring the Open-Agents contract (Exec, ReadFile, WriteFile, ReadDir, Mkdir, Stat, Snapshot, Stop)
- `pkg/snapshot/` — On-disk snapshot metadata storage (JSON files per snapshot)
- `client/typescript/` — `FirecrackerSandbox` class implementing the Open-Agents `Sandbox` interface; `connectSandbox()` factory dispatches by `state.type`

## Key Patterns

- **Dual mode**: The sandbox-api runs in simulation mode by default. Set `FIRECRACKER_BIN`, `KERNEL_IMAGE`, and `ROOTFS_IMAGE` env vars to enable real Firecracker/KVM mode. Code checks `sb.agentURL != ""` to decide whether to proxy to in-VM agent or handle locally.
- **Request proxying**: In KVM mode, the sandbox-api forwards exec/file/readdir requests to the in-VM agent at `http://{vm-ip}:9090`. The agent runs `sh -c {cmd}` inside the VM — this is intentional (isolation is the MicroVM boundary, not input validation).
- **Per-VM rootfs copy**: Each sandbox gets its own copy of the base rootfs image (overlayfs in production, file copy in POC).
- **Network**: Each VM gets a TAP device on bridge `br0` with a static IP from the 10.0.0.0/24 pool. MACs are deterministic (`AA:FC:00:00:00:{ip-octet}`).
- **Go 1.22 with `http.NewServeMux` pattern routing**: Routes use `"POST /sandboxes"` and `"GET /sandboxes/{id}"` style patterns with `{path...}` for wildcard segments.

## Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Sandbox API HTTP port |
| `BASE_DOMAIN` | `sandbox.localhost` | Public domain template |
| `SNAPSHOT_DIR` | `/tmp/firecracker-snapshots` | Snapshot storage path |
| `FIRECRACKER_BIN` | (empty = simulation mode) | Path to firecracker binary |
| `KERNEL_IMAGE` | required if FIRECRACKER_BIN set | Path to vmlinux.bin guest kernel |
| `ROOTFS_IMAGE` | required if FIRECRACKER_BIN set | Path to base rootfs ext4 image |
| `SOCKET_DIR` | `/tmp/firecracker-sockets` | Firecracker Unix socket directory |
| `AGENT_PORT` | `9090` | In-VM agent listen port |
| `WORK_DIR` | `/workspace` | In-VM agent workspace directory |

## Dependencies

- Go 1.22, module `github.com/linzhicong0/MicroVMs`
- Only external dependency: `github.com/google/uuid`
- TypeScript client has no runtime dependencies (uses native `fetch`)
