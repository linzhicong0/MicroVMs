# 🔥 Firecracker Sandbox Platform — POC

A proof-of-concept implementation of the **Firecracker Sandbox Architecture** for [Open-Agents](https://github.com/vercel-labs/open-agents). This project demonstrates a self-hosted, polyglot, microservice-capable MicroVM platform that serves as a drop-in replacement for the Vercel cloud sandbox.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│  Tier 1: Agent Runtime (unchanged from Open-Agents)             │
│  ┌──────────┐  ┌───────────┐  ┌────────────────┐               │
│  │ Agent    │  │ Tool      │  │ Sandbox        │               │
│  │ Loop     │──│ Dispatcher│──│ Interface      │               │
│  └──────────┘  └───────────┘  └───────┬────────┘               │
└───────────────────────────────────────┼─────────────────────────┘
                                        │ HTTP / WebSocket
┌───────────────────────────────────────┼─────────────────────────┐
│  Tier 2: Sandbox Management Plane     │                         │
│  ┌────────────┐ ┌─────────┐ ┌────────┴───┐ ┌───────┐          │
│  │ Sandbox    │ │ Session │ │ Snapshot   │ │ Port  │          │
│  │ API Server │ │ Registry│ │ Store      │ │ Proxy │          │
│  └──────┬─────┘ └─────────┘ └────────────┘ └───────┘          │
└─────────┼───────────────────────────────────────────────────────┘
          │ Firecracker REST API (Unix socket)
┌─────────┼───────────────────────────────────────────────────────┐
│  Tier 3: Firecracker MicroVMs (KVM)                             │
│  ┌──────┴──┐  ┌─────────┐  ┌─────────┐  ┌─────────┐          │
│  │ VM:Node │  │ VM:Java │  │VM:Python│  │VM:Go    │          │
│  │ tap0    │  │ tap1    │  │ tap2    │  │ tap3    │          │
│  │10.0.0.2 │  │10.0.0.3 │  │10.0.0.4 │  │10.0.0.5 │          │
│  └────┬────┘  └────┬────┘  └────┬────┘  └────┬────┘          │
│       └─────────────┴────────────┴─────────────┘               │
│                    Linux Bridge (br0)                            │
│                    10.0.0.1/24                                   │
└─────────────────────────────────────────────────────────────────┘
```

## Project Structure

```
MicroVMs/
├── cmd/
│   ├── sandbox-api/          # Management plane HTTP server
│   │   └── main.go
│   └── vm-agent/             # In-VM agent (exec/file proxy)
│       └── main.go
├── pkg/
│   ├── firecracker/          # Firecracker REST API client
│   │   └── client.go
│   ├── network/              # TAP device & bridge manager
│   │   └── manager.go
│   ├── sandbox/              # Sandbox interface definition
│   │   └── interface.go
│   └── snapshot/             # Snapshot storage
│       └── store.go
├── client/
│   └── typescript/           # TypeScript client (Open-Agents adapter)
│       ├── sandbox.ts        # FirecrackerSandbox class
│       ├── demo.ts           # Usage demo
│       └── package.json
├── scripts/
│   ├── setup-network.sh      # Host bridge & NAT setup
│   ├── teardown-network.sh   # Network cleanup
│   └── create-tap.sh         # Per-VM TAP creation
├── rootfs/
│   └── build-rootfs.sh       # Language rootfs builder
├── demo/
│   ├── docker-compose.yml    # Docker-based demo (no KVM needed)
│   ├── Dockerfile.api        # API server container
│   ├── Dockerfile.agent      # VM agent container
│   └── demo.sh              # End-to-end demo script
├── go.mod
├── Makefile
└── firecracker-sandbox-architecture.html  # Full design document
```

## Quick Start

### Option 1: Run locally (without KVM)

The POC includes simulation mode that works without Firecracker/KVM:

```bash
# Build
make build

# Start the sandbox API server
./bin/sandbox-api

# In another terminal, run the demo
chmod +x demo/demo.sh
./demo/demo.sh
```

### Option 2: Docker Compose demo

Simulates the full architecture using containers:

```bash
cd demo
docker-compose up --build

# In another terminal
./demo.sh
```

### Option 3: Full Firecracker (requires KVM host)

```bash
# 1. Build the binaries first
make build

# 2. Setup host networking (requires root)
sudo ./scripts/setup-network.sh

# 3. Build a rootfs image for your language
sudo ./rootfs/build-rootfs.sh node rootfs/node.ext4

# 4. Set required environment variables and start the management plane
#    FIRECRACKER_BIN  – path to the firecracker binary
#                       (download from https://github.com/firecracker-microvm/firecracker/releases)
#    KERNEL_IMAGE     – path to your vmlinux.bin guest kernel
#    ROOTFS_IMAGE     – path to the rootfs ext4 image built in step 3
export FIRECRACKER_BIN=/usr/local/bin/firecracker
export KERNEL_IMAGE=/path/to/vmlinux.bin
export ROOTFS_IMAGE=rootfs/node.ext4

sudo -E ./bin/sandbox-api

# 5. The API will manage real Firecracker VMs via KVM.
#    Each POST /sandboxes will spawn a Firecracker process, configure and boot a
#    MicroVM, and wait for the in-VM agent to become ready.
```

> **Note:** Firecracker requires KVM access.  Running `sandbox-api` as root (or a user
> in the `kvm` group with `CAP_NET_ADMIN`) is necessary for TAP device creation.
>
> Optional env vars:
> - `SOCKET_DIR` – directory for Firecracker Unix sockets (default `/tmp/firecracker-sockets`)
> - `PORT` – HTTP port for the management API (default `8080`)
> - `SNAPSHOT_DIR` – directory for VM snapshots (default `/tmp/firecracker-snapshots`)

## API Reference

### Sandbox Lifecycle

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/sandboxes` | Create a new sandbox |
| `GET` | `/sandboxes` | List all sandboxes |
| `GET` | `/sandboxes/:id` | Get sandbox details |
| `DELETE` | `/sandboxes/:id` | Stop and destroy a sandbox |

### Command Execution

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/sandboxes/:id/exec` | Execute a shell command |

### File Operations

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/sandboxes/:id/files/:path` | Read a file |
| `PUT` | `/sandboxes/:id/files/:path` | Write a file |
| `GET` | `/sandboxes/:id/readdir/:path` | List directory |

### Snapshot

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/sandboxes/:id/snapshot` | Create VM snapshot |

### Example: Create & Use a Sandbox

```bash
# Create a Node.js sandbox
curl -X POST http://localhost:8080/sandboxes \
  -H "Content-Type: application/json" \
  -d '{
    "language": "node",
    "vcpus": 2,
    "mem_size_mib": 512,
    "network_group": "my-project",
    "env_vars": {"NODE_ENV": "development"}
  }'

