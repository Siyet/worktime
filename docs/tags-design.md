# Теги и редактирование записи: принятый дизайн

Проработано панелью из четырёх независимых вариантов (mobile-first, density-first, report-fidelity, minimal-change), каждый разобран тремя враждебными судьями, победитель синтезирован с привитыми идеями остальных. Рейтинг: mobile-first 5.3, density-first 4.3, minimal-change 4.3, report-fidelity 4.0.

## Решения

### Модель данных

Cross-cutting data model, settled before the nine: tags are `tags: string[]` of lowercase names on the `time_entries` row itself. No `tags` table, no join table, no tag ids.

**Почему:** LWW resolves per row on `updated_at`, so a tag edit must be one row write - identical to editing a description, offline-safe with zero new sync machinery; a new synced table would also break `nextSeq = lastSeq - totalRows + 1` (sync.go:32, hand-counted over exactly three tables) and force an IndexedDB version bump.

### Вопрос 1

Single neutral treatment, no per-tag hue ever. Chip = 1px `var(--border)`, transparent fill, `var(--text-dim)` text, lowercase, 0.8rem. `var(--accent)` appears only as the selected/filter-on state, which is what accent already means in `.seg button.on` and `.filterpill`. Untagged is marked by shape - dashed border plus italic - never by colour.

**Почему:** Hue in an entry row already means project (15 user-picked hex colours via `paletteColors`), and teal/purple/danger are bound to vacation/dayoff/sick on Time off and in the DailyChart legend, so a third colour axis on the same row informs nothing - and the first cut's `color-mix(in srgb, var(--tag) 72%, var(--text))` lands green at roughly 3:1 in the light theme, below AA for 12px text.

### Вопрос 2

Chips on entry rows: exactly one chip plus a `+N` button, placed in a second meta line next to the project name, never in the description line. Full list in the editor.

**Почему:** 371 of 10835 entries carry 2+ tags, so one chip is the whole truth on 96.6% of rows, and a mistagged row is only ever noticed on the timeline - but the chip must sit below the right-hand time cluster's baseline so a long tag can never push the duration column.

### Вопрос 3

Split. An entry with k tags contributes `billable(entry)/k` to each; k=0 goes whole into an always-present `untagged` bucket that is distinct from the user tag `other`. Weights per entry sum to exactly 1 under every grouping, so `Σ groups = Σ entries billable(entry) = tableTotal`, independent of `groupBy`. Detail rows print their weighted share with a `1/k` marker. A `Total` row is added to the table. `Entries` counts an entry once per group it appears in and the Total row shows the distinct count.

**Почему:** Counting once per tag makes the by-tag table exceed the grand total, which is indefensible in a sheet a manager adds up, and splitting is the rule this codebase already committed to in `splitOverlapMinutes` - one sentence covers both.

### Вопрос 4

Both, split by role: creation only in the picker (Enter or Add, normalised to trimmed/collapsed/lowercase/24 chars, matched case-insensitively so `Review` toggles the existing `review`), and a Tags block in Settings that only renames, merges and removes-from-all-entries, each with a count-bearing confirm.

**Почему:** Tagging has to be a sub-second offline action so creation must live in the picker, but six years of on-the-fly creation across 10835 entries guarantees near-duplicates that silently fragment a report group, and with names-as-values rename/merge is the only repair tool.

### Вопрос 5

No pills in print. Tags render as a dim `·`-separated trail after the description in Детализация, plus a new `По тегам` figure between `По проектам` and `Детализация` with single-ink bars (`rgba(20,26,40,0.55)`), `без тега` italic, hatched and always last, and an `Итого` row added to both the project and the tag table. The figure is suppressed entirely when no entry in the range carries a tag.

**Почему:** A 1px pill border with a tinted fill at 7.5pt prints as mud on an office laser and five hues become five identical greys, whereas bar length and row order are exactly the channels that survive greyscale - and all 10835 existing entries are untagged, so historical reports must not grow an empty section.

### Вопрос 6

Native `<dialog>` with `showModal()`: bottom sheet below 34rem, centred 32rem card above it, one markup block. Not inline. The "see the neighbours" argument is answered inside the dialog by a live boundary line - `prev ends 11:00 · gap 15m · next starts 13:30`, with an `overlaps 20m` danger variant.

**Почему:** The sheet grows upward so the focused field stays above the mobile keyboard and Save/Cancel stay in the thumb zone, and the native dialog gives focus trap, Escape, `::backdrop` and background `inert` with no JS - while the boundary line delivers the neighbour information more precisely than looking at the rows.

### Вопрос 7

The full editor: description, project, tags, date, From, To. For a running entry To is empty with a dim `running` placeholder and typing a time stops it retroactively; a secondary `Stop now` fills in the current time. `To` can never be cleared - a finished entry cannot be reopened. Start is clamped to `<= now` for running entries. If the row is stopped elsewhere while the editor is open, the editor swaps to the finished layout with a dim notice instead of saving its snapshot.

**Почему:** "I forgot to stop at 17:00" is the main reason to open a running entry, and `toReportEntries` skips `stopped_at === null`, so a clearable To would let the editor silently delete hours from every report, the CSV and the printed sheet.

### Вопрос 8

Free-text mono field, `inputmode="numeric"`, `maxlength=5`, always 24h with a `24h` label. Accepts `9`, `930`, `0930`, `9:30`, `9.30`; normalises on blur. ArrowUp/Down nudge 1 minute, Shift+Arrow 15. Unparseable sets `aria-invalid`, a `var(--danger)` border, `—` in the duration readout and disables Save. End date is expressed by a `+Nd` stepper next to To in the `.seg` idiom, so a midnight-crossing entry is representable and editable. Date stays `input[type=date]` plus `-1d`/`+1d`. Duration is read-only.

**Почему:** `input[type=time]` follows the browser locale and ignores this app's own `hourCycle` preference, refuses `930`, and cannot express an end date - and the `+Nd` stepper is what stops a 23:40-00:20 entry from being permanently unsaveable or from writing `stopped_at < started_at`, which `validateChanges` rejects for the whole batch.

### Вопрос 9

No confirm for entries. Delete applies immediately and a toast rises with `Deleted · <description> · Undo`, 8s, `role="status"`, a real >=2.4rem button, living in the app shell so in-app navigation does not dismiss it. Tag removal in Settings keeps a count-bearing confirm.

**Почему:** The kebab already costs two taps and deletes are already soft `deleted_at` tombstones, so undo is one ordinary LWW write with a fresh `updated_at` that wins on every device and works offline - whereas removing a tag from 412 entries is hundreds of row rewrites and has no cheap undo.

### Вопрос 10

Row layout stays `.row.item` flex - no subgrid, no `pointer-events` tricks, no absolutely positioned whole-row hit target. Three parts: `.dot`, `.main{flex:1 1 auto;min-width:0}` (description button + meta line with project and chips), `.when{flex:0 0 auto}` (duration + range, mono/tabular-nums, `.dur` at `min-width:4.4rem`), kebab. Below 34rem `.when` stacks with the duration on top and hides the end time; the description clamps to 2 lines. The description text itself is a `<button class="desc">` that opens the editor, so editing is one tap without breaking HTML validity or tab order.

**Почему:** `.spacer` cannot protect the duration column once the left side wraps, but a fixed-basis `.when` next to a `min-width:0` `.main` is stable by construction - and a subgrid rewrite breaks the running row (explicit `.stop` placement collides with the duration track and spills it to an implicit second row).

### Вопрос 11

Sync hardening ships with the feature: `updateEntry(id, patch)` replaces whole-row writes, `mergeServerRows` skips rows that still hold a dirty marker, pushes chunk at 2000 rows, and an HTTP 400 triggers a one-row-per-request retry that quarantines the offending row into `meta.rejected` and surfaces `N rejected` in the header.

