# Мобильная вёрстка WorkTime - финальная спека синтеза

База - вариант density-first (лучшие вердикты 7/8), к нему привиты платформенные фиксы из pwa-native (16px инпуты, max-height меню ProjectSelect, theme-color), 2x2 Seg и wrap-механика меты из minimal-change, и координация toast/инсетов из thumb-first. Все must_not_ship судей учтены; ни одного не переопределяем. Ни одно settled-решение из docs/tags-design.md (Q1, Q2, Q6, Q9, Q10) не вскрывается.

Контракт e2e не тронут: `.desc`, `.kebab` c aria-label "Entry actions", Edit/Delete, `dialog.sheet`, `#ed-from`/`#ed-to`, placeholder "running", `.tag`/`.chip`/`.tagchip`, `.stat`, `tr.total`/`tr.sumrow`, имена nav-ссылок, placeholder "What are you working on?" - все селекторы и тексты сохранены, изменения только аддитивные (новые классы, обёртки, media queries). Suite гоняется на Desktop Chrome 1280px c pointer:fine - все `max-width: 34rem` и `pointer: coarse` блоки для него невидимы.

## Решения

### Двухпалубный header, не нижний таб-бар

Ниже 34rem header становится двумя рядами: лого + sync-статус, под ними nav одной непереносящейся строкой с горизонтальным скроллом как предохранителем. Чистый CSS (order + flex-basis), markup нетронут, role=link имена живы.

**Почему:** оба таб-бар-варианта убиты судьями - у pwa-native fixed-nav внутри backdrop-filtered header прибивается к хедеру, а не к вьюпорту (containing block), у thumb-first бар докуется над Android-клавиатурой при resizes-content (митигация только JS-ом). Компактный header закрывает наблюдаемую проблему (nav в две строки) без налога ~4.5rem высоты на каждый экран и без коллизии с toast.

### Платформенная мета одним коммитом с компенсацией инсетов

`viewport-fit=cover` + `interactive-widget=resizes-content` + два media-gated `theme-color`. В том же коммите - `padding-top: max(0.8rem, env(safe-area-inset-top))` на header и боковые `max(1rem, env(...))` на .shell, безусловно (не в media query).

