/**
 * Demo script — Shows how to use the FirecrackerSandbox client.
 * Run: npx tsx demo.ts (after starting the sandbox API server)
 */

import { FirecrackerSandbox, connectSandbox } from "./sandbox";

async function main() {
  console.log("🔥 Firecracker Sandbox POC Demo\n");

  // Method 1: Direct instantiation
  console.log("=== Creating sandbox via FirecrackerSandbox.create() ===");
  const sandbox = await FirecrackerSandbox.create("http://localhost:8080", {
    language: "node",
    vcpus: 2,
    memSizeMib: 512,
  });
  console.log(`  Sandbox ID: ${sandbox.id}`);
  console.log(`  Domain (3000): ${sandbox.domain(3000)}`);

  // Execute a command
  console.log("\n=== Executing command ===");
  const result = await sandbox.exec("echo 'Hello from Firecracker MicroVM!'");
  console.log(`  stdout: ${result.stdout}`);
  console.log(`  exit_code: ${result.exit_code}`);

  // Write a file
  console.log("\n=== Writing file ===");
  await sandbox.writeFile("hello.txt", "Hello from the sandbox!");
  console.log("  Written: hello.txt");

  // Read the file back
  console.log("\n=== Reading file ===");
  const content = await sandbox.readFile("hello.txt");
  console.log(`  Content: ${content}`);

  // Snapshot
  console.log("\n=== Creating snapshot ===");
  const snap = await sandbox.snapshot();
  console.log(`  Snapshot ID: ${snap.snapshot_id}`);

  // Method 2: Factory function (Open-Agents compatible)
  console.log("\n=== Using connectSandbox() factory ===");
  const sandbox2 = await connectSandbox({
    state: {
      type: "firecracker",
      language: "python",
      networkGroup: "my-project",
    },
    options: {
      mgmtUrl: "http://localhost:8080",
      vcpus: 4,
      memSizeMib: 1024,
    },
  });
  console.log(`  Sandbox2 ID: ${sandbox2.id}`);

  // Clean up
  console.log("\n=== Stopping sandboxes ===");
  await sandbox.stop();
  await sandbox2.stop();
  console.log("  Done!");
}

main().catch(console.error);
