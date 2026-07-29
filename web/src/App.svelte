<script lang="ts">
  import { route } from "./lib/router.svelte";
  import { syncState } from "./lib/sync.svelte";
  import { startSyncEngine } from "./lib/sync.svelte";
  import { appState, loadStateFromDB } from "./lib/state/app.svelte";
  import { initSession, session } from "./lib/session.svelte";
  import Logo from "./lib/components/Logo.svelte";
  import TimerPage from "./pages/TimerPage.svelte";
  import ProjectsPage from "./pages/ProjectsPage.svelte";
  import TimeOffPage from "./pages/TimeOffPage.svelte";
  import ReportsPage from "./pages/ReportsPage.svelte";
  import SettingsPage from "./pages/SettingsPage.svelte";

  void loadStateFromDB();
  void initSession();
  startSyncEngine();

  const signedOut = $derived(syncState.status === "unauthenticated" && session.checked && session.user === null);

  const tabs = [
    { path: "/", label: "Timer" },
    { path: "/projects", label: "Projects" },
    { path: "/timeoff", label: "Time off" },
    { path: "/reports", label: "Reports" },
    { path: "/settings", label: "Settings" },
  ];

  const statusLabel = $derived(
    {
      idle: "…",
      syncing: "syncing",
      synced: "synced",
      offline: "offline",
      error: "sync error",
      unauthenticated: "signed out",
    }[syncState.status],
  );
</script>

<div class="shell">
  <header>
    <span class="logo"><Logo />W<span class="logo-accent">T</span></span>
    <nav>
      {#each tabs as tab (tab.path)}
        <a href={"#" + tab.path} class:active={route.path === tab.path}>{tab.label}</a>
      {/each}
    </nav>
    <span class="status" data-status={syncState.status} title={"sync: " + syncState.status}>
      {statusLabel}{syncState.pendingCount > 0 ? ` (${syncState.pendingCount})` : ""}
    </span>
  </header>

  <main>
    {#if signedOut}
      <div class="card signin">
        <h2>Sign in to WorkTime</h2>
        {#if session.googleAvailable}
          <a class="google-button" href="/auth/google">Sign in with Google</a>
        {:else}
          <p class="muted">
            Google sign-in is not configured. Set WORKTIME_GOOGLE_CLIENT_ID and
            WORKTIME_GOOGLE_CLIENT_SECRET on the server, or run with WORKTIME_DEV_AUTH=1 for local development.
          </p>
        {/if}
      </div>
    {:else if !appState.loaded}
      <p class="muted">Loading…</p>
    {:else if route.path === "/projects"}
      <ProjectsPage />
    {:else if route.path === "/timeoff"}
      <TimeOffPage />
    {:else if route.path === "/reports"}
      <ReportsPage />
    {:else if route.path === "/settings"}
      <SettingsPage />
    {:else}
      <TimerPage />
    {/if}
  </main>
</div>

<style>
  .shell {
    max-width: 46rem;
    margin: 0 auto;
    padding: 0 1rem 3rem;
  }

  header {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.8rem 0;
    flex-wrap: wrap;
  }

  .logo {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    font-weight: 700;
    font-size: 1.1rem;
  }

  .logo-accent {
    color: var(--accent);
  }

  nav {
    display: flex;
    gap: 0.2rem;
    flex: 1;
    flex-wrap: wrap;
  }

  nav a {
    color: var(--text-dim);
    text-decoration: none;
    padding: 0.3rem 0.7rem;
    border-radius: var(--radius);
  }

  nav a.active {
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
  }

  .status {
    font-size: 0.8rem;
    color: var(--text-dim);
  }

  .status[data-status="error"],
  .status[data-status="unauthenticated"] {
    color: var(--danger);
  }

  .signin {
    text-align: center;
    padding: 3rem 1rem;
  }

  .signin h2 {
    margin-top: 0;
  }

  .google-button {
    display: inline-block;
    background: var(--accent);
    color: var(--accent-text);
    padding: 0.6rem 1.4rem;
    border-radius: var(--radius);
    text-decoration: none;
  }
</style>
