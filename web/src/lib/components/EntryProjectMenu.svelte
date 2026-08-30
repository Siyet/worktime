<script lang="ts">
  import { appState, projectByID, updateEntry } from "../state/app.svelte";
  import { t } from "../i18n";

  interface Props {
    entryID: string;
    projectID: string | null;
  }

  let { entryID, projectID }: Props = $props();
  let open = $state(false);
  let activeIndex = $state(0);
  let root = $state<HTMLElement | null>(null);
  const uid = $props.id();
  const menuID = `${uid}-entry-project-menu`;

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

  function openMenu(): void {
    activeIndex = Math.max(0, options.findIndex((option) => option.id === projectID));
    open = true;
  }

  function closeMenu(): void {
    open = false;
  }

  function choose(nextProjectID: string | null): void {
    closeMenu();
    if (nextProjectID !== projectID) void updateEntry(entryID, { project_id: nextProjectID });
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
      closeMenu();
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      activeIndex = Math.min(options.length - 1, activeIndex + 1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      activeIndex = Math.max(0, activeIndex - 1);
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      choose(options[activeIndex]!.id);
    } else if (event.key === "Tab") {
      closeMenu();
    }
  }

  function onDocumentClick(event: MouseEvent): void {
    if (open && root && !root.contains(event.target as Node)) closeMenu();
  }

  function onDocumentKeydown(event: KeyboardEvent): void {
    if (open && event.key === "Escape") closeMenu();
  }
</script>

<svelte:document onclick={onDocumentClick} onkeydown={onDocumentKeydown} />

<span class="entry-quick" bind:this={root}>
  <button
    type="button"
    class="entry-project-trigger proj"
    role="combobox"
    aria-label={t("Edit project")}
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-controls={menuID}
    onclick={() => (open ? closeMenu() : openMenu())}
    onkeydown={onTriggerKeydown}
  >
    {currentProject?.name ?? t("No project")}
  </button>
  {#if open}
    <span class="entry-quick-menu project-menu" id={menuID} role="listbox" aria-label={t("Project")}>
      {#each options as option, index (option.id)}
        <button
          type="button"
          role="option"
          aria-selected={option.id === projectID}
          class:active={index === activeIndex}
          onclick={() => choose(option.id)}
          onmouseenter={() => (activeIndex = index)}
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
