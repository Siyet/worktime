<script lang="ts">
  // A flat toggle grid rather than a popover: this lives inside a modal editor, where a
  // popover would be a stacking-context fight for no benefit, and every option stays a
  // real tap target with no hover involved.
  import { maxTagsPerEntry, normaliseTag, tagCatalogue } from "../state/app.svelte";
  import { t } from "../i18n";

  interface Props {
    selected: string[];
    onchange: (tags: string[]) => void;
  }

  let { selected, onchange }: Props = $props();

  let filter = $state("");

  const catalogue = $derived(tagCatalogue());
  const query = $derived(normaliseTag(filter));
  const visible = $derived(query === "" ? catalogue : catalogue.filter((tag) => tag.name.includes(query)));
  // Matching is case-insensitive through normaliseTag, so typing "Review" toggles the
  // existing "review" instead of minting a near-duplicate that splits a report group.
  const canCreate = $derived(query !== "" && !catalogue.some((tag) => tag.name === query));
  const atLimit = $derived(selected.length >= maxTagsPerEntry);

  function toggle(name: string): void {
    if (selected.includes(name)) {
      onchange(selected.filter((tag) => tag !== name));
      return;
    }
    if (atLimit) return;
    onchange([...selected, name].sort());
  }

  function create(): void {
    if (!canCreate || atLimit) return;
    onchange([...selected, query].sort());
    filter = "";
  }

  function onFilterKey(event: KeyboardEvent): void {
    if (event.key !== "Enter") return;
    event.preventDefault();
    if (canCreate) {
      create();
      return;
    }
    const exact = visible.find((tag) => tag.name === query);
    if (exact) {
      toggle(exact.name);
      filter = "";
    }
  }
</script>

<input
  class="tagpick-filter"
  placeholder={t("Filter or create a tag…")}
  aria-label={t("Tags")}
  bind:value={filter}
  onkeydown={onFilterKey}
/>

<div class="tagpick">
  {#each visible as tag (tag.name)}
    {@const on = selected.includes(tag.name)}
    <button
      type="button"
      class="tag"
      class:on
      aria-pressed={on}
      disabled={!on && atLimit}
      onclick={() => toggle(tag.name)}
    >
      {tag.name}
    </button>
  {/each}
  {#if visible.length === 0 && !canCreate}
    <span class="muted">{t("No tags yet.")}</span>
  {/if}
</div>

{#if canCreate}
  <div class="tagpick-add">
    <button type="button" onclick={create} disabled={atLimit}>
      {t("Create tag {name}", { name: query })}
    </button>
  </div>
{/if}

{#if atLimit}
  <p class="muted">{t("At most {count} tags per entry.", { count: String(maxTagsPerEntry) })}</p>
{/if}
