<script lang="ts">
  import { appState, addTimeOff, deleteTimeOff } from "../lib/state/app.svelte";
  import { formatDateISO, localDateISO } from "../lib/format";
  import { t } from "../lib/i18n";
  import Seg from "../lib/components/Seg.svelte";
  import TrashIcon from "../lib/components/TrashIcon.svelte";
  import type { TimeOffKind } from "../lib/types";

  let kind = $state<string>("vacation");
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
    await addTimeOff(kind as TimeOffKind, dateFrom, dateTo, submittedNote);
  }
</script>

<form class="card" onsubmit={submitAdd}>
  <div class="row wrap">
    <Seg
      options={[
        { value: "vacation", label: t("Vacation") },
        { value: "sick", label: t("Sick leave") },
        { value: "dayoff", label: t("Day off") },
      ]}
      bind:value={kind}
    />
    <input type="date" bind:value={dateFrom} aria-label={t("From")} />
    <span class="muted">-</span>
    <input type="date" bind:value={dateTo} aria-label={t("To")} />
    <input style="flex: 1; min-width: 8rem" placeholder={t("Note (optional)")} bind:value={note} aria-label={t("Note")} />
    <button class="primary" type="submit" disabled={dateTo < dateFrom}>{t("Add")}</button>
  </div>
  {#if dateTo < dateFrom}
    <p class="muted">{t("End date is before start date.")}</p>
  {/if}
</form>

<div class="card">
  {#each sorted as timeOff (timeOff.id)}
    <div class="row item">
      <span class="badge" data-kind={timeOff.kind}>
        {t(timeOff.kind === "sick" ? "Sick" : timeOff.kind === "dayoff" ? "Day off" : "Vacation")}
      </span>
      <span class="mono">{formatDateISO(timeOff.date_from)} - {formatDateISO(timeOff.date_to)}</span>
      <span class="muted">{inclusiveDays(timeOff.date_from, timeOff.date_to)}d</span>
      {#if timeOff.note}<span class="muted">{timeOff.note}</span>{/if}
      <span class="spacer"></span>
      <button class="danger icon" title={t("Delete")} onclick={() => deleteTimeOff(timeOff.id)}><TrashIcon /></button>
    </div>
  {:else}
    <p class="muted">{t("No sick leaves or vacations recorded.")}</p>
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
    background: color-mix(in srgb, var(--teal) 20%, transparent);
    border-color: var(--teal);
  }

  .badge[data-kind="sick"] {
    background: color-mix(in srgb, var(--danger) 20%, transparent);
    border-color: var(--danger);
  }

  .badge[data-kind="dayoff"] {
    background: color-mix(in srgb, var(--purple) 20%, transparent);
    border-color: var(--purple);
  }
</style>
