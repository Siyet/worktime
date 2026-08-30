<script lang="ts">
  import { onMount } from "svelte";
  import { syncState } from "../lib/sync.svelte";
  import { logout } from "../lib/session.svelte";
  import { DEMO } from "../lib/demo";
  import {
    prefs,
    updatePrefs,
    type DateFormatSetting,
    type LocaleSetting,
    type TimeFormatSetting,
  } from "../lib/settings.svelte";
  import { t } from "../lib/i18n";
  import { formatDate, formatTime } from "../lib/format";
  import { downloadText } from "../lib/download";
  import { setupPrompt, setupPromptFilename, type AgentClient } from "../lib/agentSetup";
  import { uuidv7 } from "../lib/uuid";
  import { CLIENT_BUILD_VERSION } from "../lib/buildVersion";
  import {
    applySystemUpdate,
    checkSystemUpdate,
    fetchSystemUpdate,
    fetchSystemVersion,
    watchSystemRestarts,
    setSystemAutoApply,
    type SystemUpdateStatus,
    type UpdateState,
  } from "../lib/systemUpdate";
  import type { User } from "../lib/types";

  // Language names are shown in their own language on purpose.
  const languageOptions: { value: LocaleSetting; label: string }[] = [
    { value: "auto", label: "Auto" },
    { value: "en", label: "English" },
    { value: "ru", label: "Русский" },
    { value: "es", label: "Español" },
    { value: "de", label: "Deutsch" },
    { value: "fr", label: "Français" },
    { value: "zh", label: "中文" },
  ];

  interface APIToken {
    id: string;
    name: string;
    created_at: number;
    last_used_at: number | null;
  }

  let user = $state<User | null>(null);
  let tokens = $state<APIToken[]>([]);
  let newTokenName = $state("");
  let freshToken = $state<string | null>(null);
  let loadError = $state(false);
  let promptError = $state(false);
  let systemVersion = $state<string | null>(null);
  let updateStatus = $state<SystemUpdateStatus | null>(null);
  let versionLoadError = $state(false);
  let updateLoadError = $state(false);
  let updateActionError = $state(false);
  let updateAction = $state<"check" | "policy" | "apply" | null>(null);
  let restartTarget: string | null = null;
  // Which download is in flight, so a double click cannot issue two tokens.
  let issuing = $state<AgentClient | null>(null);

  const currentVersion = $derived(systemVersion ?? updateStatus?.current_version ?? null);
  const canManageUpdates = $derived(updateStatus?.can_manage === true);
  const updateBusy = $derived(
    updateAction !== null || updateStatus?.state === "checking" || updateStatus?.state === "applying",
  );
  const canApplyUpdates = $derived(
    canManageUpdates &&
    updateStatus?.apply_mode === "automatic" &&
    updateStatus.state === "available" &&
    updateStatus.update_available &&
    updateStatus.apply_ready,
  );

  function updateStateLabel(state: UpdateState): string {
    switch (state) {
      case "idle": return t("Not checked yet");
      case "checking": return t("Checking for updates…");
      case "up_to_date": return t("Up to date");
      case "available": return t("Update available");
      case "applying": return t("Installing update…");
      case "restart_required": return t("Restart required");
      case "failed": return t("Update check failed");
    }
  }

  async function load() {
    try {
      const [meResponse, tokensResponse] = await Promise.all([fetch("/api/me"), fetch("/api/tokens")]);
      if (!meResponse.ok || !tokensResponse.ok) throw new Error("load failed");
      user = await meResponse.json();
      tokens = await tokensResponse.json();
      loadError = false;
    } catch {
      loadError = true;
    }
  }

  async function loadSystemInfo() {
    const [versionResult, updateResult] = await Promise.allSettled([
      fetchSystemVersion(),
      fetchSystemUpdate(),
    ]);
    if (versionResult.status === "fulfilled") {
      systemVersion = versionResult.value.version;
      versionLoadError = false;
    } else {
      versionLoadError = true;
    }
    if (updateResult.status === "fulfilled") {
      updateStatus = updateResult.value;
      updateLoadError = false;
    } else {
      updateLoadError = true;
    }
  }

  async function checkForUpdate() {
    if (!canManageUpdates || updateBusy) return;
    updateAction = "check";
    updateActionError = false;
    try {
      updateStatus = await checkSystemUpdate();
      updateLoadError = false;
    } catch {
      updateActionError = true;
    } finally {
      updateAction = null;
    }
  }

  async function changeAutoApply(autoApply: boolean) {
    if (!canManageUpdates || updateStatus?.apply_mode !== "automatic" || updateBusy) return;
    updateAction = "policy";
    updateActionError = false;
    try {
      updateStatus = await setSystemAutoApply(autoApply);
    } catch {
      updateActionError = true;
    } finally {
      updateAction = null;
    }
  }

  async function installUpdate() {
    if (!canApplyUpdates || updateBusy) return;
    if (!window.confirm(t("Install this update now? WorkTime may restart briefly."))) return;
    updateAction = "apply";
    updateActionError = false;
    try {
      updateStatus = await applySystemUpdate();
      restartTarget = updateStatus.latest_version;
    } catch {
      updateActionError = true;
    } finally {
      updateAction = null;
    }
  }

  onMount(() => {
    if (DEMO) return;
    const restartController = new AbortController();
    void load();
    void (async () => {
      await loadSystemInfo();
      if (restartController.signal.aborted) return;
      await watchSystemRestarts({
        baselineVersion: CLIENT_BUILD_VERSION,
        targetVersion: () => restartTarget,
        signal: restartController.signal,
        onStatus: (status) => {
          updateStatus = status;
          updateLoadError = false;
        },
        onFailure: async () => {
          restartTarget = null;
          updateActionError = true;
          await loadSystemInfo();
        },
      });
    })();
    return () => restartController.abort();
  });

  async function createToken(event: SubmitEvent) {
    event.preventDefault();
    const name = newTokenName.trim();
    if (!name) return;
    const response = await fetch("/api/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    if (!response.ok) return;
    const payload = await response.json();
    freshToken = payload.plaintext;
    newTokenName = "";
    await load();
  }

  async function deleteToken(tokenID: string) {
    await fetch(`/api/tokens/${tokenID}`, { method: "DELETE" });
    await load();
  }

  // The prompt is only useful with a token in it, and a token's plaintext exists
  // exactly once - at creation. So the download makes its own rather than asking
  // the user to paste one in; the card says so, and the token shows up in the
  // list above like any other, revocable. The name carries the time as well as
  // the date: a second download would otherwise be indistinguishable from the
  // first in the list, and revoking the right one is the whole point of listing.
  async function downloadPrompt(client: AgentClient) {
    promptError = false;
    issuing = client;
    let plaintext = "";
    try {
      const response = await fetch("/api/tokens", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        // Local date and time: two downloads on one day would otherwise be
        // indistinguishable in the list, and picking the right one to revoke is
        // the whole reason they are listed.
        body: JSON.stringify({ name: `${client} ${formatDate(Date.now())} ${formatTime(Date.now())}` }),
      });
      if (!response.ok) throw new Error("token refused");
      plaintext = (await response.json()).plaintext;
    } catch {
      promptError = true;
      issuing = null;
      return;
    }
    // Past this point the token exists, so a failure here is not "no token".
    try {
      await load();
      downloadText(
        setupPromptFilename(client),
        "text/markdown;charset=utf-8",
        setupPrompt(client, {
          origin: window.location.origin,
          token: plaintext,
          // Minted here rather than baked into the prompt: agent_sessions is keyed
          // globally, so a constant probe id would belong to whoever ran it first
          // and every other user of the instance would get a rejection, not a proof.
          // uuidv7 rather than crypto.randomUUID, which a plain-http instance does
          // not have - and this app generates every other id the same way.
          probeSession: uuidv7(),
        }),
      );
    } catch {
      promptError = true;
    } finally {
      issuing = null;
    }
  }
