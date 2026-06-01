/**
 * FirecrackerSandbox — TypeScript client adapter that implements the
 * Open-Agents Sandbox interface by talking to our management plane API.
 *
 * This is a drop-in replacement for VercelSandbox. The agent runtime
 * code doesn't need any changes — just swap the adapter.
 */

export interface ExecResult {
  stdout: string;
  stderr: string;
  exit_code: number;
}

export interface FileInfo {
  name: string;
  is_dir: boolean;
  size: number;
}

export interface SnapshotResult {
  snapshot_id: string;
}

export interface SandboxOptions {
  language?: "node" | "java" | "python" | "go" | "rust" | "ruby" | "golf" | "universal";
  vcpus?: number;
  memSizeMib?: number;
  networkGroup?: string;
  envVars?: Record<string, string>;
  snapshotId?: string;
}

/**
 * FirecrackerSandbox implements the Sandbox interface used by Open-Agents.
 * It communicates with the Sandbox API Server (management plane) over HTTP.
 */
export class FirecrackerSandbox {
  readonly type = "cloud" as const;
  readonly workingDirectory = "/workspace";

  private sandboxId: string | null = null;

  constructor(
    private readonly mgmtUrl: string,
    private readonly options: SandboxOptions = {}
  ) {}

  /**
   * Create a new sandbox or connect to an existing one.
   */
  static async create(mgmtUrl: string, options: SandboxOptions = {}): Promise<FirecrackerSandbox> {
    const sandbox = new FirecrackerSandbox(mgmtUrl, options);
    await sandbox.init();
    return sandbox;
  }

  /**
   * Connect to an existing sandbox by ID.
   */
  static async connect(mgmtUrl: string, sandboxId: string): Promise<FirecrackerSandbox> {
    const sandbox = new FirecrackerSandbox(mgmtUrl);
    sandbox.sandboxId = sandboxId;
    // Verify the sandbox exists
    const resp = await fetch(`${mgmtUrl}/sandboxes/${sandboxId}`);
    if (!resp.ok) {
      throw new Error(`Sandbox ${sandboxId} not found`);
    }
    return sandbox;
  }

  private async init(): Promise<void> {
    const resp = await fetch(`${this.mgmtUrl}/sandboxes`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        language: this.options.language || "universal",
        vcpus: this.options.vcpus || 2,
        mem_size_mib: this.options.memSizeMib || 512,
        network_group: this.options.networkGroup,
        env_vars: this.options.envVars,
        snapshot_id: this.options.snapshotId,
      }),
    });

    if (!resp.ok) {
      const err = await resp.json();
      throw new Error(`Failed to create sandbox: ${err.error}`);
    }

    const data = await resp.json();
    this.sandboxId = data.id;
  }

  get id(): string {
    if (!this.sandboxId) throw new Error("Sandbox not initialized");
    return this.sandboxId;
  }

  /**
   * Execute a shell command in the sandbox.
   */
  async exec(cmd: string, cwd?: string, timeoutMs?: number): Promise<ExecResult> {
    const resp = await fetch(`${this.mgmtUrl}/sandboxes/${this.id}/exec`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ cmd, cwd, timeout_ms: timeoutMs }),
    });

    if (!resp.ok) {
      const err = await resp.json();
      throw new Error(`Exec failed: ${err.error}`);
    }

    return resp.json();
  }

  /**
   * Read a file from the sandbox filesystem.
   */
  async readFile(path: string, encoding: "utf-8" | "base64" = "utf-8"): Promise<string> {
    const resp = await fetch(`${this.mgmtUrl}/sandboxes/${this.id}/files/${path}`);
    if (!resp.ok) {
      throw new Error(`File not found: ${path}`);
    }

    if (encoding === "base64") {
      const buffer = await resp.arrayBuffer();
      return Buffer.from(buffer).toString("base64");
    }
    return resp.text();
  }

  /**
   * Write content to a file in the sandbox filesystem.
   */
  async writeFile(path: string, content: string): Promise<void> {
    const resp = await fetch(`${this.mgmtUrl}/sandboxes/${this.id}/files/${path}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content }),
    });

    if (!resp.ok) {
      const err = await resp.json();
      throw new Error(`Write failed: ${err.error}`);
    }
  }

  /**
   * List directory contents.
   */
  async readdir(path: string): Promise<FileInfo[]> {
    const resp = await fetch(`${this.mgmtUrl}/sandboxes/${this.id}/readdir/${path}`);
    if (!resp.ok) {
      throw new Error(`Directory not found: ${path}`);
    }
    return resp.json();
  }

  /**
   * Create a snapshot of the sandbox state.
   */
  async snapshot(): Promise<SnapshotResult> {
    const resp = await fetch(`${this.mgmtUrl}/sandboxes/${this.id}/snapshot`, {
      method: "POST",
    });

    if (!resp.ok) {
      const err = await resp.json();
      throw new Error(`Snapshot failed: ${err.error}`);
    }

    return resp.json();
  }

  /**
   * Stop and destroy the sandbox.
   */
  async stop(): Promise<void> {
    await fetch(`${this.mgmtUrl}/sandboxes/${this.id}`, {
      method: "DELETE",
    });
  }

  /**
   * Get the public URL for a port exposed by this sandbox.
   */
  domain(port: number): string {
    // In production, this would be a real domain with TLS
    // e.g., https://{sandboxId}-{port}.sandbox.yourdomain.com
    return `http://${this.id}-${port}.sandbox.localhost`;
  }
}

// ─── Factory Integration ────────────────────────────────────────

export type SandboxState =
  | { type: "vercel"; /* ... vercel state */ }
  | { type: "firecracker"; sandboxId?: string; snapshotId?: string; language?: string; networkGroup?: string };

export interface SandboxConnectConfig {
  state: SandboxState;
  options?: {
    mgmtUrl?: string;
    vcpus?: number;
    memSizeMib?: number;
    envVars?: Record<string, string>;
  };
}

/**
 * connectSandbox — Factory function that dispatches to the appropriate
 * sandbox implementation based on state.type.
 */
export async function connectSandbox(config: SandboxConnectConfig): Promise<FirecrackerSandbox> {
  const { state, options } = config;

  if (state.type === "firecracker") {
    const mgmtUrl = options?.mgmtUrl || "http://localhost:8080";

    if (state.sandboxId) {
      return FirecrackerSandbox.connect(mgmtUrl, state.sandboxId);
    }

    return FirecrackerSandbox.create(mgmtUrl, {
      language: (state.language as SandboxOptions["language"]) || "universal",
      vcpus: options?.vcpus,
      memSizeMib: options?.memSizeMib,
      networkGroup: state.networkGroup,
      envVars: options?.envVars,
      snapshotId: state.snapshotId,
    });
  }

  throw new Error(`Unsupported sandbox type: ${state.type}`);
}
