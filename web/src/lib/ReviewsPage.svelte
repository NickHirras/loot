<script lang="ts">
  import { untrack } from 'svelte'
  import StatTile from './StatTile.svelte'
  import { reviews } from './reviews.svelte'
  import { sourceName } from './labels'
  import { seriesColor } from './palette'
  import type { ReviewStarFilter } from './types'
  import { REVIEWS_RANGES, dayLabel, flagEmoji, integer, percent } from './types'
  import { m } from '../paraglide/messages'

  $effect(() => untrack(() => reviews.activate()))

  const data = $derived(reviews.data)
  const rangeLabel = $derived(data ? `${data.from} → ${data.to}` : '')
  const isEmpty = $derived(!!data && data.total_ratings === 0 && data.total_reviews === 0 && data.by_app.length === 0)

  const distTotal = $derived(
    data ? data.distribution[0] + data.distribution[1] + data.distribution[2] + data.distribution[3] + data.distribution[4] : 0,
  )

  const STORES = [
    { id: '', label: () => m.reviews_all_stores() },
    { id: 'appstore', label: () => sourceName('appstore') },
    { id: 'googleplay', label: () => sourceName('googleplay') },
    { id: 'microsoftstore', label: () => sourceName('microsoftstore') },
  ] as const

  const STAR_FILTERS: { id: ReviewStarFilter; label: () => string }[] = [
    { id: 'all', label: () => m.reviews_all_stars() },
    { id: '5', label: () => '5★' },
    { id: '4', label: () => '4★' },
    { id: '3', label: () => '3★' },
    { id: 'low', label: () => m.reviews_low_stars() },
  ]

  function starString(n: number): string {
    const filled = Math.max(0, Math.min(5, Math.round(n)))
    return '★'.repeat(filled) + '☆'.repeat(5 - filled)
  }

  function formatAvg(v: number): string {
    return v > 0 ? v.toFixed(2) : '—'
  }

  // --- Rating trend SVG geometry ---
  const W = 640
  const H = 190
  const PAD_L = 34
  const PAD_R = 12
  const PAD_T = 14
  const PAD_B = 26
  const LINE_H = 112
  const BAR_TOP = PAD_T + LINE_H + 10
  const BAR_H = H - PAD_B - BAR_TOP

  const trend = $derived(data?.trend ?? [])
  const maxDaily = $derived(Math.max(1, ...trend.map((p) => p.rating_count)))

  function xAt(i: number): number {
    if (trend.length <= 1) return (PAD_L + W - PAD_R) / 2
    return PAD_L + (i / (trend.length - 1)) * (W - PAD_L - PAD_R)
  }

  function isIsolatedPoint(i: number): boolean {
    const cur = trend[i]?.average_rating
    if (cur === null || cur === undefined || cur <= 0) return false
    const prev = i > 0 ? trend[i - 1]?.average_rating : null
    const next = i < trend.length - 1 ? trend[i + 1]?.average_rating : null
    return (prev === null || prev === undefined || prev <= 0) && (next === null || next === undefined || next <= 0)
  }

  function yRating(v: number): number {
    const clamped = Math.max(1, Math.min(5, v))
    return PAD_T + ((5 - clamped) / 4) * LINE_H
  }

  const linePath = $derived.by(() => {
    let d = ''
    let penDown = false
    for (let i = 0; i < trend.length; i++) {
      const v = trend[i].average_rating
      if (v === null || v <= 0) {
        penDown = false
        continue
      }
      const x = xAt(i).toFixed(1)
      const y = yRating(v).toFixed(1)
      d += penDown ? ` L${x} ${y}` : `M${x} ${y}`
      penDown = true
    }
    return d
  })

  let hoverIdx = $state<number | null>(null)
  const hovered = $derived(hoverIdx !== null && trend[hoverIdx] ? trend[hoverIdx] : null)
</script>

