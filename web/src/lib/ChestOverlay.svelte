<script lang="ts">
  import ChestIcon from './ChestIcon.svelte'
  import { loot } from './state.svelte'
  import type { ChestSummary, Rarity } from './types'
  import { RARITIES, flagEmoji, integer, money } from './types'

  const phase = $derived(loot.chestPhase)
  const chests = $derived(loot.chests)
  const current = $derived(loot.chestCurrent)
  const revealed = $derived(loot.chestRevealed)
  const total = $derived(loot.chestExpected.length)
  const best = $derived(loot.chestBest)
  const bulk = $derived(loot.chestBulk)
  const byRarity = $derived(loot.chestHaulByRarity)
  const highlights = $derived(loot.chestHighlights)
  const highlightCount = $derived(loot.chestHighlightCount)

  /** Rarity dots for a chest row, skipping rarities the chest does not hold. */
  function dots(chest: ChestSummary): { rarity: Rarity; count: number }[] {
    return RARITIES.filter((r) => (chest.by_rarity?.[r] ?? 0) > 0).map((r) => ({
      rarity: r,
      count: chest.by_rarity[r],
    }))
  }

  /** "6 chests · Aug 12 – Aug 17", the label over a bulk haul. */
  function bulkLabel(days: string[]): string {
    if (days.length === 0) return 'Every chest'
    const span =
      days.length === 1
        ? dayLabelLong(days[0])
        : `${dayLabelLong(days[0])} – ${dayLabelLong(days[days.length - 1])}`
    return `${days.length} ${days.length === 1 ? 'chest' : 'chests'} · ${span}`
  }

  function dayLabelLong(day: string): string {
    const parsed = new Date(`${day}T00:00:00Z`)
    if (Number.isNaN(parsed.getTime())) return day
    return parsed.toLocaleDateString(undefined, {
      weekday: 'short',
      month: 'short',
      day: 'numeric',
      timeZone: 'UTC',
    })
  }
</script>

<div
  class="scrim"
  role="dialog"
  aria-modal="true"
  aria-label="Daily chest"
  tabindex="-1"
  onclick={(e) => {
    if (e.target === e.currentTarget && !loot.chestBusy) loot.hideChest()
  }}
  onkeydown={(e) => {
    if (e.key === 'Escape' && !loot.chestBusy) loot.hideChest()
  }}
