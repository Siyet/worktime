<script lang="ts">
  // Toast with Undo for a deleted entry or a stopped timer. Mounted in the app
  // shell, not on a page: navigating inside the PWA must not kill the 8-second
  // undo window.
  import { displayEntryDescription } from "../format";
  import { t } from "../i18n";
  import { dismissUndo, undoLast, undoState } from "../state/undo.svelte";

  const pending = $derived(undoState.deleted ?? undoState.stopped);
</script>

{#if pending}
  <div class="toast" role="status">
    <span>{t(undoState.deleted ? "Deleted" : "Stopped")}</span>
    <span class="what">{displayEntryDescription(pending) || t("(no description)")}</span>
    <button type="button" onclick={() => void undoLast()}>{t("Undo")}</button>
    <button type="button" class="icon" aria-label={t("Dismiss")} onclick={dismissUndo}>✕</button>
  </div>
{/if}
