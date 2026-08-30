import { expect, pushBarrier, test, triggerSync } from "./fixtures";
import type { APIRequestContext, Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";

const MINUTE = 60_000;

interface AgentSessionResult {
  id: string;
  status: "active" | "closed";
  last_heartbeat_at: number;
  ended_at: number | null;
  end_reason: string | null;
  time_entry_id: string | null;
}

// The agent hooks speak plain HTTP, so the whole loop can be driven from a test:
// signals in, rows in the browser out.
async function signal(
  request: APIRequestContext,
  serverURL: string,
  sessionID: string,
  kind: "start" | "heartbeat" | "stop",
  body: Record<string, unknown>,
): Promise<AgentSessionResult> {
  const response = await request.post(`${serverURL}/api/agent/sessions/${sessionID}/${kind}`, { data: body });
  if (!response.ok()) {
    throw new Error(`agent ${kind} failed: ${response.status()} ${await response.text()}`);
  }
  return response.json() as Promise<AgentSessionResult>;
}

async function runRealHook(
  scriptPath: string,
  event: "start" | "heartbeat" | "tool_start" | "stop",
  payload: Record<string, unknown>,
  environment: Record<string, string>,
): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const child = spawn("sh", [scriptPath, event], {
      env: { ...process.env, ...environment },
      stdio: ["pipe", "ignore", "pipe"],
    });
    let stderr = "";
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => {
      stderr += chunk;
    });
    child.on("error", reject);
    child.on("exit", (code) => {
      if (code === 0) resolve();
      else reject(new Error(`real hook exited ${code}: ${stderr}`));
    });
    child.stdin.end(JSON.stringify(payload));
  });
}

// Minimal MCP client: the server runs stateless, so one POST per call is enough.
// The response is either JSON or a single SSE frame.
async function callMCPTool(
  request: APIRequestContext,
  serverURL: string,
  name: string,
  args: Record<string, unknown>,
): Promise<void> {
  const response = await request.post(`${serverURL}/mcp`, {
    headers: { "Content-Type": "application/json", Accept: "application/json, text/event-stream" },
    data: { jsonrpc: "2.0", id: 1, method: "tools/call", params: { name, arguments: args } },
  });
  const text = await response.text();
  if (!response.ok() || text.includes('"isError":true')) {
    throw new Error(`mcp ${name} failed: ${response.status()} ${text}`);
  }
}

function runningCard(page: Page) {
  return page.locator(".card").filter({ has: page.getByRole("heading", { name: "Running" }) });
}

/** Pulls the server state into the page and waits for the expectation to hold. */
async function pollUI(page: Page, check: () => Promise<void>): Promise<void> {
  await expect(async () => {
    await triggerSync(page);
    await check();
  }).toPass({ timeout: 15_000 });
}

