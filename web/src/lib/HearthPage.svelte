<script lang="ts">
  import { untrack } from 'svelte'
  import Globe from './Globe.svelte'
  import HearthTrendPanel from './HearthTrendPanel.svelte'
  import { fetchHearthTrend } from './api'
  import { countryName } from './geo'
  import { hearth } from './hearth.svelte'
  import { rarityColor } from './palette'
  import { router } from './route.svelte'
  import { scope } from './scope.svelte'
  import { vesselName } from './sea'
  import { loot } from './state.svelte'
  import type { HearthTrend, HearthTrendTarget, TrendRange } from './types'
  import { currency, flagEmoji, integer, timeAgo, trendTargetKey } from './types'
  import { audienceBasisLabel, downloadFormatLabel, eraLabel, sourceName, tierLabel } from './labels'
  import { m } from '../paraglide/messages'

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
  /** Products some store has counted an audience for. */
  const audience = $derived((data?.audience ?? []).filter((p) => p.sources.length > 0))
  /** Products whose release files have been downloaded. */
  const downloads = $derived((data?.audience ?? []).filter((p) => p.downloads.length > 0))

  /** How much of the era bar is filled, live. */
  const progress = $derived(Math.round(hearth.eraProgress * 100))

  // --- trend drill-in -------------------------------------------------------

  let selected = $state<HearthTrendTarget | null>(null)
  let trendRange = $state<TrendRange>('30d')
  let trendData = $state<HearthTrend | null>(null)
  let trendLoading = $state(false)
  let trendError = $state('')
  let trendSeq = 0

  const selectedKey = $derived(trendTargetKey(selected))

  function isSelected(target: HearthTrendTarget): boolean {
    return selectedKey !== '' && selectedKey === trendTargetKey(target)
  }

  async function loadTrend(target: HearthTrendTarget, range: TrendRange) {
    const seq = ++trendSeq
    trendLoading = true
    trendError = ''
    try {
      const res = await fetchHearthTrend(target, range)
      if (seq !== trendSeq) return
      trendData = res
    } catch (err) {
      if (seq !== trendSeq) return
      trendError = err instanceof Error ? err.message : String(err)
    } finally {
      if (seq === trendSeq) trendLoading = false
    }
  }

  function toggleMetric(target: HearthTrendTarget) {
    if (isSelected(target)) {
      closeTrend()
      return
    }
    selectMetric(target)
  }

  function selectMetric(target: HearthTrendTarget) {
    selected = target
    void loadTrend(target, trendRange)
  }

  function changeRange(next: TrendRange) {
    trendRange = next
    if (selected) void loadTrend(selected, next)
  }

  function closeTrend() {
    trendSeq++
    selected = null
    trendData = null
    trendError = ''
    trendLoading = false
  }

  // Keep an open drill-in in step when the global app scope changes.
  $effect(() =>
    untrack(() =>
      scope.onChange(() => {
        if (!selected) return
        if (selected.product && scope.current && selected.product !== scope.current) {
          closeTrend()
          return
        }
        void loadTrend(selected, trendRange)
      }),
    ),
  )

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
    if (e.key === 'Escape') {
      if (ambient) router.exitAmbient()
      else if (selected) closeTrend()
    }
  }}
/>

