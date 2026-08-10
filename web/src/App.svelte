<script lang="ts">
  import { route } from "./lib/router.svelte";
  import { syncState } from "./lib/sync.svelte";
  import { startSyncEngine } from "./lib/sync.svelte";
  import { appState, loadStateFromDB } from "./lib/state/app.svelte";
  import { initSession, session } from "./lib/session.svelte";
  import { DEMO, seedDemoDataIfEmpty } from "./lib/demo";
  import { t } from "./lib/i18n";
  import Logo from "./lib/components/Logo.svelte";
  import UndoToast from "./lib/components/UndoToast.svelte";
  import TimerPage from "./pages/TimerPage.svelte";
  import ProjectsPage from "./pages/ProjectsPage.svelte";
  import TimeOffPage from "./pages/TimeOffPage.svelte";
  import ReportsPage from "./pages/ReportsPage.svelte";
  import PrintReportPage from "./pages/PrintReportPage.svelte";
  import SettingsPage from "./pages/SettingsPage.svelte";

  if (DEMO) {
    // Backend-less demo: seed local data once, never start sync or auth.
    void seedDemoDataIfEmpty().then(() => loadStateFromDB());
  } else {
    void loadStateFromDB();
    void initSession();
    startSyncEngine();
  }

  const signedOut = $derived(
    !DEMO && syncState.status === "unauthenticated" && session.checked && session.user === null,
  );

  const tabs = [
    { path: "/", label: "Timer" },
    { path: "/projects", label: "Projects" },
    { path: "/timeoff", label: "Time off" },
    { path: "/reports", label: "Reports" },
    { path: "/settings", label: "Settings" },
  ];

  const printRoute = $derived(route.path.startsWith("/reports/print"));

  const statusLabel = $derived(
    {
      idle: "…",
      syncing: t("syncing"),
      synced: t("synced"),
      offline: t("offline"),
      error: t("sync error"),
      unauthenticated: t("signed out"),
    }[syncState.status],
  );

  // On narrow screens the nav scrolls horizontally (ru/de labels overflow a
  // 360px viewport by a few dozen px); keep the active tab in view.
  $effect(() => {
    void route.path;
    document.querySelector("nav a.active")?.scrollIntoView({ block: "nearest", inline: "nearest" });
  });
</script>

{#if printRoute}
  {#if appState.loaded}
    <PrintReportPage />
  {:else}
    <p class="muted">{t("Loading…")}</p>
  {/if}
{:else}
<!-- One width for every page. Timer and Reports need the room for entry rows and
     report tables, and the rest could live in less - but a shell that changes
     size between pages moves the header and the navigation under the cursor on
     every click, which costs more than the extra whitespace on a short page. -->
<div class="shell">
  <header>
    <span class="logo"><Logo /><span class="wordmark">W<span class="logo-accent">T</span></span></span>
    <nav>
      {#each tabs as tab (tab.path)}
        <a href={"#" + tab.path} class:active={route.path === tab.path}>{t(tab.label)}</a>
      {/each}
    </nav>
    {#if DEMO}
      <span class="status" data-status="demo" title={t("Demo mode - data is stored only in this browser.")}>demo</span>
    {:else}
      <!-- Icon per state; the label stays in the DOM visually hidden, so
           screen readers and text assertions read the same words as before. -->
      <span
        class="status"
        data-status={syncState.status}
        title={statusLabel + (syncState.pendingCount > 0 ? ` (${syncState.pendingCount})` : "")}
      >
        {#if syncState.status === "synced"}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M20 6 9 17l-5-5" />
          </svg>
        {:else if syncState.status === "syncing" || syncState.status === "idle"}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M21 12a9 9 0 1 1-2.64-6.36" /><path d="M21 3v6h-6" />
          </svg>
        {:else if syncState.status === "offline"}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="m2 2 20 20" />
            <path d="M5.78 5.78A7 7 0 0 0 9 19h8.5a4.5 4.5 0 0 0 1.31-.19" />
            <path d="M21.53 16.5A4.5 4.5 0 0 0 17.5 10h-1.79A7 7 0 0 0 10 5.07" />
          </svg>
        {:else if syncState.status === "error"}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 20h16a2 2 0 0 0 1.73-2Z" />
            <path d="M12 9v4" /><path d="M12 17h.01" />
          </svg>
        {:else}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4" />
            <path d="m10 17 5-5-5-5" /><path d="M15 12H3" />
          </svg>
        {/if}
        <span class="sr-only">{statusLabel}{syncState.pendingCount > 0 ? " " : ""}</span>
        {#if syncState.pendingCount > 0}<span class="pending">({syncState.pendingCount})</span>{/if}
      </span>
    {/if}
  </header>

  <main>
    {#if signedOut}
      <div class="card signin">
        <h2>{t("Sign in to WorkTime")}</h2>
        {#if session.googleAvailable}
          <a class="google-button" href="/auth/google">{t("Sign in with Google")}</a>
        {:else}
          <p class="muted">
            Google sign-in is not configured. Set WORKTIME_GOOGLE_CLIENT_ID and
            WORKTIME_GOOGLE_CLIENT_SECRET on the server, or run with WORKTIME_DEV_AUTH=1 for local development.
          </p>
        {/if}
      </div>
    {:else if !appState.loaded}
      <p class="muted">{t("Loading…")}</p>
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

  <!-- Lives in the shell, not on a page: in-app navigation must not dismiss
       the 8-second undo window. -->
  <UndoToast />
</div>
{/if}

<style>
  .shell {
    max-width: 68rem;
    margin: 0 auto;
    padding: 0 1rem 3rem;
    padding-left: max(1rem, env(safe-area-inset-left));
    padding-right: max(1rem, env(safe-area-inset-right));
  }

  header {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.8rem 0;
    padding-top: max(0.8rem, env(safe-area-inset-top));
    flex-wrap: wrap;
  }

  .logo {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    font-weight: 700;
    font-size: 1.1rem;
  }

  .wordmark {
    letter-spacing: -0.08em;
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
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.8rem;
    color: var(--text-dim);
  }

  .status svg {
    display: block;
  }

  .status[data-status="error"],
  .status[data-status="unauthenticated"] {
    color: var(--danger);
  }

  .status[data-status="syncing"] svg {
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .status svg {
      animation: none;
    }
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

  /* Two-deck header below 34rem: logo + sync status on top, nav as one
     non-wrapping row with horizontal scroll as the safety valve. */
  @media (max-width: 34rem) {
    header {
      gap: 0.25rem 0.6rem;
    }

    .status {
      order: 2;
      margin-left: auto;
      white-space: nowrap;
    }

    nav {
      order: 3;
      flex: 1 0 100%;
      flex-wrap: nowrap;
      overflow-x: auto;
      scrollbar-width: none;
    }

    nav::-webkit-scrollbar {
      display: none;
    }

    nav a {
      padding: 0.3rem 0.55rem;
      font-size: 0.9rem;
      white-space: nowrap;
    }
  }
</style>
