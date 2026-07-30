<script lang="ts">
  // Chips as they appear on an entry row. Only `limit` of them fit before the right
  // column starts to suffer, so the rest collapse into a count. Rows with no tags
  // render nothing at all: about a third of them have none, and a badge on every one
  // of those is noise that carries no information.
  interface Props {
    tags: string[];
    limit?: number;
  }

  let { tags, limit = 1 }: Props = $props();

  const shown = $derived(tags.slice(0, limit));
  const hidden = $derived(Math.max(0, tags.length - limit));
</script>

{#if tags.length > 0}
  <span class="tags" title={tags.join(", ")}>
    {#each shown as tag (tag)}
      <span class="tag">{tag}</span>
    {/each}
    {#if hidden > 0}
      <span class="tag">+{hidden}</span>
    {/if}
  </span>
{/if}