{#if ambient}
  <!--
    Ambient mode: the globe and nothing else. Everything on top of it is a
    label, not a control, apart from the two buttons in the corner.
  -->
  <section class="stage" class:idle aria-label={m.hearth_ambient_label()}>
    <Globe countries={settlements} fleet={data?.fleet ?? []} capital={data?.capital ?? ''} {code} ambient />

    <div class="overlay top">
      <div class="era-mini">
        <span class="era-name">{eraLabel(data?.era?.name ?? 'Camp')}</span>
        <span class="era-xp">{m.hearth_xp({ xp: integer(hearth.totalXP) })}</span>
        {#if data?.era?.next_name}
          <span class="era-next">
            {m.hearth_era_to_next_short({ xp: integer(hearth.toNextEra), era: eraLabel(data.era.next_name) })}
          </span>
        {/if}
      </div>
      <div class="bar wide"><div class="fill" style="width: {progress}%"></div></div>
    </div>

    <div class="overlay corner">
      <button
        class="ghost"
        onclick={() => loot.toggleMute()}
        title={loot.muted ? m.hearth_ambient_unmute() : m.hearth_ambient_mute()}
      >
        {loot.muted ? '🔇' : '🔊'}
      </button>
      <button class="ghost" onclick={() => router.exitAmbient()} title={m.hearth_ambient_leave()}>✕</button>
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
        <span class="tick quiet">{m.hearth_ticker_empty()}</span>
      {/each}
    </div>
  </section>
{:else}
  <section class="hearth" aria-label={m.hearth_title()}>
    <div class="bar-head">
      <div class="titles">
        <h2>{m.hearth_title()}</h2>
        {#if data?.capital}
          <span class="capital">{m.hearth_capital({ flag: flagEmoji(data.capital), name: capitalName })}</span>
        {/if}
      </div>
      <button class="ambient-btn" onclick={() => router.enterAmbient()}>⤢ {m.hearth_ambient()}</button>
    </div>

    {#if hearth.error && !data}
      <p class="note error">{hearth.error}</p>
    {:else if !data}
      <p class="note">{m.hearth_loading()}</p>
    {:else}
      <div class="layout">
        <div class="main-col">
          {#if selected}
            <button
              type="button"
              class="sheet-backdrop"
              aria-label={m.hearth_trend_close()}
              onclick={closeTrend}
            ></button>
            <div class="trend-host">
              <HearthTrendPanel
                target={selected}
                range={trendRange}
                trend={trendData}
                hearth={data}
                loading={trendLoading}
                error={trendError}
                onRangeChange={changeRange}
                onSelectTarget={selectMetric}
                onClose={closeTrend}
              />
            </div>
          {/if}

          <div class="globe-card" class:compact={!!selected}>
            <Globe countries={settlements} fleet={data.fleet ?? []} capital={data.capital} {code} />
            <!-- Two spellings of the same sentence: a phone has no wheel and no
                 hover, and telling someone to scroll on a canvas that will not
                 is worse than saying nothing. -->
            <p class="hint">
              <span class="pointer-hint">{m.hearth_hint_pointer()}</span>
              <span class="touch-hint">{m.hearth_hint_touch()}</span>
            </p>
          </div>
        </div>

        <aside class="panel">
          <button
            type="button"
            class="card era era-btn"
            class:selected={isSelected({ metric: 'xp' })}
            aria-pressed={isSelected({ metric: 'xp' })}
            title={m.hearth_trend_inspect({ name: m.hearth_trend_xp_title() })}
            onclick={() => toggleMetric({ metric: 'xp' })}
          >
            <div class="era-line">
              <span class="era-name">{eraLabel(data.era.name)}</span>
              <span class="era-xp">
                {m.hearth_xp({ xp: integer(hearth.totalXP) })}
                <svg class="spark-cue" viewBox="0 0 14 10" width="14" height="10" aria-hidden="true">
                  <path d="M1 8.5 L4.5 5 L7.5 6.5 L13 1.5" />
                </svg>
              </span>
            </div>
            <div class="bar"><div class="fill" style="width: {progress}%"></div></div>
            <p class="era-sub">
              {#if data.era.next_name}
                {m.hearth_era_to_next({ xp: integer(hearth.toNextEra), era: eraLabel(data.era.next_name) })}
              {:else}
                {m.hearth_era_top()}
              {/if}
            </p>
          </button>

          {#if audience.length}
            <!-- The plain reading of what the globe draws: active installed base
                 explicitly separated from lifetime all-time store acquisitions. -->
            <div class="card">
              <h3>{m.hearth_audience()}</h3>
              <ul class="audience">
                {#each audience as app (app.product)}
                  {@const appTarget = { metric: 'audience' as const, product: app.product }}
                  {@const activeSources = app.sources.filter((r) => r.basis !== 'arrivals')}
                  {@const allTimeSources = app.sources.filter((r) => r.basis === 'arrivals')}
                  {@const sc = app.scorecard}
                  <li class="app-block">
                    <button
                      type="button"
                      class="drill-btn app-line"
                      class:selected={isSelected(appTarget)}
                      aria-pressed={isSelected(appTarget)}
                      title={m.hearth_trend_inspect({ name: app.product })}
                      onclick={() => toggleMetric(appTarget)}
                    >
                      <span class="app-name">{app.product}</span>
                      <span class="app-right">
                        {#if app.current > 0}
                          <span class="app-total">
                            {m.hearth_audience_active_total({ count: integer(app.current) })}
                          </span>
                        {:else if app.arrivals > 0}
                          <span class="app-total all-time-total">
                            {m.hearth_audience_all_time_total({ count: integer(app.arrivals) })}
                          </span>
                        {/if}
                        <svg class="spark-cue" viewBox="0 0 14 10" width="14" height="10" aria-hidden="true">
                          <path d="M1 8.5 L4.5 5 L7.5 6.5 L13 1.5" />
                        </svg>
                      </span>
                    </button>

                    {#if activeSources.length > 0}
                      <ul class="stores">
                        {#each activeSources as row (row.source)}
                          {@const storeTarget = {
                            metric: 'audience' as const,
                            product: app.product,
                            source: row.source,
                          }}
                          <li class:all-time={row.superseded}>
                            <button
                              type="button"
                              class="drill-btn store-btn"
                              class:selected={isSelected(storeTarget)}
                              aria-pressed={isSelected(storeTarget)}
                              title={[
                                row.as_of ? m.hearth_audience_as_of({ date: row.as_of }) : '',
                                row.superseded ? m.hearth_audience_included_in_active() : '',
                                m.hearth_trend_inspect({ name: `${app.product} ${sourceName(row.source)}` }),
                              ]
                                .filter(Boolean)
                                .join(' · ')}
                              onclick={() => toggleMetric(storeTarget)}
                            >
                              <span class="store-main">
                                <span class="store-name">{sourceName(row.source)}</span>
                                <span class="basis">{audienceBasisLabel(row.basis)}</span>
                                <span class="count">{integer(row.count)}</span>
                              </span>
                              {#if row.all_time && row.all_time > 0}
                                <span class="store-sub">
                                  {m.hearth_audience_all_time_sub({ count: integer(row.all_time) })}
                                </span>
                              {/if}
                            </button>
                          </li>
                        {/each}
                      </ul>
                    {/if}

                    {#if allTimeSources.length > 0}
                      {#if activeSources.length > 0}
                        <div class="group-sep">
                          <span class="group-label">{m.hearth_audience_all_time_group()}</span>
                          <span class="group-line" aria-hidden="true"></span>
                          <span class="group-total">{integer(app.arrivals)}</span>
                        </div>
                      {/if}
                      <ul class="stores">
                        {#each allTimeSources as row (row.source)}
                          {@const storeTarget = {
                            metric: 'audience' as const,
                            product: app.product,
                            source: row.source,
                          }}
                          <li class="all-time">
                            <button
                              type="button"
                              class="drill-btn store-btn"
                              class:selected={isSelected(storeTarget)}
                              aria-pressed={isSelected(storeTarget)}
                              title={m.hearth_trend_inspect({ name: `${app.product} ${sourceName(row.source)}` })}
                              onclick={() => toggleMetric(storeTarget)}
                            >
                              <span class="store-main">
                                <span class="store-name">{sourceName(row.source)}</span>
                                {#if activeSources.length === 0}
                                  <span class="basis">{audienceBasisLabel(row.basis)}</span>
                                {/if}
                                <span class="count">{integer(row.count)}</span>
                              </span>
                            </button>
                          </li>
                        {/each}
                      </ul>
                    {/if}

                    {#if sc && (sc.downloads > 0 || sc.purchases > 0 || sc.refunds > 0)}
                      <div class="app-score">
                        {#if sc.downloads > 0}
                          <span>{m.hearth_audience_score_downloads({ count: sc.downloads })}</span>
                        {/if}
                        {#if sc.purchases > 0}
                          {#if sc.downloads > 0}<span class="sep" aria-hidden="true">·</span>{/if}
                          <span>{m.hearth_audience_score_purchases({ count: sc.purchases })}</span>
                        {/if}
                        {#if sc.refunds > 0}
                          {#if sc.downloads > 0 || sc.purchases > 0}<span class="sep" aria-hidden="true">·</span>{/if}
                          <span class="refunds">{m.hearth_audience_score_refunds({ count: sc.refunds })}</span>
                        {/if}
                      </div>
                    {/if}
                  </li>
                {/each}
              </ul>
              <p class="fine">{m.hearth_audience_fine()}</p>
            </div>
          {/if}

          <div class="card">
            <h3>{m.hearth_civilization()}</h3>
            <div class="facts">
              <button
                type="button"
                class="drill-btn fact-btn"
                class:selected={isSelected({ metric: 'settlements' })}
                aria-pressed={isSelected({ metric: 'settlements' })}
                title={m.hearth_trend_inspect({ name: m.hearth_settlements() })}
                onclick={() => toggleMetric({ metric: 'settlements' })}
              >
                <span class="fact-k">{m.hearth_settlements()}</span>
                <span class="fact-v">{integer(settlements.length)}</span>
              </button>
              <button
                type="button"
                class="drill-btn fact-btn"
                class:selected={isSelected({ metric: 'population' })}
                aria-pressed={isSelected({ metric: 'population' })}
                title={m.hearth_trend_inspect({ name: m.hearth_population() })}
                onclick={() => toggleMetric({ metric: 'population' })}
              >
                <span class="fact-k">{m.hearth_population()}</span>
                <span class="fact-v">{integer(data.population)}</span>
              </button>
              <button
                type="button"
                class="drill-btn fact-btn"
                class:selected={isSelected({ metric: 'revenue' })}
                aria-pressed={isSelected({ metric: 'revenue' })}
                title={m.hearth_trend_inspect({ name: m.hearth_revenue() })}
                onclick={() => toggleMetric({ metric: 'revenue' })}
              >
                <span class="fact-k">{m.hearth_revenue()}</span>
                <span class="fact-v">{currency(data.revenue_base, code)}</span>
              </button>
            </div>
          </div>

          {#if fleet.length}
            <div class="card">
              <h3>{m.hearth_fleet()}</h3>
              <ul class="fleet">
                {#each fleet as ship (ship.source)}
                  {@const shipTarget = { metric: 'fleet' as const, source: ship.source }}
                  <li>
                    <button
                      type="button"
                      class="drill-btn fleet-btn"
                      class:selected={isSelected(shipTarget)}
                      aria-pressed={isSelected(shipTarget)}
                      title={m.hearth_trend_inspect({ name: vesselName(ship.source) })}
                      onclick={() => toggleMetric(shipTarget)}
                    >
                      <span class="anchor">⚓</span>
                      <span class="ship-name">{vesselName(ship.source)}</span>
                      <span class="numbers">
                        <span class="pop">{integer(ship.population)}</span>
                        {#if ship.revenue_base}
                          <span class="money">{currency(ship.revenue_base, code)}</span>
                        {/if}
                      </span>
                    </button>
                  </li>
                {/each}
              </ul>
              <p class="fine">{m.hearth_fleet_fine()}</p>
            </div>
          {/if}

          {#if downloads.length}
            <div class="card">
              <h3>{m.hearth_downloads()}</h3>
              <ul class="audience">
                {#each downloads as app (app.product)}
                  {@const dlAppTarget = { metric: 'downloads' as const, product: app.product }}
                  <li>
                    <button
                      type="button"
                      class="drill-btn app-line"
                      class:selected={isSelected(dlAppTarget)}
                      aria-pressed={isSelected(dlAppTarget)}
                      title={m.hearth_trend_inspect({ name: `${app.product} ${m.hearth_downloads()}` })}
                      onclick={() => toggleMetric(dlAppTarget)}
                    >
                      <span class="app-name">{app.product}</span>
                      <svg class="spark-cue" viewBox="0 0 14 10" width="14" height="10" aria-hidden="true">
                        <path d="M1 8.5 L4.5 5 L7.5 6.5 L13 1.5" />
                      </svg>
                    </button>
                    <ul class="stores">
                      {#each app.downloads as file (file.format)}
                        {@const dlFmtTarget = {
                          metric: 'downloads' as const,
                          product: app.product,
                          format: file.format,
                        }}
                        <li>
                          <button
                            type="button"
                            class="drill-btn store-btn"
                            class:selected={isSelected(dlFmtTarget)}
                            aria-pressed={isSelected(dlFmtTarget)}
                            title={m.hearth_trend_inspect({
                              name: `${app.product} ${downloadFormatLabel(file.format)}`,
                            })}
                            onclick={() => toggleMetric(dlFmtTarget)}
                          >
                            <span class="store-name">{downloadFormatLabel(file.format)}</span>
                            <span class="count">{integer(file.count)}</span>
                          </button>
                        </li>
                      {/each}
                    </ul>
                  </li>
                {/each}
              </ul>
              <p class="fine">{m.hearth_downloads_fine()}</p>
            </div>
          {/if}

          <div class="card grow">
            <h3>{m.hearth_settlements()}</h3>
            <ul class="settlements">
              {#each settlements as place (place.country)}
                {@const countryTarget = { metric: 'country' as const, country: place.country }}
                <li class:capital-row={place.country === data.capital}>
                  <button
                    type="button"
                    class="drill-btn settlement-btn"
                    class:selected={isSelected(countryTarget)}
                    aria-pressed={isSelected(countryTarget)}
                    title={m.hearth_trend_inspect({ name: countryName(place.country) })}
                    onclick={() => toggleMetric(countryTarget)}
                  >
                    <span class="flag">{flagEmoji(place.country)}</span>
                    <span class="place">
                      <span class="place-name">{countryName(place.country)}</span>
                      <span class="tier tier-{place.tier?.index ?? 0}">{tierLabel(place.tier?.name ?? 'outpost')}</span>
                    </span>
                    <span class="numbers">
                      <span class="pop">{integer(place.population)}</span>
                      <span class="money">{currency(place.revenue_base, code)}</span>
                    </span>
                  </button>
                </li>
              {:else}
                <li class="empty">{m.hearth_settlements_empty()}</li>
              {/each}
            </ul>
          </div>

          <div class="card">
            <h3>{m.hearth_arrivals()}</h3>
            <ul class="arrivals">
              {#each (data.recent ?? []).slice(0, 12) as drop (drop.id)}
                <li style="--r: {rarityColor(drop.rarity)}">
                  <span class="dot"></span>
                  <span class="flag">{flagEmoji(drop.country)}</span>
                  <span class="what">{drop.title}</span>
                  <span class="when">{timeAgo(drop.created_at)}</span>
                </li>
              {:else}
                <li class="empty">{m.hearth_arrivals_empty()}</li>
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

  .main-col {
    position: sticky;
    top: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.8rem;
    min-width: 0;
  }

  .sheet-backdrop {
    display: none;
  }

  .trend-host {
    min-width: 0;
  }

  .globe-card {
    position: relative;
    height: min(72vh, 640px);
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: radial-gradient(120% 100% at 50% 30%, #0c1220 0%, #070a12 70%);
    overflow: hidden;
    transition: height 0.2s ease;
  }

  .globe-card.compact {
    height: clamp(240px, 34vh, 360px);
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

  .era-btn {
    width: 100%;
    text-align: left;
    display: block;
  }

  .era-btn:hover {
    border-color: color-mix(in oklab, var(--accent) 32%, var(--border));
    background: linear-gradient(180deg, var(--panel), color-mix(in oklab, var(--accent) 5%, var(--panel-2)));
  }

  .era-btn.selected {
    border-color: color-mix(in oklab, var(--accent) 55%, transparent);
    background: linear-gradient(
      180deg,
      color-mix(in oklab, var(--accent) 8%, var(--panel)),
      color-mix(in oklab, var(--accent) 4%, var(--panel-2))
    );
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
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.82rem;
    color: var(--text-dim);
    font-variant-numeric: tabular-nums;
  }

  .era-sub {
    margin: 0.35rem 0 0;
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  /* Drill-in metric rows: inherit the card's calm typography, reveal a hover
     tint and highlight cleanly when their trend chart is open. */
  .drill-btn {
    width: calc(100% + 0.7rem);
    margin: 0 -0.35rem;
    padding: 0.18rem 0.35rem;
    border: 1px solid transparent;
    border-radius: 6px;
    background: transparent;
    text-align: left;
    line-height: 1.35;
  }

  .drill-btn:hover {
    background: color-mix(in oklab, var(--accent) 7%, transparent);
    border-color: color-mix(in oklab, var(--accent) 18%, transparent);
  }

  .drill-btn.selected {
    background: color-mix(in oklab, var(--accent) 13%, transparent);
    border-color: color-mix(in oklab, var(--accent) 42%, transparent);
    color: var(--text);
  }

  .spark-cue {
    flex: none;
    fill: none;
    stroke: var(--text-faint);
    stroke-width: 1.5;
    stroke-linecap: round;
    stroke-linejoin: round;
    opacity: 0.38;
    transition:
      opacity 0.15s ease,
      stroke 0.15s ease;
  }

  .drill-btn:hover .spark-cue,
  .era-btn:hover .spark-cue,
  .drill-btn.selected .spark-cue,
  .era-btn.selected .spark-cue {
    stroke: var(--accent);
    opacity: 1;
  }

  .facts {
    margin: 0;
    display: grid;
    gap: 0.1rem;
    font-size: 0.8rem;
  }

  .fact-btn {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.6rem;
  }

  .fact-k {
    color: var(--text-faint);
  }

  .fact-btn.selected .fact-k,
  .fact-btn:hover .fact-k {
    color: var(--text-dim);
  }

  .fact-v {
    font-variant-numeric: tabular-nums;
  }

  .audience,
  .stores {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }

  .audience {
    gap: 0.6rem;
  }

  .app-block + .app-block {
    padding-top: 0.55rem;
    border-top: 1px solid var(--border-soft);
  }

  .app-line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.6rem;
    font-size: 0.86rem;
    font-weight: 600;
  }

  .app-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .app-right {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    flex: none;
  }

  .app-total {
    font-size: 0.82rem;
    font-weight: 600;
    color: var(--accent);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  .app-total.all-time-total {
    color: var(--text-dim);
  }

  .stores {
    gap: 0.05rem;
    margin-top: 0.15rem;
  }

  .stores li {
    color: var(--text-dim);
  }

  .store-btn {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
    font-size: 0.76rem;
  }

  .store-btn:has(.store-main) {
    flex-direction: column;
    align-items: stretch;
    gap: 0.06rem;
  }

  .store-main {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
    width: 100%;
  }

  .store-sub {
    align-self: flex-end;
    font-size: 0.65rem;
    color: var(--text-faint);
    font-variant-numeric: tabular-nums;
  }

  .store-name {
    white-space: nowrap;
  }

  .basis {
    font-size: 0.66rem;
    color: var(--text-faint);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .stores .count {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
  }

  .group-sep {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0.32rem 0 0.12rem;
    font-size: 0.64rem;
    color: var(--text-faint);
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }

  .group-line {
    flex: 1;
    height: 1px;
    background: var(--border-soft);
  }

  .group-total {
    font-variant-numeric: tabular-nums;
    color: var(--text-dim);
    text-transform: none;
    letter-spacing: 0;
  }

  /* A running total sits beside the levels but is visibly not one of them. */
  .stores li.all-time,
  .stores li.all-time .count {
    color: var(--text-faint);
  }

  .app-score {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.25rem;
    margin-top: 0.3rem;
    font-size: 0.67rem;
    color: var(--text-faint);
    font-variant-numeric: tabular-nums;
  }

  .app-score .sep {
    opacity: 0.55;
  }

  .app-score .refunds {
    color: var(--cursed);
  }

  .fleet {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }

  .fleet-btn {
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
    gap: 0.15rem;
    max-height: 320px;
    overflow-y: auto;
  }

  .settlements {
    /* Let negative-margin drill buttons breathe inside the scroll container. */
    padding: 0 0.35rem;
    margin: 0 -0.35rem;
  }

  .settlement-btn {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    font-size: 0.78rem;
    width: 100%;
    margin: 0;
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

    .main-col {
      position: static;
    }

    .sheet-backdrop {
      display: block;
      position: fixed;
      inset: 0;
      z-index: 29;
      padding: 0;
      border: 0;
      border-radius: 0;
      background: rgb(4 6 11 / 0.72);
      backdrop-filter: blur(4px);
    }

    .trend-host {
      position: fixed;
      left: 0.75rem;
      right: 0.75rem;
      top: max(1rem, env(safe-area-inset-top, 0px));
      max-height: calc(100dvh - 2rem);
      overflow-y: auto;
      z-index: 30;
    }

    .pointer-hint {
      display: none;
    }

    .touch-hint {
      display: inline;
    }

    .globe-card,
    .globe-card.compact {
      height: min(56vh, 420px);
    }

    .settlements,
    .arrivals {
      max-height: 260px;
    }
  }
</style>
