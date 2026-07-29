<script lang="ts">
  import { syncState } from "../lib/sync.svelte";
  import { logout } from "../lib/session.svelte";
  import { prefs, updatePrefs, type LocaleSetting, type TimeFormatSetting } from "../lib/settings.svelte";
  import { t } from "../lib/i18n";
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
  void load();

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
</script>

{#if loadError}
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
      <span class="muted">{t("created {date}", { date: new Date(token.created_at).toLocaleDateString() })}</span>
      <span class="spacer"></span>
      <button class="danger" onclick={() => deleteToken(token.id)}>{t("Revoke")}</button>
    </div>
  {:else}
    <p class="muted">{t("No tokens yet.")}</p>
  {/each}
</div>

<div class="card">
  <h3>{t("Sync")}</h3>
  <p class="muted">
    {t("Status")}: {syncState.status}, {t("pending changes")}: {syncState.pendingCount}
    {#if syncState.lastSyncedAt}
      , {t("last synced")} {new Date(syncState.lastSyncedAt).toLocaleTimeString()}
    {/if}
  </p>
</div>

<style>
  h3 {
    margin: 0 0 0.5rem;
    font-size: 0.95rem;
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

  .fresh {
    margin-top: 0.75rem;
    padding: 0.6rem;
    border: 1px dashed var(--accent);
    border-radius: var(--radius);
    word-break: break-all;
  }
</style>