**Почему:** env(safe-area-inset-bottom) в .rowmenu/.toast/.ed-foot сейчас мёртвый код (index.html:5 без viewport-fit). Компенсация вне media query закрывает и standalone-планшет >34rem. `apple-mobile-web-app-status-bar-style: black-translucent` - НЕ добавлять (белый текст статус-бара поверх #f5f3ee светлой темы, убито обоими судьями pwa-native).

### Клавиатура: .sheet form становится flex-колонкой

`dialog.sheet` - flex column, но его единственный ребёнок `<form>` - block, поэтому flex/overflow правила `.ed-body` инертны и ed-foot уезжает. Фикс: `.sheet form { display: flex; flex-direction: column; min-height: 0 }` безусловно + scroll-padding на .ed-body.

**Почему:** без этого мета-фикс клавиатуры "не работает как заявлено" (must_not_ship по thumb-first); на десктопе это тоже строгое улучшение - футер с Save перестаёт скроллиться из вида на низких окнах.

### Закон одного усечения + wrap как предохранитель

На строке записи ellipsis положен только имени проекта (точка дублирует его цветом). Чипы и +N атомарны (`flex: 0 0 auto`); когда проект + чипы не влезают, блок .tags переносится на свою строку с полными лейблами.

**Почему:** "MySky Qu…" + "develo…" + "+…" - три эллипсиса, ноль информации. Варианты с clip/overflow:hidden убиты (переполнение бюджета .main на 360px, клип +N); wrap - единственный механизм, у которого нет worst case, и он явно совместим с Q2 (чип ниже baseline .when, duration-колонку не толкает).

### Инпуты 16px ниже 34rem

Все текстовые инпуты и select - `font-size: 1rem` в мобильном диапазоне, с явным перебоем 0.9rem у .timefield, .tagpick-add input, .tagpick-filter.

**Почему:** iOS Safari зумит страницу при фокусе на инпуте <16px - самый ценный платформенный фикс всего сета по вердиктам. Тач-таргеты - `pointer: coarse` правила с исключениями `:not([type=checkbox]):not([type=radio]):not([type=color])` (ковровая версия убита: 44px чекбоксы, растянутые свотчи).

### Чип-стрипы Reports продолжают wrap-иться

Никакого горизонтального скролла стрипов фильтров.

**Почему:** must_not_ship density-first - выключенный чип (opacity 0.4), уехавший за экран, это невидимый активный фильтр, молча искажающий каждую цифру на странице. Высота тулбара компенсируется другими рядами (full-width Seg, даты в одну строку, 2x2 Group by).

### Числа не переносятся никогда

`td.num, th.num { white-space: nowrap }` безусловно, `<div class="tablewrap">` вокруг таблицы, nowrap на mono-тоталах в заголовках карточек, nowrap на date-range в Time off.

**Почему:** "16h" / "30m" в две строки - баг рендера, не компромисс. Единогласно у всех четырёх вариантов, строгое улучшение на всех ширинах.

### Инлайновые style="flex: 1" заменяются классом .grow

TimerPage:74, ProjectsPage:31,45 - inline-стиль меняется на `class="grow"`.

**Почему:** inline-стиль побеждает любой media query - все три CSS-only обхода (min-width:100%, `flex: 1 1 100%`, `input[aria-label]`) убиты судьями как no-op или ломающиеся на локализации. Правка маркапа разрешена и необходима; `.grow` в ProjectsPage дополнительно несёт `min-width: 0` - настоящую причину обрезанного trash (intrinsic min-width инпута ~170px).

---

## Изменения по файлам

Порядок - применять сверху вниз. MUST 1-9, NICE 10-12.

### 1. web/index.html - MUST

Заменить строки 5-6:

```html
<meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover, interactive-widget=resizes-content" />
<meta name="theme-color" content="#151a24" media="(prefers-color-scheme: dark)" />
<meta name="theme-color" content="#f5f3ee" media="(prefers-color-scheme: light)" />
```

Никакого `apple-mobile-web-app-status-bar-style`.

### 2. web/src/app.css - MUST

**2a. Строка записи** - расширить существующий блок `@media (max-width: 34rem)` (тот, где `.item .when` и line-clamp у .desc, строки 352-371) - добавить внутрь:

```css
  .item .when {
    font-size: 0.85rem;
  }

  .item .when .dur {
    min-width: 0;
  }

  /* One-truncation law: only .proj may ellipsize (the dot already encodes the
     project). Chips and the +N counter are atomic; when project + chips don't
     fit, the tag block wraps to its own line with full labels. */
  .item .meta {
    flex-wrap: wrap;
    row-gap: 0.1rem;
  }

  .item .meta .tags {
    flex: 0 0 auto;
    flex-wrap: wrap;
  }

  .item .meta .tags .tag {
    flex: 0 0 auto;
  }
```

`.tag` max-width 9rem не трогаем (глобальный кап задел бы toggle-кнопки TagPicker - убито судьёй density-first). 9rem-чип + wrap внутри .tags покрывает и 360px: если чип + "+N" шире строки, +N переносится, но никогда не обрезается.

**2b. Editor sheet** - безусловно, рядом с блоком `dialog.sheet`:

```css
/* dialog.sheet is the flex column, but its only child is the <form>: without
   making the form itself a min-height:0 flex column, .ed-body's flex/overflow
   rules are inert and the footer scrolls out of view. */
.sheet form {
  display: flex;
  flex-direction: column;
  min-height: 0;
}
```

В существующее правило `.sheet .ed-body` добавить:

```css
  scroll-padding-block: 3rem;
```

**2c. iOS focus-zoom** - в конец файла:

```css
/* iOS Safari zooms the page when a focused input is under 16px. */
@media (max-width: 34rem) {
  input,
  select {
    font-size: 1rem;
  }

  .timefield {
    font-size: 1rem;
    width: 5.5rem;
  }

  .tagpick-add input,
  .tagpick-filter {
    font-size: 1rem;
  }
}
```

**2d. Тач-таргеты** - в конец файла:

```css
@media (pointer: coarse) {
  button {
    min-height: 2.75rem;
  }

  .seg button {
    min-height: 2.6rem;
  }

  /* Filter chips keep the settled compact pill look, same as button.tag. */
  .chip {
    min-height: 2.1rem;
  }

  input:not([type="checkbox"]):not([type="radio"]):not([type="color"]),
  select {
    min-height: 2.75rem;
  }
}
```

Каскад проверен: `button.tag` (2.1rem), `.rowmenu button` (2.4/3rem), `.toast button` (2.4rem), `.kebab` (2.75rem) - все свои правила 0,1,1 и выше, блэнкет 0,0,1 их не перебивает.

### 3. web/src/App.svelte - MUST

Markup не меняется. В `<style>`:

В `.shell` добавить:

```css
    padding-left: max(1rem, env(safe-area-inset-left));
    padding-right: max(1rem, env(safe-area-inset-right));
```

В `header` добавить (после существующего `padding: 0.8rem 0;`):

```css
    padding-top: max(0.8rem, env(safe-area-inset-top));
```

В конец `<style>`:

```css
  @media (max-width: 34rem) {
    header {
      gap: 0.25rem 0.6rem;
    }

    .status {
      order: 2;
      margin-left: auto;
      white-space: nowrap;
    }

    nav {
      order: 3;
      flex: 1 0 100%;
      flex-wrap: nowrap;
      overflow-x: auto;
      scrollbar-width: none;
    }

    nav::-webkit-scrollbar {
      display: none;
    }

    nav a {
      padding: 0.3rem 0.55rem;
      font-size: 0.9rem;
      white-space: nowrap;
    }
  }
```

Full-bleed через `margin: 0 -1rem` НЕ делаем - разъедется с safe-area паддингом .shell в landscape (weakness судьи density-first). На 390px en-лейблы влезают целиком; ru/de скроллятся на пару десятков px - предохранитель, не основной режим.

### 4. web/src/pages/TimerPage.svelte - MUST

Markup, строка 74 - заменить `style="flex: 1"` на класс:

```svelte
  <input
    class="grow"
    placeholder={t("What are you working on?")}
    bind:value={description}
    aria-label={t("Description")}
  />
```

В `<style>`:

```css
  .grow {
    flex: 1;
  }

  /* Day totals like "3h 10m" must never break mid-value. */
  .row > .mono {
    white-space: nowrap;
  }

  @media (max-width: 34rem) {
    form.row {
      flex-wrap: wrap;
    }

    .grow {
      flex: 1 1 100%;
    }

    form.row :global(.pselect) {
      flex: 1 1 auto;
      min-width: 0;
    }

    form.row :global(.pselect > button) {
      width: 100%;
      max-width: none;
    }

    form.row :global(.pselect .caret) {
      margin-left: auto;
    }

    form.row > button.primary {
      flex: 0 0 auto;
      min-width: 7rem;
    }
  }
```

Результат: инпут на всю первую строку, ProjectSelect + Start на второй, Start больше не обрезан. Override .pselect скоупится через `form.row :global(...)` - в EntryEditor триггер проекта не протекает (это было замечание судьи к density-first).

### 5. web/src/pages/ReportsPage.svelte - MUST

Markup - обернуть таблицу (строки 575-652):

```svelte
<div class="tablewrap">
  <table>
    ...
  </table>
</div>
```

В `<style>` - в существующее правило `td.num, th.num` добавить:

```css
  white-space: nowrap;
```

Дальше добавить:

```css
  .tablewrap {
    overflow-x: auto;
  }

  /* Header totals ("16h 30m") and per-project durations never wrap. */
  .row > .mono {
    white-space: nowrap;
  }

  @media (max-width: 34rem) {
    .toolbar {
      gap: 0.5rem;
    }

    .toolbar > :global(.seg) {
      display: flex;
      width: 100%;
    }

    /* flex-basis auto + min-width 0: long locale labels ("Прошлая неделя")
       keep their width and only shrink under real pressure, with ellipsis
       instead of the .seg overflow clip. */
    .toolbar > :global(.seg button) {
      flex: 1 1 auto;
      min-width: 0;
      padding: 0.35rem 0.4rem;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .toolbar > input[type="date"] {
      flex: 1 1 40%;
      min-width: 0;
    }

    .toolbar .overlap-toggle {
      flex: 1 1 100%;
    }

    .toolbar > .spacer {
      display: none;
    }

    /* Only the two action buttons; the day-filter pill keeps its shape. */
    .toolbar > button:not(.filterpill) {
      flex: 1 1 40%;
      min-height: 2.75rem;
    }

    /* Group by: 2x2 grid instead of a 150px vertical stack. Page-scoped
       :global compiles to .builder.hash .seg... = 0,4,x and beats Seg's
       scoped rules (0,3,2 with :where) - verified against compiled CSS. */
    .builder :global(.seg.vertical) {
      display: grid;
      grid-template-columns: 1fr 1fr;
      width: 100%;
    }

    .builder :global(.seg.vertical button) {
      border: none;
    }

    .builder :global(.seg.vertical button:nth-child(even)) {
      border-left: 1px solid var(--border);
    }

    .builder :global(.seg.vertical button:nth-child(n + 3)) {
      border-top: 1px solid var(--border);
    }

    table {
      font-size: 0.85rem;
    }

    td,
    th {
      padding: 0.4rem 0.3rem;
    }

    td:first-child {
      overflow-wrap: anywhere;
    }
  }
```

Чип-стрипы не трогаем - wrap-ятся как сейчас (решение 6). Тулбар сжимается за счёт full-width Seg (1 ряд), дат в одну строку, overlap-toggle на своей строке и пары Export CSV / PDF report на последней.

### 6. web/src/pages/ProjectsPage.svelte - MUST

Markup - в обоих местах (строка 31 форма, строка 45 строка проекта) заменить `style="flex: 1"` на `class="grow"`.

В `<style>`:

```css
  /* min-width: 0 is the actual unclip: a text input's intrinsic minimum is
     ~170px (default size attribute) and flex-basis 0 alone cannot shrink it. */
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
```

`:not(.icon)` обязателен - без него правило РАСШИРЯЕТ trash-кнопку (padding-inline у button.icon = 0.5rem; убито судьёй minimal-change).

### 7. web/src/pages/TimeOffPage.svelte - MUST

В `<style>`:

```css
  @media (max-width: 34rem) {
    .row.wrap input[type="date"] {
      flex: 1 1 40%;
      min-width: 0;
    }

    .row.wrap > button.primary {
      flex: 1 1 100%;
    }

    .item {
      flex-wrap: wrap;
    }

    /* A date range is one token - the row wraps, the range never does. */
    .item .mono {
      white-space: nowrap;
      font-size: 0.85rem;
    }

    /* When the row wraps, the delete control right-aligns instead of
       stranding flush-left on line 2. */
    .item > button.icon {
      margin-left: auto;
    }
  }
```

### 8. web/src/lib/components/ProjectSelect.svelte - MUST

В существующее правило `.menu` добавить:

```css
    max-height: 40dvh;
    overflow-y: auto;
    overscroll-behavior: contain;
```

И в конец `<style>`:

```css
  @media (pointer: coarse) {
    .menu button {
      min-height: 2.6rem;
    }
  }
```

Латентный баг (15 проектов = поповер за нижней границей вьюпорта), строгое улучшение и на десктопе.

### 9. web/src/lib/components/DailyChart.svelte - MUST (самый крупный пункт)

Проблема: viewBox 760 units в ~326 CSS px -> подписи осей рендерятся ~4.3px. Фикс - канвас в реальных пикселях, гейт держит десктоп и print байт-в-байт (must_not_ship судьи density-first учтён: на десктопе и при `interactive={false}` формулы дают ровно сегодняшние 760/260/ceil(n/14)).

Script - заменить константы:

```ts
const PAD_LEFT = 36;
const PAD_TOP = 14;
const PAD_BOTTOM = 20;

let containerWidth = $state(760);
// True-pixel canvas only for the interactive chart under 34rem (544px):
// the print sheet (interactive={false}) and desktop keep the fixed 760x260
// viewBox, so their rendering is byte-identical.
const width = $derived(interactive && containerWidth < 544 ? Math.max(300, containerWidth) : 760);
const height = $derived(width < 480 ? 200 : 260);
const baselineY = $derived(height - PAD_BOTTOM);
const plotHeight = $derived(baselineY - PAD_TOP);
```

`columnWidth` и `labelStep` заменить на:

```ts
const columnWidth = $derived((width - PAD_LEFT) / Math.max(1, days.length));
// Pixel budget: ~46px per label; capped at 14 so desktop density is
// exactly today's ceil(days / 14).
const labelStep = $derived(
  Math.max(1, Math.ceil(days.length / Math.min(14, Math.max(4, Math.floor(width / 46))))),
);
```

`yOf` и `bars` не меняются (уже читают baselineY/plotHeight).

Template - обернуть svg и заменить ссылки на константы:

```svelte
<div class="chartwrap" bind:clientWidth={containerWidth}>
  <svg class="chart" viewBox="0 0 {width} {height}" role="img" aria-label={t("Daily tracked hours")}>
```

Точечные замены внутри svg: `x2={WIDTH}` -> `x2={width}` (обе линии - grid и avg), `x={WIDTH - 4}` -> `x={width - 4}`, `y={HEIGHT - 5}` -> `y={height - 5}`, закрыть `</svg></div>`.

Подпись avg на узких ширинах прячем (~130px на 326px канвасе легли бы на бары; тайл "avg per work day" в stats дублирует цифру):

```svelte
{#if avgMinutes > 0}
  <line ... />
  {#if width >= 480}
    <text x={width - 4} y={yOf(avgMinutes) - 5} text-anchor="end" font-size="10" fill="var(--accent)">
      {avgCaption}
    </text>
  {/if}
{/if}
```

Style:

```css
  .chartwrap {
    width: 100%;
  }
```

### 10. web/src/app.css - платформенный блок - NICE

```css
html {
  -webkit-text-size-adjust: 100%;
  text-size-adjust: 100%;
  -webkit-tap-highlight-color: transparent;
}

button,
a,
input,
select,
.overlap-toggle {
  touch-action: manipulation;
}
```

И перенос hover-правил за hover-гейт + пресс-фидбек. Тела существующих `button:hover`, `a:hover`, `button.primary:hover`, `.kebab:hover` переехать внутрь одного блока:

```css
@media (hover: hover) {
  a:hover { color: var(--accent); filter: brightness(1.15); }
  button:hover { border-color: var(--text-dim); }
  button.primary:hover { filter: brightness(1.08); }
  .kebab:hover { color: var(--text); border-color: var(--border); }
}

button:active {
  background: var(--hover);
}

button.primary:active {
  background: var(--accent);
  filter: brightness(0.92);
}
```

Никакого нейтрализатора `@media (hover: none) { button:hover {...} }` - он перебивает прозрачный бордер .kebab и возвращает sticky-hover, который призван убрать (must_not_ship pwa-native).

### 11. web/src/app.css - анимации шитов + stats grid - NICE

```css
@keyframes sheet-up {
  from { transform: translateY(1.5rem); opacity: 0.5; }
}

@keyframes fade-in {
  from { opacity: 0; }
}

@media (max-width: 34rem) {
  .rowmenu { animation: sheet-up 160ms ease-out; }
  dialog.sheet[open] { animation: sheet-up 200ms ease-out; }
  dialog.sheet::backdrop { animation: fade-in 200ms ease-out; }
  .menu-scrim { animation: fade-in 160ms ease-out; overscroll-behavior: contain; }

  .stats {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.8rem 1rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .rowmenu,
  dialog.sheet[open],
  dialog.sheet::backdrop,
  .menu-scrim {
    animation: none;
  }
}
```

`.stat`-тайлы и их markup нетронуты - меняется только контейнер.

### 12. web/src/App.svelte - активная вкладка в зоне видимости - NICE

```ts
$effect(() => {
  void route.path;
  document.querySelector("nav a.active")?.scrollIntoView({ block: "nearest", inline: "nearest" });
});
```

Нужен только для ru/de локалей на 360px, где nav чуть скроллится.

---

## Что осознанно не делаем

- **Нижний таб-бар** (thumb-first, pwa-native). Убит: fixed внутри backdrop-filtered header прибивается не к вьюпорту (Chromium containing block); с resizes-content бар всплывает над Android-клавиатурой прямо в flow набора текста; плюс постоянный налог высоты и коллизия с .toast. Компактный header решает наблюдаемую проблему дешевле.
- **Горизонтально скроллящиеся чип-стрипы в Reports** (density-first, thumb-first, pwa-native). Must_not_ship: выключенный чип за экраном - невидимый активный фильтр, молча искажающий каждый stat, бар и строку таблицы.
- **`min-width: 100%` и `flex: 1 1 100%` против inline style="flex: 1"** (minimal-change, thumb-first). Инлайн побеждает либо даёт инпут уже сегодняшнего; селектор `input[aria-label="Name"]` умирает вне en-локали. Заменено правкой маркапа (.grow).
- **Ковровое `input, select { min-height: 2.75rem }`** (thumb-first). 44px чекбоксы в Columns/Detail и overlap-toggle, растянутые color-свотчи. Заменено версией с исключениями по типам.
- **`.toolbar > button { flex: 1 1 ... }` без скоупа** (thumb-first, minimal-change). Захватывает .filterpill. Заменено `:not(.filterpill)` + overlap-toggle на basis 100%.
- **`apple-mobile-web-app-status-bar-style: black-translucent`** (pwa-native). Белый текст статус-бара нечитаем поверх #f5f3ee светлой темы.
- **`@media (hover: none)` нейтрализатор button:hover** (pwa-native). Перебивает прозрачный бордер .kebab - возвращает артефакт, который должен убрать.
- **Глобальный мобильный кап `.tag { max-width: 6.5-8rem }`** (thumb-first, density-first). Задевает toggle-кнопки TagPicker в редакторе, где лейбл надо читать, чтобы тыкать. Наш фикс меты обходится без нового капа.
- **CSS-бамп шрифта чарта до 17 units** (minimal-change). ~7.3px реальных - всё равно нечитаемо; проблема закрыта настоящим рефакторингом viewBox.
- **Sticky day headers** (density-first). Низкая ценность для 7-дневного окна, купленная связкой с паддингом .card и швом радиуса.
- **Vertical Seg -> горизонтальный ряд 4 колонки** (density-first, pwa-native). "Beschreibung"/"Описание" клиппятся в четверти от 326px. Заменено 2x2 grid.
- **Полный bleed nav через `margin: 0 -1rem`** (density-first, minimal-change). Разъезжается с новым safe-area паддингом .shell в landscape с вырезом.
- **Ужимание .timefield ниже 24rem** (density-first). From/To на 5.25rem уже влезают в 360px - фикс несуществующей проблемы.

## Риски

- `interactive-widget=resizes-content` действует на весь layout: на Android открытие клавиатуры (в т.ч. на дате в Reports) перекладывает страницу. Косметика. iOS токен игнорирует - там ed-body-скролл + scroll-padding; Save может требовать подскролла, это принятый режим.
- `pointer: coarse` правила срабатывают на touch-ноутбуках и планшетах при >=34rem - кнопки там станут выше сегодняшних. Осознанное отступление от "desktop unchanged" в пользу тача; проговорить на ревью.
- Специфичность `:global(.seg.vertical ...)` override проверена по скомпилированному CSS (Svelte 5 скоупит через `:where()`, страничные 0,4,x побеждают) - но перепроверить в devtools после build; если Seg получит новый класс, каскад пересмотреть.
- DailyChart: при 30 днях на 326px бары ~7px - тонкие, но в настоящих пикселях с читаемыми подписями. Прогнать print e2e: гейт `interactive && containerWidth < 544` обязан держать печатный лист на 760x260.
- `td.num` nowrap на десктопе: с 4 колонками + Show individual entries + длинными описаниями первая колонка сжимается и переносится, .tablewrap - страховка скроллом. Глянуть десктопную таблицу глазами один раз.
- ru/de nav на 360px скроллится на десятки px без видимого скроллбара; NICE-пункт 12 - митигация.
- 16px инпуты видны и в узком (<34rem) окне десктопного браузера - принято, это тот же media-режим.
- Правки `.item .meta`/.tags в app.css зеркалятся в design/_tags.css - после имплементации обновить design/ бандл и запушить через DesignSync, иначе превью разъедутся с кодом.
- e2e гоняется против встроенного билда: после фронтовых правок обязателен `make build`, иначе Playwright молча тестирует старый UI (и на Windows проверить, что рядом с bin/worktime.exe не лежит старый extension-less bin/worktime).

Чек-лист завершения: `cd web && npm run check`, `make build`, полный Playwright suite, руками - оба theme-скриншота на 390px и 360px (timer, editor с клавиатурой, reports, projects), print-страница с телефона.