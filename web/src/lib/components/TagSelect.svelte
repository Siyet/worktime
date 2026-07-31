<script lang="ts">
  // Compact tag control for the timer start form: a ProjectSelect-style trigger
  // that opens the full TagPicker in a popover (bottom sheet below 34rem). The
  // start form stays one line; the flat picker grid lives in the editor dialog.
  import { t } from "../i18n";
  import TagPicker from "./TagPicker.svelte";

  let { selected = $bindable([]) }: { selected?: string[] } = $props();

  let open = $state(false);
  let root = $state<HTMLElement | null>(null);
  const uid = $props.id();
  const menuID = `${uid}-tagmenu`;

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
    class="tagselect"
    aria-label={t("Tags")}
    aria-expanded={open}
    aria-controls={menuID}
    onclick={() => (open = !open)}
  >
    {#if selected.length === 0}
      <span class="muted">{t("Tags")}</span>
    {:else}
      <span class="tag">{selected[0]}</span>
      {#if selected.length > 1}<span class="tag">+{selected.length - 1}</span>{/if}
    {/if}
    <span class="caret">▾</span>
  </button>

  {#if open}
    <button type="button" class="menu-scrim" aria-label={t("Close menu")} onclick={() => (open = false)}></button>
    <div class="tagmenu" id={menuID} role="group" aria-label={t("Tags")}>
      <TagPicker {selected} onchange={(tags) => (selected = tags)} />
    </div>
  {/if}
</span>
