<script lang="ts">
  // Toast with Undo for a deleted entry or stopped timers. Mounted in the app
  // shell, not on a page: navigating inside the PWA must not kill the 8-second
  // undo window.
  import { displayEntryDescription } from "../format";
  import { t } from "../i18n";
  import { dismissUndo, undoLast, undoState } from "../state/undo.svelte";

  const single = $derived(undoState.deleted ?? (undoState.stopped.length === 1 ? undoState.stopped[0]! : null));
  const what = $derived(
    single === null
      ? t("{n} timers", { n: undoState.stopped.length })
      : displayEntryDescription(single) || t("(no description)"),
  );
</script>

{#if undoState.deleted !== null || undoState.stopped.length > 0}
  <div class="toast" role="status">
    <span>{t(undoState.deleted ? "Deleted" : "Stopped")}</span>
    <span class="what">{what}</span>
    <button type="button" onclick={() => void undoLast()}>{t("Undo")}</button>
    <button type="button" class="icon" aria-label={t("Dismiss")} onclick={dismissUndo}>✕</button>
  </div>
{/if}
