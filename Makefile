.PHONY: all build build-api build-agent test clean demo start-poc

all: build

build: build-api build-agent

build-api:
	go build -o bin/sandbox-api ./cmd/sandbox-api

build-agent:
	go build -o bin/vm-agent ./cmd/vm-agent

test:
	go test ./...

clean:
	rm -rf bin/

# Run the demo (simulates Firecracker sandbox without KVM)
demo: build
	@echo "Starting sandbox API server..."
	./bin/sandbox-api &
	@sleep 1
	@echo ""
	@echo "=== Creating a sandbox ==="
	curl -s -X POST http://localhost:8080/sandboxes \
		-H "Content-Type: application/json" \
		-d '{"language":"node","vcpus":2,"mem_size_mib":512}' | jq .
	@echo ""
	@echo "=== Demo complete. Kill sandbox-api with: pkill sandbox-api ==="

# Setup host networking (requires root)
setup-network:
	sudo ./scripts/setup-network.sh

# Teardown host networking (requires root)
teardown-network:
	sudo ./scripts/teardown-network.sh

# Interactively generate an ext4 rootfs for Go or Java (requires root)
generate-rootfs:
	sudo ./rootfs/generate-rootfs.sh

# Non-interactive shorthand targets
rootfs-go:
	sudo ./rootfs/generate-rootfs.sh --language go

rootfs-java:
	sudo ./rootfs/generate-rootfs.sh --language java

rootfs-node:
	sudo ./rootfs/generate-rootfs.sh --language node

rootfs-python:
	sudo ./rootfs/generate-rootfs.sh --language python

rootfs-universal:
	sudo ./rootfs/generate-rootfs.sh --language universal

# One-click POC startup (downloads kernel + firecracker, builds, generates rootfs, starts API)
# Requires: Linux + KVM, Go 1.22+, root
# Usage: sudo make start-poc LANGUAGE=node
start-poc:
	sudo ./scripts/start-poc.sh --language $(or $(LANGUAGE),go)
