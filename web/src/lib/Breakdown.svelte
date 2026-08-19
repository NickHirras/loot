<script lang="ts">
  import type { BreakdownRow } from './types'
  import { currency, integer, percent } from './types'

  let {
    title,
    rows,
    code,
    color,
    empty = 'Nothing here yet.',
  }: {
    title: string
    rows: BreakdownRow[]
    code: string
    /** Bar color per row; a single hue when the rows are not their own series. */
    color: (row: BreakdownRow) => string
    empty?: string
  } = $props()

  // Bars are drawn relative to the biggest row, so a long tail stays readable.
  const top = $derived(Math.max(...rows.map((r) => Math.abs(r.revenue_base)), 0) || 1)
</script>

<section class="panel">
  <h3>{title}</h3>

  {#if rows.length === 0}
    <p class="empty">{empty}</p>
  {:else}
    <ul>
      {#each rows as row (row.key)}
        <li>
          <div class="row-top">
            <span class="name">
              {#if row.prefix}<span class="prefix" aria-hidden="true">{row.prefix}</span>{/if}
              <span class="label" title={row.label}>{row.label}</span>
            </span>
            <span class="figures">
              <span class="amount">{currency(row.revenue_base, code)}</span>
              <span class="share">{percent(row.share)}</span>
            </span>
          </div>
          <div class="track">
            <div
              class="bar"
              style="width: {Math.max(1.5, (Math.abs(row.revenue_base) / top) * 100)}%; background: {color(row)}"
            ></div>
          </div>
          <div class="units">{integer(row.units)} {row.units === 1 ? 'unit' : 'units'}</div>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .panel {
    padding: 0.85rem 0.95rem 1rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    min-width: 0;
  }

  h3 {
    margin: 0 0 0.7rem;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-faint);
    font-weight: 600;
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
    max-height: 340px;
    overflow-y: auto;
  }

  .row-top {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.6rem;
    font-size: 0.8rem;
  }

  .name {
    display: inline-flex;
    align-items: baseline;
    gap: 0.35rem;
    min-width: 0;
  }

  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .figures {
    display: inline-flex;
    align-items: baseline;
    gap: 0.45rem;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }

  .amount {
    color: var(--text);
  }

  .share {
    color: var(--text-faint);
    font-size: 0.72rem;
    min-width: 3.2ch;
    text-align: right;
  }

  .track {
    height: 6px;
    margin-top: 0.25rem;
    border-radius: 3px;
    background: #0c1017;
    overflow: hidden;
  }

  .bar {
    height: 100%;
    border-radius: 0 3px 3px 0;
  }

  .units {
    margin-top: 0.2rem;
    font-size: 0.68rem;
    color: var(--text-faint);
  }

  .empty {
    margin: 0;
    font-size: 0.78rem;
    color: var(--text-faint);
  }
</style>