# Execute a command
curl -X POST http://localhost:8080/sandboxes/{id}/exec \
  -H "Content-Type: application/json" \
  -d '{"cmd": "node -e \"console.log(42)\"", "cwd": "/workspace"}'

# Write a file
curl -X PUT http://localhost:8080/sandboxes/{id}/files/app.js \
  -H "Content-Type: application/json" \
  -d '{"content": "console.log(\"Hello!\");"}'

# Snapshot the sandbox
curl -X POST http://localhost:8080/sandboxes/{id}/snapshot
```

## TypeScript Client (Open-Agents Compatible)

```typescript
import { FirecrackerSandbox, connectSandbox } from "@microvms/sandbox-client";

// Direct usage
const sandbox = await FirecrackerSandbox.create("http://localhost:8080", {
  language: "node",
  vcpus: 2,
  memSizeMib: 512,
});

const result = await sandbox.exec("npm install && npm start");
console.log(result.stdout);

// Or via the factory (Open-Agents compatible)
const sandbox2 = await connectSandbox({
  state: { type: "firecracker", language: "python" },
  options: { mgmtUrl: "http://localhost:8080" },
});
```

## Key Design Decisions

1. **Vsock for host↔VM communication** — Zero network overhead, works even without TAP networking configured. The in-VM agent listens on vsock CID 3, port 52.

2. **TAP + Linux Bridge for VM↔VM** — Each VM gets a private IP on a shared /24 subnet. Microservices in different VMs can call each other directly.

3. **MMDS for metadata injection** — Environment variables, tokens, and sandbox config are injected via Firecracker's MMDS (MicroVM Metadata Service) without rebuilding images.

4. **Overlayfs for rootfs** — Language base images are read-only; each sandbox gets a thin read-write overlay. Creates new sandboxes instantly.

5. **Snapshot/Resume** — Full VM state (memory + disk) is snapshotted via Firecracker's native API. Resume time is <150ms.

## Components Explained

### Sandbox API Server (`cmd/sandbox-api`)
The management plane. Accepts HTTP requests from the agent runtime, manages Firecracker VM lifecycle, allocates network resources, and proxies operations to in-VM agents.

### VM Agent (`cmd/vm-agent`)
Runs inside every MicroVM. Receives commands from the management plane via vsock (HTTP in POC mode), executes shell commands, and handles file operations in the guest OS.

### Firecracker Client (`pkg/firecracker`)
Go client for Firecracker's REST API (served over a Unix domain socket). Handles VM configuration, boot, snapshot, and network setup.

### Network Manager (`pkg/network`)
Manages TAP device allocation, IP assignment, and Linux bridge configuration for inter-VM networking.

### Snapshot Store (`pkg/snapshot`)
Manages VM snapshot metadata and files. In production, uploads to S3/NFS for persistent storage.

### TypeScript Client (`client/typescript`)
Drop-in replacement for `@vercel/sandbox`. Implements the same `Sandbox` interface so the Open-Agents runtime code requires zero changes.

## Production Deployment

For a production deployment on a KVM-enabled host:

1. **Hardware**: Any x86_64 machine with KVM support (bare metal or EC2 `.metal` instances)
2. **Firecracker**: Download from [firecracker-microvm/firecracker](https://github.com/firecracker-microvm/firecracker)
3. **Kernel**: Use a minimal guest kernel (vmlinux) — Firecracker provides reference builds
4. **TLS**: Use Caddy with wildcard certs for `*.sandbox.yourdomain.com`
5. **Storage**: S3 or NFS for snapshot persistence

## License

MIT
