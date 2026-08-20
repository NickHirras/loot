<script lang="ts">
  import { untrack } from 'svelte'
  import Globe from './Globe.svelte'
  import { countryName } from './geo'
  import { hearth } from './hearth.svelte'
  import { rarityColor } from './palette'
  import { router } from './route.svelte'
  import { vesselName } from './sea'
  import { loot } from './state.svelte'
  import { currency, flagEmoji, integer, timeAgo } from './types'

  // The page owns the polling: mounting starts it, leaving stops it. The
  // store already untracks its own setup; untracking here as well means a
  // future edit to either side cannot turn this into a fetch loop.
  $effect(() => untrack(() => hearth.activate()))

  const data = $derived(hearth.data)
  const code = $derived(data?.display_currency ?? 'USD')
  const ambient = $derived(router.ambient)
  const settlements = $derived(data?.countries ?? [])
  /** The vessels: people no source could place. Empty means no section. */
  const fleet = $derived(data?.fleet ?? [])
  const capitalName = $derived(data?.capital ? countryName(data.capital) : '')

  /** How much of the era bar is filled, live. */
  const progress = $derived(Math.round(hearth.eraProgress * 100))

  /** In ambient mode the cursor gets out of the way after a few seconds. */
  let idle = $state(false)
  let idleTimer: ReturnType<typeof setTimeout> | null = null

  function stirCursor() {
    idle = false
    if (idleTimer) clearTimeout(idleTimer)
    idleTimer = setTimeout(() => (idle = true), 3_000)
  }

  $effect(() => {
    if (!ambient) {
      idle = false
      return
    }
    stirCursor()
    addEventListener('pointermove', stirCursor)
    return () => {
      removeEventListener('pointermove', stirCursor)
      if (idleTimer) clearTimeout(idleTimer)
      idle = false
    }
  })
</script>

<svelte:window
  onkeydown={(e) => {
    if (e.key === 'Escape') router.exitAmbient()
  }}
/>

