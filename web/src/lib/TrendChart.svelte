<script lang="ts">
  import { max, min } from 'd3-array'
  import { scaleLinear } from 'd3-scale'
  import { area, curveMonotoneX, line, stack, stackOffsetNone, stackOrderNone } from 'd3-shape'
  import { seriesColor } from './palette'
  import { trendSeriesLabel } from './labels'
  import type { HearthTrend, TrendPoint } from './types'
  import { currency, currencyCompact, dayLabel, integer, integerCompact } from './types'
  import { m } from '../paraglide/messages'

  let {
    trend,
    title,
    code = 'USD',
    onDrillSeries,
  }: {
    trend: HearthTrend
    title: string
    code?: string
    /** Optional callback when a user clicks a legend item in a stacked chart. */
    onDrillSeries?: (key: string) => void
  } = $props()

  // --- geometry -------------------------------------------------------------

  const PAD = { top: 12, right: 14, bottom: 20, left: 52 }
  const MAIN_H = 176
  const GAP = 24
  const DAILY_H = 44

  let width = $state(640)
  const height = MAIN_H + GAP + DAILY_H + PAD.top + PAD.bottom
  const padL = $derived(width < 420 ? 44 : PAD.left)
  const padR = $derived(width < 420 ? 10 : PAD.right)
  const plotW = $derived(Math.max(80, width - padL - padR))

  const points = $derived(trend.points ?? [])
  const n = $derived(points.length)
  const seriesNames = $derived(trend.series_names ?? [])
  const stacked = $derived(seriesNames.length > 1)
  const activePoints = $derived(points.filter((p) => p.value !== null))
  const hasData = $derived(activePoints.length > 0)

  const xScale = $derived(scaleLinear().domain([0, Math.max(1, n - 1)]).range([padL, padL + plotW]))
  const xAt = (i: number) => (n <= 1 ? padL + plotW / 2 : xScale(i))

  const mainTop = PAD.top
  const mainBottom = PAD.top + MAIN_H
  const dailyTop = mainBottom + GAP
  const dailyBottom = dailyTop + DAILY_H
  const dailyMid = dailyTop + DAILY_H / 2

  const minVal = $derived(Math.min(0, min(activePoints, (p) => p.value ?? 0) ?? 0))
  const maxVal = $derived(Math.max(1, max(activePoints, (p) => p.value ?? 0) ?? 0))
  const y = $derived(scaleLinear().domain([minVal, maxVal]).nice(4).range([mainBottom, mainTop]))

  // Diverging daily bars for level series or any series with negative days (refunds).
  const diverging = $derived(trend.kind === 'level' || points.some((p) => p.daily < 0))
  const maxAbsDaily = $derived(Math.max(1, max(points, (p) => Math.abs(p.daily)) ?? 0))
  const yDailyPos = $derived(scaleLinear().domain([0, maxAbsDaily]).range([dailyBottom, dailyTop]))
  const yDailyDiv = $derived(scaleLinear().domain([-maxAbsDaily, maxAbsDaily]).range([dailyBottom, dailyTop]))

  // --- formatting -----------------------------------------------------------

  function fmtValue(v: number): string {
    if (trend.unit === 'money') return currency(v, code)
    if (trend.unit === 'xp') return m.hearth_xp({ xp: integer(v) })
    return integer(v)
  }

  function fmtTick(v: number): string {
    if (trend.unit === 'money') return currencyCompact(v, code)
    return integerCompact(v)
  }

  function fmtAbsDaily(v: number): string {
    const abs = Math.abs(v)
    if (trend.unit === 'money') return currency(abs, code)
    if (trend.unit === 'xp') return m.hearth_xp({ xp: integer(abs) })
    return integer(abs)
  }

  const spansYears = $derived(n > 1 && points[0].day.slice(0, 4) !== points[n - 1].day.slice(0, 4))

  // --- shapes ---------------------------------------------------------------

  type Band = { key: string; fill: string; edge: string }

  /** Primary accent hue for single-series charts. */
  const primaryColor = $derived.by(() => {
    if (trend.unit === 'xp') return 'var(--legendary)'
    if (trend.unit === 'money') return 'var(--uncommon)'
    return 'var(--accent)'
  })

  const bands = $derived.by((): Band[] => {
    if (!n || !hasData) return []

    if (!stacked) {
      const shape = area<TrendPoint>()
        .defined((p) => p.value !== null)
        .x((_, i) => xAt(i))
        .y0(() => y(Math.max(0, minVal)))
        .y1((p) => y(p.value ?? 0))
        .curve(curveMonotoneX)
      const edge = line<TrendPoint>()
        .defined((p) => p.value !== null)
        .x((_, i) => xAt(i))
        .y((p) => y(p.value ?? 0))
        .curve(curveMonotoneX)

      return [{ key: 'total', fill: shape(points) ?? '', edge: edge(points) ?? '' }]
    }

    const layers = stack<TrendPoint>()
      .keys(seriesNames)
      .value((p, key) => (p.value === null ? 0 : (p.by_series?.[key] ?? 0)))
      .order(stackOrderNone)
      .offset(stackOffsetNone)(points)

    const shape = area<[number, number]>()
      .defined((_, i) => points[i].value !== null)
      .x((_, i) => xAt(i))
      .y0((d) => y(d[0]))
      .y1((d) => y(d[1]))
      .curve(curveMonotoneX)
    const edge = line<[number, number]>()
      .defined((_, i) => points[i].value !== null)
      .x((_, i) => xAt(i))
      .y((d) => y(d[1]))
      .curve(curveMonotoneX)

    return layers.map((layer) => {
      const coords = layer.map((p) => [p[0], p[1]] as [number, number])
      return { key: String(layer.key), fill: shape(coords) ?? '', edge: edge(coords) ?? '' }
    })
  })

  /** Index of the final point carrying a value, for a subtle terminal dot. */
  const lastActiveIndex = $derived.by(() => {
    for (let i = n - 1; i >= 0; i--) {
      if (points[i].value !== null) return i
    }
    return -1
  })

  const barWidth = $derived(Math.max(1.5, Math.min(14, (plotW / Math.max(1, n)) * 0.62)))

  // --- ticks ----------------------------------------------------------------

  const yTicks = $derived(y.ticks(4))

  const xTicks = $derived.by(() => {
    if (n <= 1) return points.map((p, i) => ({ i, day: p.day }))
    const wanted = Math.max(2, Math.min(6, Math.floor(plotW / 92)))
    const step = Math.max(1, Math.round((n - 1) / (wanted - 1)))
    const out: { i: number; day: string }[] = []
    for (let i = 0; i < n; i += step) out.push({ i, day: points[i].day })
    const last = n - 1
    if (out[out.length - 1].i !== last) out.push({ i: last, day: points[last].day })
    return out
  })

  // --- hover ----------------------------------------------------------------

  let hover = $state<number | null>(null)
  const point = $derived(hover === null ? null : (points[hover] ?? null))

  function track(event: PointerEvent) {
    if (!n) return
    const rect = (event.currentTarget as SVGRectElement).getBoundingClientRect()
    const px = event.clientX - rect.left + padL
    const i = n <= 1 ? 0 : Math.round(xScale.invert(px))
    hover = Math.max(0, Math.min(n - 1, i))
  }

  const hoverRows = $derived.by(() => {
    if (!point || point.value === null || !stacked) return []
    return seriesNames
      .map((key) => ({ key, value: point.by_series?.[key] ?? 0 }))
      .filter((row) => row.value !== 0)
      .sort((a, b) => b.value - a.value)
  })

  const flipTooltip = $derived(hover !== null && xAt(hover) > padL + plotW * 0.58)
  const tooltipX = $derived(hover === null ? 0 : xAt(hover))

  const dailyCaption = $derived.by(() => {
    if (!point || point.value === null || point.daily === 0) return ''
    if (trend.kind === 'level') {
      const sign = point.daily > 0 ? '+' : '−'
      return m.hearth_trend_tip_delta({ value: `${sign}${fmtAbsDaily(point.daily)}` })
    }
    if (point.daily > 0) {
      return m.hearth_trend_tip_added({ value: fmtAbsDaily(point.daily) })
    }
    return m.hearth_trend_tip_net({ value: `−${fmtAbsDaily(point.daily)}` })
  })
