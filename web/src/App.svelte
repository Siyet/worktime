<script lang="ts">
  import { route } from "./lib/router.svelte";
  import { syncState } from "./lib/sync.svelte";
  import { startSyncEngine } from "./lib/sync.svelte";
  import { appState, loadStateFromDB } from "./lib/state/app.svelte";
  import TimerPage from "./pages/TimerPage.svelte";
  import ProjectsPage from "./pages/ProjectsPage.svelte";
  import TimeOffPage from "./pages/TimeOffPage.svelte";
  import ReportsPage from "./pages/ReportsPage.svelte";
  import SettingsPage from "./pages/SettingsPage.svelte";

  void loadStateFromDB();
  startSyncEngine();

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
    <span class="logo">WorkTime</span>
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
    {#if !appState.loaded}
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
    font-weight: 700;
    font-size: 1.1rem;
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
</style>
