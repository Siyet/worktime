import { describe, expect, it, vi } from "vitest";
import {
  applySystemUpdate,
  checkSystemUpdate,
  fetchSystemUpdate,
  fetchSystemVersion,
  monitorSystemRestart,
  setSystemAutoApply,
  watchSystemRestarts,
  type SystemUpdateStatus,
} from "./systemUpdate";
import { CLIENT_BUILD_VERSION } from "./buildVersion";

const status: SystemUpdateStatus = {
  state: "available",
  current_version: "1.0.0",
  latest_version: "1.1.0",
  update_available: true,
  apply_ready: true,
  checked_at: 1_788_000_000_000,
  changelog_url: "https://github.com/Siyet/worktime/releases/tag/v1.1.0",
  auto_apply: false,
  can_manage: true,
  apply_mode: "automatic",
  message: null,
};

function response(payload: unknown, ok = true, responseStatus = 200): Response {
  return { ok, status: responseStatus, json: async () => payload } as Response;
}

describe("system update API", () => {
  it("loads version and update status from their read-only endpoints", async () => {
    const request = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(response({ version: "1.0.0" }))
      .mockResolvedValueOnce(response(status));

    await expect(fetchSystemVersion(request)).resolves.toEqual({ version: "1.0.0" });
    await expect(fetchSystemUpdate(request)).resolves.toEqual(status);
    expect(request).toHaveBeenNthCalledWith(1, "/api/system/version", undefined);
    expect(request).toHaveBeenNthCalledWith(2, "/api/system/update", undefined);
  });

  it("uses explicit mutation methods and the exact policy body", async () => {
    const request = vi.fn<typeof fetch>().mockResolvedValue(response(status));

    await checkSystemUpdate(request);
    await setSystemAutoApply(true, request);
    await applySystemUpdate(request);

    expect(request).toHaveBeenNthCalledWith(1, "/api/system/update/check", { method: "POST" });
    expect(request).toHaveBeenNthCalledWith(2, "/api/system/update/policy", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: '{"auto_apply":true}',
    });
    expect(request).toHaveBeenNthCalledWith(3, "/api/system/update/apply", { method: "POST" });
  });

  it("rejects a non-success response without trying to parse it", async () => {
    const json = vi.fn();
    const request = vi.fn<typeof fetch>().mockResolvedValue({ ok: false, status: 403, json } as unknown as Response);

    await expect(checkSystemUpdate(request)).rejects.toThrow("request failed with status 403");
    expect(json).not.toHaveBeenCalled();
  });

  it("polls through a manual-update outage and refreshes the service worker before reload", async () => {
    const events: string[] = [];
    const controller = new AbortController();
    const removeListener = vi.spyOn(controller.signal, "removeEventListener");
    const request = vi
      .fn<typeof fetch>()
      .mockRejectedValueOnce(new Error("server stopped"))
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.0.0" }))
      .mockResolvedValueOnce(response(status))
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.1.0" }))
      .mockResolvedValueOnce(response({ ...status, state: "up_to_date", current_version: "1.1.0", update_available: false, apply_ready: false }));

    await monitorSystemRestart({
      baselineVersion: "1.0.0",
      targetVersion: () => "1.1.0",
      signal: controller.signal,
      request,
      intervalMs: 0,
      refreshServiceWorker: async () => { events.push("service-worker"); },
      reload: () => { events.push("reload"); },
    });

    expect(request).toHaveBeenCalledWith("/healthz", expect.objectContaining({ cache: "no-store" }));
    expect(events).toEqual(["service-worker", "reload"]);
    expect(removeListener).toHaveBeenCalled();
  });

  it("detects an automatic restart from the build version without a manual target", async () => {
    const request = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "2.0.0" }))
      .mockResolvedValueOnce(response({ ...status, state: "up_to_date", current_version: "2.0.0", update_available: false, apply_ready: false }));
    const reload = vi.fn();

    await monitorSystemRestart({
      baselineVersion: "1.0.0",
      targetVersion: () => null,
      signal: new AbortController().signal,
      request,
      intervalMs: 0,
      refreshServiceWorker: async () => {},
      reload,
    });

    expect(reload).toHaveBeenCalledOnce();
  });

  it("does not reload-loop when the release client and server versions match", async () => {
    const controller = new AbortController();
    const request = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: CLIENT_BUILD_VERSION }))
      .mockResolvedValueOnce(response({ ...status, current_version: CLIENT_BUILD_VERSION }));
    const reload = vi.fn();

    const result = await monitorSystemRestart({
      baselineVersion: CLIENT_BUILD_VERSION,
      targetVersion: () => null,
      signal: controller.signal,
      request,
      intervalMs: 0,
      reload,
      onStatus: () => controller.abort(),
    });

    expect(result).toBeNull();
    expect(reload).not.toHaveBeenCalled();
  });

  it("stops polling and surfaces a pre-handoff failure without reloading", async () => {
    const failed = { ...status, state: "failed" as const, message: "preflight failed", apply_ready: false };
    const request = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.0.0" }))
      .mockResolvedValueOnce(response(failed));
    const reload = vi.fn();

    const result = await monitorSystemRestart({
      baselineVersion: "1.0.0",
      targetVersion: () => "1.1.0",
      signal: new AbortController().signal,
      request,
      intervalMs: 0,
      refreshServiceWorker: async () => {},
      reload,
    });

    expect(result).toEqual({ outcome: "failed", message: "preflight failed" });
    expect(reload).not.toHaveBeenCalled();
  });

  it("treats a recovered unchanged version after an update outage as rollback", async () => {
    const request = vi
      .fn<typeof fetch>()
      .mockRejectedValueOnce(new Error("process restarting"))
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.0.0" }))
      .mockResolvedValueOnce(response({ ...status, apply_ready: false }))
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.0.0" }))
      .mockResolvedValueOnce(response({ ...status, apply_ready: false }));

    const result = await monitorSystemRestart({
      baselineVersion: "1.0.0",
      targetVersion: () => "1.1.0",
      signal: new AbortController().signal,
      request,
      intervalMs: 0,
    });

    expect(result?.outcome).toBe("failed");
  });

  it("detects a fast rollback from two healthy settled polls without an outage", async () => {
    const settled = { ...status, state: "available" as const, apply_ready: false };
    const request = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.0.0" }))
      .mockResolvedValueOnce(response(settled))
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.0.0" }))
      .mockResolvedValueOnce(response(settled));

    const result = await monitorSystemRestart({
      baselineVersion: "1.0.0",
      targetVersion: () => "1.1.0",
      signal: new AbortController().signal,
      request,
      intervalMs: 0,
    });

    expect(result?.outcome).toBe("failed");
    expect(request).toHaveBeenCalledTimes(6);
  });

  it("continues after a handled failure and completes a freshly checked retry", async () => {
    let target: string | null = "1.1.0";
    const failed = { ...status, state: "failed" as const, message: "preflight failed", apply_ready: false };
    const request = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.0.0" }))
      .mockResolvedValueOnce(response(failed))
      .mockResolvedValueOnce(response(null))
      .mockResolvedValueOnce(response({ version: "1.1.0" }))
      .mockResolvedValueOnce(response({ ...status, state: "up_to_date", current_version: "1.1.0", update_available: false, apply_ready: false }));
    const failures: string[] = [];
    const reload = vi.fn();

    const result = await watchSystemRestarts({
      baselineVersion: "1.0.0",
      targetVersion: () => target,
      signal: new AbortController().signal,
      request,
      intervalMs: 0,
      refreshServiceWorker: async () => {},
      reload,
      onFailure: (message) => {
        failures.push(message);
        // The UI clears the consumed target, performs a fresh signed check, and
        // sets the target again only after the retry is accepted.
        target = null;
        target = "1.1.0";
      },
    });

    expect(failures).toEqual(["preflight failed"]);
    expect(result).toEqual({ outcome: "updated", version: "1.1.0" });
    expect(reload).toHaveBeenCalledOnce();
  });
});
