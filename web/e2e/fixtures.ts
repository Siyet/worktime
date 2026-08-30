// Test fixture: every test gets its own WorkTime server with a fresh SQLite
// database, so tests are fully isolated and can run in parallel.
import { test as base } from "@playwright/test";
import { type ChildProcess, spawn } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

export { expect } from "@playwright/test";

const binaryName = process.platform === "win32" ? "worktime.exe" : "worktime";
const binaryPath = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../bin", binaryName);

async function findFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.listen(0, "127.0.0.1", () => {
      const address = probe.address();
      if (typeof address === "object" && address !== null) {
        const port = address.port;
        probe.close(() => resolve(port));
      } else {
        probe.close(() => reject(new Error("no port")));
      }
    });
  });
}

async function waitForHealthz(url: string, timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url + "/healthz");
      if (response.ok) return;
    } catch {
      // Server not up yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`server did not become healthy at ${url}`);
}

export interface WorktimeServer {
  url: string;
  /** Environment overrides the server was started with. */
  env: Record<string, string>;
}

interface ServerOptions {
  devAuth: boolean;
  /** Extra environment for the server process (agent thresholds, for example). */
  env?: Record<string, string>;
}

async function launchServer(options: ServerOptions): Promise<{ server: WorktimeServer; child: ChildProcess; dataDir: string }> {
  const port = await findFreePort();
  const dataDir = mkdtempSync(path.join(tmpdir(), "wt-e2e-"));
  const env: Record<string, string> = {
    WORKTIME_ADDR: `127.0.0.1:${port}`,
    WORKTIME_DB: path.join(dataDir, "e2e.db"),
    WORKTIME_DEV_AUTH: options.devAuth ? "1" : "",
    ...options.env,
  };
  const child = spawn(binaryPath, [], { env: { ...process.env, ...env }, stdio: "ignore" });
  const url = `http://127.0.0.1:${port}`;
  await waitForHealthz(url);
  return { server: { url, env }, child, dataDir };
}

interface Fixtures {
  /** Running server with dev auth enabled and an empty database. */
  server: WorktimeServer;
  /** Running server WITHOUT dev auth (for sign-in screen tests). */
  serverNoAuth: WorktimeServer;
  /** Server for agent-session tests: the reconciliation job runs every second. */
  agentServer: WorktimeServer;
  /**
   * Same, but a session goes stale after three seconds of silence, so the
   * reconciliation path can be observed inside a test timeout. Kept separate:
   * with that grace period no running row survives long enough to be clicked.
   */
  agentServerStale: WorktimeServer;
}

export const test = base.extend<Fixtures>({
  server: async ({}, use) => {
    const { server, child, dataDir } = await launchServer({ devAuth: true });
    await use(server);
    child.kill();
    rmSync(dataDir, { recursive: true, force: true, maxRetries: 3 });
  },
  serverNoAuth: async ({}, use) => {
    const { server, child, dataDir } = await launchServer({ devAuth: false });
    await use(server);
    child.kill();
    rmSync(dataDir, { recursive: true, force: true, maxRetries: 3 });
  },
  agentServer: async ({}, use) => {
    const { server, child, dataDir } = await launchServer({
      devAuth: true,
      env: { WORKTIME_AGENT_RECONCILE: "1s" },
    });
    await use(server);
    child.kill();
    rmSync(dataDir, { recursive: true, force: true, maxRetries: 3 });
  },
  agentServerStale: async ({}, use) => {
    const { server, child, dataDir } = await launchServer({
      devAuth: true,
      env: { WORKTIME_AGENT_GRACE: "3s", WORKTIME_AGENT_RECONCILE: "1s" },
    });
    await use(server);
    child.kill();
    rmSync(dataDir, { recursive: true, force: true, maxRetries: 3 });
  },
});

/** Forces an immediate sync attempt by re-firing the browser online event. */
export async function triggerSync(page: import("@playwright/test").Page): Promise<void> {
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
}

/**
 * Deterministic push barrier. The header can read "synced" for a few ms after a
 * local mutation (pending count refresh is fire-and-forget while the push waits
 * behind a debounce), so waiting for the status text races. Instead, register
 * this BEFORE the mutation and await the actual /api/sync response whose
 * request body contains the pushed change.
 */
export function pushBarrier(
  page: import("@playwright/test").Page,
  bodyMatch: string,
): Promise<unknown> {
  return page.waitForResponse(
    (response) =>
      response.url().includes("/api/sync") &&
      response.ok() &&
      (response.request().postData() ?? "").includes(bodyMatch),
    { timeout: 15_000 },
  );
}

interface SeedEntry {
  description: string;
  startedAt: number;
  stoppedAt: number | null;
  projectID?: string;
  tags?: string[];
}

/** Seeds data directly through the server sync endpoint (dev-auth instance). */
export async function seedServer(
  serverURL: string,
  data: {
    projects?: { id: string; name: string; color?: string; archived?: boolean }[];
    entries?: SeedEntry[];
    timeOff?: { kind: "sick" | "vacation" | "dayoff"; dateFrom: string; dateTo: string }[];
  },
): Promise<void> {
  const now = Date.now();
  const changes = {
    projects: (data.projects ?? []).map((project) => ({
      id: project.id,
      name: project.name,
      color: project.color ?? "#2563eb",
      archived: project.archived ?? false,
      created_at: now,
      updated_at: now,
      deleted_at: null,
    })),
    time_entries: (data.entries ?? []).map((entry) => ({
      id: crypto.randomUUID(),
      project_id: entry.projectID ?? null,
      description: entry.description,
      tags: entry.tags ?? [],
      started_at: entry.startedAt,
      stopped_at: entry.stoppedAt,
      created_at: entry.startedAt,
      updated_at: entry.startedAt,
      deleted_at: null,
    })),
    time_off: (data.timeOff ?? []).map((timeOff) => ({
      id: crypto.randomUUID(),
      kind: timeOff.kind,
      date_from: timeOff.dateFrom,
      date_to: timeOff.dateTo,
      note: "",
      created_at: now,
      updated_at: now,
      deleted_at: null,
    })),
  };
  const response = await fetch(serverURL + "/api/sync", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ since: Number.MAX_SAFE_INTEGER, changes }),
  });
  if (!response.ok) {
    throw new Error(`seed failed: ${response.status} ${await response.text()}`);
  }
}
