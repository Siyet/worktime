import { describe, expect, it } from "vitest";
import { setupPrompt, setupPromptFilename, type AgentClient } from "./agentSetup";

const CLIENTS: AgentClient[] = ["claude-code", "codex"];
const options = { origin: "https://wt.example.com", token: "wt_secret", probeSession: "8f14e45f-ea8b-4b3f-9a4d-2b1c0d7e5a61" };

describe("agent setup prompts", () => {
  it("addresses the instance the page is served from", () => {
    for (const client of CLIENTS) {
      const prompt = setupPrompt(client, options);
      expect(prompt).toContain("https://wt.example.com/api/agent/hook.sh");
      expect(prompt).toContain("https://wt.example.com/mcp");
      expect(prompt).toContain("wt_secret");
    }
    expect(setupPrompt("claude-code", options)).toContain("https://wt.example.com/api/agent/statusline.sh");
    expect(setupPrompt("codex", options)).not.toContain("/api/agent/statusline.sh");
  });

  // window.location.origin never has one, but an instance behind a path prefix
  // reaches this through configuration, and "//api/..." is a different host to curl.
  it("never doubles the slash when the origin carries one", () => {
    const prompt = setupPrompt("claude-code", { ...options, origin: "https://wt.example.com/" });
    expect(prompt).not.toContain("com//");
    expect(prompt).toContain("https://wt.example.com/api/me");
  });

  it("leaves no unsubstituted placeholder behind", () => {
    for (const client of CLIENTS) {
      const prompt = setupPrompt(client, options);
      expect(prompt).not.toMatch(/undefined|\[object Object\]|\{\{/);
    }
  });

  // An agent usually runs each shell command in a fresh process, so a command
  // reading $WORKTIME_TOKEN sends an empty header and every step "succeeds" with
  // a 401. Commands carry the values; only the hook's own environment prefix and
  // the MCP header, which Claude Code expands itself, may name the variable.
  it("never depends on a variable an earlier command exported", () => {
    for (const client of CLIENTS) {
      for (const line of setupPrompt(client, options).split("\n")) {
        if (!line.includes("$WORKTIME_TOKEN")) continue;
        expect(line).toMatch(/WORKTIME_TOKEN="wt_|\$\{WORKTIME_TOKEN\}|WORKTIME_TOKEN$/);
      }
    }
  });

  // The two halves the docs keep insisting are independent: hooks track the time,
  // set_agent_task names it. A prompt that drops either one sets up half a system.
  it("covers both halves and proves the chain", () => {
    for (const client of CLIENTS) {
      const prompt = setupPrompt(client, options);
      expect(prompt).toContain("set_agent_task");
      expect(prompt).toContain("list_running_timers");
      // The signal sent before a tool runs; without it a long tool call is
      // subtracted from the entry as idle time.
      expect(prompt).toContain("PreToolUse");
      expect(prompt).toContain(options.probeSession);
    }
  });

  // The name the user is told to look for is the server's: the first eight hex
  // characters of the session id, which is minted per download. A constant here
  // would send them hunting for a row that does not exist.
  it("names the probe entry the way the server will", () => {
    expect(setupPrompt("claude-code", options)).toContain("Claude Code #8f14e45f");
    expect(setupPrompt("codex", options)).toContain("Codex #8f14e45f");
  });

  // A lone backslash before a newline is a line continuation inside a template
  // literal: both vanish, and a two-line shell command silently becomes one long
  // line with a hole in the middle. Nothing about the rendered text looks wrong
  // until you read it, which is how it got shipped past two reviews.
  it("keeps indented command blocks readable", () => {
    for (const client of CLIENTS) {
      const lines = setupPrompt(client, options).split("\n");
      lines.forEach((line, index) => {
        if (!line.startsWith("    ")) return;
        expect(line.trimEnd().length, `over-long command line: ${line}`).toBeLessThan(200);
        // A command continued on the next line must say so. If the source ever
        // loses one of the two backslashes the continuation needs, the newline
        // goes with it and the two lines silently become one.
        const next = lines[index + 1] ?? "";
        if (/^\s+\|/.test(next)) {
          expect(line.trimEnd(), `continuation without a backslash: ${line}`).toMatch(/\\$/);
        }
      });
    }
  });

  it("tells each client apart", () => {
    const claude = setupPrompt("claude-code", options);
    const codex = setupPrompt("codex", options);
    expect(claude).toContain("~/.claude/settings.json");
    expect(claude).toContain("wt-statusline.sh");
    expect(claude).not.toContain("WORKTIME_AGENT_SOURCE");
    expect(codex).toContain("WORKTIME_AGENT_SOURCE=codex");
    expect(codex).toContain("~/.codex/");
    expect(setupPromptFilename("claude-code")).not.toBe(setupPromptFilename("codex"));
  });
});
