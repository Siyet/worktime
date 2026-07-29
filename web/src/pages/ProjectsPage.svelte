<script lang="ts">
  import { appState, createProject, deleteProject, updateProject } from "../lib/state/app.svelte";
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
  <input type="color" bind:value={newColor} aria-label="Project color" />
  <input style="flex: 1" placeholder="New project name" bind:value={newName} aria-label="Project name" />
  <button class="primary" type="submit">Add</button>
</form>

<div class="card">
  {#each sorted as project (project.id)}
    <div class="row item" class:archived={project.archived}>
      <input
        type="color"
        value={project.color}
        aria-label="Color"
        onchange={(event) => updateProject({ ...project, color: event.currentTarget.value })}
      />
      <input
        style="flex: 1"
        value={project.name}
        aria-label="Name"
        onchange={(event) => {
          const name = event.currentTarget.value.trim();
          if (name) updateProject({ ...project, name });
        }}
      />
      <button onclick={() => updateProject({ ...project, archived: !project.archived })}>
        {project.archived ? "Unarchive" : "Archive"}
      </button>
      <button class="danger icon" title="Delete project" onclick={() => deleteProject(project.id)}><TrashIcon /></button>
    </div>
  {:else}
    <p class="muted">No projects yet.</p>
  {/each}
</div>

<style>
  .item {
    padding: 0.35rem 0;
  }

  .item.archived {
    opacity: 0.5;
  }
</style>