**Почему:** Without these, a tag written offline dies to its own echo (`row.updated_at >= local.updated_at` lets the server win a tie), an MCP `stop_timer` blanks tags via a whole-row push, and one rejected row wedges every pending change on the device forever behind a silent `sync error`.

## Сходимость сумм

ONE PER-ENTRY NUMBER, ONE WEIGHT FUNCTION

Everything reconciles because exactly one number is defined per entry and the weights that distribute it always sum to 1.

billable(entry):
- overlaps-once off: roundMinutes(entry.minutes, rounding)
- overlaps-once on:  splitOverlapMinutes(dateRangeEntries).get(entry.id), and rounding is off (the Seg is disabled)

Rounding and overlaps-once are mutually exclusive by design. Rounding a share that was deliberately halved re-inflates the overlap the option removes - roundMinutes has a one-step minimum (Math.max(step, ...)), so 15-minute shares with 1h rounding all become 60 minutes. exportCSV already passes `overlapOnce ? 0 : rounding` for precisely this reason; the UI is being brought in line with what the export already does.

groupKeysOf(entry, groupBy):
- project / day / description: exactly one key (projectID ?? NO_PROJECT_KEY, entry.date, description)
- tag: entry.tags when non-empty, otherwise [UNTAGGED_KEY]

Every entry has at least one key under every grouping. Weight of an entry inside one of its groups is billable(entry) / groupKeysOf(entry).length.

THE IDENTITY

  tableTotal
    = Σ over groups Σ over entries in group  billable(entry) / k(entry)
    = Σ over entries  billable(entry) × (k(entry) / k(entry))
    = Σ over entries  billable(entry)

The inner sum collapses because each entry appears in exactly k of its groups with weight 1/k. So tableTotal does not depend on groupBy: switching Group by between Project, Tag, Day and Description never changes the header total. That is the property the previous overlaps-once code did not have (it rounded group sums, so 15 project groups and 30 day groups produced different totals), and it is fixed here for all groupings at once.

WHAT EQUALS WHAT

With rounding off and overlaps-once off - the default state - billable(entry) = entry.minutes, so all of these are the same number:

- the "total tracked" stat card (totalMinutes)
- Σ By project
- Σ By tag, including the untagged bucket
- the Report table header and its Total row
- Σ Duration column of the exported CSV
- fmtHoursRu(totalMinutes) on the printed sheet, and the Итого row of both "По проектам" and "По тегам"

With rounding on, the table and the CSV move together to Σ roundMinutes(entry.minutes, step) and the stat cards stay unrounded - which is the existing contract (rounding lives inside the Custom report card and has never applied to the stat cards or the By project panel).

DETAIL ROWS

A detail row under a tag group prints its weighted share, billable(entry)/k, with a dim `1/k` marker when k > 1. Detail rows therefore sum to their group header exactly. Printing the entry's full duration there - what the code does today at ReportsPage.svelte:463 - would show a 1h row under a 30m header and the same entry twice, which is precisely where a reader checks the arithmetic.

THE UNTAGGED BUCKET

UNTAGGED_KEY is a first-class group and a first-class filter chip, mirroring NO_PROJECT_KEY. Roughly a third of the real 10835 entries will have no tags; without a key of their own they would match no active tag chip and silently vanish from totalMinutes, avgMinutes, peakDay, the DailyChart, By project, the table and the CSV. It is kept distinct from the user tag "other": "I classified this as other" and "I never tagged this" are different facts.

FILTER INDEPENDENCE

The split denominator is the entry's own tag count, never the number of tags active in the filter. Toggling a tag chip therefore never rewrites another tag's number - the same rule already documented for overlap shares, which are computed on dateRangeEntries before the project filter.

WHAT DOES NOT RECONCILE, STATED IN THE UI

The Entries column is a count, not a total. An entry with two tags is one entry in each of its two tag groups, so the group counts sum above the distinct count. The Total row shows the distinct count, and a dim caption under the table names the multi-tag count (371 on the real data) and says exactly this. Avg / entry is group.minutes / group.entries.length, which stays meaningful per group.

PRINT DISPLAY ROUNDING

fmtRu rounds each group independently, so displayed values can drift from the displayed Итого and percentages can sum to 99 or 102. Both printed tables run their values through apportion() (largest-remainder allocation against the displayed total) so displayed minutes sum exactly to Итого and displayed percentages sum exactly to 100. Without this, adding the Итого row would merely make the existing drift visible.

## Печатный отчёт

TREATMENT

No pills anywhere on the sheet. A 1px pill border with a tinted fill at 7.5pt fills with toner on any office laser, and a 16% tint of five different hues lands within a few percent of the same grey - five chips print as five identical blobs. Tags carry their meaning as text, and the only graphical channel used is bar length, which is exactly what survives greyscale. Since the app's chips are already neutral, nothing has to be translated for print.

Three additions, all conditional on the range containing at least one tagged entry - all 10835 existing entries are untagged, and a report for last March must not grow an empty section.

1. Детализация rows gain a dim tag trail after the description. Same 7.5pt as the existing .note, no borders, wraps with the cell.
2. A new "По тегам" figure between "По проектам" and "Детализация", structurally identical to "По проектам" but with no dot column and a single-ink bar. "без тега" is italic, hatched and always last: three redundant cues, and if background graphics are switched off in the print dialog the label and the ordering still carry it.
3. An "Итого" row on BOTH tables. This is the highest-value line on the sheet: it is what lets a reader add the column up and check that the tag breakdown and the project breakdown reconcile against the same number, without doing arithmetic.

Because the Итого row invites exactly that arithmetic, the displayed values must actually sum to it. fmtRu rounds each group independently, so 15 project groups can drift several minutes from the total and three equal groups render as 17%+17%+17%. Both tables therefore run their values through apportion() (largest-remainder) before display: displayed group minutes sum exactly to the displayed Итого, and displayed percentages sum exactly to 100.

The detail list stays grouped by project and each row shows the entry's full billable minutes, so the detail rows reconcile against "По проектам". The tag table shows split shares. The note states the split rule, so both reconciliation paths on the sheet are labelled rather than left to be discovered as a discrepancy.

MARKUP

