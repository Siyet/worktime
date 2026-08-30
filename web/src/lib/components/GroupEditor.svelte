<script lang="ts">
  // A group editor captures membership at open and writes that fixed set locally in
  // one transaction. Only grouping metadata can invalidate the draft; a remote Stop
  // or boundary edit is merged from the latest row at save time.
  import { untrack } from "svelte";
  import type { GroupEditSaveResult } from "../group-edit";
  import { t } from "../i18n";
  import { maxTextLength } from "../limits";
  import {
    appState,
    clock,
    entryTags,
    projectByID,
    updateEntries,
    type GroupEntryPatch,
  } from "../state/app.svelte";
  import { suggestionWindowStart, taskKey, taskSuggestions, type TaskGroupSnapshot } from "../tasks";
  import DescriptionInput from "./DescriptionInput.svelte";
  import ProjectSelect from "./ProjectSelect.svelte";
  import TagPicker from "./TagPicker.svelte";

  interface Props {
    snapshot: TaskGroupSnapshot;
    onclose: (result: GroupEditSaveResult | null) => void;
  }

  let { snapshot, onclose }: Props = $props();

  const initial = untrack(() => snapshot);
  let draftDescription = $state(initial.representativeDescription);
  let draftProjectID = $state(initial.projectID);
  let draftTags = $state([...initial.tags]);
  let saving = $state(false);
  let saveError = $state("");
  let savedResult: GroupEditSaveResult | null = null;
  let dialogElement = $state<HTMLDialogElement | null>(null);

  $effect(() => {
    if (dialogElement && !dialogElement.open) {
      dialogElement.showModal();
      requestAnimationFrame(() => dialogElement?.querySelector<HTMLInputElement>("#group-ed-desc")?.focus());
    }
  });

  const members = $derived(snapshot.entryIDs.map((id) => appState.entries.find((entry) => entry.id === id)));
  const groupChanged = $derived(
    members.some(
      (entry) =>
        entry === undefined || taskKey(entry.description, entry.project_id, entryTags(entry)) !== snapshot.key,
    ),
  );

  const activeProjects = $derived(
    appState.projects
      // Keep an archived current project visible and selected. It disappears
      // after the user chooses another project, matching the entry quick menu.
      .filter((project) => !project.archived || project.id === draftProjectID)
      .sort((a, b) => a.name.localeCompare(b.name)),
  );
  const suggestionCutoff = $derived(suggestionWindowStart(clock.now));
  const memberIDs = new Set(initial.entryIDs);
  const suggestionSource = $derived(
    appState.entries.filter(
      (entry) =>
        !memberIDs.has(entry.id) && (entry.stopped_at === null || entry.started_at >= suggestionCutoff),
    ),
  );
  const suggestions = $derived(taskSuggestions(suggestionSource, draftDescription, suggestionCutoff));

  function projectName(projectID: string | null): string {
    return projectByID(projectID)?.name ?? "";
  }

  function applySuggestion(suggestion: { projectID: string | null; tags: string[] }) {
    if (draftProjectID === null) draftProjectID = suggestion.projectID;
    if (draftTags.length === 0) draftTags = [...suggestion.tags];
  }

  function sameTags(left: string[], right: string[]): boolean {
    return left.length === right.length && left.every((tag, index) => tag === right[index]);
  }

  function buildPatch(): GroupEntryPatch {
    const patch: GroupEntryPatch = {};
    if (draftDescription !== snapshot.representativeDescription) patch.description = draftDescription;
    if (draftProjectID !== snapshot.projectID) patch.project_id = draftProjectID;
    if (!sameTags(draftTags, snapshot.tags)) patch.tags = [...draftTags];
    return patch;
  }

  async function save() {
    if (saving || groupChanged) return;
    saving = true;
    saveError = "";
    const patch = buildPatch();
    if (Object.keys(patch).length === 0) {
      dialogElement?.close();
      return;
    }
    try {
      const receipt = await updateEntries(snapshot.entryIDs, patch);
      savedResult = { receipt, patch };
      dialogElement?.close();
    } catch (error) {
      console.error("group edit failed", error);
      saveError = t("The group could not be saved locally. Try again.");
      saving = false;
    }
  }
</script>

<dialog
  class="sheet group-editor"
  bind:this={dialogElement}
  aria-labelledby="group-editor-title"
  aria-busy={saving}
  onclose={() => onclose(savedResult)}
  oncancel={(event) => {
    if (saving) event.preventDefault();
  }}
  onclick={(event) => {
    if (event.target === dialogElement && !saving) dialogElement?.close();
  }}
>
  <form
    method="dialog"
    onsubmit={(event) => {
      event.preventDefault();
      void save();
    }}
  >
    <div class="ed-head">
      <h3 id="group-editor-title">{t("Edit task group")}</h3>
      <span class="spacer"></span>
      <span class="group-count">{t("{n} entries", { n: snapshot.entryIDs.length })}</span>
    </div>

    <div class="ed-body">
      {#if groupChanged}
        <p class="ed-hint bad" role="alert">
          {t("This group changed on another device. Close and reopen it before editing.")}
        </p>
      {/if}
      {#if saveError}<p class="ed-hint bad" role="alert">{saveError}</p>{/if}

      <fieldset class="ed-fields" disabled={saving}>
        <div class="ed-field">
          <label class="ed-label" for="group-ed-desc">{t("Description")}</label>
          <DescriptionInput
            id="group-ed-desc"
            bind:value={draftDescription}
            {suggestions}
            {projectName}
            onpick={applySuggestion}
            maxlength={maxTextLength}
          />
          {#if draftDescription !== snapshot.representativeDescription}
            <p class="ed-hint">
              {t("A description different from an agent's automatic name prevents it from renaming the entries it currently owns.")}
            </p>
          {/if}
        </div>

        <div class="ed-field">
          <span class="ed-label">{t("Project")}</span>
          <div><ProjectSelect projects={activeProjects} bind:value={draftProjectID} /></div>
        </div>

        <div class="ed-field">
          <span class="ed-label">{t("Tags")} <span class="muted">{draftTags.length}/8</span></span>
          <TagPicker selected={draftTags} onchange={(tags) => (draftTags = tags)} />
        </div>

        <p class="ed-hint">
          {t("These {n} entries were selected when the dialog opened; newer matching entries are not changed.", {
            n: snapshot.entryIDs.length,
          })}
        </p>
        <p class="ed-hint">{t("Changes are saved on this device first; the sync indicator reports server delivery.")}</p>
      </fieldset>
    </div>

    <div class="ed-foot">
      <span class="spacer"></span>
      <button type="button" disabled={saving} onclick={() => dialogElement?.close()}>{t("Cancel")}</button>
      <button type="submit" class="primary" disabled={groupChanged || saving}>
        {saving ? t("Saving…") : t("Save")}
      </button>
    </div>
  </form>
</dialog>

<style>
  .group-count {
    color: var(--text-dim);
    font-size: 0.82rem;
    font-variant-numeric: tabular-nums;
  }

  .ed-fields {
    display: grid;
    gap: 0.75rem;
    min-width: 0;
    margin: 0;
    padding: 0;
    border: 0;
  }
</style>
