<script lang="ts">
  import { appState, addTimeOff, deleteTimeOff } from "../lib/state/app.svelte";
  import { localDateISO } from "../lib/format";
  import type { TimeOffKind } from "../lib/types";

  let kind = $state<TimeOffKind>("vacation");
  let dateFrom = $state(localDateISO(Date.now()));
  let dateTo = $state(localDateISO(Date.now()));
  let note = $state("");

  const sorted = $derived([...appState.timeOff].sort((left, right) => right.date_from.localeCompare(left.date_from)));

  function inclusiveDays(from: string, to: string): number {
    return Math.round((Date.parse(to) - Date.parse(from)) / 86400000) + 1;
  }

  async function submitAdd(event: SubmitEvent) {
    event.preventDefault();
    if (dateTo < dateFrom) return;
    const submittedNote = note.trim();
    note = "";
    await addTimeOff(kind, dateFrom, dateTo, submittedNote);
  }
</script>

<form class="card" onsubmit={submitAdd}>
  <div class="row wrap">
    <select bind:value={kind} aria-label="Kind">
      <option value="vacation">Vacation</option>
      <option value="sick">Sick leave</option>
    </select>
    <input type="date" bind:value={dateFrom} aria-label="From" />
    <span class="muted">-</span>
    <input type="date" bind:value={dateTo} aria-label="To" />
    <input style="flex: 1; min-width: 8rem" placeholder="Note (optional)" bind:value={note} aria-label="Note" />
    <button class="primary" type="submit" disabled={dateTo < dateFrom}>Add</button>
  </div>
  {#if dateTo < dateFrom}
    <p class="muted">End date is before start date.</p>
  {/if}
</form>

<div class="card">
  {#each sorted as timeOff (timeOff.id)}
    <div class="row item">
      <span class="badge" data-kind={timeOff.kind}>{timeOff.kind === "sick" ? "Sick" : "Vacation"}</span>
      <span class="mono">{timeOff.date_from} - {timeOff.date_to}</span>
      <span class="muted">{inclusiveDays(timeOff.date_from, timeOff.date_to)}d</span>
      {#if timeOff.note}<span class="muted">{timeOff.note}</span>{/if}
      <span class="spacer"></span>
      <button class="danger" title="Delete" onclick={() => deleteTimeOff(timeOff.id)}>×</button>
    </div>
  {:else}
    <p class="muted">No sick leaves or vacations recorded.</p>
  {/each}
</div>

<style>
  .row.wrap {
    flex-wrap: wrap;
  }

  .item {
    padding: 0.35rem 0;
  }

  .badge {
    font-size: 0.8rem;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    border: 1px solid var(--border);
  }

  .badge[data-kind="vacation"] {
    background: color-mix(in srgb, var(--accent) 15%, transparent);
  }

  .badge[data-kind="sick"] {
    background: color-mix(in srgb, var(--danger) 15%, transparent);
  }
</style>