</script>

<figure class="chart" bind:clientWidth={width}>
  {#if stacked}
    <ul class="legend">
      {#each seriesNames as key (key)}
        <li>
          {#if onDrillSeries}
            <button type="button" class="legend-btn" onclick={() => onDrillSeries?.(key)}>
              <span class="swatch" style="background: {seriesColor(key)}"></span>
              <span>{trendSeriesLabel(trend.metric, key)}</span>
            </button>
          {:else}
            <span class="swatch" style="background: {seriesColor(key)}"></span>
            <span>{trendSeriesLabel(trend.metric, key)}</span>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  {#if !hasData}
    <p class="empty">
      {#if trend.last_day}
        {m.hearth_trend_stale({ date: trend.last_day })}
      {:else}
        {m.hearth_trend_empty()}
      {/if}
    </p>
  {:else}
    <div class="plot">
      <svg
        viewBox="0 0 {width} {height}"
        {height}
        width="100%"
        role="img"
        aria-label={m.hearth_trend_chart_aria({ title })}
      >
        <!-- y gridlines and ticks -->
        {#each yTicks as tick (tick)}
          <line class="grid" x1={padL} x2={padL + plotW} y1={y(tick)} y2={y(tick)} />
          <text class="tick" x={padL - 8} y={y(tick)} text-anchor="end" dominant-baseline="middle">
            {fmtTick(tick)}
          </text>
        {/each}

        <!-- Main trend bands -->
        {#each bands as band, i (band.key)}
          {#if stacked}
            <path class="band-fill solid" d={band.fill} fill={seriesColor(band.key)} />
            {#if i === bands.length - 1}
              <path class="band-line" d={band.edge} stroke={seriesColor(band.key)} />
            {:else}
              <path class="seam" d={band.edge} />
            {/if}
          {:else}
            <path class="band-fill" d={band.fill} fill={primaryColor} />
            <path class="band-line" d={band.edge} stroke={primaryColor} />
          {/if}
        {/each}

        <!-- Latest point marker (also ensures a single-day series is clearly visible) -->
        {#if lastActiveIndex >= 0 && hover === null}
          <circle
            class="terminal"
            cx={xAt(lastActiveIndex)}
            cy={y(points[lastActiveIndex].value ?? 0)}
            r={activePoints.length === 1 ? 4.5 : 3.2}
            style="--dot: {stacked ? seriesColor(seriesNames[seriesNames.length - 1]) : primaryColor}"
          />
        {/if}

        <line class="axis" x1={padL} x2={padL + plotW} y1={mainBottom} y2={mainBottom} />

        <!-- Daily additions / daily delta strip -->
        {#if diverging}
          <line class="zero-axis" x1={padL} x2={padL + plotW} y1={dailyMid} y2={dailyMid} />
          {#each points as p, i (p.day)}
            {#if p.value !== null && p.daily !== 0}
              {@const y0 = yDailyDiv(0)}
              {@const y1 = yDailyDiv(p.daily)}
              <rect
                class="bar"
                class:pos={p.daily > 0}
                class:neg={p.daily < 0}
                x={xAt(i) - barWidth / 2}
                y={Math.min(y0, y1)}
                width={barWidth}
                height={Math.max(1.5, Math.abs(y1 - y0))}
                rx="1.5"
              />
            {/if}
          {/each}
        {:else}
          {#each points as p, i (p.day)}
            {#if p.value !== null && p.daily > 0}
              <rect
                class="bar neutral"
                x={xAt(i) - barWidth / 2}
                y={yDailyPos(p.daily)}
                width={barWidth}
                height={Math.max(1.5, dailyBottom - yDailyPos(p.daily))}
                rx="1.5"
              />
            {/if}
          {/each}
        {/if}
        <line class="axis" x1={padL} x2={padL + plotW} y1={dailyBottom} y2={dailyBottom} />
        <text
          class="panel-label"
          x={padL - 8}
          y={dailyTop + DAILY_H / 2}
          text-anchor="end"
          dominant-baseline="middle"
        >
          {diverging ? m.hearth_trend_daily_delta() : m.hearth_trend_daily()}
        </text>

        <!-- x ticks -->
        {#each xTicks as tick, i (tick.i)}
          <text
            class="tick"
            x={xAt(tick.i)}
            y={height - 4}
            text-anchor={xTicks.length === 1
              ? 'middle'
              : i === 0
                ? 'start'
                : i === xTicks.length - 1
                  ? 'end'
                  : 'middle'}
          >
            {dayLabel(tick.day, spansYears)}
          </text>
        {/each}

        <!-- crosshair -->
        {#if hover !== null && point}
          <line class="crosshair" x1={xAt(hover)} x2={xAt(hover)} y1={mainTop} y2={dailyBottom} />
          {#if point.value !== null}
            <circle
              class="marker"
              cx={xAt(hover)}
              cy={y(point.value)}
              r="4"
              style="--dot: {stacked ? seriesColor(seriesNames[seriesNames.length - 1]) : primaryColor}"
            />
          {/if}
        {/if}

        <rect
          class="capture"
          x={padL}
          y={mainTop}
          width={plotW}
          height={dailyBottom - mainTop}
          onpointermove={track}
          onpointerleave={() => (hover = null)}
          role="presentation"
        />
      </svg>

      {#if point}
        <div class="tooltip" class:flip={flipTooltip} style="left: {tooltipX}px">
          <div class="tip-day">{dayLabel(point.day, true)}</div>
          {#if point.value === null}
            <div class="tip-none">{m.hearth_trend_no_reading()}</div>
          {:else}
            <div class="tip-total">{fmtValue(point.value)}</div>
            {#each hoverRows as row (row.key)}
              <div class="tip-row">
                <span class="swatch" style="background: {seriesColor(row.key)}"></span>
                <span class="tip-key">{trendSeriesLabel(trend.metric, row.key)}</span>
                <span class="tip-val">{fmtValue(row.value)}</span>
              </div>
            {/each}
            {#if dailyCaption}
              <div class="tip-delta" class:up={point.daily > 0} class:down={point.daily < 0}>
                {dailyCaption}
              </div>
            {/if}
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</figure>

<style>
  .chart {
    margin: 0;
  }

  .plot {
    position: relative;
  }

  svg {
    display: block;
    overflow: visible;
  }

  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem 0.65rem;
    list-style: none;
    margin: 0 0 0.5rem;
    padding: 0;
    font-size: 0.72rem;
    color: var(--text-dim);
  }

  .legend li {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }

  .legend-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.14rem 0.42rem;
    border: 1px solid transparent;
    border-radius: 999px;
    background: transparent;
    font-size: 0.72rem;
    color: var(--text-dim);
  }

  .legend-btn:hover {
    color: var(--text);
    background: var(--panel-2);
    border-color: var(--border);
  }

  .swatch {
    width: 9px;
    height: 9px;
    border-radius: 2px;
    flex: 0 0 9px;
  }

  .grid {
    stroke: var(--border-soft);
    stroke-width: 1;
  }

  .axis {
    stroke: var(--border);
    stroke-width: 1;
  }

  .zero-axis {
    stroke: var(--border);
    stroke-width: 1;
    stroke-dasharray: 2 3;
  }

  .tick,
  .panel-label {
    fill: var(--text-faint);
    font-size: 10px;
    font-family: var(--font);
  }

  .panel-label {
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 9px;
  }

  .band-fill {
    fill-opacity: 0.18;
    stroke: none;
  }

  .band-fill.solid {
    fill-opacity: 0.88;
  }

  .seam {
    fill: none;
    stroke: var(--panel);
    stroke-width: 2;
  }

  .band-line {
    fill: none;
    stroke-width: 2;
    stroke-linejoin: round;
    stroke-linecap: round;
  }

  .terminal {
    fill: var(--dot, var(--accent));
    stroke: var(--panel);
    stroke-width: 1.8;
  }

  .bar.neutral {
    fill: var(--text-faint);
    fill-opacity: 0.75;
  }

  .bar.pos {
    fill: var(--uncommon);
    fill-opacity: 0.8;
  }

  .bar.neg {
    fill: var(--cursed);
    fill-opacity: 0.78;
  }

  .crosshair {
    stroke: var(--accent);
    stroke-width: 1;
    stroke-opacity: 0.5;
  }

  .marker {
    fill: var(--dot, var(--accent));
    stroke: var(--panel);
    stroke-width: 2;
  }

  .capture {
    fill: transparent;
    cursor: crosshair;
  }

  .tooltip {
    position: absolute;
    top: 4px;
    transform: translateX(10px);
    pointer-events: none;
    min-width: 145px;
    padding: 0.45rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: color-mix(in oklab, var(--panel) 96%, transparent);
    backdrop-filter: blur(8px);
    box-shadow: 0 10px 30px rgb(0 0 0 / 0.45);
    font-size: 0.74rem;
    z-index: 5;
  }

  .tooltip.flip {
    transform: translateX(calc(-100% - 10px));
  }

  .tip-day {
    color: var(--text-faint);
    font-size: 0.68rem;
  }

  .tip-total {
    font-weight: 700;
    font-size: 0.92rem;
    margin-bottom: 0.15rem;
    font-variant-numeric: tabular-nums;
  }

  .tip-none {
    font-style: italic;
    color: var(--text-faint);
    margin-top: 0.15rem;
  }

  .tip-row {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    color: var(--text-dim);
  }

  .tip-key {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .tip-val {
    font-variant-numeric: tabular-nums;
  }

  .tip-delta {
    margin-top: 0.2rem;
    font-size: 0.68rem;
    color: var(--text-faint);
    font-variant-numeric: tabular-nums;
  }

  .tip-delta.up {
    color: var(--uncommon);
  }

  .tip-delta.down {
    color: var(--cursed);
  }

  .empty {
    margin: 2rem 0;
    text-align: center;
    font-size: 0.8rem;
    color: var(--text-faint);
    font-style: italic;
  }

  @media (max-width: 900px) {
    .legend-btn {
      min-height: 30px;
    }
  }
</style>
