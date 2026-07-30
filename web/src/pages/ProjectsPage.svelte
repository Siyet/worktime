<script lang="ts">
  import { appState, createProject, deleteProject, updateProject } from "../lib/state/app.svelte";
  import { t } from "../lib/i18n";
  import TrashIcon from "../lib/components/TrashIcon.svelte";

  // Design palette defaults: new projects cycle through these instead of a single blue.
  const paletteColors = ["#e8a33d", "#607dbe", "#d76a9b", "#40bec4", "#b57de8", "#46b478"];

  let newName = $state("");
  let newColor = $state(paletteColors[0]!);

  const sorted = $derived(
    [...appState.projects].sort((left, right) => {
      if (left.archived !== right.archived) return left.archived ? 1 : -1;
      return left.name.localeCompare(right.name);
    }),
  );

  async function submitCreate(event: SubmitEvent) {
    event.preventDefault();
    const name = newName.trim();
    if (!name) return;
    newName = "";
    await createProject(name, newColor);
    newColor = paletteColors[appState.projects.length % paletteColors.length]!;
  }
</script>

<form class="card row" onsubmit={submitCreate}>
  <input type="color" bind:value={newColor} aria-label={t("Project color")} />
  <input class="grow" placeholder={t("New project name")} bind:value={newName} aria-label={t("Project name")} />
  <button class="primary" type="submit">{t("Add")}</button>
</form>

<div class="card">
  {#each sorted as project (project.id)}
    <div class="row item" class:archived={project.archived}>
      <input
        type="color"
        value={project.color}
        aria-label={t("Color")}
        onchange={(event) => updateProject({ ...project, color: event.currentTarget.value })}
      />
      <input
        class="grow"
        value={project.name}
        aria-label={t("Name")}
        onchange={(event) => {
          const name = event.currentTarget.value.trim();
          if (name) updateProject({ ...project, name });
        }}
      />
      <button onclick={() => updateProject({ ...project, archived: !project.archived })}>
        {project.archived ? t("Unarchive") : t("Archive")}
      </button>
      <button class="danger icon" title={t("Delete project")} onclick={() => deleteProject(project.id)}>
        <TrashIcon />
      </button>
    </div>
  {:else}
    <p class="muted">{t("No projects yet.")}</p>
  {/each}
</div>

<style>
  .item {
    padding: 0.35rem 0;
  }

  .item.archived {
    opacity: 0.5;
  }

  /* min-width: 0 is the actual unclip: a text input's intrinsic minimum is
     ~170px (default size attribute) and flex-basis alone cannot shrink it. */
  .grow {
    flex: 1 1 auto;
    min-width: 0;
  }

  @media (max-width: 34rem) {
    input[type="color"] {
      width: 2.4rem;
      flex: 0 0 auto;
      padding: 0.15rem;
    }

    .item > button:not(.icon) {
      padding-inline: 0.6rem;
    }
  }
</style>
