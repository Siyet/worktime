<script lang="ts">
  // Description field with task suggestions taken from what the page already
  // shows. Not a <datalist>: that fires no "a suggestion was picked" event, and
  // picking one is exactly what has to fill in the project and the tags. It also
  // cannot be ordered, matched or themed, so this follows ProjectSelect instead.
  import { t } from "../i18n";
  import type { TaskSuggestion } from "../tasks";

  interface Props {
    value: string;
    suggestions: TaskSuggestion[];
    onpick?: (suggestion: TaskSuggestion) => void;
    placeholder?: string;
    ariaLabel?: string;
    id?: string;
    maxlength?: number;
    projectName?: (projectID: string | null) => string;
  }

  let {
    value = $bindable(""),
    suggestions,
    onpick,
    placeholder = "",
    ariaLabel = "Description",
    id,
    maxlength,
    projectName,
  }: Props = $props();

  // "The user is typing and has not dismissed the list", not "the list is
  // visible": whether anything is worth showing is derived from the current
  // value and suggestions, which the input event alone cannot know yet - the
  // binding updates the parent after the handler runs.
  let typing = $state(false);
  let activeIndex = $state(-1);
  let root = $state<HTMLElement | null>(null);
  const uid = $props.id();
  const listID = `${uid}-list`;
  const optionID = (index: number) => `${uid}-opt-${index}`;

  const open = $derived(typing && value.trim() !== "" && suggestions.length > 0);

  function close() {
    typing = false;
    activeIndex = -1;
  }

  // Closing on blur would break picking with the mouse: blur fires before the
  // click on the option. ProjectSelect solves it the same way.
  function onDocumentClick(event: MouseEvent) {
    if (open && root && !root.contains(event.target as Node)) close();
  }

  function pick(index: number) {
    const suggestion = suggestions[index];
    if (suggestion === undefined) return;
    value = suggestion.description;
    close();
    onpick?.(suggestion);
  }

  function onInput() {
    // The list only ever opens from typing (or an explicit ArrowDown): on a
    // narrow screen the form wraps to three rows and a popover on focus would
    // cover the Start button.
    typing = true;
    activeIndex = -1;
  }

  function onKeydown(event: KeyboardEvent) {
    // An IME composition session sends Enter to accept the candidate.
    if (event.isComposing) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (!open) {
        typing = true;
        activeIndex = 0;
        return;
      }
      activeIndex = Math.min(suggestions.length - 1, activeIndex + 1);
      return;
    }
    if (!open) return;
    if (event.key === "ArrowUp") {
      event.preventDefault();
      activeIndex = Math.max(0, activeIndex - 1);
    } else if (event.key === "Enter") {
      // Without an active option the form must still submit on Enter, which is
      // how the timer is started.
      if (activeIndex < 0) return;
      event.preventDefault();
      pick(activeIndex);
    } else if (event.key === "Escape") {
      // Inside the entry editor the dialog is a native <dialog>: Escape reaches
      // it as a cancel, so closing the list has to consume the key.
      event.preventDefault();
      event.stopPropagation();
      close();
    } else if (event.key === "Tab") {
      close();
    }
  }
</script>

<svelte:document onclick={onDocumentClick} />

<span class="dinput" bind:this={root}>
  <input
    {id}
    {placeholder}
    {maxlength}
    bind:value
    role="combobox"
    aria-label={t(ariaLabel)}
    aria-autocomplete="list"
    aria-expanded={open}
    aria-controls={listID}
    aria-activedescendant={open && activeIndex >= 0 ? optionID(activeIndex) : undefined}
    autocomplete="off"
    oninput={onInput}
    onkeydown={onKeydown}
  />
  {#if open && suggestions.length > 0}
    <ul class="menu" id={listID} role="listbox" aria-label={t("Recent tasks")}>
      {#each suggestions as suggestion, index (suggestion.description)}
        {@const project = projectName?.(suggestion.projectID) ?? ""}
        <li role="option" aria-selected={index === activeIndex}>
          <button
            type="button"
            class:active={index === activeIndex}
            id={optionID(index)}
            onclick={() => pick(index)}
            onmouseenter={() => (activeIndex = index)}
          >
            <span class="stext">{suggestion.description}</span>
            <!-- The project and tags are shown because picking fills them in;
                 a silent substitution would be a surprise. -->
            {#if project || suggestion.tags.length > 0}
              <span class="smeta muted">
                {project}{project && suggestion.tags.length > 0 ? " · " : ""}{suggestion.tags.join(", ")}
              </span>
            {/if}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</span>

<style>
  .dinput {
    position: relative;
    display: flex;
    flex: 1;
    min-width: 0;
  }

  .dinput > input {
    width: 100%;
    min-width: 0;
  }

  .menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    max-height: 40dvh;
    overflow-y: auto;
    overscroll-behavior: contain;
    margin: 0;
    padding: 0.25rem;
    list-style: none;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    z-index: 6;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  }

  .menu button {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    width: 100%;
    border: none;
    background: transparent;
    text-align: left;
    padding: 0.35rem 0.6rem;
    border-radius: 5px;
  }

  .menu button.active {
    background: var(--hover);
  }

  .stext {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .smeta {
    margin-left: auto;
    font-size: 0.8rem;
    white-space: nowrap;
  }

  @media (pointer: coarse) {
    .menu button {
      min-height: 2.6rem;
    }
  }
</style>
