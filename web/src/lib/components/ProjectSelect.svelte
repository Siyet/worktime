<script lang="ts">
  import type { Project } from "../types";

  let {
    projects,
    value = $bindable(null),
    label = "Project",
  }: {
    projects: Project[];
    value?: string | null;
    label?: string;
  } = $props();

  let open = $state(false);
  let activeIndex = $state(0);
  let root = $state<HTMLElement | null>(null);
  const uid = $props.id();
  const menuID = `${uid}-menu`;

  // "No project" is always the first option.
  const options = $derived([
    { id: null as string | null, name: "No project", color: "var(--border)" },
    ...projects.map((project) => ({ id: project.id as string | null, name: project.name, color: project.color })),
  ]);
  const selected = $derived(options.find((option) => option.id === value) ?? options[0]!);

  function openMenu() {
    activeIndex = Math.max(
      0,
      options.findIndex((option) => option.id === value),
    );
    open = true;
  }

  function choose(id: string | null) {
    value = id;
    open = false;
  }

  function onTriggerKeydown(event: KeyboardEvent) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp" || event.key === "Enter" || event.key === " ") {
      if (!open) {
        event.preventDefault();
        openMenu();
        return;
      }
    }
    if (!open) return;
    if (event.key === "Escape") {
      open = false;
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
      open = false;
    }
  }

  function onDocumentClick(event: MouseEvent) {
    if (open && root && !root.contains(event.target as Node)) {
      open = false;
    }
  }
</script>

<svelte:document onclick={onDocumentClick} />

<span class="pselect" bind:this={root}>
  <button
    type="button"
    role="combobox"
    aria-label={label}
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-controls={menuID}
    onclick={() => (open ? (open = false) : openMenu())}
    onkeydown={onTriggerKeydown}
  >
    <span class="dot" style="background: {selected.color}"></span>
    <span class="plabel">{selected.name}</span>
    <span class="caret">▾</span>
  </button>
  {#if open}
    <span class="menu" id={menuID} role="listbox" aria-label="{label} options">
      {#each options as option, index (option.id)}
        <button
          type="button"
          role="option"
          aria-selected={option.id === value}
          class:active={index === activeIndex}
          onclick={() => choose(option.id)}
          onmouseenter={() => (activeIndex = index)}
        >
          <span class="dot" style="background: {option.color}"></span>{option.name}
        </button>
      {/each}
    </span>
  {/if}
</span>

<style>
  .pselect {
    position: relative;
    display: inline-block;
  }

  .pselect > button {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    max-width: 14rem;
  }

  .plabel {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .caret {
    color: var(--text-dim);
    font-size: 0.7rem;
  }

  .menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    min-width: 100%;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.25rem;
    z-index: 5;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    display: block;
  }

  .menu button {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    border: none;
    background: transparent;
    text-align: left;
    padding: 0.35rem 0.6rem;
    border-radius: 5px;
    white-space: nowrap;
  }

  .menu button.active {
    background: var(--hover);
  }
</style>