<!-- new figure, after "По проектам" -->
{#if byTag.length > 0}
  <div class="figure">
    <h2>По тегам</h2>
    <table>
      <thead>
        <tr>
          <th style="width: 8rem">Тег</th>
          <th aria-label="Доля"></th>
          <th class="num" style="width: 5rem">Время</th>
          <th class="num" style="width: 2.6rem">%</th>
        </tr>
      </thead>
      <tbody>
        {#each byTagDisplay as row (row.key)}
          <tr>
            <td class:untagged-label={row.key === UNTAGGED_KEY}>
              {row.key === UNTAGGED_KEY ? "без тега" : row.key}
            </td>
            <td>
              <div class="pbar">
                <div
                  class={row.key === UNTAGGED_KEY ? "hatch" : "ink"}
                  style="width: {row.pct}%"
                ></div>
              </div>
            </td>
            <td class="num">{fmtRu(row.minutes)}</td>
            <td class="num">{row.pct}%</td>
          </tr>
        {/each}
        <tr class="sumrow">
          <td>Итого</td>
          <td></td>
          <td class="num">{fmtRu(totalMinutes)}</td>
          <td class="num">100%</td>
        </tr>
      </tbody>
    </table>
  </div>
{/if}

<!-- same sumrow added to the existing "По проектам" tbody -->
<tr class="sumrow">
  <td>Итого</td><td></td>
  <td class="num">{fmtRu(totalMinutes)}</td>
  <td class="num">100%</td>
</tr>

<!-- Детализация detail row gains the trail -->
<tr class="entry">
  <td>
    {entry.description || "(без описания)"}
    {#if entry.tags.length > 0}<span class="tagtrail">{entry.tags.join(" · ")}</span>{/if}
  </td>
  <td class="num">{dayShort(entry.date)}</td>
  <td class="num">{startTime(entry.startedAt)}</td>
  <td class="num">{fmtRu(minutesOf(entry))}</td>
</tr>

<!-- .note gains one sentence, only when the tag figure is present -->
{#if byTag.length > 0}
  Запись с несколькими тегами делит своё время между ними поровну, поэтому суммы
  по тегам и по проектам совпадают с общим итогом.
{/if}

ROUTE CONTRACT

openPrint must pass the tag filter, or the sheet prints different numbers than the screen it was printed from:

  const params = new URLSearchParams({ from: dateFrom, to: dateTo });
  if (disabledProjects.size > 0) params.set("projects", [...activeProjectKeys].join(","));
  if (disabledTags.size > 0) params.set("tags", [...activeTagKeys].join(","));
  if (overlapOnce) params.set("overlap", "1");

PrintReportPage parses tags= the same way it parses projects= and applies the same any-of match, with UNTAGGED_KEY serialised as the literal token "__untagged" (it cannot collide: user tags are normalised to lowercase without leading underscores).

## Заметки по реализации

STORE MIGRATION (internal/store/store.go)

Append migration 003 - never edit an existing entry:

  ALTER TABLE time_entries ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';

TEXT holding a JSON array, NOT NULL with a DEFAULT so the ON CONFLICT path works for all 10835 pre-existing rows. No tags table and no join table: a fourth synced table would break `nextSeq = lastSeq - totalRows + 1` (sync.go:32 sums exactly three slices - omit tags there and rows get duplicate server_seq, which permanently strands a client whose cursor lands on the duplicate), would need its own push ordering guarantee (sync.go:44 documents why projects go first), and would force an IndexedDB version bump.

WIRE FORMAT (internal/store/dtos.go)

  Tags []string `json:"tags"`

on TimeEntry. The DTO is the API schema, so this is a wire change for existing offline clients: old clients ignore the field, new clients treat a missing field as []. Always initialise to []string{} when scanning so it marshals as [] and never null.

Marshal/unmarshal at the SQL boundary only - json.Marshal on write, json.Unmarshal on scan into a []string, empty string and NULL both mapping to []string{}.

SYNC (internal/store/sync.go) - the checklist, because every miss compiles fine and silently wipes tags

1. INSERT column list and the VALUES placeholder for time_entries.
2. `tags = excluded.tags` in the ON CONFLICT DO UPDATE SET list. Without this, inserts carry tags and every update drops them.
3. The pull SELECT at sync.go:118 and its Scan.
4. queries.go ListRunningEntries SELECT + Scan.
5. queries.go GetTimeEntry SELECT + Scan. This one is what MCP stop_timer reads before pushing the whole row back (mcpserver/server.go:220), so a miss here means every MCP stop blanks that entry's tags and broadcasts the blanking with a fresh server_seq.
6. validateChanges: at most 8 tags per entry, each 1..24 chars, lowercase, trimmed, no duplicates. Three writers exist (PWA, MCP, API tokens), so the limits cannot be client-only.

Also fix the poison-row wedge while here, because the editor is the first path by which a user can author a row the server rejects: validateChanges failing returns ErrInvalidInput for the WHOLE batch before the transaction opens, and syncOnce never clears dirty markers on error - one bad row blocks every pending change on that device forever, surfacing only as "sync error".

INDEXEDDB (web/src/lib/db.ts)

No version bump - tags ride on the existing time_entries object store, and openDB("worktime", 1) stays as is.

mergeServerRows must skip rows that still hold a dirty marker. Add "dirty" to the transaction store list and skip when a marker exists for `${table}:${row.id}`. Today the merge condition is `row.updated_at >= local.updated_at`, so the server wins a tie - and the push echo comes back in the same response (pull is server_seq > since) with an identical updated_at. Against a server that has not been migrated yet, that means a tag dies milliseconds after it is created, silently. A pending local edit always beats the echo of itself; the echo carries no new information.

sync.svelte.ts:
- chunk the push at 2000 rows per request and loop until the queue drains. A Settings merge across 3600 entries plus anything else offline crosses maxBatchRows = 10000 (sync.go:15) and returns 400 on every subsequent sync.
- on HTTP 400, retry the batch as single-row requests, move the row(s) that still fail into meta.rejected, exclude them from later pushes and surface "N rejected" in the header. Without this the wedge above is permanent.

STATE (web/src/lib/state/app.svelte.ts)

- `updateEntry(id: string, patch: Partial<TimeEntry>)` replaces the whole-row signature. It reads the current row from appState.entries at call time and merges the patch, so no caller can write a stale snapshot. This is what stops the editor from resurrecting an entry that was stopped underneath it, and stops any caller that predates tags from blanking them.
- stopTimer patches only { stopped_at, updated_at } instead of spreading the whole entry.
- add `restoreEntry(id)`: clears deleted_at with a fresh updated_at. That is the whole undo implementation - the tombstone loses LWW on every device and it works offline.
- `entryTags(entry) = entry.tags ?? []` is the single accessor. tags is optional on the TimeEntry interface (`tags?: string[]`) because 10835 existing IndexedDB rows and every row pulled from a pre-migration server have no such key; typing it as a required array would pass `npm run check` and then throw on first render.
- the tag catalogue is one $derived.by pass over appState.entries producing { name, count }, unioned with SEED_TAGS = ["analysis", "development", "meeting", "other", "review"], sorted by count desc then name. One pass over 10835 rows is nothing and runes memoise it.
- normalise(name): trim, collapse inner whitespace, lowercase, slice to 24. Applied on create and on rename; matching against the catalogue is case-insensitive so `Review` toggles the existing `review` rather than minting a twin.
- tags are stored sorted alphabetically, so which one lands in the row's single chip is predictable.
- renameTag / mergeTag / removeTagEverywhere rewrite the affected entries through updateEntry in chunks; each is an ordinary LWW row write, offline-safe, and the chunked push keeps them under the batch limit.

report.ts

- ReportEntry gains `tags: string[]`, filled from entryTags(entry) in toReportEntries so it is always concrete.
- export const UNTAGGED_KEY = "__untagged" - user tags are normalised to lowercase and cannot begin with an underscore, so no collision.
- export function groupKeysOf(entry, groupBy): string[] - one key for project/day/description, entry.tags or [UNTAGGED_KEY] for tag. This replaces the single `key()` closure in tableGroups, which cannot express one entry landing in several groups.
- export function apportion(values: number[], total: number): number[] - largest-remainder allocation so displayed values sum exactly to Math.round(total). Used by both printed tables and by the % columns.
- buildCSV gains a Tags column (semicolon-joined, "(untagged)" when empty) and its minutesFor contract is documented as billable-per-entry: a CSV row is an entry, not a group, so it never carries a share. With rounding disabled under overlaps-once, `Σ CSV Duration = tableTotal` exactly, which the current `overlapOnce ? 0 : rounding` special case was already reaching for.

ReportsPage.svelte

- disabledKeys splits into disabledProjects and disabledTags; rangeEntries filters on both, AND-ed; each strip keeps at least one chip active.
- tableGroups iterates groupKeysOf and accumulates billable(entry) / keys.length, and carries per-entry rows as { entry, share, of } so detail rows can print their weighted contribution.
- distinctEntryCount and multiTagCount feed the Total row and the reconciliation caption.
- openPrint adds tags= (see print_treatment).

E2E AND DESIGN BUNDLE

- timers.spec.ts:36, timers.spec.ts:123 and conflicts.spec.ts:81 use getByTitle("Delete entry") on the row. Rewrite to: click the kebab, click Delete, then assert the row is gone and the toast is present; the conflicts test additionally needs the toast dismissed or the Undo path asserted. Register pushBarrier before the delete, never wait for "synced".
- Rebuild bin/worktime (make build) before running e2e - the suite tests the embedded build.
- Mirror the global CSS into design/_shared.css, update design/components/timer-rows.html to the new row markup, rewrite design/components/tags.html (drop the per-tag hues and the hover-only "+1" span) and design/components/row-actions.html (drop Duplicate, the ellipsis glyph, the bare select), then push the bundle back through DesignSync.
- Run go vet ./..., cd web && npm run check, go test ./... and the e2e suite.

## Принятые риски

- A 60-minute meeting tagged meeting+review reports as 30m under each - a number the user never entered. It is the price of reconciliation and the same price overlaps-once already charges; it bites 371 of 10835 entries (3.4%), and the 1/k marker plus the table caption are the only mitigation. If the split turns out to be unacceptable in practice, the fallback is a second grouping mode ("count each tag fully") that is explicitly excluded from the Total row - not a change to the split.
- Rounding and overlaps-once are now mutually exclusive. Anyone who had both switched on loses the combination, and the Seg going disabled on a checkbox tick needs the hint text to land or it reads as a bug.
- No colour on tags means a tag-heavy day is scanned by reading, not by pattern. There is no escape hatch: a user who wants a tag to be visually distinct has to promote it to a project. Requests for tag colours cannot be granted without reopening decision 1, because the palette is genuinely exhausted.
- Undo is an 8-second window in the app shell. Closing the PWA or hard-navigating inside it still loses the affordance; the row is recoverable only from another device that has not yet pulled the tombstone, or by hand from the server DB. No "recently deleted" list ships.
- Rename and merge rewrite every affected entry, and updated_at moves on the whole row. A merge touching 3600 entries clobbers concurrent description or time edits made on another device that have not yet been pulled. The chunked push keeps it under the batch limit but does nothing about that.
- The tag string list is one LWW unit, so two devices tagging the same entry concurrently resolve as last-writer-takes-the-whole-set, not as a union - one device's added tag disappears. Identical to how description already behaves, and the cost of not having a join table.
- The +Nd end-day stepper makes overnight entries representable, but it is a control users will not look for. An entry typed as 23:40 to 00:20 without touching the stepper gets its offset bumped automatically, and there is no signal beyond the live duration readout that this happened.
- dvh, dialog::backdrop and inset on a modal dialog are all well supported now, but the sheet layout leans on them entirely. On an engine that ignores max-height: 92dvh the sheet can grow past the viewport with its footer off-screen.
- The Итого row invites arithmetic that apportion() makes correct for minutes and percentages, but the Детализация list still shows full durations while По тегам shows shares. The note explains it; a reader who adds only the detail rows of one tag will still get a different number.
- The row-body tap target is the description text only. Tapping the project name or the tag chip does nothing on a row whose description is empty, where the target collapses to the "(no description)" placeholder - narrow, and the kebab is the only fallback.
- "Rounding is off while overlaps are counted once" and the reconciliation caption are the two places the design explains itself in prose. Both need translating into all six locales, and both will be the first things to rot if the aggregation rules are touched again.

## CSS

```css
/* ============================================================================
   1. web/src/app.css  +  design/_shared.css   (global, additive)
   Mirror every rule into both files - the design workflow keeps them in sync.
   Tokens used: --surface --text --text-dim --border --accent --accent-text
   --danger --hover --radius --mono. No new tokens, no new hues.
   ========================================================================== */

/* --- tag chip --------------------------------------------------------------
   One neutral treatment. Hue in an entry row already means "project" (15 of
   them, user-picked), and teal/purple/danger already mean vacation/dayoff/sick
   on Time off. A tag is read, not decoded. Transparent fill so the chip keeps
   the contrast of whatever surface it sits on (text-dim on --surface is 6.8:1
   dark / 4.75:1 light - AA for 12px text; a tinted fill would drop it under). */
.tag {
  display: inline-block;
  max-width: 9rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: baseline;
  padding: 0.05rem 0.45rem;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: transparent;
  color: var(--text-dim);
  font-size: 0.8rem;
  line-height: 1.45;
}

/* Absence is a shape, not a colour: survives greyscale, print and CVD. */
.tag.untagged {
  border-style: dashed;
  font-style: italic;
}

/* --accent is this system's only "chosen" signal (.seg button.on, .filterpill). */
.tag.on {
  border-color: var(--accent);
  color: var(--text);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}

.tag.off {
  opacity: 0.4;
}

/* Interactive chips (picker toggles, the row's +N). button{} base rules are
   specificity 0,0,1 so .tag wins; .tag.on is 0,2,0 so it beats button:hover. */
button.tag {
  cursor: pointer;
  font-family: inherit;
  min-height: 1.6rem;
}

button.tag:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}

@media (pointer: coarse) {
  button.tag {
    min-height: 2.1rem;
    padding: 0.25rem 0.6rem;
  }
}

.tags {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  min-width: 0;
}

/* --- entry row -------------------------------------------------------------
   Still .row.item flex. .main takes the slack with min-width:0, .when has a
   fixed basis, so no description or tag list can push the right column. */
.item .main {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 0.05rem;
}

/* One-tap edit on the biggest target in the row. A real button: valid HTML,
   correct tab order (description, then kebab), no pointer-events tricks. */
.item .desc {
  all: unset;
  display: block;
  cursor: pointer;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 4px;
}

.item .desc:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.item .desc:active {
  color: var(--accent);
}

.item .meta {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  min-width: 0;
  font-size: 0.85rem;
}

.item .meta .proj {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 0 1 auto;
}

.item .when {
  flex: 0 0 auto;
  display: flex;
  align-items: baseline;
  gap: 0.7rem;
  font-family: var(--mono);
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.item .when .dur {
  min-width: 4.4rem;
}

@media (max-width: 34rem) {
  .item .when {
    flex-direction: column;
    align-items: flex-end;
    gap: 0;
  }

  /* The end time is derivable from the next row's start; it goes first. */
  .item .when .to {
    display: none;
  }

  .item .desc {
    white-space: normal;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
  }
}

/* --- kebab ---------------------------------------------------------------- */
.kebab {
  padding: 0.3rem 0.45rem;
  min-width: 2.2rem;
  min-height: 2.2rem;
  line-height: 1;
  color: var(--text-dim);
  background: transparent;
  border-color: transparent;
}

.kebab:hover {
  color: var(--text);
  border-color: var(--border);
}

/* Open state must read without hover - mobile has none. */
.kebab[aria-expanded="true"] {
  color: var(--text);
  border-color: var(--border);
  background: var(--hover);
}

@media (pointer: coarse) {
  .kebab {
    min-width: 2.75rem;
    min-height: 2.75rem;
  }
}

/* --- row menu: popover on wide, bottom sheet below 34rem ------------------ */
.menu-wrap {
  position: relative;
  display: inline-flex;
}

.rowmenu {
  position: absolute;
  right: 0;
  top: calc(100% + 0.25rem);
  z-index: 30;
  min-width: 10rem;
  padding: 0.25rem;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.28);
}

.rowmenu button {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  width: 100%;
  min-height: 2.4rem;
  padding: 0.4rem 0.55rem;
  border: none;
  border-radius: 6px;
  background: transparent;
  text-align: left;
}

.rowmenu button:hover {
  background: var(--hover);
}

.rowmenu button:active {
  background: var(--hover);
}

.rowmenu button.danger:active {
  background: color-mix(in srgb, var(--danger) 14%, transparent);
}

.rowmenu .sep {
  height: 1px;
  margin: 0.25rem 0.1rem;
  background: var(--border);
}

.rowmenu svg {
  display: block;
  opacity: 0.85;
}

.menu-scrim {
  display: none;
}

@media (max-width: 34rem) {
  .rowmenu {
    position: fixed;
    inset: auto 0 0 0;
    z-index: 31;
    min-width: 0;
    padding: 0.4rem 0.4rem max(0.4rem, env(safe-area-inset-bottom));
    border-radius: var(--radius) var(--radius) 0 0;
    border-bottom: none;
  }

  .rowmenu button {
    min-height: 3rem;
    font-size: 1rem;
  }

  .menu-scrim {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 30;
    background: rgba(0, 0, 0, 0.45);
    border: none;
    padding: 0;
  }
}

/* --- delete toast: undo instead of a confirm ------------------------------ */
.toast {
  position: fixed;
  left: 50%;
  transform: translateX(-50%);
  bottom: max(1rem, env(safe-area-inset-bottom));
  z-index: 50;
  display: flex;
  align-items: center;
  gap: 0.7rem;
  max-width: calc(100vw - 1.5rem);
  padding: 0.4rem 0.4rem 0.4rem 0.9rem;
  background: var(--surface);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
}

.toast .what {
  max-width: 14rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-dim);
  font-size: 0.88rem;
}

.toast button {
  min-height: 2.4rem;
  padding: 0.35rem 0.9rem;
  white-space: nowrap;
}

/* --- entry editor: native dialog, sheet below 34rem, card above ----------- */
dialog.sheet {
  position: fixed;
  inset: auto 0 0 0;
  margin: 0;
  padding: 0;
  width: 100%;
  max-width: none;
  max-height: 92dvh;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  color: var(--text);
  border: 1px solid var(--border);
  border-bottom: none;
  border-radius: var(--radius) var(--radius) 0 0;
  box-shadow: 0 -8px 32px rgba(0, 0, 0, 0.35);
}

dialog.sheet::backdrop {
  background: rgba(0, 0, 0, 0.5);
}

@media (min-width: 34rem) {
  dialog.sheet {
    inset: 0;
    margin: auto;
    width: 32rem;
    max-height: 88dvh;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.4);
  }
}

.sheet .ed-head {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.8rem 1rem 0.5rem;
}

.sheet .ed-head h3 {
  margin: 0;
  font-size: 0.95rem;
}

.sheet .ed-body {
  flex: 1 1 auto;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 0.3rem 1rem 0.9rem;
  display: grid;
  gap: 0.75rem;
}

.sheet .ed-foot {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.7rem 1rem max(0.7rem, env(safe-area-inset-bottom));
  border-top: 1px solid var(--border);
  background: var(--surface);
}

.sheet .ed-foot button {
  min-height: 2.6rem;
}

.ed-field {
  display: grid;
  gap: 0.25rem;
}

.ed-label {
  font-size: 0.72rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-dim);
}

.ed-line {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}

/* Why the edit is happening, without hiding it behind the backdrop. */
.ed-neighbours {
  display: flex;
  align-items: center;
  gap: 0.5rem 0.9rem;
  flex-wrap: wrap;
  margin: 0;
  font-size: 0.8rem;
  color: var(--text-dim);
}

.ed-neighbours b {
  font-family: var(--mono);
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  color: var(--text);
}

.ed-neighbours .gap.bad {
  color: var(--danger);
}

.timefield {
  width: 5.25rem;
  padding: 0.4rem 0.5rem;
  text-align: center;
  font-family: var(--mono);
  font-variant-numeric: tabular-nums;
}

.timefield[aria-invalid="true"] {
  border-color: var(--danger);
}

.timefield:disabled {
  opacity: 0.45;
}

.ed-hint {
  margin: 0;
  font-size: 0.78rem;
  color: var(--text-dim);
}

.ed-hint.bad {
  color: var(--danger);
}

.ed-calc {
  font-family: var(--mono);
  font-variant-numeric: tabular-nums;
  color: var(--text-dim);
  min-width: 5rem;
}

/* --- tag picker: toggle grid, no popover inside a modal ------------------- */
.tagpick {
  display: flex;
  flex-wrap: wrap;
  gap: 0.35rem;
  max-height: 9rem;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 0.45rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
}

.tagpick:focus-within {
  border-color: var(--accent);
}

.tagpick-add {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  margin-top: 0.35rem;
}

.tagpick-add input {
  flex: 1 1 auto;
  min-width: 6rem;
  font-size: 0.9rem;
}

.tagpick-filter {
  margin-bottom: 0.35rem;
  width: 100%;
  font-size: 0.9rem;
}

/* ============================================================================
   2. web/src/pages/ReportsPage.svelte  (scoped <style> additions)
   ========================================================================== */

/* Two chip strips now, so each needs a name. */
.chipstrip {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  flex-wrap: wrap;
}

.chipstrip > .caption {
  font-size: 0.72rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-dim);
}

/* Tag filter chip: the .chip idiom minus the dot. */
.chip.tagchip {
  padding: 0.2rem 0.7rem;
}

.chip.tagchip.untagged {
  border-style: dashed;
  font-style: italic;
}

/* Rounding is unavailable while overlaps-once is on: rounding a share that was
   deliberately halved re-inflates the very overlap the option removes, and
   exportCSV already forces rounding to 0 in that mode. */
.seg.disabled {
  opacity: 0.45;
  pointer-events: none;
}

/* Right column is a stack now: By project + By tag. */
.side {
  display: grid;
  gap: 1rem;
  align-content: start;
}

.by-item {
  padding: 0.35rem 0;
  border-top: 1px solid var(--border);
}

.by-item .untaglabel {
  font-style: italic;
  color: var(--text-dim);
}

/* Proof row: Duration and % add up to the header, in the table itself. */
tr.total td {
  font-weight: 600;
  border-top: 1.5px solid var(--text-dim);
  border-bottom: none;
}

/* Weighted contribution of a multi-tag entry under one tag group. */
.splitmark {
  font-family: var(--mono);
  font-size: 0.78rem;
  color: var(--text-dim);
  margin-left: 0.35rem;
}

.reconcile {
  margin: 0.6rem 0 0;
  font-size: 0.78rem;
  color: var(--text-dim);
}

/* ============================================================================
   3. web/src/pages/PrintReportPage.svelte  (scoped, fixed light sheet)
   Single ink, no hue: identical in colour and in greyscale.
   ========================================================================== */

.tagtrail {
  color: var(--text-dim);
  font-size: 7.5pt;
}

.tagtrail::before {
  content: " · ";
}

/* All tag bars share one ink, so the only variable is length - the channel
   that survives a greyscale laser. */
.pbar div.ink {
  background: rgba(20, 26, 40, 0.55);
}

/* Redundant reinforcement for "no tag": hatch plus an italic label plus last
   position. If background graphics are off in the print dialog, the label and
   the ordering still carry it. */
.pbar div.hatch {
  background: repeating-linear-gradient(
    45deg,
    rgba(20, 26, 40, 0.55) 0 2px,
    rgba(20, 26, 40, 0.12) 2px 4px
  );
}

td.untagged-label {
  font-style: italic;
}

tr.sumrow td {
  font-weight: 600;
  border-top: 1.2px solid #232936;
  border-bottom: none;
  background: transparent;
}
```

## Разметка: chips_markup

```svelte
<!-- ==========================================================================
     A. Chip primitives - every state, no hover dependency
     ======================================================================== -->
<span class="tags">
  <span class="tag">meeting</span>
  <span class="tag">development</span>
  <span class="tag">analysis</span>
  <span class="tag">review</span>
  <span class="tag">other</span>
  <span class="tag untagged">untagged</span>
</span>

<!-- selected (picker) / dimmed (filter off) -->
<button type="button" class="tag on" aria-pressed="true">development</button>
<button type="button" class="tag" aria-pressed="false">refactoring</button>
<button type="button" class="tag off" aria-pressed="false">analysis</button>

<!-- ==========================================================================
     B. Entry row - TimerPage.svelte, replaces the current .row.item body.
     .dot | .main (flex:1, min-width:0) | .when (fixed basis) | kebab
     ======================================================================== -->
<div class="row item">
  <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>

  <span class="main">
    <button type="button" class="desc" onclick={() => openEditor(entry.id)}>
      {entry.description || t("(no description)")}
    </button>
    <span class="meta">
      {#if project}<span class="muted proj">{project.name}</span>{/if}
      {#if visibleTags(entry).length > 0}
        <span class="tags">
          <span class="tag">{visibleTags(entry)[0]}</span>
          {#if entryTags(entry).length > 1}
            <!-- a button, not a hover title: the overflow must be reachable by tap -->
            <button
              type="button"
              class="tag more"
              aria-label={t("{n} more tags: {names}", {
                n: entryTags(entry).length - 1,
                names: entryTags(entry).slice(1).join(", "),
              })}
              onclick={() => openEditor(entry.id)}
            >+{entryTags(entry).length - 1}</button>
          {/if}
        </span>
      {/if}
    </span>
  </span>

  <span class="when">
    <span class="dur">{formatDurationShort(entry.stopped_at! - entry.started_at)}</span>
    <span class="range muted">
      <span class="from">{formatTime(entry.started_at)}</span><!--
   --><span class="to">–{formatTime(entry.stopped_at!)}</span>
    </span>
  </span>

  <!-- kebab markup: see kebab_markup -->
</div>

<!-- Static preview of the same row, for design/components/timer-rows.html -->
<div class="row item">
  <span class="dot" style="background: var(--accent)"></span>
  <span class="main">
    <button type="button" class="desc">Доработка топологии</button>
    <span class="meta">
      <span class="muted proj">MySky</span>
      <span class="tags">
        <span class="tag">development</span>
        <button type="button" class="tag more" aria-label="1 more tag: review">+1</button>
      </span>
    </span>
  </span>
  <span class="when">
    <span class="dur">1h 45m</span>
    <span class="range muted"><span class="from">09:15</span><span class="to">–11:00</span></span>
  </span>
  <button class="kebab icon" title="Actions" aria-expanded="false">⋮</button>
</div>

<!-- Untagged rows show no placeholder chip: ~3600 rows of "untagged" pills
     would be pure noise. The gap is surfaced once, in the day header:
       <span class="muted">3 untagged</span>
     which is also a button that filters the day to untagged entries. -->
```

## Разметка: picker_markup

```svelte
<!-- ==========================================================================
     TagPicker.svelte - toggle grid, no popover, no typeahead by default.
     Lives inside the editor dialog, so a second overlay layer with its own
     outside-click semantics is exactly what must be avoided. Nothing depends
     on hover: state is read from the border and the accent wash.
     ======================================================================== -->
<div class="ed-field">
  <span class="ed-label" id={`${uid}-taglabel`}>
    {t("Tags")}
    <span class="muted">{selected.length}/8</span>
  </span>

  <!-- The filter input appears only past 12 known tags: on a short list a tap
       is faster than a keystroke and needs no keyboard at all. -->
  {#if catalogue.length > 12}
    <input
      class="tagpick-filter"
      type="search"
      placeholder={t("Filter tags")}
      bind:value={filterText}
      aria-label={t("Filter tags")}
    />
  {/if}

  <!-- Order: selected first, then by usage count desc, then name. Selected
       chips never jump out from under the finger while toggling. -->
  <div class="tagpick" role="group" aria-labelledby={`${uid}-taglabel`}>
    {#each shown as tag (tag.name)}
      <button
        type="button"
        class="tag"
        class:on={selected.includes(tag.name)}
        aria-pressed={selected.includes(tag.name)}
        disabled={selected.length >= 8 && !selected.includes(tag.name)}
        onclick={() => toggle(tag.name)}
      >{tag.name}</button>
    {:else}
      <span class="muted" style="font-size: 0.85rem">{t("No tags yet")}</span>
    {/each}
  </div>

  <!-- Create-on-the-fly. Normalised on commit: trim, collapse inner
       whitespace, lowercase, 24 chars. An existing name toggles that chip on
       instead of creating a duplicate, which is what actually prevents
       Meeting/meeting both existing across 10835 entries. -->
  <div class="tagpick-add">
    <input
      bind:value={draftTag}
      maxlength="24"
      placeholder={t("New tag")}
      aria-label={t("New tag")}
      disabled={selected.length >= 8}
      onkeydown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          commitDraft();
        }
      }}
    />
    <button type="button" disabled={!normalise(draftTag) || selected.length >= 8} onclick={commitDraft}>
      {t("Add")}
    </button>
  </div>

  {#if selected.length >= 8}
    <p class="ed-hint">{t("Eight tags per entry is the limit.")}</p>
  {/if}
</div>

<!-- ==========================================================================
     SettingsPage.svelte - the Tags block. Creation does NOT live here; this is
     the repair tool for near-duplicates that on-the-fly creation will produce.
     ======================================================================== -->
<div class="card">
  <h3>{t("Tags")}</h3>
  {#each catalogue as tag (tag.name)}
    <div class="row item">
      <span class="tag">{tag.name}</span>
      <span class="muted" style="font-size: 0.85rem">{t("{n} entries", { n: tag.count })}</span>
      <span class="spacer"></span>
      <button onclick={() => startRename(tag.name)}>{t("Rename")}</button>
      <button class="danger" onclick={() => confirmRemove(tag)}>{t("Remove")}</button>
    </div>
    {#if renaming === tag.name}
      <div class="row item">
        <input bind:value={renameTo} maxlength="24" aria-label={t("New name")} />
        <span class="spacer"></span>
        <button onclick={() => (renaming = null)}>{t("Cancel")}</button>
        <!-- Renaming into an existing name is a merge; the confirm says so. -->
        <button class="primary" onclick={() => applyRename(tag)}>
          {catalogue.some((other) => other.name === normalise(renameTo)) ? t("Merge") : t("Rename")}
        </button>
      </div>
    {/if}
    {#if removing === tag.name}
      <!-- Count-bearing confirm, and no undo: this rewrites hundreds of rows,
           a different risk shape from deleting one entry. -->
      <div class="row item">
        <span class="muted">{t("Remove «{tag}» from {n} entries?", { tag: tag.name, n: tag.count })}</span>
        <span class="spacer"></span>
        <button onclick={() => (removing = null)}>{t("Cancel")}</button>
        <button class="danger" onclick={() => removeTagEverywhere(tag.name)}>{t("Remove")}</button>
      </div>
    {/if}
  {:else}
    <p class="muted">{t("Tags appear here once you add them to an entry.")}</p>
  {/each}
  <p class="ed-hint">{t("Renaming and removing rewrite every affected entry; changes sync like any other edit.")}</p>
</div>
```

## Разметка: kebab_markup

```svelte
<!-- ==========================================================================
     Kebab + menu. Replaces the always-visible trash. Absolutely positioned
     popover on wide screens (the ProjectSelect .menu idiom); a fixed bottom
     sheet with a scrim below 34rem, so it can never be clipped by a card and
     both items are 3rem thumb targets. Same markup, CSS-only difference.
     ======================================================================== -->
<span class="menu-wrap" bind:this={menuRoot}>
  <button
    type="button"
    class="kebab icon"
    aria-label={t("Entry actions")}
    aria-haspopup="menu"
    aria-expanded={openMenuID === entry.id}
    aria-controls={`m-${entry.id}`}
    onclick={() => (openMenuID = openMenuID === entry.id ? null : entry.id)}
  >
    <svg width="15" height="15" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <circle cx="12" cy="5" r="1.7" />
      <circle cx="12" cy="12" r="1.7" />
      <circle cx="12" cy="19" r="1.7" />
    </svg>
  </button>

  {#if openMenuID === entry.id}
    <!-- Scrim is display:none above 34rem; outside-click there is handled by
         the same svelte:document onclick pattern ProjectSelect already uses. -->
    <button
      type="button"
      class="menu-scrim"
      aria-label={t("Close menu")}
      onclick={() => (openMenuID = null)}
    ></button>

    <div class="rowmenu" id={`m-${entry.id}`} role="menu">
      <button type="button" role="menuitem" onclick={() => { openMenuID = null; openEditor(entry.id); }}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
             stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M12 20h9" /><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
        </svg>
        {t("Edit")}
      </button>
      <div class="sep"></div>
      <!-- No confirm step: deletes are soft tombstones, so Undo is one LWW
           write. See the toast below. -->
      <button type="button" role="menuitem" class="danger" onclick={() => { openMenuID = null; removeEntry(entry); }}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
             stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M3 6h18" /><path d="M8 6V4h8v2" /><path d="M6 6l1 14h10l1-14" />
        </svg>
        {t("Delete")}
      </button>
    </div>
  {/if}
</span>

<!-- ==========================================================================
     Undo toast. Lives in the app shell (App.svelte), not in TimerPage, so
     navigating between pages does not dismiss it. role="status" announces it
     without stealing focus; the Undo button is a real 2.4rem target.
     ======================================================================== -->
{#if deleted}
  <div class="toast" role="status">
    <span>{t("Deleted")}</span>
    <span class="what">{deleted.description || t("(no description)")}</span>
    <button type="button" onclick={undoDelete}>{t("Undo")}</button>
    <button type="button" class="icon" aria-label={t("Dismiss")} onclick={() => (deleted = null)}>✕</button>
  </div>
{/if}

<!-- undoDelete is restoreEntry(id): clears deleted_at with a fresh updated_at.
     Under LWW that beats the tombstone on every device and works offline, the
     same write path as any other edit. -->
```

## Разметка: editor_markup

```svelte
<!-- ==========================================================================
     EntryEditor.svelte - native <dialog> opened with showModal(). Bottom sheet
     below 34rem, centred 32rem card above. Focus trap, Escape, ::backdrop and
     background inert come from the browser.
     ======================================================================== -->
<dialog
  class="sheet"
  bind:this={dialogElement}
  aria-labelledby="ed-title"
  onclose={close}
  oncancel={close}
>
  <form method="dialog" onsubmit={(event) => { event.preventDefault(); save(); }}>
    <div class="ed-head">
      <h3 id="ed-title">{t("Edit entry")}</h3>
      <span class="spacer"></span>
      <span class="ed-calc">{durationLabel}</span>
    </div>

    <div class="ed-body">
      <!-- The reason to edit is almost always a boundary. This line carries the
           neighbours' boundaries and the live gap/overlap, which is the actual
           information an inline editor would have shown by staying in the list. -->
      {#if neighbours}
        <p class="ed-neighbours">
          {#if neighbours.prevEnd}<span>{t("prev ends")} <b>{neighbours.prevEnd}</b></span>{/if}
          {#if neighbours.overlapMin > 0}
            <span class="gap bad">{t("overlaps {n}", { n: fmtMin(neighbours.overlapMin) })}</span>
          {:else if neighbours.gapMin > 0}
            <span class="gap">{t("gap {n}", { n: fmtMin(neighbours.gapMin) })}</span>
          {/if}
          {#if neighbours.nextStart}<span>{t("next starts")} <b>{neighbours.nextStart}</b></span>{/if}
        </p>
      {/if}

      <!-- The row can be stopped by another device or by MCP while this is open.
           Save never writes a stale snapshot: it patches only the fields the
           user touched, and this notice reports what changed underneath. -->
      {#if stoppedElsewhere}
        <p class="ed-hint bad">{t("This entry was stopped on another device; the end time below is the stored one.")}</p>
      {/if}

      <div class="ed-field">
        <label class="ed-label" for="ed-desc">{t("Description")}</label>
        <input id="ed-desc" bind:value={draft.description} maxlength="2000" />
      </div>

      <div class="ed-field">
        <span class="ed-label">{t("Project")}</span>
        <div><ProjectSelect projects={activeProjects} bind:value={draft.projectID} /></div>
      </div>

      <!-- TagPicker: see picker_markup -->
      <TagPicker bind:selected={draft.tags} {catalogue} />

      <div class="ed-field">
        <span class="ed-label">{t("Date")}</span>
        <div class="ed-line">
          <input type="date" bind:value={draft.dateISO} aria-label={t("Date")} />
          <!-- "yesterday" is the most common date edit; one tap, no calendar. -->
          <span class="seg">
            <button type="button" onclick={() => shiftDate(-1)}>−1d</button>
            <button type="button" onclick={() => shiftDate(1)}>+1d</button>
          </span>
        </div>
      </div>

      <div class="ed-field">
        <span class="ed-label">{t("Time")} <span class="muted">24h</span></span>
        <div class="ed-line">
          <label class="muted" for="ed-from">{t("From")}</label>
          <input
            id="ed-from"
            class="timefield"
            inputmode="numeric"
            maxlength="5"
            bind:value={draft.fromText}
            aria-invalid={fromInvalid}
            onblur={normaliseFrom}
            onkeydown={nudge}
          />

          <label class="muted" for="ed-to">{t("To")}</label>
          <input
            id="ed-to"
            class="timefield"
            inputmode="numeric"
            maxlength="5"
            placeholder={isRunning ? t("running") : ""}
            bind:value={draft.toText}
            aria-invalid={toInvalid}
            onblur={normaliseTo}
            onkeydown={nudge}
          />

          <!-- End-date offset. This is what makes 23:40-00:20 representable and
               editable at all: without it the guard "To must be > From" makes an
               existing overnight entry permanently unsaveable, and removing the
               guard writes stopped_at < started_at, which validateChanges
               rejects for the entire push batch. -->
          <span class="seg" aria-label={t("End day offset")}>
            <button type="button" onclick={() => shiftEndDay(-1)} disabled={draft.endDayOffset <= 0}>−</button>
            <button type="button" class:on={draft.endDayOffset > 0}>+{draft.endDayOffset}d</button>
            <button type="button" onclick={() => shiftEndDay(1)}>+</button>
          </span>

          <span class="ed-calc">{durationLabel}</span>
        </div>

        {#if isRunning}
          <p class="ed-hint">{t("Entering an end time stops this timer at that moment. Start cannot be in the future.")}</p>
        {/if}
        {#if timeError}
          <p class="ed-hint bad">{timeError}</p>
        {:else if longEntry}
          <!-- Soft warning, never a block: MCP add_time_entry can legitimately
               create long entries and they must stay editable. -->
          <p class="ed-hint">{t("This entry is longer than 12 hours - check the +Nd offset.")}</p>
        {/if}
      </div>
    </div>

    <div class="ed-foot">
      {#if isRunning}
        <button type="button" onclick={stopNow}>{t("Stop now")}</button>
      {/if}
      <span class="spacer"></span>
      <button type="button" onclick={close}>{t("Cancel")}</button>
      <button type="submit" class="primary" disabled={!canSave}>{t("Save")}</button>
    </div>
  </form>
</dialog>

<!-- Behaviour contract, all of it load-bearing:

  Running entry: every field editable. To is empty with a dim "running"
  placeholder; typing a time stops the entry at that moment. "Stop now" fills
  in the current time. started_at is clamped to <= now, because a future start
  makes formatDuration clamp to 0 and reads as a broken timer, then produces
  stopped_at < started_at on the next Stop.

  To is NEVER clearable. A finished entry cannot be reopened from the editor:
  toReportEntries skips stopped_at === null, so a cleared To would silently
  remove the hours from the stat cards, the chart, the CSV and the printed
  Russian sheet with no signal anywhere. Clearing restores the stored value on
  blur.

  Save patches, it does not write the snapshot. updateEntry(id, patch) reads
  the current row at save time and applies only the fields the user touched, so
  a concurrent Stop (row, other device, or MCP) is not undone and tags written
  by another writer are not blanked.

  Time parsing: 9 | 930 | 0930 | 9:30 | 9.30, always 24h, normalised to HH:MM
  on blur. Unparseable -> aria-invalid, danger border, "—" in the duration
  readout, Save disabled. ArrowUp/Down nudge one minute, Shift+Arrow fifteen.

  Timestamps compose via new Date(year, month - 1, day, hour, minute), which is
  DST-correct; a value that falls in a spring-forward gap is written back into
  the field after normalisation so the stored time is the one on screen. -->
```

## Разметка: reports_markup

```svelte
<!-- ==========================================================================
     A. Toolbar - two named chip strips. Untagged is a real chip with a real
     key, exactly like NO_PROJECT_KEY: every entry has at least one tag key, so
     the ~1/3 of entries with no tags can never fall out of the totals.
     ======================================================================== -->
<span class="chipstrip">
  <span class="caption">{t("Projects")}</span>
  {#each projectChips as chip (chip.key)}
    <button type="button" class="chip" class:off={disabledProjects.has(chip.key)}
            aria-pressed={!disabledProjects.has(chip.key)} onclick={() => toggleProject(chip.key)}>
      <span class="dot" style="background: {chip.color}"></span>{chip.name}
    </button>
  {/each}
</span>

<span class="chipstrip">
  <span class="caption">{t("Tags")}</span>
  {#each tagChips as chip (chip.key)}
    <button
      type="button"
      class="chip tagchip"
      class:untagged={chip.key === UNTAGGED_KEY}
      class:off={disabledTags.has(chip.key)}
      aria-pressed={!disabledTags.has(chip.key)}
      onclick={() => toggleTag(chip.key)}
    >{chip.name}</button>
  {/each}
</span>

<!-- Filtering is any-of within a strip and AND across strips. The split
     denominator is always the entry's OWN tag list, never the active set, so a
     tag row's number never moves when an unrelated chip is toggled. -->

<!-- ==========================================================================
     B. Builder - Group by gains Tag; Rounding is disabled under overlaps-once
     ======================================================================== -->
<div>
  <h4>{t("Group by")}</h4>
  <Seg
    vertical
    options={[
      { value: "project", label: t("Project") },
      { value: "tag", label: t("Tag") },
      { value: "day", label: t("Day") },
      { value: "description", label: t("Description") },
    ]}
    bind:value={groupBy}
  />
</div>
<div>
  <h4 style="margin-top: 0.7rem">{t("Rounding")}</h4>
  <Seg
    disabled={overlapOnce}
    options={[
      { value: "0", label: t("Off") },
      { value: "15", label: "15m" },
      { value: "30", label: "30m" },
      { value: "60", label: "1h" },
    ]}
    value={overlapOnce ? "0" : String(rounding)}
    onselect={(value) => (rounding = Number(value))}
  />
  {#if overlapOnce}
    <p class="reconcile">{t("Rounding is off while overlaps are counted once: rounding a halved share puts the overlap back.")}</p>
  {/if}
</div>

<!-- ==========================================================================
     C. By tag panel - the parallel of By project, in the right-hand stack.
     Rendered whenever any entry in range carries a tag.
     ======================================================================== -->
<div class="side">
  <div class="card">
    <h3>{t("By project")}</h3>
    <!-- unchanged -->
  </div>

  {#if byTag.length > 0}
    <div class="card">
      <h3>{t("By tag")}</h3>
      {#each byTag as [key, minutes] (key)}
        <div class="by-item">
          <div class="row">
            {#if key === UNTAGGED_KEY}
              <span class="untaglabel">{t("untagged")}</span>
            {:else}
              <span>{key}</span>
            {/if}
            <span class="spacer"></span>
            <span class="mono">{fmtMin(minutes)}</span>
            <span class="muted mono pct">{totalMinutes ? Math.round((minutes / totalMinutes) * 100) : 0}%</span>
          </div>
          <!-- One ink for every bar: length ranks, hue would collide with the
               project dots one card above. -->
          <div class="pbar">
            <div style="width: {totalMinutes ? (minutes / totalMinutes) * 100 : 0}%; background: var(--accent)"></div>
          </div>
        </div>
      {/each}
      <p class="reconcile">{t("An entry with several tags splits its time equally between them, so this adds up to the same total as By project.")}</p>
    </div>
  {/if}
</div>

<!-- ==========================================================================
     D. Report table - detail rows carry their weighted share, and a Total row
     makes the reconciliation visible instead of asserted.
     ======================================================================== -->
<table>
  <thead>
    <tr>
      <th>{groupHeading}</th>
      {#each visibleColumns as column (column)}<th class="num">{columnHead(column)}</th>{/each}
    </tr>
  </thead>
  <tbody>
    {#each tableGroups as group (group.key)}
      <tr class="group">
        <td>
          {#if group.color}<span class="dot inline-dot" style="background: {group.color}"></span>{/if}
          {#if group.key === UNTAGGED_KEY}
            <span class="untaglabel">{t("untagged")}</span>
          {:else}{group.label}{/if}
        </td>
        {#each visibleColumns as column (column)}<td class="num">{cellValue(group, column)}</td>{/each}
      </tr>
      {#if showEntries}
        {#each group.entries as row (row.entry.id)}
          <tr class="entry">
            <td>
              {row.entry.description || t("(no description)")}
              <span class="muted">
                · {chipName(projectKey(row.entry))} · {formatDayISO(row.entry.date)} {formatTime(row.entry.startedAt)}
              </span>
            </td>
            {#each visibleColumns as column (column)}
              <td class="num muted">
                {#if column === "duration"}
                  <!-- row.share, not the entry's full duration: this is the
                       number that makes the detail rows sum to their group
                       header. The 1/k marker says why it is smaller. -->
                  {fmtMin(row.share)}
                  {#if row.of > 1}<span class="splitmark" title={t("shared with {n} tags", { n: row.of })}>1/{row.of}</span>{/if}
                {/if}
              </td>
            {/each}
          </tr>
        {/each}
      {/if}
    {:else}
      <tr><td colspan="9" class="muted">{t("No entries in range")}</td></tr>
    {/each}

    {#if tableGroups.length > 0}
      <tr class="total">
        <td>{t("Total")}</td>
        {#each visibleColumns as column (column)}
          <td class="num">
            {column === "duration"
              ? fmtMin(tableTotal)
              : column === "pct"
                ? "100%"
                : column === "entries"
                  ? String(distinctEntryCount)
                  : fmtMin(Math.round(tableTotal / Math.max(1, distinctEntryCount)))}
          </td>
        {/each}
      </tr>
    {/if}
  </tbody>
</table>

{#if groupBy === "tag" && multiTagCount > 0}
  <p class="reconcile">
    {t("{n} entries carry more than one tag. Their duration is split equally, so Duration and % add up to the total exactly; the Entries column counts such an entry in every tag row, which is why the group counts sum above {total}.", { n: multiTagCount, total: distinctEntryCount })}
  </p>
{/if}
```

