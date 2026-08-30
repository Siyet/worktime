export type UpdateState =
  | "idle"
  | "checking"
  | "up_to_date"
  | "available"
  | "applying"
  | "restart_required"
  | "failed";

export type UpdateApplyMode = "automatic" | "notification_only";

export interface SystemVersion {
  version: string;
  revision?: string;
  built_at?: string;
}

export interface SystemUpdateStatus {
  state: UpdateState;
  current_version: string;
  latest_version: string | null;
  update_available: boolean;
  apply_ready: boolean;
  checked_at: number | null;
  changelog_url: string | null;
  auto_apply: boolean;
  can_manage: boolean;
  apply_mode: UpdateApplyMode;
  message: string | null;
}

type Request = typeof fetch;

async function requestJSON<T>(request: Request, path: string, init?: RequestInit): Promise<T> {
  const response = await request(path, init);
  if (!response.ok) throw new Error(`request failed with status ${response.status}`);
  return response.json() as Promise<T>;
}

export function fetchSystemVersion(request: Request = fetch, init?: RequestInit): Promise<SystemVersion> {
  return requestJSON(request, "/api/system/version", init);
}

export function fetchSystemUpdate(request: Request = fetch, init?: RequestInit): Promise<SystemUpdateStatus> {
  return requestJSON(request, "/api/system/update", init);
}

export function checkSystemUpdate(request: Request = fetch): Promise<SystemUpdateStatus> {
  return requestJSON(request, "/api/system/update/check", { method: "POST" });
}

export function setSystemAutoApply(autoApply: boolean, request: Request = fetch): Promise<SystemUpdateStatus> {
  return requestJSON(request, "/api/system/update/policy", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ auto_apply: autoApply }),
  });
}

export function applySystemUpdate(request: Request = fetch): Promise<SystemUpdateStatus> {
  return requestJSON(request, "/api/system/update/apply", { method: "POST" });
}

interface RestartMonitorOptions {
  baselineVersion: string;
  targetVersion: () => string | null;
  signal: AbortSignal;
  request?: Request;
  intervalMs?: number;
  refreshServiceWorker?: () => Promise<void>;
  reload?: () => void;
  onStatus?: (status: SystemUpdateStatus) => void;
}

interface RestartWatchOptions extends RestartMonitorOptions {
  onFailure: (message: string) => void | Promise<void>;
}

export type RestartMonitorResult =
  | { outcome: "updated"; version: string }
  | { outcome: "failed"; message: string };

async function waitForNextPoll(milliseconds: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return;
  await new Promise<void>((resolve) => {
    let settled = false;
    let timer: ReturnType<typeof globalThis.setTimeout>;
    const finish = () => {
      if (settled) return;
      settled = true;
      globalThis.clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    };
    timer = globalThis.setTimeout(finish, milliseconds);
    signal.addEventListener("abort", finish, { once: true });
    if (signal.aborted) finish();
  });
}

export async function refreshUpdateServiceWorker(): Promise<void> {
  if (!("serviceWorker" in navigator)) return;
  const registrations = await navigator.serviceWorker.getRegistrations();
  await Promise.all(registrations.map((registration) => registration.update()));
}

// Keep watching while Settings is open. A manual apply supplies an exact target;
// automatic updates are detected by any version change from the loaded build.
// Transient API failures are expected during replacement; a healthy unchanged
// process plus a settled update status identifies rollback instead of spinning.
export async function monitorSystemRestart(options: RestartMonitorOptions): Promise<RestartMonitorResult | null> {
  const request = options.request ?? fetch;
  const interval = options.intervalMs ?? 2_000;
  let sawUpdateActivity = options.targetVersion() !== null;
  let unchangedRecoveryPolls = 0;
  while (!options.signal.aborted) {
    try {
      const health = await request("/healthz", { cache: "no-store", signal: options.signal });
      if (!health.ok) throw new Error(`health check failed with status ${health.status}`);
      const [version, status] = await Promise.all([
        fetchSystemVersion(request, { cache: "no-store", signal: options.signal }),
        fetchSystemUpdate(request, { cache: "no-store", signal: options.signal }),
      ]);
      options.onStatus?.(status);
      const target = options.targetVersion();
      if (target !== null || status.state === "applying") sawUpdateActivity = true;
      const reached = target ? version.version === target : version.version !== options.baselineVersion;
      if (reached) {
        await (options.refreshServiceWorker ?? refreshUpdateServiceWorker)();
        (options.reload ?? (() => window.location.reload()))();
        return { outcome: "updated", version: version.version };
      }
      if (sawUpdateActivity && status.state === "failed") {
        return { outcome: "failed", message: status.message ?? "The update failed before the running version changed." };
      }
      if (sawUpdateActivity && status.state !== "applying" && status.state !== "checking") {
        unchangedRecoveryPolls++;
        if (unchangedRecoveryPolls >= 2) {
          return { outcome: "failed", message: "The update restart ended without changing the running version." };
        }
      } else {
        unchangedRecoveryPolls = 0;
      }
    } catch {
      if (options.signal.aborted) return null;
    }
    await waitForNextPoll(interval, options.signal);
  }
  return null;
}

// A failed attempt is a state transition, not the end of monitoring. Settings
// handles it, clears the consumed manual target, and this loop keeps watching so
// a fresh check/retry or a later automatic update can still complete the reload.
export async function watchSystemRestarts(options: RestartWatchOptions): Promise<RestartMonitorResult | null> {
  const { onFailure, ...monitorOptions } = options;
  while (!options.signal.aborted) {
    const result = await monitorSystemRestart(monitorOptions);
    if (result?.outcome !== "failed") return result;
    await onFailure(result.message);
  }
  return null;
}