<section class="reviews-page" aria-label={m.reviews_title()}>
  <div class="bar">
    <div class="titles">
      <h2>{m.reviews_title()}</h2>
      {#if rangeLabel}<span class="range mono">{rangeLabel}</span>{/if}
    </div>

    <div class="bar-controls">
      <div class="picker" role="group" aria-label={m.reviews_store_label()}>
        {#each STORES as st (st.id)}
          <button
            type="button"
            class="range-btn"
            class:current={reviews.source === st.id}
            aria-pressed={reviews.source === st.id}
            onclick={() => reviews.setSource(st.id)}
          >
            {st.label()}
          </button>
        {/each}
      </div>

      <div class="picker" role="group" aria-label={m.reviews_range_label()}>
        {#each REVIEWS_RANGES as r (r)}
          <button
            type="button"
            class="range-btn"
            class:current={reviews.range === r}
            aria-pressed={reviews.range === r}
            onclick={() => reviews.setRange(r)}
          >
            {r === 'all' ? 'All' : r === '1y' ? '1Y' : r}
          </button>
        {/each}
      </div>
    </div>
  </div>

  {#if reviews.error && !data}
    <p class="note error">{reviews.error}</p>
  {:else if !data}
    <p class="note">{m.reviews_loading()}</p>
  {:else if isEmpty}
    <div class="empty">
      <h3>
        {reviews.source
          ? m.reviews_empty_store_title({ store: sourceName(reviews.source) })
          : m.reviews_empty_title()}
      </h3>
      <p>{m.reviews_empty_body()}</p>
      {#if reviews.source}
        <button type="button" class="reset-btn" onclick={() => reviews.setSource('')}>
          {m.reviews_show_all_stores()}
        </button>
      {/if}
    </div>
  {:else}
    <div class="tiles">
      <StatTile
        label={m.reviews_tile_avg()}
        value={data.average_rating > 0 ? `★ ${formatAvg(data.average_rating)}` : '—'}
        note={data.total_ratings > 0 ? `${integer(data.total_ratings)} ratings` : ''}
      />
      <StatTile label={m.reviews_tile_ratings()} value={integer(data.total_ratings)} />
      <StatTile label={m.reviews_tile_reviews()} value={integer(data.total_reviews)} />
      <StatTile
        label={data.low_reviews > 0 ? m.reviews_tile_low() : m.reviews_tile_five_star()}
        value={data.low_reviews > 0 ? integer(data.low_reviews) : distTotal > 0 ? percent(data.five_star_share) : '—'}
        note={data.low_reviews > 0 && distTotal > 0 ? `${percent(data.five_star_share)} 5★` : ''}
      />
    </div>

    <div class="top-grid">
      <div class="card chart-card">
        <div class="card-head">
          <h3>{m.reviews_chart_title()}</h3>
          <span class="hint">
            {#if hovered}
              {dayLabel(hovered.day, true)} ·
              {hovered.average_rating !== null ? `★ ${hovered.average_rating.toFixed(2)}` : '—'}
              {#if hovered.rating_count > 0}· +{integer(hovered.rating_count)}{/if}
            {:else}
              {m.reviews_chart_hint()}
            {/if}
          </span>
        </div>

        <svg
          class="chart"
          viewBox="0 0 {W} {H}"
          role="img"
          aria-label={m.reviews_chart_title()}
          onmouseleave={() => (hoverIdx = null)}
        >
          {#each [5, 4, 3, 2, 1] as tick (tick)}
            {@const y = yRating(tick)}
            <line x1={PAD_L} x2={W - PAD_R} y1={y} y2={y} class="grid" />
            <text x={PAD_L - 6} y={y + 3} class="axis" text-anchor="end">{tick}★</text>
          {/each}

          {#if linePath}
            <path d={linePath} class="rating-line" />
          {/if}

          {#each trend as pt, i (pt.day)}
            {@const x = xAt(i)}
            {@const barW = Math.max(2, Math.min(10, (W - PAD_L - PAD_R) / Math.max(1, trend.length) - 1.5))}
            {#if pt.rating_count > 0}
              {@const bh = Math.max(2, (pt.rating_count / maxDaily) * BAR_H)}
              <rect
                x={x - barW / 2}
                y={BAR_TOP + BAR_H - bh}
                width={barW}
                height={bh}
                rx="1.5"
                class="vol-bar"
                class:active={hoverIdx === i}
              />
            {/if}
            {#if hoverIdx === i && pt.average_rating !== null}
              <line x1={x} x2={x} y1={PAD_T} y2={H - PAD_B} class="cursor" />
              <circle cx={x} cy={yRating(pt.average_rating)} r="4" class="dot" />
            {:else if isIsolatedPoint(i) && pt.average_rating !== null}
              <circle cx={x} cy={yRating(pt.average_rating)} r="3.5" class="dot" />
            {/if}
            <rect
              role="presentation"
              x={x - Math.max(4, (W - PAD_L - PAD_R) / Math.max(1, trend.length) / 2)}
              y={PAD_T}
              width={Math.max(8, (W - PAD_L - PAD_R) / Math.max(1, trend.length))}
              height={H - PAD_T - PAD_B}
              fill="transparent"
              onmouseenter={() => (hoverIdx = i)}
            />
          {/each}

          {#if trend.length === 1}
            <text x={xAt(0)} y={H - 6} class="axis" text-anchor="middle">{dayLabel(trend[0].day)}</text>
          {:else if trend.length > 1}
            <text x={PAD_L} y={H - 6} class="axis" text-anchor="start">{dayLabel(trend[0].day)}</text>
            {#if trend.length > 2}
              <text x={(PAD_L + W - PAD_R) / 2} y={H - 6} class="axis" text-anchor="middle">
                {dayLabel(trend[Math.floor(trend.length / 2)].day)}
              </text>
            {/if}
            <text x={W - PAD_R} y={H - 6} class="axis" text-anchor="end">
              {dayLabel(trend[trend.length - 1].day)}
            </text>
          {/if}
        </svg>
      </div>

      <div class="card dist-card">
        <div class="card-head">
          <h3>{m.reviews_distribution()}</h3>
          {#if distTotal > 0}<span class="hint">{integer(distTotal)}</span>{/if}
        </div>
        <ul class="dist">
          {#each [5, 4, 3, 2, 1] as star (star)}
            {@const count = data.distribution[star - 1]}
            {@const frac = distTotal > 0 ? count / distTotal : 0}
            <li>
              <span class="star-lbl">{star}★</span>
              <div class="bar-track">
                <div
                  class="bar-fill"
                  class:low={star <= 2}
                  style="width: {count > 0 ? Math.max(2, frac * 100) : 0}%"
                ></div>
              </div>
              <span class="star-cnt">{integer(count)}</span>
            </li>
          {/each}
        </ul>
      </div>
    </div>

    {#if data.by_app.length > 0}
      <div class="card">
        <div class="card-head">
          <h3>{m.reviews_by_app()}</h3>
        </div>
        <div class="apps-grid">
          {#each data.by_app as app (app.product)}
            <div class="app-card">
              <div class="app-head">
                <span class="app-name">{app.product}</span>
                <span class="app-avg">
                  {app.average_rating > 0 ? `★ ${formatAvg(app.average_rating)}` : '—'}
                </span>
              </div>
              <ul class="store-list">
                {#each app.stores as st (st.source)}
                  <li>
                    <span class="dot-src" style="background: {seriesColor(st.source)}"></span>
                    <span class="st-name">{sourceName(st.source)}</span>
                    <span class="st-meta">
                      {st.average_rating > 0 ? `★ ${formatAvg(st.average_rating)}` : '—'}
                      · {integer(st.rating_count)}
                    </span>
                  </li>
                {/each}
              </ul>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    <div class="card feed-card">
      <div class="feed-head">
        <h3>{m.reviews_feed_title()}</h3>
        <div class="filters">
          <div class="pill-group">
            {#each STAR_FILTERS as sf (sf.id)}
              <button
                type="button"
                class="pill"
                class:active={reviews.stars === sf.id}
                aria-pressed={reviews.stars === sf.id}
                onclick={() => reviews.setStars(sf.id)}
              >
                {sf.label()}
              </button>
            {/each}
          </div>
        </div>
      </div>

      {#if data.reviews.length === 0}
        <p class="empty-inline">{m.reviews_empty_filtered()}</p>
      {:else}
        <ul class="review-list">
          {#each data.reviews as rev (rev.id)}
            <li class="review-item" class:low={rev.rating <= 2}>
              <div class="rev-top">
                <span class="stars" class:low={rev.rating <= 2} aria-label="{rev.rating} of 5 stars">
                  {starString(rev.rating)}
                </span>
                {#if rev.title}<strong class="rev-title">{rev.title}</strong>{/if}
                <span class="rev-meta">
                  <span class="src-badge" style="--c: {seriesColor(rev.source)}">{sourceName(rev.source)}</span>
                  <span>{rev.product}</span>
                  {#if rev.country}<span>{flagEmoji(rev.country)} {rev.country}</span>{/if}
                  <span class="mono">{rev.day}</span>
                </span>
              </div>
              {#if rev.body}
                <p class="rev-body">{rev.body}</p>
              {:else}
                <p class="rev-body muted">{m.reviews_no_body()}</p>
              {/if}
              <div class="rev-foot">
                {#if rev.author}<span>{rev.author}</span>{/if}
                {#if rev.app_version}<span class="mono">v{rev.app_version}</span>{/if}
                {#if rev.device}<span>{rev.device}</span>{/if}
                {#if rev.url}
                  <a href={rev.url} target="_blank" rel="noreferrer">Play Console ↗</a>
                {/if}
              </div>
              {#if rev.reply_text}
                <div class="rev-reply">
                  <span class="reply-lbl">
                    {m.reviews_reply_label()}{#if rev.reply_date} · {rev.reply_date}{/if}
                  </span>
                  <p>{rev.reply_text}</p>
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</section>

<style>
  .reviews-page {
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
  }

  h2 {
    margin: 0;
    font-size: 1.1rem;
  }

  .range {
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .bar-controls {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.45rem;
  }

  .picker {
    display: inline-flex;
    gap: 0.2rem;
    padding: 0.15rem;
    border: 1px solid var(--border-soft);
    border-radius: 999px;
    background: #0d111a;
  }

  .range-btn {
    border: 1px solid transparent;
    background: transparent;
    border-radius: 999px;
    padding: 0.2rem 0.65rem;
    font-size: 0.78rem;
    color: var(--text-dim);
  }

  .range-btn:hover {
    background: var(--panel-2);
    color: var(--text);
  }

  .range-btn.current {
    color: var(--text);
    background: color-mix(in oklab, var(--accent) 14%, var(--panel-2));
    border-color: color-mix(in oklab, var(--accent) 38%, transparent);
  }

  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(180px, 100%), 1fr));
    gap: 0.6rem;
    margin-bottom: 0.9rem;
  }

  .top-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.8fr) minmax(240px, 1fr);
    gap: 0.6rem;
    margin-bottom: 0.9rem;
  }

  @media (max-width: 760px) {
    .top-grid {
      grid-template-columns: 1fr;
    }
  }

  .card {
    padding: 0.9rem 1rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    margin-bottom: 0.9rem;
  }

  .top-grid .card {
    margin-bottom: 0;
  }

  .card-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.6rem;
    margin-bottom: 0.55rem;
  }

  .card-head h3,
  .feed-head h3 {
    margin: 0;
    font-size: 0.82rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-dim);
  }

  .hint {
    font-size: 0.7rem;
    color: var(--text-faint);
    font-variant-numeric: tabular-nums;
  }

  .chart {
    width: 100%;
    height: auto;
    display: block;
  }

  .grid {
    stroke: var(--border-soft);
    stroke-width: 1;
    stroke-dasharray: 2 3;
  }

  .axis {
    fill: var(--text-faint);
    font-size: 10px;
    font-variant-numeric: tabular-nums;
  }

  .rating-line {
    fill: none;
    stroke: var(--legendary);
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }

  .vol-bar {
    fill: color-mix(in oklab, var(--accent) 45%, transparent);
  }

  .vol-bar.active {
    fill: var(--accent);
  }

  .cursor {
    stroke: var(--border);
    stroke-width: 1;
  }

  .dot {
    fill: var(--legendary);
    stroke: #0c1017;
    stroke-width: 1.5;
  }

  .dist {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .dist li {
    display: grid;
    grid-template-columns: 2.2rem 1fr 2.4rem;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.78rem;
  }

  .star-lbl {
    color: var(--legendary);
    font-variant-numeric: tabular-nums;
  }

  .bar-track {
    height: 7px;
    border-radius: 999px;
    background: #0d111a;
    overflow: hidden;
  }

  .bar-fill {
    height: 100%;
    border-radius: 999px;
    background: var(--legendary);
  }

  .bar-fill.low {
    background: var(--cursed);
  }

  .star-cnt {
    text-align: right;
    color: var(--text-dim);
    font-variant-numeric: tabular-nums;
  }

  .apps-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 0.6rem;
  }

  .app-card {
    padding: 0.65rem 0.75rem;
    border: 1px solid var(--border-soft);
    border-radius: 8px;
    background: color-mix(in oklab, var(--panel) 70%, #090c13);
  }

  .app-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
    margin-bottom: 0.45rem;
  }

  .app-name {
    font-weight: 600;
    font-size: 0.86rem;
  }

  .app-avg {
    color: var(--legendary);
    font-weight: 600;
    font-size: 0.86rem;
    font-variant-numeric: tabular-nums;
  }

  .store-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.75rem;
    color: var(--text-dim);
  }

  .store-list li {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  .dot-src {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex: none;
  }

  .st-meta {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
    color: var(--text-faint);
  }

  .feed-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.6rem;
    margin-bottom: 0.8rem;
  }

  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.45rem;
  }

  .pill-group {
    display: inline-flex;
    gap: 0.15rem;
    padding: 0.12rem;
    border: 1px solid var(--border-soft);
    border-radius: 999px;
    background: #0d111a;
  }

  .pill {
    border: 1px solid transparent;
    background: transparent;
    border-radius: 999px;
    padding: 0.18rem 0.55rem;
    font-size: 0.72rem;
    color: var(--text-dim);
  }

  .pill:hover {
    color: var(--text);
  }

  .pill.active {
    color: var(--text);
    background: color-mix(in oklab, var(--accent) 16%, var(--panel-2));
    border-color: color-mix(in oklab, var(--accent) 42%, transparent);
  }

  .review-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.65rem;
  }

  .review-item {
    padding: 0.75rem 0.85rem;
    border: 1px solid var(--border-soft);
    border-radius: 8px;
    background: color-mix(in oklab, var(--panel) 65%, #090c13);
  }

  .review-item.low {
    border-color: color-mix(in oklab, var(--cursed) 38%, var(--border-soft));
  }

  .rev-top {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 0.45rem;
    margin-bottom: 0.35rem;
  }

  .stars {
    color: var(--legendary);
    letter-spacing: 0.04em;
    font-size: 0.82rem;
  }

  .stars.low {
    color: var(--cursed);
  }

  .rev-title {
    font-size: 0.86rem;
    color: var(--text);
  }

  .rev-meta {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .src-badge {
    padding: 0.06rem 0.42rem;
    border-radius: 999px;
    border: 1px solid color-mix(in oklab, var(--c) 45%, transparent);
    background: color-mix(in oklab, var(--c) 12%, transparent);
    color: var(--text-dim);
    font-size: 0.68rem;
  }

  .rev-body {
    margin: 0 0 0.45rem;
    font-size: 0.84rem;
    line-height: 1.5;
    color: var(--text-dim);
    white-space: pre-wrap;
  }

  .rev-body.muted {
    color: var(--text-faint);
    font-style: italic;
  }

  .rev-foot {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    font-size: 0.7rem;
    color: var(--text-faint);
  }

  .rev-foot a {
    color: var(--accent);
    margin-left: auto;
  }

  .rev-reply {
    margin-top: 0.55rem;
    padding: 0.5rem 0.7rem;
    border-left: 2px solid var(--accent);
    background: color-mix(in oklab, var(--accent) 5%, transparent);
    border-radius: 0 6px 6px 0;
  }

  .reply-lbl {
    display: block;
    font-size: 0.68rem;
    font-weight: 600;
    color: var(--text-dim);
    margin-bottom: 0.2rem;
  }

  .rev-reply p {
    margin: 0;
    font-size: 0.78rem;
    color: var(--text-dim);
    white-space: pre-wrap;
  }

  .empty {
    padding: 1.4rem 1.3rem 1.6rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    background: #0e121b;
    color: var(--text-dim);
  }

  .empty h3 {
    margin: 0 0 0.5rem;
    font-size: 1rem;
    color: var(--text);
  }

  .empty p {
    margin: 0;
    font-size: 0.85rem;
    max-width: 64ch;
  }

  .reset-btn {
    margin-top: 0.85rem;
    padding: 0.35rem 0.8rem;
    border-radius: 999px;
    border: 1px solid color-mix(in oklab, var(--accent) 42%, transparent);
    background: color-mix(in oklab, var(--accent) 14%, var(--panel-2));
    color: var(--text);
    font-size: 0.78rem;
  }

  .reset-btn:hover {
    background: color-mix(in oklab, var(--accent) 22%, var(--panel-2));
  }

  .empty-inline {
    margin: 0.5rem 0 0;
    font-size: 0.8rem;
    color: var(--text-faint);
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
