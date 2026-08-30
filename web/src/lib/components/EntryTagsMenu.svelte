<script lang="ts">
  import { updateEntry } from "../state/app.svelte";
  import { t } from "../i18n";
  import TagChips from "./TagChips.svelte";
  import TagPicker from "./TagPicker.svelte";

  interface Props {
    entryID: string;
    tags: string[];
  }

  let { entryID, tags }: Props = $props();
  let open = $state(false);
  let root = $state<HTMLElement | null>(null);
  const uid = $props.id();
  const menuID = `${uid}-entry-tags-menu`;

  function closeMenu(): void {
    open = false;
  }

  function change(nextTags: string[]): void {
    void updateEntry(entryID, { tags: nextTags });
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
    class="entry-tags-trigger"
    aria-label={t(tags.length === 0 ? "Add tags" : "Edit tags")}
    aria-expanded={open}
    aria-controls={menuID}
    onclick={() => (open = !open)}
  >
    {#if tags.length > 0}
      <TagChips {tags} />
    {:else}
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="M20.6 13.6 11 23.2 1.8 14V1.8H14l6.6 6.6a3.7 3.7 0 0 1 0 5.2Z" /><circle cx="7" cy="7" r="1.4" />
      </svg>
    {/if}
  </button>
  {#if open}
    <span class="entry-quick-menu tags-menu" id={menuID} role="group" aria-label={t("Tags")}>
      <TagPicker selected={tags} onchange={change} />
    </span>
  {/if}
</span>

<style>
  .entry-quick {
    position: relative;
    display: inline-flex;
    min-width: 0;
  }

  .entry-tags-trigger {
    all: unset;
    display: inline-flex;
    align-items: center;
    border-radius: 4px;
    color: var(--text-dim);
    cursor: pointer;
  }

  .entry-tags-trigger:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }

  .entry-tags-trigger:active {
    color: var(--accent);
  }

  .entry-tags-trigger svg {
    display: block;
  }

  .entry-quick-menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    width: min(22rem, 85vw);
    padding: 0.65rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    z-index: 8;
  }

  @media (max-width: 34rem) {
    .entry-quick-menu {
      position: fixed;
      inset: auto 0 0 0;
      width: 100%;
      padding: 0.9rem 1rem max(0.9rem, env(safe-area-inset-bottom));
      border-radius: var(--radius) var(--radius) 0 0;
      z-index: 12;
    }
  }
</style>