{#if ambient}
  <!--
    Ambient mode: the globe and nothing else. Everything on top of it is a
    label, not a control, apart from the two buttons in the corner.
  -->
  <section class="stage" class:idle aria-label="The Hearth, ambient">
    <Globe countries={settlements} fleet={data?.fleet ?? []} capital={data?.capital ?? ''} {code} ambient />

    <div class="overlay top">
      <div class="era-mini">
        <span class="era-name">{data?.era?.name ?? 'Camp'}</span>
        <span class="era-xp">{integer(hearth.totalXP)} XP</span>
        {#if data?.era?.next_name}
          <span class="era-next">{integer(hearth.toNextEra)} to {data.era.next_name}</span>
        {/if}
      </div>
      <div class="bar wide"><div class="fill" style="width: {progress}%"></div></div>
    </div>

    <div class="overlay corner">
      <button class="ghost" onclick={() => loot.toggleMute()} title={loot.muted ? 'Unmute' : 'Mute'}>
        {loot.muted ? '🔇' : '🔊'}
      </button>
      <button class="ghost" onclick={() => router.exitAmbient()} title="Leave ambient mode (Esc)">✕</button>
    </div>

    <div class="overlay ticker">
      {#each (data?.recent ?? []).slice(0, 8) as drop (drop.id)}
        <span class="tick" style="--r: {rarityColor(drop.rarity)}">
          <span class="dot"></span>
          <span class="flag">{flagEmoji(drop.country)}</span>
          <span class="what">{drop.title}</span>
          <span class="when">{timeAgo(drop.created_at)}</span>
        </span>
      {:else}
        <span class="tick quiet">Waiting for the world to wake up…</span>
      {/each}
    </div>
  </section>
{:else}
  <section class="hearth" aria-label="Hearth">
    <div class="bar-head">
      <div class="titles">
        <h2>Hearth</h2>
        {#if data?.capital}
          <span class="capital">capital {flagEmoji(data.capital)} {capitalName}</span>
        {/if}
      </div>
      <button class="ambient-btn" onclick={() => router.enterAmbient()}>⤢ Ambient</button>
    </div>

    {#if hearth.error && !data}
      <p class="note error">{hearth.error}</p>
    {:else if !data}
      <p class="note">Lighting the fire…</p>
    {:else}
      <div class="layout">
        <div class="globe-card">
          <Globe countries={settlements} fleet={data.fleet ?? []} capital={data.capital} {code} />
          <!-- Two spellings of the same sentence: a phone has no wheel and no
               hover, and telling someone to scroll on a canvas that will not
               is worse than saying nothing. -->
          <p class="hint">
            <span class="pointer-hint">Drag to turn · scroll to zoom · double click to recentre</span>
            <span class="touch-hint">Drag to turn · tap a marker · double tap to recentre</span>
          </p>
        </div>

        <aside class="panel">
          <div class="card era">
            <div class="era-line">
              <span class="era-name">{data.era.name}</span>
              <span class="era-xp">{integer(hearth.totalXP)} XP</span>
            </div>
            <div class="bar"><div class="fill" style="width: {progress}%"></div></div>
            <p class="era-sub">
              {#if data.era.next_name}
                {integer(hearth.toNextEra)} XP to {data.era.next_name}
              {:else}
                The top of the ladder. Nothing left to become.
              {/if}
            </p>
          </div>

          <div class="card">
            <h3>Civilization</h3>
            <dl class="facts">
              <div><dt>Settlements</dt><dd>{integer(settlements.length)}</dd></div>
              <div><dt>Population</dt><dd>{integer(data.population)}</dd></div>
              <div><dt>Revenue</dt><dd>{currency(data.revenue_base, code)}</dd></div>
            </dl>
          </div>

          {#if fleet.length}
            <div class="card">
              <h3>The fleet</h3>
              <ul class="fleet">
                {#each fleet as ship (ship.source)}
                  <li>
                    <span class="anchor">⚓</span>
                    <span class="ship-name">{vesselName(ship.source)}</span>
                    <span class="numbers">
                      <span class="pop">{integer(ship.population)}</span>
                      {#if ship.revenue_base}
                        <span class="money">{currency(ship.revenue_base, code)}</span>
                      {/if}
                    </span>
                  </li>
                {/each}
              </ul>
              <p class="fine">People whose store never says where they are. They sail.</p>
            </div>
          {/if}

          <div class="card grow">
            <h3>Settlements</h3>
            <ul class="settlements">
              {#each settlements as place (place.country)}
                <li class:capital-row={place.country === data.capital}>
                  <span class="flag">{flagEmoji(place.country)}</span>
                  <span class="place">
                    <span class="place-name">{countryName(place.country)}</span>
                    <span class="tier tier-{place.tier?.index ?? 0}">{place.tier?.name ?? 'outpost'}</span>
                  </span>
                  <span class="numbers">
                    <span class="pop">{integer(place.population)}</span>
                    <span class="money">{currency(place.revenue_base, code)}</span>
                  </span>
                </li>
              {:else}
                <li class="empty">No country has bought anything yet. The globe is dark.</li>
              {/each}
            </ul>
          </div>

          <div class="card">
            <h3>Recent arrivals</h3>
            <ul class="arrivals">
              {#each (data.recent ?? []).slice(0, 12) as drop (drop.id)}
                <li style="--r: {rarityColor(drop.rarity)}">
                  <span class="dot"></span>
                  <span class="flag">{flagEmoji(drop.country)}</span>
                  <span class="what">{drop.title}</span>
                  <span class="when">{timeAgo(drop.created_at)}</span>
                </li>
              {:else}
                <li class="empty">Nothing has landed with a country attached yet.</li>
              {/each}
            </ul>
          </div>
        </aside>
      </div>
    {/if}
  </section>
{/if}

<style>
  /* ------------------------------------------------------------- ambient */

  .stage {
    position: fixed;
    inset: 0;
    z-index: 40;
    background:
      radial-gradient(1000px 700px at 50% 45%, #0b1120 0%, transparent 70%),
      linear-gradient(180deg, #05070d, #04060b);
  }

  .stage.idle {
    cursor: none;
  }

  .overlay {
    position: absolute;
    pointer-events: none;
  }

  .overlay.top {
    top: max(1.4rem, env(safe-area-inset-top, 0px));
    left: max(1.6rem, env(safe-area-inset-left, 0px));
    max-width: 340px;
  }

  .era-mini {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    flex-wrap: wrap;
  }

  .era-mini .era-name {
    font-size: 1.5rem;
    font-weight: 700;
    letter-spacing: 0.01em;
    background: linear-gradient(92deg, var(--legendary), var(--epic) 70%, var(--rare));
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }

  .era-mini .era-xp {
    font-size: 0.9rem;
    color: var(--text-dim);
    font-variant-numeric: tabular-nums;
  }

  .era-next {
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .overlay.corner {
    top: max(1.2rem, env(safe-area-inset-top, 0px));
    right: max(1.4rem, env(safe-area-inset-right, 0px));
    display: flex;
    gap: 0.35rem;
    pointer-events: auto;
  }

  .ghost {
    background: color-mix(in oklab, var(--panel) 55%, transparent);
    border-color: transparent;
    opacity: 0.4;
    font-size: 0.8rem;
  }

  .ghost:hover {
    opacity: 1;
  }

  .stage.idle .ghost {
    opacity: 0;
  }

  .overlay.ticker {
    left: 0;
    right: 0;
    bottom: max(1.2rem, env(safe-area-inset-bottom, 0px));
    display: flex;
    justify-content: center;
    flex-wrap: wrap;
    gap: 0.4rem 1.1rem;
    padding: 0 1.5rem;
    font-size: 0.74rem;
    color: var(--text-dim);
  }

  .tick {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    max-width: 320px;
  }

  .tick .what {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .tick.quiet {
    color: var(--text-faint);
    font-style: italic;
  }

  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--r, var(--common));
    box-shadow: 0 0 8px var(--r, var(--common));
    flex: none;
  }

  .when {
    color: var(--text-faint);
    font-variant-numeric: tabular-nums;
  }

  .bar {
    height: 5px;
    margin-top: 0.4rem;
    border-radius: 999px;
    background: #0d111a;
    border: 1px solid var(--border-soft);
    overflow: hidden;
  }

  .bar.wide {
    width: 260px;
  }

  .fill {
    height: 100%;
    background: linear-gradient(90deg, var(--rare), var(--epic), var(--legendary));
    transition: width 0.6s cubic-bezier(0.22, 1, 0.36, 1);
  }

  /* --------------------------------------------------------------- page */

  .hearth {
    max-width: 1240px;
    margin: 0 auto;
    padding: 1rem 1.1rem 3rem;
  }

  .bar-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.6rem;
    margin-bottom: 0.9rem;
  }

  .titles {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
  }

  h2 {
    margin: 0;
    font-size: 1.1rem;
  }

  .capital {
    font-size: 0.74rem;
    color: var(--text-faint);
  }

  .ambient-btn {
    font-size: 0.78rem;
  }

  .layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 320px;
    gap: 0.8rem;
    align-items: start;
  }

  .globe-card {
    position: relative;
    height: min(72vh, 640px);
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: radial-gradient(120% 100% at 50% 30%, #0c1220 0%, #070a12 70%);
    overflow: hidden;
  }

  .touch-hint {
    display: none;
  }

  .globe-card .hint {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0.5rem;
    margin: 0;
    text-align: center;
    font-size: 0.68rem;
    color: var(--text-faint);
    opacity: 0.65;
    pointer-events: none;
  }

  .panel {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }

  .card {
    padding: 0.75rem 0.85rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
  }

  .card h3 {
    margin: 0 0 0.5rem;
    font-size: 0.78rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-dim);
  }

  .era-line {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
  }

  .era .era-name {
    font-size: 1.15rem;
    font-weight: 700;
    color: var(--legendary);
  }

  .era .era-xp {
    font-size: 0.82rem;
    color: var(--text-dim);
    font-variant-numeric: tabular-nums;
  }

  .era-sub {
    margin: 0.35rem 0 0;
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .facts {
    margin: 0;
    display: grid;
    gap: 0.2rem;
    font-size: 0.8rem;
  }

  .facts div {
    display: flex;
    justify-content: space-between;
    gap: 0.6rem;
  }

  dt {
    color: var(--text-faint);
  }

  dd {
    margin: 0;
    font-variant-numeric: tabular-nums;
  }

  .fleet {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }

  .fleet li {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    font-size: 0.78rem;
  }

  .anchor {
    font-size: 0.8rem;
    opacity: 0.75;
  }

  .ship-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .fine {
    margin: 0.55rem 0 0;
    padding-top: 0.45rem;
    border-top: 1px dashed var(--border);
    font-size: 0.68rem;
    color: var(--text-faint);
  }

  .settlements,
  .arrivals {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    max-height: 320px;
    overflow-y: auto;
  }

  .settlements li {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    font-size: 0.78rem;
  }

  .settlements li.capital-row .place-name {
    color: var(--accent);
  }

  .place {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .place-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .tier {
    font-size: 0.6rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-faint);
  }

  .tier-4,
  .tier-5 {
    color: var(--legendary);
  }

  .tier-3 {
    color: var(--text-dim);
  }

  .numbers {
    margin-left: auto;
    text-align: right;
    display: flex;
    flex-direction: column;
    font-variant-numeric: tabular-nums;
  }

  .money {
    font-size: 0.68rem;
    color: var(--text-faint);
  }

  .arrivals li {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.75rem;
    color: var(--text-dim);
  }

  .arrivals .what {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .arrivals .when {
    margin-left: auto;
  }

  .empty {
    color: var(--text-faint);
    font-style: italic;
    font-size: 0.75rem;
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

  /*
   * Ambient mode on a phone: the era line ran the full width of a 375px screen
   * and straight under the two corner buttons. Keep it clear of them, and make
   * the whole caption a size that suits the screen it is on.
   */
  @media (max-width: 560px) {
    .overlay.top {
      max-width: calc(100% - 8.5rem);
    }

    .era-mini .era-name {
      font-size: 1.2rem;
    }

    .era-mini .era-xp {
      font-size: 0.8rem;
    }

    .bar.wide {
      width: 100%;
    }

    .overlay.ticker {
      padding: 0 1rem;
      gap: 0.3rem 0.8rem;
      font-size: 0.7rem;
    }

    .tick {
      max-width: 100%;
    }
  }

  @media (max-width: 900px) {
    .layout {
      grid-template-columns: 1fr;
    }

    .pointer-hint {
      display: none;
    }

    .touch-hint {
      display: inline;
    }

    .globe-card {
      height: min(56vh, 420px);
    }

    .settlements,
    .arrivals {
      max-height: 260px;
    }
  }
</style>