>
  <div class="sheet" class:wide={phase === 'cascade' || phase === 'done'}>
    <button class="close" onclick={() => loot.hideChest()} disabled={loot.chestBusy} aria-label="Close">✕</button>

    {#if phase === 'idle'}
      <header class="head">
        <ChestIcon size={40} glow={chests.length > 0} idle={chests.length > 0} />
        <div>
          <h2>Daily chest</h2>
          <p class="sub">
            {#if chests.length}
              {integer(loot.chestCount)}
              {loot.chestCount === 1 ? 'drop' : 'drops'} waiting in {chests.length}
              {chests.length === 1 ? 'chest' : 'chests'}.
            {:else}
              Nothing waiting. A day of store sales lands here as one chest.
            {/if}
          </p>
        </div>
      </header>

      {#if chests.length}
        <ul class="chests">
          {#each chests as chest (chest.date)}
            <li class="chest-row">
              <div class="when">
                <span class="date">{dayLabelLong(chest.date)}</span>
                <span class="iso mono">{chest.date}</span>
              </div>
              <div class="counts">
                <span class="count">{chest.count} {chest.count === 1 ? 'drop' : 'drops'}</span>
                <span class="xp">+{integer(chest.xp)} XP</span>
              </div>
              <ul class="dots">
                {#each dots(chest) as dot (dot.rarity)}
                  <li class="dot rarity-{dot.rarity}" title="{dot.count} {dot.rarity}">
                    <span class="pip"></span>{dot.count}
                  </li>
                {/each}
              </ul>
              <button class="open" onclick={() => loot.open(chest.date)} disabled={loot.chestBusy}>Open</button>
            </li>
          {/each}
        </ul>

        <button class="primary" onclick={() => loot.open()} disabled={loot.chestBusy}>
          Open the oldest chest
        </button>

        {#if chests.length > 1}
          <button class="bulk" onclick={() => loot.openAll()} disabled={loot.chestBusy}>
            Open all · {chests.length} chests
            <span class="bulk-sub">{integer(loot.chestCount)} drops in one go</span>
          </button>
        {/if}
      {/if}

      {#if loot.chestError}
        <p class="error">{loot.chestError}</p>
      {/if}
    {:else if phase === 'opening'}
      <div class="stage">
        <div class="lid-stage">
          <ChestIcon size={190} open glow />
        </div>
        <p class="opening">
          {#if bulk}
            Opening every chest…
          {:else}
            Opening {loot.chestDate || 'the chest'}…
          {/if}
        </p>
      </div>
    {:else if bulk}
      <div class="stage">
        <div class="progress">
          <span class="counter mono">{revealed.length} / {total} drops</span>
          {#if phase === 'cascade'}
            <button class="skip" onclick={() => loot.skipCascade()}>Skip</button>
          {/if}
        </div>

        {#if phase === 'done'}
          <div class="final">
            <p class="final-label">{bulkLabel(loot.chestDates)}</p>
            <p class="final-xp">+{integer(loot.chestHaulXP)} XP</p>

            <ul class="tally">
              {#each RARITIES as rarity (rarity)}
                <li class="tally-item rarity-{rarity}" class:empty={!byRarity[rarity]}>
                  <span class="tally-count">{byRarity[rarity] ?? 0}</span>
                  <span class="tally-name">{rarity}</span>
                </li>
              {/each}
            </ul>

            {#if best}
              <div class="best rarity-{best.rarity}">
                <span class="badge">best · {best.rarity}</span>
                <span class="best-title">{best.title}</span>
              </div>
            {/if}

            {#if highlights.length}
              <ul class="highlights">
                {#each highlights as drop (drop.id)}
                  <li class="haul-row rarity-{drop.rarity}">
                    <span class="pip"></span>
                    <span class="haul-title">{drop.title}</span>
                    <span class="haul-xp">+{drop.xp}</span>
                  </li>
                {/each}
                {#if highlightCount > highlights.length}
                  <li class="more">and {highlightCount - highlights.length} more below</li>
                {/if}
              </ul>
            {/if}

            <button class="primary" onclick={() => loot.hideChest()}>Nice.</button>
          </div>
        {/if}

        <ul class="grid">
          {#each revealed as drop (drop.id)}
            <li class="cell rarity-{drop.rarity}" title="{drop.title} · +{drop.xp} XP">
              <span class="pip"></span>
              <span class="cell-title">{drop.title}</span>
              <span class="cell-xp">+{drop.xp}</span>
            </li>
          {/each}
        </ul>
      </div>
    {:else}
      <div class="stage">
        <div class="progress">
          <span class="counter mono">{revealed.length} / {total}</span>
          {#if phase === 'cascade'}
            <button class="skip" onclick={() => loot.skipCascade()}>Skip</button>
          {/if}
        </div>

        {#if phase === 'cascade' && current}
          {#key current.id}
            <article class="big rarity-{current.rarity}">
              <span class="badge">{current.rarity}</span>
              <h3>{current.title}</h3>
              {#if current.subtitle}<p class="sub">{current.subtitle}</p>{/if}
              <div class="meta">
                <span class="xp-big">+{integer(current.xp)} XP</span>
                {#if money(current.amount, current.currency)}
                  <span class="chip money">{money(current.amount, current.currency)}</span>
                {/if}
                {#if current.country}<span class="chip">{flagEmoji(current.country)} {current.country}</span>{/if}
                <span class="chip mono">{current.source}</span>
              </div>
            </article>
          {/key}
        {:else if phase === 'done'}
          <div class="final">
            <p class="final-label">Chest of {loot.chestDate}</p>
            <p class="final-xp">+{integer(loot.chestHaulXP)} XP</p>
            {#if best}
              <div class="best rarity-{best.rarity}">
                <span class="badge">best · {best.rarity}</span>
                <span class="best-title">{best.title}</span>
              </div>
            {/if}
            <button class="primary" onclick={() => loot.hideChest()}>Nice.</button>
          </div>
        {/if}

        <ul class="haul">
          {#each revealed as drop (drop.id)}
            <li class="haul-row rarity-{drop.rarity}">
              <span class="pip"></span>
              <span class="haul-title">{drop.title}</span>
              <span class="haul-xp">+{drop.xp}</span>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 40;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 1rem;
    background: color-mix(in oklab, #04060b 78%, transparent);
    backdrop-filter: blur(6px);
    animation: fade 0.18s ease-out both;
  }

  .sheet {
    position: relative;
    width: min(520px, 100%);
    max-height: min(86vh, 760px);
    overflow: auto;
    padding: 1.3rem 1.3rem 1.4rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel-2), var(--panel));
    box-shadow: 0 30px 80px rgb(0 0 0 / 0.6);
    animation: rise 0.24s cubic-bezier(0.22, 1, 0.36, 1) both;
  }

  .sheet.wide {
    width: min(620px, 100%);
  }

  .close {
    position: absolute;
    top: 0.7rem;
    right: 0.7rem;
    padding: 0.15rem 0.45rem;
    font-size: 0.8rem;
    color: var(--text-faint);
    background: transparent;
    border-color: transparent;
  }

  .close:disabled {
    opacity: 0.25;
    cursor: not-allowed;
  }

  .head {
    display: flex;
    align-items: center;
    gap: 0.9rem;
    margin-bottom: 1rem;
  }

  h2 {
    margin: 0;
    font-size: 1.05rem;
  }

  .sub {
    margin: 0.15rem 0 0;
    font-size: 0.82rem;
    color: var(--text-dim);
  }

  /* --- the waiting list --------------------------------------------------- */

  .chests {
    list-style: none;
    margin: 0 0 0.9rem;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }

  .chest-row {
    display: grid;
    grid-template-columns: 1fr auto auto;
    grid-template-areas:
      'when counts open'
      'dots dots open';
    gap: 0.25rem 0.7rem;
    align-items: center;
    padding: 0.55rem 0.7rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius-sm);
    background: #0e121b;
  }

  .when {
    grid-area: when;
    display: flex;
    align-items: baseline;
    gap: 0.45rem;
  }

  .date {
    font-weight: 600;
    font-size: 0.9rem;
  }

  .iso {
    font-size: 0.68rem;
    color: var(--text-faint);
  }

  .counts {
    grid-area: counts;
    display: flex;
    gap: 0.6rem;
    font-size: 0.75rem;
    color: var(--text-dim);
  }

  .counts .xp {
    color: var(--legendary);
  }

  .dots {
    grid-area: dots;
    list-style: none;
    display: flex;
    flex-wrap: wrap;
    gap: 0.45rem;
    margin: 0;
    padding: 0;
    font-size: 0.68rem;
    color: var(--text-faint);
  }

  .dot {
    display: inline-flex;
    align-items: center;
    gap: 0.22rem;
  }

  .pip {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--r, var(--common));
    box-shadow: 0 0 6px color-mix(in oklab, var(--r, var(--common)) 60%, transparent);
  }

  .open {
    grid-area: open;
    align-self: center;
    font-size: 0.78rem;
  }

  .primary {
    width: 100%;
    padding: 0.55rem;
    font-weight: 650;
    color: #1a1405;
    border-color: transparent;
    background: linear-gradient(92deg, var(--legendary), #ffd97a);
  }

  .primary:hover {
    background: linear-gradient(92deg, #ffcf5c, var(--legendary));
  }

  .primary:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  /* A second, quieter action under the primary one: same shape, no gold. */
  .bulk {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.1rem;
    width: 100%;
    margin-top: 0.45rem;
    padding: 0.45rem 0.55rem;
    font-weight: 600;
    font-size: 0.85rem;
    color: var(--text);
    border: 1px solid color-mix(in oklab, var(--legendary) 35%, var(--border));
    background: color-mix(in oklab, var(--legendary) 7%, #0e121b);
  }

  .bulk:hover {
    background: color-mix(in oklab, var(--legendary) 14%, #0e121b);
  }

  .bulk:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  .bulk-sub {
    font-weight: 400;
    font-size: 0.7rem;
    color: var(--text-faint);
  }

  .error {
    margin: 0.7rem 0 0;
    font-size: 0.78rem;
    color: var(--cursed);
  }

  /* --- the reveal --------------------------------------------------------- */

  .stage {
    display: flex;
    flex-direction: column;
    gap: 0.8rem;
    min-height: 300px;
  }

  .lid-stage {
    display: grid;
    place-items: center;
    padding: 2.2rem 0 1rem;
  }

  .opening {
    text-align: center;
    color: var(--text-dim);
    font-size: 0.85rem;
    margin: 0;
  }

  .progress {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.6rem;
  }

  .counter {
    font-size: 0.72rem;
    color: var(--text-faint);
    letter-spacing: 0.06em;
  }

  .skip {
    font-size: 0.72rem;
    color: var(--text-dim);
  }

  .big {
    border: 1px solid color-mix(in oklab, var(--r) 45%, var(--border));
    border-radius: var(--radius);
    background:
      radial-gradient(120% 100% at 50% 0%, color-mix(in oklab, var(--r) 16%, transparent), transparent 70%),
      linear-gradient(180deg, var(--panel), var(--panel-2));
    padding: 1.1rem 1.1rem 1.2rem;
    text-align: center;
    box-shadow: 0 0 30px color-mix(in oklab, var(--r) 22%, transparent);
    animation: pop 0.45s cubic-bezier(0.22, 1.3, 0.36, 1) both;
  }

  .badge {
    display: inline-block;
    text-transform: uppercase;
    letter-spacing: 0.09em;
    font-weight: 700;
    font-size: 0.64rem;
    color: var(--r);
    background: color-mix(in oklab, var(--r) 14%, transparent);
    border: 1px solid color-mix(in oklab, var(--r) 38%, transparent);
    padding: 0.1rem 0.45rem;
    border-radius: 999px;
  }

  .big h3 {
    margin: 0.5rem 0 0;
    font-size: 1.3rem;
    line-height: 1.25;
    overflow-wrap: anywhere;
  }

  .big .sub {
    margin: 0.25rem 0 0;
  }

  .meta {
    display: flex;
    justify-content: center;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.4rem;
    margin-top: 0.7rem;
  }

  .xp-big {
    font-weight: 700;
    color: color-mix(in oklab, var(--r) 80%, var(--text));
  }

  .chip {
    font-size: 0.72rem;
    color: var(--text-dim);
    background: #0e121b;
    border: 1px solid var(--border-soft);
    border-radius: 999px;
    padding: 0.08rem 0.5rem;
  }

  .chip.money {
    color: var(--uncommon);
    border-color: color-mix(in oklab, var(--uncommon) 30%, transparent);
  }

  /* --- final screen ------------------------------------------------------- */

  .final {
    text-align: center;
    padding: 0.6rem 0 0.2rem;
    animation: pop 0.4s cubic-bezier(0.22, 1.3, 0.36, 1) both;
  }

  .final-label {
    margin: 0;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.09em;
    color: var(--text-faint);
  }

  .final-xp {
    margin: 0.1rem 0 0.7rem;
    font-size: 2.6rem;
    font-weight: 700;
    line-height: 1;
    background: linear-gradient(92deg, var(--legendary), var(--epic) 70%, var(--rare));
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }

  .best {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    max-width: 100%;
    margin-bottom: 0.9rem;
    padding: 0.45rem 0.7rem;
    border: 1px solid color-mix(in oklab, var(--r) 40%, var(--border));
    border-radius: var(--radius-sm);
    background: color-mix(in oklab, var(--r) 10%, #0e121b);
  }

  .best-title {
    font-size: 0.86rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* --- the bulk fill ------------------------------------------------------ */

  .grid {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 240px), 1fr));
    gap: 0.25rem;
  }

  .cell {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    padding: 0.28rem 0.5rem;
    border: 1px solid color-mix(in oklab, var(--r) 22%, var(--border-soft));
    border-radius: var(--radius-sm);
    background: color-mix(in oklab, var(--r) 6%, #0e121b);
    font-size: 0.76rem;
    animation: pop 0.22s ease-out both;
  }

  .cell-title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-dim);
  }

  .cell-xp {
    font-size: 0.72rem;
    color: color-mix(in oklab, var(--r) 75%, var(--text));
  }

  /* The six counters on a bulk haul, mirroring the header's rarity tally. */
  .tally {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 0.35rem;
    list-style: none;
    margin: 0 0 0.9rem;
    padding: 0;
  }

  .tally-item {
    min-width: 62px;
    padding: 0.25rem 0.45rem;
    border-radius: var(--radius-sm);
    background: color-mix(in oklab, var(--r) 8%, #0d111a);
    border: 1px solid color-mix(in oklab, var(--r) 26%, transparent);
  }

  .tally-item.empty {
    opacity: 0.35;
  }

  .tally-count {
    display: block;
    font-weight: 700;
    font-size: 0.95rem;
    line-height: 1.15;
    color: var(--r);
  }

  .tally-name {
    display: block;
    font-size: 0.6rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-faint);
  }

  /* Settlements and trophies: the drops of a bulk haul worth reading. */
  .highlights {
    list-style: none;
    margin: 0 0 0.9rem;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    text-align: left;
  }

  .highlights .more {
    padding: 0.1rem 0.55rem;
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  /* --- the growing haul --------------------------------------------------- */

  .haul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .haul-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.3rem 0.55rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius-sm);
    background: #0e121b;
    font-size: 0.8rem;
    animation: slide 0.3s ease-out both;
  }

  .haul-title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-dim);
  }

  .haul-xp {
    font-size: 0.75rem;
    color: color-mix(in oklab, var(--r) 75%, var(--text));
  }

  @keyframes fade {
    from {
      opacity: 0;
    }
  }

  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(14px) scale(0.98);
    }
  }

  @keyframes pop {
    from {
      opacity: 0;
      transform: scale(0.9);
    }
  }

  @keyframes slide {
    from {
      opacity: 0;
      transform: translateX(-8px);
    }
  }

  /* Reduced motion fills the haul instantly (see LootState.openAll); the
     cards must not animate their way in behind it either. */
  @media (prefers-reduced-motion: reduce) {
    .scrim,
    .sheet,
    .big,
    .final,
    .cell,
    .haul-row {
      animation: none;
    }
  }
</style>
