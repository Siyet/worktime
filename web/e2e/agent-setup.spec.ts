import { expect, test } from "./fixtures";
import { readFileSync } from "node:fs";
import path from "node:path";

async function downloadPrompt(page: import("@playwright/test").Page, button: string, file: string): Promise<string> {
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: button }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe(file);
  const savedPath = path.join(test.info().outputDir, file);
  await download.saveAs(savedPath);
  return readFileSync(savedPath, "utf8");
}

test.describe("agent setup prompt", () => {
  test("each download carries a working token and this instance's URL", async ({ page, server }) => {
    await page.goto(server.url + "/#/settings");
    await expect(page.getByRole("heading", { name: "Connect an agent" })).toBeVisible();

    const claude = await downloadPrompt(page, "Claude Code prompt", "worktime-setup-claude-code.md");
    expect(claude).toContain(`${server.url}/api/agent/hook.sh`);
    expect(claude).toContain("~/.claude/settings.json");

    const token = claude.match(/WORKTIME_TOKEN = (wt_\S+)/)?.[1];
    expect(token).toBeTruthy();

    // The point of embedding a token is that the agent can use it straight away.
    // The hook script it fetches has to be the real one, not the SPA's index.html.
    const script = await page.request.get(`${server.url}/api/agent/hook.sh`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(script.status()).toBe(200);
    expect((await script.text()).split("\n")[0]).toBe("#!/bin/sh");

    const codex = await downloadPrompt(page, "Codex prompt", "worktime-setup-codex.md");
    expect(codex).toContain("WORKTIME_AGENT_SOURCE=codex");
    // A second download must not hand out the first token again.
    expect(codex).not.toContain(token!);

    // Both tokens are listed and revocable rather than issued invisibly.
    await expect(page.locator(".card", { hasText: "API tokens" }).getByText(/claude-code|codex/)).toHaveCount(2);
  });
});
