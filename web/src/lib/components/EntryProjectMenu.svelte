<script lang="ts">
  import { tick } from "svelte";
  import { appState, projectByID, updateEntry } from "../state/app.svelte";
  import { t } from "../i18n";

  interface Props {
    entryID: string;
    projectID: string | null;
  }

  let { entryID, projectID }: Props = $props();
  let open = $state(false);
  let activeProjectKey = $state("none");
  let saving = $state(false);
  let root = $state<HTMLElement | null>(null);
  let triggerElement = $state<HTMLButtonElement | null>(null);
  const uid = $props.id();
  const menuID = `${uid}-entry-project-menu`;

  function optionID(projectOptionID: string | null): string {
    return `${menuID}-option-${projectOptionID ?? "none"}`;
  }

  function optionKey(projectOptionID: string | null): string {
    return projectOptionID ?? "none";
  }

  const currentProject = $derived(projectByID(projectID));
  const assignableProjects = $derived(
    appState.projects
      .filter((project) => !project.archived || project.id === projectID)
      .sort((left, right) => left.name.localeCompare(right.name)),
  );
  const options = $derived([
    { id: null as string | null, name: t("No project"), color: "var(--border)", archived: false },
    ...assignableProjects.map((project) => ({
      id: project.id as string | null,
      name: project.name,
      color: project.color,
      archived: project.archived,
    })),
  ]);
  const activeIndex = $derived(Math.max(0, options.findIndex((option) => optionKey(option.id) === activeProjectKey)));
  const activeOption = $derived(options[activeIndex]!);

  // A project may be archived or deleted by sync while this menu is open. Keep
  // the active descendant pointing at an option that still exists.
  $effect(() => {
    if (open && !options.some((option) => optionKey(option.id) === activeProjectKey)) {
      activeProjectKey = options.some((option) => option.id === projectID) ? optionKey(projectID) : "none";
    }
  });

  function openMenu(): void {
    if (saving) return;
    activeProjectKey = optionKey(projectID);
    open = true;
  }

  function closeMenu(restoreFocus = false): void {
    if (!open || saving) return;
    open = false;
    if (restoreFocus) queueMicrotask(() => triggerElement?.focus());
  }

  async function focusFreshTrigger(): Promise<void> {
    await tick();
    const replacement = [...document.querySelectorAll<HTMLButtonElement>("[data-entry-project-id]")].find(
      (candidate) => candidate.dataset.entryProjectId === entryID,
    );
    const fallback = document.querySelector<HTMLInputElement>('input[placeholder="What are you working on?"]');
    (replacement ?? fallback)?.focus();
  }

  async function choose(nextProjectID: string | null): Promise<void> {
    if (saving) return;
    if (nextProjectID === projectID) {
      open = false;
      await focusFreshTrigger();
      return;
    }

    saving = true;
    try {
      await updateEntry(entryID, { project_id: nextProjectID });
      open = false;
      await focusFreshTrigger();
    } catch {
      // A local write failure leaves the existing row authoritative. Keep the
      // menu open and usable so the same choice can be retried safely.
      activeProjectKey = optionKey(projectID);
    } finally {
      saving = false;
    }
  }

  function onTriggerKeydown(event: KeyboardEvent): void {
    if (!open && ["ArrowDown", "ArrowUp", "Enter", " "].includes(event.key)) {
      event.preventDefault();
      openMenu();
      return;
    }
    if (!open) return;
    if (event.key === "Escape") {
      event.preventDefault();
      closeMenu(true);
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      activeProjectKey = optionKey(options[Math.min(options.length - 1, activeIndex + 1)]!.id);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      activeProjectKey = optionKey(options[Math.max(0, activeIndex - 1)]!.id);
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      void choose(activeOption.id);
    } else if (event.key === "Tab") {
      closeMenu();
    }
  }

  function onDocumentClick(event: MouseEvent): void {
    if (open && root && !event.composedPath().includes(root)) closeMenu();
  }

  function onDocumentKeydown(event: KeyboardEvent): void {
    if (open && event.key === "Escape") {
      event.preventDefault();
      closeMenu(true);
    }
  }
</script>

<svelte:document onclick={onDocumentClick} onkeydown={onDocumentKeydown} />

<span class="entry-quick" bind:this={root}>
  <button
    bind:this={triggerElement}
    type="button"
    class="entry-project-trigger proj"
    role="combobox"
    aria-label={t("Edit project")}
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-controls={menuID}
    aria-activedescendant={open ? optionID(activeOption.id) : undefined}
    data-entry-project-id={entryID}
    disabled={saving}
    onclick={() => (open ? closeMenu() : openMenu())}
    onkeydown={onTriggerKeydown}
  >
    {currentProject?.name ?? t("No project")}
  </button>
  {#if open}
    <span class="entry-quick-menu project-menu" id={menuID} role="listbox" aria-label={t("Project")} aria-busy={saving}>
      {#each options as option, index (option.id)}
        <button
          id={optionID(option.id)}
          type="button"
          role="option"
          aria-selected={option.id === projectID}
          class:active={index === activeIndex}
          disabled={saving}
          onclick={() => void choose(option.id)}
          onmouseenter={() => (activeProjectKey = optionKey(option.id))}
        >
          <span class="dot" style="background: {option.color}"></span>
          <span>{option.name}</span>
          {#if option.archived}<span class="muted">({t("Archived")})</span>{/if}
        </button>
      {/each}
    </span>
  {/if}
</span>

<style>
  .entry-quick {
    position: relative;
    display: inline-flex;
    min-width: 0;
  }

  .entry-project-trigger {
    all: unset;
    display: block;
    max-width: 12rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    border-radius: 4px;
    cursor: pointer;
  }

  .entry-project-trigger:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }

  .entry-project-trigger:active {
    color: var(--accent);
  }

  .entry-quick-menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    min-width: 13rem;
    max-width: min(20rem, 85vw);
    max-height: 40dvh;
    overflow-y: auto;
    padding: 0.25rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    z-index: 8;
  }

  .entry-quick-menu button {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    border: none;
    background: transparent;
    text-align: left;
    white-space: nowrap;
  }

  .entry-quick-menu button.active {
    background: var(--hover);
  }

  @media (pointer: coarse) {
    .entry-quick-menu button {
      min-height: 2.6rem;
    }
  }
</style>
