<script lang="ts">
  import type { Delta } from './types'

  let {
    label,
    value,
    delta,
    note = '',
    tone = 'normal',
    invert = false,
  }: {
    label: string
    value: string
    /** Period-over-period change; omitted when there is nothing to compare. */
    delta?: Delta
    note?: string
    /** `aside` marks a number that is deliberately not part of the ledger. */
    tone?: 'normal' | 'aside'
    /** For a metric where growth is bad news — refunds — so up reads red. */
    invert?: boolean
  } = $props()

  const arrow = $derived(delta?.direction === 'up' ? '▲' : delta?.direction === 'down' ? '▼' : '')

  /** Color follows whether the change is *good*, the arrow follows the number. */
  const tint = $derived(
    !delta || delta.direction === 'flat' ? 'flat' : (delta.direction === 'up') !== invert ? 'up' : 'down',
  )
</script>

<div class="tile" class:aside={tone === 'aside'}>
  <div class="label">{label}</div>
  <div class="value">{value}</div>
  <div class="foot">
    {#if delta?.label}
      <span class="delta {tint}">
        {#if arrow}<span aria-hidden="true">{arrow}</span>{/if}
        {delta.label}
        <span class="vs">vs prev</span>
      </span>
    {/if}
    {#if note}<span class="note">{note}</span>{/if}
  </div>
</div>

<style>
  .tile {
    padding: 0.7rem 0.85rem 0.75rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    min-width: 0;
  }

  .tile.aside {
    background: #0e121b;
    border-style: dashed;
  }

  .label {
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-faint);
  }

  .value {
    margin-top: 0.15rem;
    font-size: 1.45rem;
    font-weight: 650;
    line-height: 1.15;
    overflow-wrap: anywhere;
  }

  .aside .value {
    font-size: 1.15rem;
    color: var(--text-dim);
  }

  .foot {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.4rem;
    margin-top: 0.25rem;
    min-height: 1rem;
    font-size: 0.72rem;
  }

  .delta {
    display: inline-flex;
    align-items: baseline;
    gap: 0.2rem;
    font-weight: 600;
    color: var(--text-faint);
  }

  /* Direction is carried by the arrow glyph as well as the color, so the
     up/down reading never depends on hue alone. */
  .delta.up {
    color: #0ca30c;
  }

  .delta.down {
    color: #e66767;
  }

  .vs {
    font-weight: 400;
    color: var(--text-faint);
  }

  .note {
    color: var(--text-faint);
  }
</style>
