<script lang="ts">
  import { syncState } from "../lib/sync.svelte";
  import type { User } from "../lib/types";

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
  <div class="card muted">Server is unreachable - settings need a connection.</div>
{:else if user}
  <div class="card row">
    {#if user.picture_url}<img class="avatar" src={user.picture_url} alt="" />{/if}
    <div>
      <div>{user.name}</div>
      <div class="muted">{user.email}</div>
    </div>
  </div>
{/if}

<div class="card">
  <h3>API tokens</h3>
  <p class="muted">
    Use tokens to connect MCP clients and scripts: send them as <code>Authorization: Bearer &lt;token&gt;</code>.
  </p>
  <form class="row" onsubmit={createToken}>
    <input style="flex: 1" placeholder="Token name (e.g. claude-mcp)" bind:value={newTokenName} />
    <button class="primary" type="submit">Create</button>
  </form>
  {#if freshToken}
    <div class="fresh">
      <p>Copy the token now - it will not be shown again:</p>
      <code>{freshToken}</code>
    </div>
  {/if}
  {#each tokens as token (token.id)}
    <div class="row item">
      <span>{token.name}</span>
      <span class="muted">created {new Date(token.created_at).toLocaleDateString()}</span>
      <span class="spacer"></span>
      <button class="danger" onclick={() => deleteToken(token.id)}>Revoke</button>
    </div>
  {:else}
    <p class="muted">No tokens yet.</p>
  {/each}
</div>

<div class="card">
  <h3>Sync</h3>
  <p class="muted">
    Status: {syncState.status}, pending changes: {syncState.pendingCount}
    {#if syncState.lastSyncedAt}
      , last synced {new Date(syncState.lastSyncedAt).toLocaleTimeString()}
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

  .fresh {
    margin-top: 0.75rem;
    padding: 0.6rem;
    border: 1px dashed var(--accent);
    border-radius: var(--radius);
    word-break: break-all;
  }
</style>
