<script lang="ts">
  import { max } from 'd3-array'
  import { scaleLinear } from 'd3-scale'
  import { area, curveMonotoneX, line, stack, stackOffsetNone, stackOrderNone } from 'd3-shape'
  import { seriesColor } from './palette'
  import type { VaultPoint } from './types'
  import { currency, currencyCompact, dayLabel, integer } from './types'

  let {
    series,
    sources,
    code,
  }: {
    /** Zero-filled, one point per day, oldest first. */
    series: VaultPoint[]
    /** Which sources to stack, in a stable order. */
    sources: string[]
    /** Display currency code. */
    code: string
  } = $props()

  // --- geometry -------------------------------------------------------------

  const PAD = { top: 12, right: 14, bottom: 18, left: 54 }
  const REVENUE_H = 180
  const GAP = 26
  const UNITS_H = 44

  let width = $state(680)
  const height = REVENUE_H + GAP + UNITS_H + PAD.top + PAD.bottom
  const plotW = $derived(Math.max(80, width - PAD.left - PAD.right))

  const n = $derived(series.length)
  const x = $derived(scaleLinear().domain([0, Math.max(1, n - 1)]).range([PAD.left, PAD.left + plotW]))

  const revenueTop = PAD.top
  const revenueBottom = PAD.top + REVENUE_H
  const unitsTop = revenueBottom + GAP
  const unitsBottom = unitsTop + UNITS_H

  const maxRevenue = $derived(Math.max(1, max(series, (d) => d.revenue_base) ?? 0))
  const y = $derived(scaleLinear().domain([0, maxRevenue]).nice(4).range([revenueBottom, revenueTop]))

  const maxUnits = $derived(Math.max(1, max(series, (d) => d.units) ?? 0))
  const yUnits = $derived(scaleLinear().domain([0, maxUnits]).range([unitsBottom, unitsTop]))

  const stacked = $derived(sources.length > 1)

  // --- shapes ---------------------------------------------------------------

  /** One stacked band: its filled shape and the path along its top edge. */
  type Band = { key: string; fill: string; edge: string }

  const bands = $derived.by((): Band[] => {
    if (!n) return []
    const shape = area<[number, number]>()
      .x((_, i) => x(i))
      .y0((d) => y(d[0]))
      .y1((d) => y(d[1]))
      .curve(curveMonotoneX)
    const edge = line<[number, number]>()
      .x((_, i) => x(i))
      .y((d) => y(d[1]))
      .curve(curveMonotoneX)

    if (!stacked) {
      const key = sources[0] ?? 'revenue'
      const points = series.map((p) => [0, p.revenue_base] as [number, number])
      return [{ key, fill: shape(points) ?? '', edge: edge(points) ?? '' }]
    }

    const layers = stack<VaultPoint>()
      .keys(sources)
      .value((p, key) => p.by_source?.[key] ?? 0)
      .order(stackOrderNone)
      .offset(stackOffsetNone)(series)

    return layers.map((layer) => {
      const points = layer.map((p) => [p[0], p[1]] as [number, number])
      return { key: String(layer.key), fill: shape(points) ?? '', edge: edge(points) ?? '' }
    })
  })

  const barWidth = $derived(Math.max(1, Math.min(14, (plotW / Math.max(1, n)) * 0.62)))

  // --- ticks ----------------------------------------------------------------

  const yTicks = $derived(y.ticks(4))

  const xTicks = $derived.by(() => {
    if (n <= 1) return series.map((p, i) => ({ i, day: p.day }))
    const wanted = Math.max(2, Math.min(6, Math.floor(plotW / 90)))
    const step = Math.max(1, Math.round((n - 1) / (wanted - 1)))
    const out: { i: number; day: string }[] = []
    for (let i = 0; i < n; i += step) out.push({ i, day: series[i].day })
    const last = n - 1
    if (out[out.length - 1].i !== last) out.push({ i: last, day: series[last].day })
    return out
  })

  // --- hover ----------------------------------------------------------------

  let hover = $state<number | null>(null)
  const point = $derived(hover === null ? null : series[hover])

  function track(event: PointerEvent) {
    const rect = (event.currentTarget as SVGRectElement).getBoundingClientRect()
    const px = event.clientX - rect.left + PAD.left
    const i = Math.round(x.invert(px))
    hover = Math.max(0, Math.min(n - 1, i))
  }

  /** Per-source rows for the tooltip, biggest first, zeros omitted. */
  const hoverRows = $derived.by(() => {
    if (!point) return []
    if (!stacked) return []
    return sources
      .map((key) => ({ key, value: point.by_source?.[key] ?? 0 }))
      .filter((row) => row.value !== 0)
      .sort((a, b) => b.value - a.value)
  })

  /** Keeps the tooltip inside the chart instead of off its right edge. */
  const tooltipLeft = $derived(hover === null ? 0 : Math.min(Math.max(x(hover), PAD.left + 6), width - 150))
</script>