test.describe("agent tracking", () => {
  test("the shipped hook speaks to the live binary with a minted Bearer token", async ({ request, agentServer }) => {
    test.skip(process.platform === "win32", "the reference hook requires a POSIX shell");

    const tokenResponse = await request.post(`${agentServer.url}/api/tokens`, {
      data: { name: "real hook e2e" },
    });
    expect(tokenResponse.status()).toBe(201);
    const token = (await tokenResponse.json()) as { plaintext: string };
    expect(token.plaintext).toMatch(/^wt_/);

    const sessionID = crypto.randomUUID();
    const queueDir = path.join(test.info().outputDir, "real-hook-queue");
    const scriptPath = path.join(test.info().outputDir, "wt-hook.sh");
    const scriptResponse = await request.get(`${agentServer.url}/api/agent/hook.sh`, {
      headers: { Authorization: `Bearer ${token.plaintext}` },
    });
    expect(scriptResponse.ok()).toBe(true);
    const script = await scriptResponse.text();
    expect(script.startsWith("#!/bin/sh\n")).toBe(true);
    mkdirSync(test.info().outputDir, { recursive: true });
    writeFileSync(scriptPath, script, { mode: 0o600 });
    const hookEnvironment = {
      WORKTIME_URL: agentServer.url,
      WORKTIME_TOKEN: token.plaintext,
      WORKTIME_QUEUE_DIR: queueDir,
      WORKTIME_AGENT_SOURCE: "codex",
    };
    await runRealHook(scriptPath, "start", { session_id: sessionID, cwd: test.info().project.testDir }, hookEnvironment);
    await runRealHook(scriptPath, "heartbeat", { session_id: sessionID }, hookEnvironment);
    await callMCPTool(request, agentServer.url, "set_agent_task", {
      session_id: sessionID,
      task_key: "#1",
      task_title: "Live hook protocol smoke",
    });
    await runRealHook(scriptPath, "stop", { session_id: sessionID, reason: "clear" }, hookEnvironment);

    const entriesResponse = await request.get(`${agentServer.url}/api/entries`);
    expect(entriesResponse.ok()).toBe(true);
    const entries = (await entriesResponse.json()) as Array<{
      agent_session_id: string | null;
      description: string;
      stopped_at: number | null;
    }>;
    const tracked = entries.filter((entry) => entry.agent_session_id === sessionID);
    expect(tracked).toHaveLength(1);
    expect(tracked[0]).toMatchObject({ description: "#1 Live hook protocol smoke" });
    expect(tracked[0].stopped_at).not.toBeNull();
  });

  test("an untouched technical session that would display as 0m leaves no feed row", async ({
    page,
    request,
    agentServer,
  }) => {
    const sessionID = crypto.randomUUID();
    const base = Date.now() - 10_000;
    await signal(request, agentServer.url, sessionID, "start", { started_at: base, source: "codex" });
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: base });
    await signal(request, agentServer.url, sessionID, "stop", { ended_at: base + 10_000, reason: "subagent_stop" });

    await page.goto(agentServer.url + "/#/");
    await pollUI(page, async () => {
      await expect(page.locator(".item").filter({ hasText: "Codex" })).toHaveCount(0);
    });
  });

  test("a session with a long pause becomes two entries and the pause is not billed", async ({
    page,
    request,
    agentServer,
  }) => {
    const sessionID = crypto.randomUUID();
    // Yesterday at 09:00 keeps both segments on one card at every wall-clock
    // time the suite can run.
    const base = new Date().setHours(9, 0, 0, 0) - 24 * 60 * MINUTE;
    await signal(request, agentServer.url, sessionID, "start", { started_at: base, source: "claude-code" });
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: base + 5 * MINUTE });
    // Ninety minutes of silence: far past the idle threshold and with no
    // timezone sent. The pause still has to split the work into two segments.
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: base + 95 * MINUTE });
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: base + 100 * MINUTE });
    await signal(request, agentServer.url, sessionID, "stop", { ended_at: base + 100 * MINUTE, reason: "clear" });

    await page.goto(agentServer.url + "/#/");
    const group = page.locator(".group-row").filter({ hasText: "Claude Code" });
    await pollUI(page, async () => {
      await expect(group.locator(".count-value")).toHaveText("2");
      await expect(group.locator(".dur")).toHaveText("10m");
      await expect(group).not.toContainText(/#[0-9a-f]{8}/);
    });
    await group.click();
    const segments = page.locator(".item.member").filter({ hasText: "Claude Code" });
    await expect(segments).toHaveCount(2);
    await expect(segments.locator(".dur")).toHaveText(["5m", "5m"]);
    expect((await segments.allInnerTexts()).every((text) => !/#[0-9a-f]{8}/.test(text))).toBe(true);
  });

  test("two sessions are two rows with different tags, one task groups them", async ({
    page,
    request,
    agentServer,
  }) => {
    const first = crypto.randomUUID();
    const second = crypto.randomUUID();
    // Recent enough that the reconciliation job leaves both sessions alone.
    const base = Date.now() - 2 * MINUTE;
    await signal(request, agentServer.url, first, "start", { started_at: base, cwd: "/home/dev/alpha" });
    await signal(request, agentServer.url, second, "start", { started_at: base + MINUTE, cwd: "/home/dev/beta" });
    // The row is opened by activity, so a session that only started has nothing
    // to show yet.
    await signal(request, agentServer.url, first, "heartbeat", { at: base });
    await signal(request, agentServer.url, second, "heartbeat", { at: base + MINUTE });

    await page.goto(agentServer.url + "/#/");
    const card = runningCard(page);
    await pollUI(page, async () => {
      await expect(card.locator(".item")).toHaveCount(2);
    });
    const names = await card.locator(".item .desc").allInnerTexts();
    expect(names).toEqual(["Claude Code", "Claude Code"]);
    await expect(card).not.toContainText(/#[0-9a-f]{8}/);

    await card.locator(".item .desc").first().click();
    const identifier = page.locator("dialog.sheet").getByLabel("Session identifier");
    await expect(identifier).toHaveText(new RegExp(`^(${first}|${second})$`));
    await page.locator("dialog.sheet").getByRole("button", { name: "Cancel" }).click();

    // Both sessions turn out to be the same task: the rows keep their identity
    // but read as one line of work.
    for (const sessionID of [first, second]) {
      await callMCPTool(request, agentServer.url, "set_agent_task", {
        session_id: sessionID,
        task_key: "MT-12345",
        task_title: "Slow AMaaS quote creation",
      });
    }

    const group = card.locator(".group-row").filter({ hasText: "MT-12345 Slow AMaaS quote creation" });
    await pollUI(page, async () => {
      await expect(group.locator(".count-value")).toHaveText("2");
    });
    // A collapsed running group cannot be stopped in one click: undo is per entry.
    await expect(card.getByRole("button", { name: "Stop" })).toHaveCount(0);

    await group.click();
    await expect(card.locator(".item.member")).toHaveCount(2);
    // Expanded members repeat the session's task name; the identifier belongs
    // in the full editor, not in either list row.
    await expect(card.locator(".item.member .desc")).toHaveText([
      "MT-12345 Slow AMaaS quote creation",
      "MT-12345 Slow AMaaS quote creation",
    ]);
    await expect(card).not.toContainText(/#[0-9a-f]{8}/);
  });

  test("stopping an agent row by hand does not lose the rest of the session", async ({
    page,
    request,
    agentServer,
  }) => {
    const sessionID = crypto.randomUUID();
    const base = Date.now() - 2 * MINUTE;
    await signal(request, agentServer.url, sessionID, "start", { started_at: base });
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: base });

    await page.goto(agentServer.url + "/#/");
    const card = runningCard(page);
    await pollUI(page, async () => {
      await expect(card.locator(".item")).toHaveCount(1);
    });
    // The heartbeat below must arrive after the stop, or the server would still
    // believe it owns a running row and simply keep writing into it.
    const stopPushed = pushBarrier(page, '"stopped_at":1');
    await card.getByRole("button", { name: "Stop" }).click();
    await expect(page.getByRole("heading", { name: "Running" })).toHaveCount(0);
    await stopPushed;

    // The agent keeps working: its time has to land somewhere.
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: Date.now() });
    await pollUI(page, async () => {
      await expect(runningCard(page).locator(".item")).toHaveCount(1);
      await expect(page.locator(".item").filter({ hasText: "Claude Code" })).toHaveCount(2);
    });
  });

  test("reconciliation closes a silent session at its last activity", async ({ page, request, agentServerStale }) => {
    const sessionID = crypto.randomUUID();
    const lastActivity = Date.now() - MINUTE;
    await signal(request, agentServerStale.url, sessionID, "start", { started_at: lastActivity - 5 * MINUTE });
    await signal(request, agentServerStale.url, sessionID, "heartbeat", { at: lastActivity });

    await page.goto(agentServerStale.url + "/#/");
    // The session goes silent; the grace period is three seconds on this server
    // and the job runs every second, so the row must close on its own.
    // Every assertion belongs inside the poll, because the poll is the only thing
    // that makes this page pull. The start arrives already older than the three
    // second grace, so on a loaded machine the job closes the session before the
    // heartbeat lands, the heartbeat reopens the same row, and the next turn of
    // the job closes it again: a check made once, after a single sync, can land
    // on any of those states.
    await pollUI(page, async () => {
      await expect(page.getByRole("heading", { name: "Running" })).toHaveCount(0);
      const row = page.locator(".item").filter({ hasText: "Claude Code" });
      await expect(row).toHaveCount(1);
      // Closed at the last heartbeat, not at the moment the job noticed: five
      // minutes of work, not six.
      await expect(row.locator(".dur")).toHaveText("5m");
    });

    // A later duplicate stop is a readback of the already-closed session. It
    // pins the periodic job's durable outcome rather than only its UI shape.
    const reconciled = await signal(request, agentServerStale.url, sessionID, "stop", {
      ended_at: Date.now(),
      reason: "late_probe",
    });
    expect(reconciled).toMatchObject({
      status: "closed",
      last_heartbeat_at: lastActivity,
      ended_at: lastActivity,
      end_reason: "stale_heartbeat",
    });
  });

  test("startup reconciliation closes work that became stale while the server was down", async ({
    request,
    agentServerRestart,
  }) => {
    const sessionID = crypto.randomUUID();
    const lastActivity = Date.now();
    await signal(request, agentServerRestart.url, sessionID, "start", {
      started_at: lastActivity - MINUTE,
      source: "codex",
    });
    await signal(request, agentServerRestart.url, sessionID, "heartbeat", { at: lastActivity });
    await callMCPTool(request, agentServerRestart.url, "set_agent_task", {
      session_id: sessionID,
      task_key: "#1",
      task_title: "Startup reconcile probe",
    });

    // The first process has a one-hour grace and cannot close this session. It
    // becomes stale only while no process is running. The replacement process
    // also has a one-hour periodic interval, so a prompt close can only be its
    // mandatory first reconciliation pass.
    await agentServerRestart.stop();
    await new Promise((resolve) => setTimeout(resolve, 1_200));
    await agentServerRestart.restart({
      WORKTIME_AGENT_GRACE: "1s",
      WORKTIME_AGENT_RECONCILE: "1h",
    });

    await expect
      .poll(async () => {
        const response = await request.get(`${agentServerRestart.url}/api/entries`);
        if (!response.ok()) return null;
        const entries = (await response.json()) as Array<{
          agent_session_id: string | null;
          stopped_at: number | null;
        }>;
        return entries.find((entry) => entry.agent_session_id === sessionID)?.stopped_at ?? null;
      })
      .toBe(lastActivity);

    const reconciled = await signal(request, agentServerRestart.url, sessionID, "stop", {
      ended_at: Date.now(),
      reason: "late_probe",
    });
    expect(reconciled).toMatchObject({
      status: "closed",
      last_heartbeat_at: lastActivity,
      ended_at: lastActivity,
      end_reason: "stale_heartbeat",
    });
  });
});
