<script lang="ts">
  import { untrack } from 'svelte'
  import AchievementCard from './AchievementCard.svelte'
  import RecapCard from './RecapCard.svelte'
  import { codexState } from './codex.svelte'
  import type { CodexFilter } from './codex.svelte'
  import { rarityColor } from './palette'
  import { currency, dayLabel, integer, monthLabel } from './types'

  // The page owns the polling: mounting starts it, leaving stops it. The store
  // already untracks its own setup; untracking here as well means a future
  // edit to either side cannot turn this into a fetch loop.
  $effect(() => untrack(() => codexState.activate()))

  const board = $derived(codexState.board)
  const code = $derived(board?.display_currency ?? 'USD')
  const records = $derived(board?.records)
  const totals = $derived(board?.totals)

  const FILTERS: { id: CodexFilter; label: string }[] = [
    { id: 'all', label: 'All' },
    { id: 'unlocked', label: 'Unlocked' },
    { id: 'locked', label: 'To win' },
  ]

  const achievements = $derived(
    (board?.achievements ?? []).filter((a) =>
      codexState.filter === 'unlocked' ? !!a.unlocked_at : codexState.filter === 'locked' ? !a.unlocked_at : true,
    ),
  )

  /** One row of the records list: a label, a value, and where it happened. */
  interface Row {
    label: string
    value: string
    note?: string
  }

  const recordRows: Row[] = $derived.by(() => {
    if (!records || !totals) return []
    const rows: Row[] = []
    const day = (d: string) => (d ? dayLabel(d, true) : '—')

    if (records.best_revenue_day.value > 0) {
      rows.push({
        label: 'Best revenue day',
        value: currency(records.best_revenue_day.value, code, 0),
        note: day(records.best_revenue_day.day),
      })
    }
    for (const s of records.best_revenue_day_by_source ?? []) {
      rows.push({ label: `Best day · ${s.source}`, value: currency(s.value, code, 0), note: day(s.day) })
    }
    if (records.best_units_day.value > 0) {
      rows.push({ label: 'Best units day', value: integer(records.best_units_day.value), note: day(records.best_units_day.day) })
    }
    if (records.best_install_day.value > 0) {
      rows.push({
        label: 'Best install day',
        value: integer(records.best_install_day.value),
        note: day(records.best_install_day.day),
      })
    }
    if (records.most_drops_day.value > 0) {
      rows.push({ label: 'Most drops in a day', value: integer(records.most_drops_day.value), note: day(records.most_drops_day.day) })
    }
    if (records.most_xp_day.value > 0) {
      rows.push({ label: 'Most XP in a day', value: integer(records.most_xp_day.value), note: day(records.most_xp_day.day) })
    }
    if (records.most_countries_day.value > 0) {
      rows.push({
        label: 'Most countries settled in a day',
        value: integer(records.most_countries_day.value),
        note: day(records.most_countries_day.day),
      })
    }
    if (records.biggest_drop) {
      rows.push({
        label: 'Biggest single drop',
        value: `${integer(records.biggest_drop.xp)} XP`,
        note: `${records.biggest_drop.title} · ${day(records.biggest_drop.day)}`,
      })
    }
    if (records.longest_revenue_run.days > 0) {
      rows.push({
        label: 'Longest run of earning days',
        value: `${integer(records.longest_revenue_run.days)} days`,
        note: `ended ${day(records.longest_revenue_run.ended_on)}`,
      })
    }
    if (records.first_event_day) {
      rows.push({
        label: 'First ever event',
        value: day(records.first_event_day),
        note: `${integer(records.days_since_first_event)} days of history`,
      })
    }
    return rows
  })

  const totalRows: Row[] = $derived.by(() => {
    if (!totals) return []
    const rows: Row[] = [
      { label: 'Lifetime revenue', value: currency(totals.revenue_base, code, 0) },
      { label: 'Units sold', value: integer(totals.units), note: totals.refunds ? `${integer(totals.refunds)} refunded` : undefined },
      { label: 'Installs', value: integer(totals.installs) },
      { label: 'Drops', value: integer(totals.drops) },
      { label: 'XP', value: integer(totals.xp), note: `${totals.era.name} era` },
      { label: 'Chests opened', value: integer(totals.chests_opened) },
      {
        label: 'Countries settled',
        value: integer(totals.countries),
        note: `${integer(totals.continents)} of 6 continents`,
      },
      { label: 'Currencies taken', value: integer(totals.currencies) },
      { label: 'Record days', value: integer(totals.record_days) },
      { label: 'Quests completed', value: integer(totals.quests_completed) },
      { label: 'Mysteries explained', value: integer(totals.mysteries_solved) },
    ]
    if (totals.stars > 0) rows.push({ label: 'GitHub stars', value: integer(totals.stars) })
    return rows
  })

  const recap = $derived(codexState.recap)
  const accent = $derived(rarityColor(recap?.top_rarity || 'rare'))