<figure class="chart" bind:clientWidth={width}>
  {#if sources.length > 1}
    <ul class="legend">
      {#each sources as source (source)}
        <li><span class="swatch" style="background: {seriesColor(source)}"></span>{source}</li>
      {/each}
    </ul>
  {/if}

  <div class="plot">
    <svg viewBox="0 0 {width} {height}" {height} width="100%" role="img" aria-label="Revenue and units per day">
      <!-- y gridlines and ticks -->
      {#each yTicks as tick (tick)}
        <line class="grid" x1={PAD.left} x2={PAD.left + plotW} y1={y(tick)} y2={y(tick)} />
        <text class="tick" x={PAD.left - 8} y={y(tick)} text-anchor="end" dominant-baseline="middle">
          {currencyCompact(tick, code)}
        </text>
      {/each}

      <!--
        Revenue bands. A stack is drawn near-solid — the palette was validated
        on solid colors — and each seam wears a 2px line in the surface color,
        which is what keeps touching segments apart. A single series is a 10%
        wash under a 2px line instead.
      -->
      {#each bands as band, i (band.key)}
        <path class="band-fill" class:solid={stacked} d={band.fill} fill={seriesColor(band.key)} />
        {#if !stacked || i === bands.length - 1}
          <path class="band-line" d={band.edge} stroke={seriesColor(band.key)} />
        {:else}
          <path class="seam" d={band.edge} />
        {/if}
      {/each}
      <line class="axis" x1={PAD.left} x2={PAD.left + plotW} y1={revenueBottom} y2={revenueBottom} />

      <!-- units bars -->
      {#each series as p, i (p.day)}
        {#if p.units > 0}
          <rect
            class="bar"
            x={x(i) - barWidth / 2}
            y={yUnits(p.units)}
            width={barWidth}
            height={Math.max(1, unitsBottom - yUnits(p.units))}
            rx="2"
          />
        {/if}
      {/each}
      <line class="axis" x1={PAD.left} x2={PAD.left + plotW} y1={unitsBottom} y2={unitsBottom} />
      <text class="panel-label" x={PAD.left - 8} y={unitsTop + UNITS_H / 2} text-anchor="end" dominant-baseline="middle"
        >units</text
      >

      <!-- x ticks -->
      {#each xTicks as tick (tick.i)}
        <text class="tick" x={x(tick.i)} y={height - 4} text-anchor="middle">{dayLabel(tick.day)}</text>
      {/each}

      <!-- crosshair -->
      {#if hover !== null && point}
        <line class="crosshair" x1={x(hover)} x2={x(hover)} y1={revenueTop} y2={unitsBottom} />
        <circle class="marker" cx={x(hover)} cy={y(point.revenue_base)} r="4" />
      {/if}

      <rect
        class="capture"
        x={PAD.left}
        y={revenueTop}
        width={plotW}
        height={unitsBottom - revenueTop}
        onpointermove={track}
        onpointerleave={() => (hover = null)}
        role="presentation"
      />
    </svg>

    {#if point}
      <div class="tooltip" style="left: {tooltipLeft}px">
        <div class="tip-day">{dayLabel(point.day, true)}</div>
        <div class="tip-total">{currency(point.revenue_base, code)}</div>
        {#each hoverRows as row (row.key)}
          <div class="tip-row">
            <span class="swatch" style="background: {seriesColor(row.key)}"></span>
            <span class="tip-key">{row.key}</span>
            <span class="tip-val">{currency(row.value, code)}</span>
          </div>
        {/each}
        <div class="tip-units">{integer(point.units)} units</div>
      </div>
    {/if}
  </div>
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
    gap: 0.2rem 0.9rem;
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
    fill-opacity: 0.22;
    stroke: none;
  }

  .band-fill.solid {
    fill-opacity: 0.9;
  }

  /* The surface gap: 2px of background between two touching bands. */
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

  .bar {
    fill: var(--text-faint);
    fill-opacity: 0.75;
  }

  .crosshair {
    stroke: var(--accent);
    stroke-width: 1;
    stroke-opacity: 0.5;
  }

  .marker {
    fill: var(--accent);
    stroke: var(--panel);
    stroke-width: 2;
  }

  .capture {
    fill: transparent;
    cursor: crosshair;
  }

  .tooltip {
    position: absolute;
    top: 0;
    transform: translateX(8px);
    pointer-events: none;
    min-width: 140px;
    padding: 0.45rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: color-mix(in oklab, var(--panel) 96%, transparent);
    backdrop-filter: blur(8px);
    box-shadow: 0 10px 30px rgb(0 0 0 / 0.45);
    font-size: 0.74rem;
  }

  .tip-day {
    color: var(--text-faint);
    font-size: 0.68rem;
  }

  .tip-total {
    font-weight: 700;
    font-size: 0.9rem;
    margin-bottom: 0.2rem;
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

  .tip-units {
    margin-top: 0.2rem;
    color: var(--text-faint);
    font-size: 0.68rem;
  }
</style>
