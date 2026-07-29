<script lang="ts">
  import { t } from "../i18n";
  import { formatDayISO, isWeekend } from "../report";
  import type { TimeOffKind } from "../types";

  export interface ChartSlice {
    key: string;
    name: string;
    color: string;
    minutes: number;
  }

  export interface ChartDay {
    date: string;
    slices: ChartSlice[];
  }

  let {
    days,
    off,
    avgMinutes,
    avgCaption = "",
    selected = null,
    interactive = true,
    hourUnit = "h",
    onselect,
  }: {
    days: ChartDay[];
    off: Map<string, TimeOffKind>;
    avgMinutes: number;
    avgCaption?: string;
    selected?: string | null;
    interactive?: boolean;
    hourUnit?: string;
    onselect?: (day: string | null) => void;
  } = $props();

  const uid = $props.id();
  const hatchID = `${uid}-hatch`;

  const WIDTH = 760;
  const HEIGHT = 260;
  const PAD_LEFT = 36;
  const PAD_TOP = 14;
  const PAD_BOTTOM = 20;
  const baselineY = HEIGHT - PAD_BOTTOM;
  const plotHeight = baselineY - PAD_TOP;

  const dayTotals = $derived(days.map((day) => day.slices.reduce((sum, slice) => sum + slice.minutes, 0)));
  const yMax = $derived(Math.ceil(Math.max(120, ...dayTotals) / 120) * 120);
  const columnWidth = $derived((WIDTH - PAD_LEFT) / Math.max(1, days.length));
  const barWidth = $derived(Math.min(columnWidth * 0.72, 22));
  const labelStep = $derived(Math.max(1, Math.ceil(days.length / 14)));
  const gridLines = $derived.by(() => {
    const lines: number[] = [];
    for (let level = 0; level <= yMax; level += 120) lines.push(level);
    return lines;
  });

  function yOf(minutes: number): number {
    return baselineY - (plotHeight * minutes) / yMax;
  }

  const bars = $derived(
    days.map((day, index) => {
      const x = PAD_LEFT + index * columnWidth + (columnWidth - barWidth) / 2;
      let top = baselineY;
      const rects: { y: number; height: number; color: string }[] = [];
      for (const slice of day.slices) {
        if (!slice.minutes) continue;
        const height = (plotHeight * slice.minutes) / yMax;
        top -= height;
        rects.push({ y: top, height, color: slice.color });
      }
      return { date: day.date, x, rects };
    }),
  );

  const BAND_COLORS: Partial<Record<TimeOffKind, string>> = {
    vacation: "rgba(64,190,196,0.15)",
    dayoff: "rgba(181,125,232,0.17)",
  };

  function dayKindLabel(dayISO: string): string {
    const kind = off.get(dayISO);
    if (kind) return t(kind === "vacation" ? "vacation" : kind === "sick" ? "sick leave" : "day off");
    return isWeekend(dayISO) ? t("weekend") : t("work day");
  }

  let tip = $state<{ x: number; y: number; index: number } | null>(null);

  function onMove(event: MouseEvent, index: number) {
    if (!interactive) return;
    tip = {
      x: Math.min(event.clientX + 14, window.innerWidth - 210),
      y: event.clientY + 16,
      index,
    };
  }

  function formatMinutes(minutes: number): string {
    return `${Math.floor(minutes / 60)}${hourUnit} ${String(Math.round(minutes % 60)).padStart(2, "0")}m`;
  }
</script>

