<script lang="ts">
  // Deleted-entry toast with Undo. Mounted in the app shell, not on a page:
  // navigating inside the PWA must not kill the 8-second undo window.
  import { t } from "../i18n";
  import { dismissUndo, undoDelete, undoState } from "../state/undo.svelte";
</script>

{#if undoState.deleted}
  <div class="toast" role="status">
    <span>{t("Deleted")}</span>
    <span class="what">{undoState.deleted.description || t("(no description)")}</span>
    <button type="button" onclick={() => void undoDelete()}>{t("Undo")}</button>
    <button type="button" class="icon" aria-label={t("Dismiss")} onclick={dismissUndo}>✕</button>
  </div>
{/if}
