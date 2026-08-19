<script lang="ts">
  import type { MysteryPoint } from './types'
  import { currency, dayLabel, integer } from './types'

  let {
    points,
    flagDay,
    baseline = 0,
    unit = 'count',
    code = 'USD',
  }: {
    /** The trailing window, oldest first; the flagged day is the last one. */
    points: MysteryPoint[]
    /** Which day to highlight. */
    flagDay: string
    /** The median the day was measured against, drawn as a dashed rule. */
    baseline?: number
    unit?: 'money' | 'count'
    code?: string
  } = $props()

  // Geometry. The sparkline is drawn in its own viewBox and scaled to fit, so
  // it needs no measurement and no resize handling — the one place in Loot
  // where a chart is small enough for that to be the right trade.
  const W = 320
  const H = 54
  const PAD = 4

  const values = $derived(points.map((p) => p.value))
  const top = $derived(Math.max(...values, 1))
  const step = $derived(points.length > 1 ? (W - PAD * 2) / (points.length - 1) : 0)

  const x = (i: number) => PAD + i * step
  const y = (v: number) => H - PAD - (Math.max(0, v) / top) * (H - PAD * 2)

  const line = $derived(points.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(p.value).toFixed(1)}`).join(' '))

  const fill = $derived(
    points.length === 0 ? '' : `${line} L${x(points.length - 1).toFixed(1)},${H - PAD} L${x(0).toFixed(1)},${H - PAD} Z`,
  )

  const flagIndex = $derived(points.findIndex((p) => p.day === flagDay))
  const flagged = $derived(flagIndex >= 0 ? points[flagIndex] : null)

  function format(v: number): string {
    return unit === 'money' ? currency(v, code, 0) : integer(v)
  }

  const label = $derived(
    points.length === 0
      ? 'no history'
      : `${points.length} days to ${dayLabel(points[points.length - 1].day)}, ending at ${format(
          points[points.length - 1].value,
        )}`,
  )
</script>

<figure class="spark">
  <svg viewBox="0 0 {W} {H}" width="100%" height={H} preserveAspectRatio="none" role="img" aria-label={label}>
    {#if baseline > 0}
      <line class="baseline" x1={PAD} x2={W - PAD} y1={y(baseline)} y2={y(baseline)} />
    {/if}
    <path class="fill" d={fill} />
    <path class="line" d={line} />
    {#if flagged}
      <line class="flag-rule" x1={x(flagIndex)} x2={x(flagIndex)} y1={PAD} y2={H - PAD} />
      <circle class="flag-dot" cx={x(flagIndex)} cy={y(flagged.value)} r="3.5" />
    {/if}
  </svg>
  <figcaption>
    <span>{points.length ? dayLabel(points[0].day) : ''}</span>
    {#if baseline > 0}<span class="usual">usual {format(baseline)}</span>{/if}
    <span>{points.length ? dayLabel(points[points.length - 1].day) : ''}</span>
  </figcaption>
</figure>

<style>
  .spark {
    margin: 0.5rem 0 0.4rem;
  }

  svg {
    display: block;
    overflow: visible;
  }

  .fill {
    fill: color-mix(in oklab, var(--accent) 14%, transparent);
  }

  .line {
    fill: none;
    stroke: var(--accent);
    stroke-width: 1.5;
    /* preserveAspectRatio=none stretches the drawing; this keeps the stroke
       an even width instead of squashing it with the geometry. */
    vector-effect: non-scaling-stroke;
  }

  .baseline {
    stroke: var(--text-faint);
    stroke-width: 1;
    stroke-dasharray: 3 4;
    vector-effect: non-scaling-stroke;
  }

  .flag-rule {
    stroke: color-mix(in oklab, var(--legendary) 55%, transparent);
    stroke-width: 1;
    vector-effect: non-scaling-stroke;
  }

  .flag-dot {
    fill: var(--legendary);
    stroke: #0d111a;
    stroke-width: 1.5;
    vector-effect: non-scaling-stroke;
  }

  figcaption {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
    margin-top: 0.15rem;
    font-size: 0.66rem;
    color: var(--text-faint);
  }

  .usual {
    font-family: var(--mono);
  }
</style>
