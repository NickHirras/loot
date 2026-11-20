<script lang="ts">
  import TrendChart from './TrendChart.svelte'
  import { countryName } from './geo'
  import { audienceBasisLabel, downloadFormatLabel, sourceName, tierLabel, trendRangeLabel } from './labels'
  import { vesselName } from './sea'
  import type { Hearth, HearthTrend, HearthTrendTarget, TrendRange } from './types'
  import { TREND_RANGES, currency, dayLabel, flagEmoji, integer, percent } from './types'
  import { m } from '../paraglide/messages'

  let {
    target,
    range,
    trend,
    hearth,
    loading = false,
    error = '',
    onRangeChange,
    onSelectTarget,
    onClose,
  }: {
    target: HearthTrendTarget
    range: TrendRange
    trend: HearthTrend | null
    hearth: Hearth | null
    loading?: boolean
    error?: string
    onRangeChange: (r: TrendRange) => void
    onSelectTarget: (next: HearthTrendTarget) => void
    onClose: () => void
  } = $props()

  const code = $derived(trend?.display_currency || hearth?.display_currency || 'USD')

  /** Human-readable headline for the currently selected metric. */
  const title = $derived.by(() => {
    switch (target.metric) {
      case 'audience':
        return target.source
          ? `${target.product} · ${sourceName(target.source)}`
          : (target.product ?? m.hearth_audience())
      case 'population':
        return m.hearth_population()
      case 'settlements':
        return m.hearth_settlements()
      case 'revenue':
        return m.hearth_revenue()
      case 'fleet':
        return vesselName(target.source ?? '')
      case 'downloads':
        return target.format
          ? `${target.product} · ${downloadFormatLabel(target.format)}`
          : `${target.product} · ${m.hearth_downloads()}`
      case 'country':
        return `${flagEmoji(target.country ?? '')} ${countryName(target.country ?? '')}`.trim()
      case 'xp':
        return m.hearth_trend_xp_title()
    }
  })

  /** Contextual subtitle explaining what the metric measures. */
  const subtitle = $derived.by(() => {
    switch (target.metric) {
      case 'audience':
        if (trend?.basis) return audienceBasisLabel(trend.basis)
        return m.hearth_audience()
      case 'population':
      case 'settlements':
      case 'revenue':
        return m.hearth_civilization()
      case 'fleet':
        return m.hearth_fleet()
      case 'downloads':
        return m.hearth_downloads()
      case 'country': {
        const place = hearth?.countries?.find((c) => c.country === target.country)
        return place?.tier ? tierLabel(place.tier.name) : m.hearth_settlements()
      }
      case 'xp':
        return hearth?.era?.name ?? ''
    }
  })

  /** Optional parent drill-up link when viewing a single store or format. */
  const parentLink = $derived.by((): { label: string; target: HearthTrendTarget } | null => {
    if (target.metric === 'audience' && target.source && target.product) {
      return {
        label: target.product,
        target: { metric: 'audience', product: target.product },
      }
    }
    if (target.metric === 'downloads' && target.format && target.product) {
      return {
        label: target.product,
        target: { metric: 'downloads', product: target.product },
      }
    }
    return null
  })

  /** Allow clicking a legend item inside a stacked chart to drill one level deeper. */
  const onDrillSeries = $derived.by((): ((key: string) => void) | undefined => {
    if (target.metric === 'audience' && !target.source && target.product) {
      return (source: string) => onSelectTarget({ metric: 'audience', product: target.product, source })
    }
    if (target.metric === 'downloads' && !target.format && target.product) {
      return (format: string) => onSelectTarget({ metric: 'downloads', product: target.product, format })
    }
    return undefined
  })

  function fmt(v: number): string {
    if (!trend) return integer(v)
    if (trend.unit === 'money') return currency(v, code)
    if (trend.unit === 'xp') return m.hearth_xp({ xp: integer(v) })
    return integer(v)
  }

  function fmtSigned(v: number): string {
    if (v === 0) return fmt(0)
    const sign = v > 0 ? '+' : '−'
    return `${sign}${fmt(Math.abs(v))}`
  }

  const pctChange = $derived.by(() => {
    if (!trend || trend.start <= 0 || trend.change === 0) return ''
    const ratio = trend.change / trend.start
    if (Math.abs(ratio) < 0.0005) return ''
    return `${ratio > 0 ? '+' : ''}${percent(ratio)}`
  })

  const rangeSpan = $derived(trend ? `${trend.from} → ${trend.to}` : '')
  const shorterHistory = $derived(!!trend?.first_day && trend.first_day > trend.from)
  const hasLag = $derived(!!trend && trend.kind === 'level' && !!trend.last_day && trend.last_day < trend.to)
</script>