</script>

{#if DEMO}
  <div class="card muted">{t("Demo mode - data is stored only in this browser.")}</div>
{:else if loadError}
  <div class="card muted">{t("Server is unreachable - settings need a connection.")}</div>
{:else if user}
  <div class="card row">
    {#if user.picture_url}<img class="avatar" src={user.picture_url} alt="" />{/if}
    <div>
      <div>{user.name}</div>
      <div class="muted">{user.email}</div>
    </div>
    <span class="spacer"></span>
    <button onclick={() => logout()}>{t("Sign out")}</button>
  </div>
{/if}

<div class="card">
  <h3>{t("Preferences")}</h3>
  <div class="row pref">
    <label for="language">{t("Language")}</label>
    <span class="spacer"></span>
    <select
      id="language"
      value={prefs.locale}
      onchange={(event) => updatePrefs({ locale: event.currentTarget.value as LocaleSetting })}
    >
      {#each languageOptions as option (option.value)}
        <option value={option.value}>{option.value === "auto" ? t("Auto") : option.label}</option>
      {/each}
    </select>
  </div>
  <div class="row pref">
    <label for="time-format">{t("Time format")}</label>
    <span class="spacer"></span>
    <select
      id="time-format"
      value={prefs.timeFormat}
      onchange={(event) => updatePrefs({ timeFormat: event.currentTarget.value as TimeFormatSetting })}
    >
      <option value="auto">{t("Auto")}</option>
      <option value="12">{t("12-hour")}</option>
      <option value="24">{t("24-hour")}</option>
    </select>
  </div>
  <div class="row pref">
    <label for="date-format">{t("Date format")}</label>
    <span class="spacer"></span>
    <select
      id="date-format"
      value={prefs.dateFormat}
      onchange={(event) => updatePrefs({ dateFormat: event.currentTarget.value as DateFormatSetting })}
    >
      <option value="auto">{t("Auto")}</option>
      <option value="dmy">31.12.2025</option>
      <option value="mdy">12/31/2025</option>
      <option value="ymd">2025-12-31</option>
    </select>
  </div>
</div>

{#if !DEMO}
<div class="card updates" aria-labelledby="updates-heading">
  <h3 id="updates-heading">{t("Version and updates")}</h3>
  <dl class="version-grid">
    <div>
      <dt>{t("Current version")}</dt>
      <dd class:muted={!currentVersion} class="mono">{currentVersion ?? t("Unavailable")}</dd>
    </div>
    <div>
      <dt>{t("Latest version")}</dt>
      <dd class:muted={!updateStatus?.latest_version} class="mono">
        {updateStatus?.latest_version ?? t("Not checked yet")}
      </dd>
    </div>
  </dl>

  {#if versionLoadError && !currentVersion}
    <p class="rejected" role="status">{t("Version information is unavailable.")}</p>
  {/if}

  {#if updateStatus}
    <p class="update-state" aria-live="polite">
      <strong>{updateStateLabel(updateStatus.state)}</strong>
      {#if updateStatus.checked_at}
        <span class="muted">
          · {t("checked {date} at {time}", {
            date: formatDate(updateStatus.checked_at),
            time: formatTime(updateStatus.checked_at),
          })}
        </span>
      {/if}
    </p>
    {#if updateStatus.message}
      <p class="muted update-message">{updateStatus.message}</p>
    {/if}
    {#if updateStatus.changelog_url}
      <p class="update-message">
        <a href={updateStatus.changelog_url} target="_blank" rel="noreferrer">{t("Read the changelog")}</a>
      </p>
    {/if}

    {#if updateStatus.apply_mode === "notification_only"}
      <p class="muted update-note">
        {t("This installation can announce updates, but it must be updated through its deployment platform.")}
      </p>
    {:else}
      <label class="row auto-apply" class:muted={!canManageUpdates}>
        <input
          type="checkbox"
          checked={updateStatus.auto_apply}
          disabled={!canManageUpdates || updateBusy}
          onchange={(event) => void changeAutoApply(event.currentTarget.checked)}
        />
        <span>{t("Install updates automatically")}</span>
      </label>
    {/if}

    {#if !canManageUpdates}
      <p class="muted update-note">{t("Only an administrator can manage updates.")}</p>
    {/if}

    <div class="row update-actions">
      <button disabled={!canManageUpdates || updateBusy} onclick={() => void checkForUpdate()}>
        {updateAction === "check" || updateStatus.state === "checking" ? t("Checking…") : t("Check now")}
      </button>
      {#if updateStatus.apply_mode === "automatic" && updateStatus.update_available}
        <button
          class="primary"
          disabled={!canApplyUpdates || updateBusy}
          onclick={() => void installUpdate()}
        >
          {updateAction === "apply" || updateStatus.state === "applying" ? t("Installing…") : t("Install update")}
        </button>
      {/if}
    </div>
  {:else if updateLoadError}
    <p class="rejected" role="status">{t("Update information is unavailable.")}</p>
  {:else}
    <p class="muted" aria-live="polite">{t("Loading update information…")}</p>
  {/if}

  {#if updateActionError}
    <p class="rejected" role="alert">{t("The update request failed. Try again.")}</p>
  {/if}
</div>

<div class="card">
  <h3>{t("API tokens")}</h3>
  <p class="muted">
    {t("Use tokens to connect MCP clients and scripts: send them as")}
    <code>Authorization: Bearer &lt;token&gt;</code>.
  </p>
  <form class="row" onsubmit={createToken}>
    <input style="flex: 1" placeholder={t("Token name (e.g. claude-mcp)")} bind:value={newTokenName} />
    <button class="primary" type="submit">{t("Create")}</button>
  </form>
  {#if freshToken}
    <div class="fresh">
      <p>{t("Copy the token now - it will not be shown again:")}</p>
      <code>{freshToken}</code>
    </div>
  {/if}
  {#each tokens as token (token.id)}
    <div class="row item">
      <span>{token.name}</span>
      <span class="muted">{t("created {date}", { date: formatDate(token.created_at) })}</span>
      <span class="spacer"></span>
      <button class="danger" onclick={() => deleteToken(token.id)}>{t("Revoke")}</button>
    </div>
  {:else}
    <p class="muted">{t("No tokens yet.")}</p>
  {/each}
</div>

<div class="card">
  <h3>{t("Connect an agent")}</h3>
  <p class="muted">
    {t(
      "Download a setup prompt and paste it into a fresh agent session. The agent installs the hooks, connects the MCP server and proves its own time is being tracked.",
    )}
  </p>
  <div class="row wrap">
    <button disabled={issuing !== null} onclick={() => downloadPrompt("claude-code")}>{t("Claude Code prompt")}</button>
    <button disabled={issuing !== null} onclick={() => downloadPrompt("codex")}>{t("Codex prompt")}</button>
  </div>
  <p class="muted note">
    {t("Each download issues a new API token and writes it into the file - treat the file as a secret and revoke the token above when it is no longer needed.")}
  </p>
  {#if promptError}
    <p class="rejected" role="alert">{t("The token could not be created, so there is nothing to download.")}</p>
  {/if}
</div>

<div class="card">
  <h3>{t("Export")}</h3>
  <p class="muted">{t("Your projects, entries and time off as a standalone SQLite database.")}</p>
  <a class="export-link" href="/api/export.sqlite" download>{t("Download .sqlite")}</a>
</div>

<div class="card">
  <h3>{t("Sync")}</h3>
  <p class="muted">
    {t("Status")}: {syncState.status}, {t("pending changes")}: {syncState.pendingCount}
    {#if syncState.lastSyncedAt}
      , {t("last synced")} {new Date(syncState.lastSyncedAt).toLocaleTimeString()}
    {/if}
  </p>
  <!-- A refused row stays on this device only, and the status above still reads
       "synced" - without this line there is nothing anywhere that says so. -->
  {#if syncState.rejectedCount > 0}
    <p class="rejected">
      {syncState.rejectedCount}
      {t("changes were refused by the server and stay on this device until you edit them again.")}
    </p>
  {/if}
</div>
{/if}

<style>
  h3 {
    margin: 0 0 0.5rem;
    font-size: 0.95rem;
  }

  .rejected {
    margin: 0.4rem 0 0;
    font-size: 0.85rem;
    color: var(--danger);
  }

  /* Two buttons whose labels are translated: German and French leave no slack at
     360px, and .row does not wrap on its own. */
  .row.wrap {
    flex-wrap: wrap;
  }

  /* The line about the token being written into the file: it has to be read
     before the click, not after, so it sits with the buttons rather than in the
     paragraph above them. */
  .note {
    margin: 0.6rem 0 0;
    font-size: 0.85rem;
  }

  .avatar {
    width: 40px;
    height: 40px;
    border-radius: 50%;
  }

  .item {
    padding: 0.35rem 0;
    border-top: 1px solid var(--border);
    margin-top: 0.5rem;
  }

  .pref {
    padding: 0.25rem 0;
  }

  .pref select {
    min-width: 11rem;
  }

  .version-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.75rem;
    margin: 0.75rem 0;
  }

  .version-grid > div {
    min-width: 0;
    padding: 0.65rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .version-grid dt {
    color: var(--text-dim);
    font-size: 0.78rem;
  }

  .version-grid dd {
    margin: 0.15rem 0 0;
    overflow-wrap: anywhere;
  }

  .update-state,
  .update-message,
  .update-note {
    margin: 0.55rem 0 0;
  }

  .auto-apply {
    width: fit-content;
    margin-top: 0.75rem;
  }

  .update-actions {
    margin-top: 0.85rem;
    flex-wrap: wrap;
  }

  @media (max-width: 28rem) {
    .version-grid {
      grid-template-columns: 1fr;
    }
  }

  .fresh {
    margin-top: 0.75rem;
    padding: 0.6rem;
    border: 1px dashed var(--accent);
    border-radius: var(--radius);
    word-break: break-all;
  }

  /* An anchor dressed as a button: the download is a plain same-origin GET,
     so a real link keeps middle-click and "save link as" working. */
  .export-link {
    display: inline-block;
    padding: 0.4rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    text-decoration: none;
    background: var(--surface);
  }

  .export-link:hover {
    background: var(--hover);
  }
</style>