<svg class="chart" viewBox="0 0 {WIDTH} {HEIGHT}" role="img" aria-label={t("Daily tracked hours")}>
  <defs>
    <pattern id={hatchID} width="7" height="7" patternTransform="rotate(45)" patternUnits="userSpaceOnUse">
      <rect width="7" height="7" fill="rgba(224,82,82,0.10)" />
      <line x1="0" y1="0" x2="0" y2="7" stroke="rgba(224,82,82,0.38)" stroke-width="1.6" />
    </pattern>
  </defs>

  {#each days as day, index (day.date)}
    {@const bandX = PAD_LEFT + index * columnWidth}
    {@const kind = off.get(day.date)}
    {#if kind === "sick"}
      <rect x={bandX} y={PAD_TOP} width={columnWidth} height={plotHeight} fill="url(#{hatchID})" />
    {:else if kind}
      <rect x={bandX} y={PAD_TOP} width={columnWidth} height={plotHeight} fill={BAND_COLORS[kind]} />
    {:else if isWeekend(day.date)}
      <rect x={bandX} y={PAD_TOP} width={columnWidth} height={plotHeight} fill="rgba(96,125,190,0.13)" />
    {/if}
  {/each}

  {#each gridLines as level (level)}
    <line x1={PAD_LEFT} y1={yOf(level)} x2={WIDTH} y2={yOf(level)} stroke="var(--grid)" />
    {#if level > 0}
      <text x={PAD_LEFT - 6} y={yOf(level) + 3.5} text-anchor="end" font-size="10" fill="var(--text-dim)">
        {level / 60}{hourUnit}
      </text>
    {/if}
  {/each}

  {#each bars as bar, index (bar.date)}
    {#each bar.rects as rect (rect.y)}
      <!-- The outline keeps dark project colors readable against the dark background:
           without it a dark segment inside a stack looks like a gap in the bar. -->
      <rect
        x={bar.x}
        y={rect.y}
        width={barWidth}
        height={rect.height}
        rx="1.5"
        fill={rect.color}
        stroke="var(--axis)"
        stroke-width="0.6"
      />
    {/each}
    {#if index % labelStep === 0}
      <text x={bar.x + barWidth / 2} y={HEIGHT - 5} text-anchor="middle" font-size="9.5" fill="var(--text-dim)">
        {Number(bar.date.slice(8))}
      </text>
    {/if}
    {#if selected === bar.date}
      <rect
        x={PAD_LEFT + index * columnWidth + 0.5}
        y={PAD_TOP}
        width={columnWidth - 1}
        height={plotHeight}
        fill="none"
        stroke="var(--accent)"
        stroke-width="1.4"
        rx="3"
      />
    {/if}
  {/each}

  {#if avgMinutes > 0}
    <line
      x1={PAD_LEFT}
      y1={yOf(avgMinutes)}
      x2={WIDTH}
      y2={yOf(avgMinutes)}
      stroke="var(--accent)"
      stroke-width="1.3"
      stroke-dasharray="6 5"
      opacity="0.75"
    />
    <text x={WIDTH - 4} y={yOf(avgMinutes) - 5} text-anchor="end" font-size="10" fill="var(--accent)">
      {avgCaption}
    </text>
  {/if}

  <line x1={PAD_LEFT} y1={baselineY} x2={WIDTH} y2={baselineY} stroke="var(--axis)" />

  {#if interactive}
    {#each days as day, index (day.date)}
      <rect
        class="hit"
        x={PAD_LEFT + index * columnWidth}
        y={PAD_TOP}
        width={columnWidth}
        height={plotHeight}
        fill="transparent"
        role="button"
        tabindex="0"
        aria-label={t("Filter by {day}", { day: formatDayISO(day.date) })}
        onmousemove={(event) => onMove(event, index)}
        onmouseleave={() => (tip = null)}
        onclick={() => onselect?.(selected === day.date ? null : day.date)}
        onkeydown={(event) => {
          if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            onselect?.(selected === day.date ? null : day.date);
          }
        }}
      />
    {/each}
  {/if}
</svg>

{#if tip && days[tip.index]}
  {@const tipDay = days[tip.index]!}
  {@const tipTotal = dayTotals[tip.index]!}
  {@const breakdown = tipDay.slices.filter((slice) => slice.minutes > 0)}
  <div class="tip" style="left: {tip.x}px; top: {tip.y}px">
    <b>{formatDayISO(tipDay.date)}</b><br />
    {tipTotal ? formatMinutes(tipTotal) : t("no tracking")}
    {#if breakdown.length > 1}
      {#each breakdown as slice (slice.key)}
        <br />{slice.name}: {formatMinutes(slice.minutes)}
      {/each}
    {/if}
    <br /><span class="muted">{dayKindLabel(tipDay.date)}</span>
  </div>
{/if}

<style>
  .chart {
    width: 100%;
    display: block;
    font-family: var(--mono);
  }

  .hit {
    cursor: pointer;
  }

  .tip {
    position: fixed;
    pointer-events: none;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 0.45rem 0.65rem;
    border-radius: 8px;
    font-family: var(--mono);
    font-size: 0.78rem;
    line-height: 1.45;
    z-index: 10;
    white-space: nowrap;
    box-shadow: 0 6px 22px rgba(0, 0, 0, 0.45);
  }
</style>