<section class="trend-card" aria-label={title}>
  <div class="head">
    <div class="ident">
      {#if parentLink}
        <button
          type="button"
          class="back"
          onclick={() => onSelectTarget(parentLink.target)}
          title={m.hearth_trend_back({ name: parentLink.label })}
        >
          ← {parentLink.label}
        </button>
      {/if}
      <div class="title-line">
        <h3>{title}</h3>
        {#if subtitle}<span class="sub">{subtitle}</span>{/if}
      </div>
      {#if rangeSpan}<span class="span mono">{rangeSpan}</span>{/if}
    </div>

    <div class="controls">
      <div class="picker" role="group" aria-label={m.hearth_trend_range_label()}>
        {#each TREND_RANGES as r (r)}
          <button
            type="button"
            class="range-btn"
            class:current={range === r}
            aria-pressed={range === r}
            onclick={() => onRangeChange(r)}
          >
            {trendRangeLabel(r)}
          </button>
        {/each}
      </div>
      <button type="button" class="close-btn" onclick={onClose} aria-label={m.hearth_trend_close()} title={m.hearth_trend_close()}>
        ✕
      </button>
    </div>
  </div>

  {#if error && !trend}
    <p class="note error">{error}</p>
  {:else if !trend}
    <p class="note">{m.hearth_trend_loading()}</p>
  {:else}
    <div class="body" class:dim={loading}>
      <div class="stats">
        <div class="stat">
          <span class="k">{m.hearth_trend_current()}</span>
          <span class="v">{fmt(trend.current)}</span>
        </div>
        <div class="stat">
          <span class="k">
            {trend.kind === 'cumulative' ? m.hearth_trend_change_added() : m.hearth_trend_change()}
          </span>
          <span class="v" class:up={trend.change > 0} class:down={trend.change < 0}>
            {fmtSigned(trend.change)}
            {#if pctChange}<span class="pct">({pctChange})</span>{/if}
          </span>
        </div>
        <div class="stat">
          <span class="k">{m.hearth_trend_peak()}</span>
          <span class="v">
            {fmt(trend.peak)}
            {#if trend.peak_day}
              <span class="peak-day">{m.hearth_trend_peak_on({ day: dayLabel(trend.peak_day) })}</span>
            {/if}
          </span>
        </div>
      </div>

      <TrendChart {trend} {title} {code} {onDrillSeries} />

      {#if shorterHistory || hasLag || error}
        <div class="footnotes">
          {#if shorterHistory && trend.first_day}
            <span>{m.hearth_trend_history_since({ date: trend.first_day })}</span>
          {/if}
          {#if hasLag && trend.last_day}
            <span>{m.hearth_trend_as_of({ date: trend.last_day })}</span>
          {/if}
          {#if error}
            <span class="err">{error}</span>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</section>

<style>
  .trend-card {
    padding: 0.85rem 0.95rem 0.7rem;
    border: 1px solid color-mix(in oklab, var(--accent) 28%, var(--border-soft));
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    box-shadow: 0 12px 32px rgb(0 0 0 / 0.35);
  }

  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.55rem;
    margin-bottom: 0.7rem;
  }

  .ident {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    min-width: 0;
  }

  .back {
    align-self: flex-start;
    padding: 0.08rem 0.4rem;
    margin-bottom: 0.1rem;
    border: 1px solid var(--border-soft);
    border-radius: 999px;
    background: transparent;
    font-size: 0.68rem;
    color: var(--text-dim);
  }

  .back:hover {
    color: var(--accent);
    border-color: color-mix(in oklab, var(--accent) 35%, transparent);
    background: color-mix(in oklab, var(--accent) 8%, transparent);
  }

  .title-line {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  h3 {
    margin: 0;
    font-size: 0.96rem;
    font-weight: 700;
    color: var(--text);
  }

  .sub {
    font-size: 0.7rem;
    color: var(--text-faint);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .span {
    font-size: 0.68rem;
    color: var(--text-faint);
  }

  .controls {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    flex-wrap: wrap;
  }

  .picker {
    display: inline-flex;
    gap: 0.12rem;
    padding: 0.14rem;
    border: 1px solid var(--border-soft);
    border-radius: 999px;
    background: #0d111a;
  }

  .range-btn {
    border: 1px solid transparent;
    background: transparent;
    border-radius: 999px;
    padding: 0.18rem 0.52rem;
    font-size: 0.72rem;
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

  .close-btn {
    padding: 0.22rem 0.5rem;
    font-size: 0.78rem;
    line-height: 1;
    color: var(--text-dim);
    background: transparent;
    border-color: var(--border-soft);
  }

  .close-btn:hover {
    color: var(--text);
  }

  .body {
    transition: opacity 0.15s ease;
  }

  .body.dim {
    opacity: 0.55;
  }

  .stats {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 1.3rem;
    margin-bottom: 0.65rem;
    padding-bottom: 0.55rem;
    border-bottom: 1px solid var(--border-soft);
  }

  .stat {
    display: flex;
    flex-direction: column;
    gap: 0.05rem;
  }

  .stat .k {
    font-size: 0.64rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-faint);
  }

  .stat .v {
    font-size: 0.92rem;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .stat .v.up {
    color: var(--uncommon);
  }

  .stat .v.down {
    color: var(--cursed);
  }

  .pct,
  .peak-day {
    font-size: 0.72rem;
    font-weight: 400;
    color: var(--text-faint);
    margin-left: 0.25rem;
  }

  .footnotes {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 1rem;
    margin-top: 0.45rem;
    padding-top: 0.4rem;
    border-top: 1px dashed var(--border);
    font-size: 0.68rem;
    color: var(--text-faint);
  }

  .footnotes .err {
    color: var(--cursed);
  }

  .note {
    margin: 2rem 0;
    text-align: center;
    font-size: 0.82rem;
    color: var(--text-faint);
  }

  .note.error {
    color: var(--cursed);
  }

  @media (max-width: 900px) {
    .range-btn,
    .back {
      min-height: 32px;
    }

    .close-btn {
      min-height: 36px;
      min-width: 36px;
    }
  }
</style>