</script>

<section class="codex" aria-label="Codex" style="--accent-poster: {accent}">
  <div class="bar">
    <div class="titles">
      <h2>Codex</h2>
      <span class="sub">everything that has ever happened · trophies are permanent</span>
    </div>
    {#if board}
      <span class="counter" title="{board.unlocked} of {board.total} achievements unlocked">
        <strong>{board.unlocked}</strong> / {board.total}
      </span>
    {/if}
  </div>

  {#if codexState.error && !board}
    <p class="note error">{codexState.error}</p>
  {:else if codexState.loading && !board}
    <p class="note">Opening the codex…</p>
  {:else}
    <h3 class="section">
      Trophy wall
      <span class="chips" role="group" aria-label="Filter achievements">
        {#each FILTERS as f (f.id)}
          <button
            class="chip"
            class:current={codexState.filter === f.id}
            aria-pressed={codexState.filter === f.id}
            onclick={() => codexState.setFilter(f.id)}
          >
            {f.label}
          </button>
        {/each}
      </span>
    </h3>
    <p class="lede">
      Achievements only ever unlock. Nothing here decays, expires or can be taken away — a run that ended still
      happened. Each unlock pays a real drop: uncommon for bronze, rare for silver, epic for gold, legendary for
      legendary.
    </p>

    {#if achievements.length === 0}
      <p class="empty">Nothing to show with this filter.</p>
    {:else}
      <div class="grid">
        {#each achievements as achievement (achievement.key)}
          <AchievementCard {achievement} {code} />
        {/each}
      </div>
    {/if}

    <h3 class="section">Records</h3>
    <p class="lede">
      Computed fresh every time, never stored — so a restated report improves a record instead of leaving a stale one
      behind, and a record can only ever go up.
    </p>
    <div class="columns">
      <ul class="rows">
        {#each recordRows as row (row.label)}
          <li>
            <span class="r-label">{row.label}</span>
            <span class="r-value">{row.value}</span>
            {#if row.note}<span class="r-note">{row.note}</span>{/if}
          </li>
        {:else}
          <li class="r-empty">No records yet — they arrive with the first day that beats the one before it.</li>
        {/each}
      </ul>
      <ul class="rows">
        {#each totalRows as row (row.label)}
          <li>
            <span class="r-label">{row.label}</span>
            <span class="r-value">{row.value}</span>
            {#if row.note}<span class="r-note">{row.note}</span>{/if}
          </li>
        {/each}
      </ul>
    </div>

    <h3 class="section">Season recap</h3>
    <div class="picker" role="group" aria-label="Recap period">
      {#each codexState.periods as period (period.key)}
        <button
          class="period"
          class:current={codexState.period === period.key}
          class:season={period.kind === 'season'}
          aria-pressed={codexState.period === period.key}
          onclick={() => codexState.setPeriod(period.key)}
        >
          {period.kind === 'season' ? 'This year' : monthLabel(period)}
        </button>
      {/each}
    </div>

    {#if codexState.recapError}
      <p class="note error">{codexState.recapError}</p>
    {:else if !recap}
      <p class="note">Writing it up…</p>
    {:else}
      <div class="poster-wrap" class:dim={codexState.recapLoading}>
        <RecapCard {recap} />
      </div>
    {/if}
  {/if}
</section>

<style>
  .codex {
    max-width: 980px;
    margin: 0 auto;
    padding: 1rem 1.1rem 4rem;
  }

  .bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.6rem;
    margin-bottom: 0.9rem;
  }

  .titles {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    flex-wrap: wrap;
  }

  h2 {
    margin: 0;
    font-size: 1.1rem;
  }

  .sub {
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .counter {
    font-size: 0.8rem;
    color: var(--text-dim);
    font-family: var(--mono);
    padding: 0.15rem 0.55rem;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--panel);
    cursor: help;
  }

  .counter strong {
    color: var(--legendary);
    font-size: 0.95rem;
  }

  .section {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin: 1.6rem 0 0.5rem;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-faint);
    font-weight: 600;
  }

  .chips {
    display: flex;
    gap: 0.25rem;
  }

  .chip {
    font-size: 0.68rem;
    text-transform: none;
    letter-spacing: 0;
    padding: 0.12rem 0.5rem;
    border-radius: 999px;
    color: var(--text-dim);
  }

  .chip.current {
    color: var(--text);
    border-color: color-mix(in oklab, var(--accent) 40%, transparent);
    background: color-mix(in oklab, var(--accent) 12%, var(--panel-2));
  }

  .lede {
    margin: -0.1rem 0 0.7rem;
    font-size: 0.78rem;
    color: var(--text-dim);
    max-width: 74ch;
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(230px, 100%), 1fr));
    gap: 0.5rem;
    align-items: start;
  }

  .columns {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(300px, 100%), 1fr));
    gap: 0.6rem 1.6rem;
  }

  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  /* A two-column grid rather than a flex row: a label long enough to wrap
     ("Most countries settled in a day") must wrap *within its own column*,
     not around the number, which is what a flex row did to it. */
  .rows li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    column-gap: 0.8rem;
    padding: 0.34rem 0;
    border-bottom: 1px dashed var(--border-soft);
    font-size: 0.8rem;
  }

  .r-label {
    grid-column: 1;
    color: var(--text-dim);
  }

  .r-value {
    grid-column: 2;
    grid-row: 1;
    text-align: right;
    font-weight: 600;
    font-family: var(--mono);
    white-space: nowrap;
  }

  .r-note {
    grid-column: 1 / -1;
    font-size: 0.68rem;
    color: var(--text-faint);
    text-align: right;
  }

  .r-empty {
    display: block;
    color: var(--text-faint);
    font-style: italic;
    border-bottom: 0;
  }

  .picker {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
    margin-bottom: 0.7rem;
  }

  .period {
    font-size: 0.72rem;
    padding: 0.18rem 0.55rem;
    border-radius: 999px;
    color: var(--text-dim);
  }

  .period.current {
    color: var(--text);
    border-color: color-mix(in oklab, var(--accent-poster) 45%, transparent);
    background: color-mix(in oklab, var(--accent-poster) 14%, var(--panel-2));
  }

  .period.season {
    font-weight: 600;
  }

  .poster-wrap {
    transition: opacity 0.2s ease;
  }

  .poster-wrap.dim {
    opacity: 0.55;
  }

  .empty {
    padding: 0.9rem 1rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    background: #0e121b;
    color: var(--text-dim);
    font-size: 0.82rem;
    max-width: 72ch;
  }

  .note {
    margin: 1.5rem auto;
    text-align: center;
    color: var(--text-faint);
    font-size: 0.85rem;
  }

  .note.error {
    color: var(--cursed);
  }
</style>
