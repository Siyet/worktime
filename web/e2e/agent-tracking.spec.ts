import { expect, pushBarrier, test, triggerSync } from "./fixtures";
import type { APIRequestContext, Page } from "@playwright/test";

const MINUTE = 60_000;

// The agent hooks speak plain HTTP, so the whole loop can be driven from a test:
// signals in, rows in the browser out.
async function signal(
  request: APIRequestContext,
  serverURL: string,
  sessionID: string,
  kind: "start" | "heartbeat" | "stop",
  body: Record<string, unknown>,
): Promise<void> {
  const response = await request.post(`${serverURL}/api/agent/sessions/${sessionID}/${kind}`, { data: body });
  if (!response.ok()) {
    throw new Error(`agent ${kind} failed: ${response.status()} ${await response.text()}`);
  }
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

  test("a session with a long pause stays one entry and the pause is not billed", async ({
    page,
    request,
    agentServer,
  }) => {
    const sessionID = crypto.randomUUID();
    const base = Date.now() - 100 * MINUTE;
    await signal(request, agentServer.url, sessionID, "start", { started_at: base, source: "claude-code" });
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: base + 5 * MINUTE });
    // Ninety minutes of silence: far past the idle threshold, well under the
    // maximum pause, and no timezone was ever sent - nothing may cut the row.
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: base + 95 * MINUTE });
    await signal(request, agentServer.url, sessionID, "heartbeat", { at: base + 100 * MINUTE });
    await signal(request, agentServer.url, sessionID, "stop", { ended_at: base + 100 * MINUTE, reason: "clear" });

    await page.goto(agentServer.url + "/#/");
    const row = page.locator(".item").filter({ hasText: "Claude Code" });
    await pollUI(page, async () => {
      await expect(row).toHaveCount(1);
      // A hundred minutes of interval, ninety of them idle.
      await expect(row.locator(".dur")).toHaveText("10m");
    });
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
  });
});
