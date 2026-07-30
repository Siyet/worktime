<script lang="ts">
  // Kebab menu for an entry row: an absolutely positioned popover on wide
  // screens (the ProjectSelect idiom), a fixed bottom sheet with a scrim below
  // 34rem. Same markup, CSS-only difference - see .rowmenu in app.css.
  import { t } from "../i18n";

  interface Props {
    onedit: () => void;
    ondelete: () => void;
  }

  let { onedit, ondelete }: Props = $props();

  let open = $state(false);
  let root = $state<HTMLElement | null>(null);
  const uid = $props.id();
  const menuID = `${uid}-menu`;

  function onDocumentClick(event: MouseEvent) {
    if (open && root && !root.contains(event.target as Node)) {
      open = false;
    }
  }

  function onDocumentKeydown(event: KeyboardEvent) {
    if (open && event.key === "Escape") {
      open = false;
    }
  }
</script>

<svelte:document onclick={onDocumentClick} onkeydown={onDocumentKeydown} />

<span class="menu-wrap" bind:this={root}>
  <button
    type="button"
    class="kebab icon"
    aria-label={t("Entry actions")}
    aria-haspopup="menu"
    aria-expanded={open}
    aria-controls={menuID}
    onclick={() => (open = !open)}
  >
    <svg width="15" height="15" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <circle cx="12" cy="5" r="1.7" />
      <circle cx="12" cy="12" r="1.7" />
      <circle cx="12" cy="19" r="1.7" />
    </svg>
  </button>

  {#if open}
    <!-- Scrim is display:none above 34rem; outside-click there is handled by
         the svelte:document handler. -->
    <button type="button" class="menu-scrim" aria-label={t("Close menu")} onclick={() => (open = false)}></button>

    <div class="rowmenu" id={menuID} role="menu">
      <button
        type="button"
        role="menuitem"
        onclick={() => {
          open = false;
          onedit();
        }}
      >
        <svg
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.8"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="M12 20h9" /><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
        </svg>
        {t("Edit")}
      </button>
      <div class="sep"></div>
      <!-- No confirm step: deletes are soft tombstones, so Undo is one LWW
           write - see the toast in the app shell. -->
      <button
        type="button"
        role="menuitem"
        class="danger"
        onclick={() => {
          open = false;
          ondelete();
        }}
      >
        <svg
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.8"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="M3 6h18" /><path d="M8 6V4h8v2" /><path d="M6 6l1 14h10l1-14" />
        </svg>
        {t("Delete")}
      </button>
    </div>
  {/if}
</span>
