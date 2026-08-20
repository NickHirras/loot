<script lang="ts">
  import { untrack } from 'svelte'
  import Breakdown from './Breakdown.svelte'
  import RevenueChart from './RevenueChart.svelte'
  import StatTile from './StatTile.svelte'
  import { SERIES_COLORS, seriesColor } from './palette'
  import type { BreakdownRow } from './types'
  import { VAULT_RANGES, currency, delta, flagEmoji, integer } from './types'
  import { vault } from './vault.svelte'

  // The page owns the polling: mounting starts it, leaving stops it. The
  // store already untracks its own setup; untracking here as well means a
  // future edit to either side cannot turn this into a fetch loop.
  $effect(() => untrack(() => vault.activate()))

  const summary = $derived(vault.summary)
  const code = $derived(summary?.display_currency ?? 'USD')
  const totals = $derived(summary?.totals)
  const prev = $derived(summary?.prev_totals)

  /** Sources that actually earned something, in a stable (alphabetical) order. */
  const sources = $derived(
    (summary?.by_source ?? [])
      .filter((s) => s.revenue_base !== 0 || s.units !== 0)
      .map((s) => s.source)
      .sort(),
  )

  const isEmpty = $derived(
    !!totals && totals.revenue_base === 0 && totals.units === 0 && totals.refunds === 0 && summary?.by_source.length === 0,
  )

  const sourceRows: BreakdownRow[] = $derived(
    (summary?.by_source ?? []).map((s) => ({
      key: s.source,
      label: s.source,
      revenue_base: s.revenue_base,
      units: s.units,
      share: s.share,
    })),
  )

  const appRows: BreakdownRow[] = $derived(
    (summary?.by_app ?? []).map((a) => ({
      key: a.app || '(no app)',
      label: a.app || '(no app)',
      revenue_base: a.revenue_base,
      units: a.units,
      share: a.share,
    })),
  )

  const countryRows: BreakdownRow[] = $derived(
    (summary?.by_country ?? []).map((c) => ({
      key: c.country || '(unknown)',
      label: c.country === 'other' ? 'other countries' : c.country || 'unknown',
      prefix: c.country === 'other' ? '…' : flagEmoji(c.country),
      revenue_base: c.revenue_base,
      units: c.units,
      share: c.share,
    })),
  )

  const subsAsOf = $derived(summary?.subscriptions?.as_of ?? '')
  const rangeLabel = $derived(summary ? `${summary.range.from} → ${summary.range.to}` : '')
</script>

<section class="vault" aria-label="Vault">
  <div class="bar">
    <div class="titles">
      <h2>Vault</h2>
      {#if rangeLabel}<span class="range mono">{rangeLabel}</span>{/if}
    </div>

    <div class="picker" role="group" aria-label="Time range">
      {#each VAULT_RANGES as range (range)}
        <button
          class="range-btn"
          class:current={vault.range === range}
          aria-pressed={vault.range === range}
          onclick={() => vault.setRange(range)}
        >
          {range}
        </button>
      {/each}
    </div>
  </div>

  {#if vault.error && !summary}
    <p class="note error">{vault.error}</p>
  {:else if !summary}
    <p class="note">Counting the coins…</p>
  {:else}
    <div class="tiles">
      <StatTile
        label="Revenue"
        value={currency(totals?.revenue_base ?? 0, code)}
        delta={delta(totals?.revenue_base ?? 0, prev?.revenue_base ?? 0)}
      />
      <StatTile
        label="Units"
        value={integer(totals?.units ?? 0)}
        delta={delta(totals?.units ?? 0, prev?.units ?? 0)}
      />
      <StatTile
        label="Refunds"
        value={integer(totals?.refunds ?? 0)}
        delta={delta(totals?.refunds ?? 0, prev?.refunds ?? 0)}
        invert
      />
      <StatTile
        label="Active subs"
        value={summary.subscriptions?.active === null || summary.subscriptions?.active === undefined
          ? '—'
          : integer(summary.subscriptions.active)}
        note={subsAsOf ? `as of ${subsAsOf}` : 'no source reports subscriptions'}
      />
      <StatTile label="Countries" value={integer(totals?.countries ?? 0)} note="seen in this window" />
      <StatTile
        tone="aside"
        label="Sighted today (RevenueCat)"
        value={currency(summary.realtime?.revenuecat_amount_base_today ?? 0, code)}
        note="{integer(summary.realtime?.revenuecat_purchases_today ?? 0)} purchases · estimate, not ledger revenue"
      />
    </div>

    {#if isEmpty}
      <div class="empty">
        <h3>No ledger money in this window.</h3>
        <p>
          Revenue only counts <strong>ledger rows</strong> — settled money from the stores' own financial reports. To
          start filling the vault, configure
          <a href="https://github.com/nickhirras/loot#app-store-connect-and-google-play-quest-2" target="_blank" rel="noreferrer">
            App Store Connect and Google Play
          </a>
          in your <code>loot.yaml</code>, then let a day settle.
        </p>
        <p class="fine">
          RevenueCat webhooks still land in the feed instantly, but their amounts are pre-tax, pre-store-cut
          <em>sightings</em>: they show up in the tile above and are never added to revenue. See
          <a href="https://github.com/nickhirras/loot#the-vault" target="_blank" rel="noreferrer">the vault</a> and
          <a href="https://github.com/nickhirras/loot#the-daily-chest" target="_blank" rel="noreferrer">the daily chest</a>
          for the whole rule.
        </p>
      </div>
    {:else}
      <div class="card">
        <div class="card-head">
          <h3>Revenue per day</h3>
          <span class="hint">in {code} · units below</span>
        </div>
        <RevenueChart series={summary.series} sources={sources.length ? sources : ['revenue']} {code} />
      </div>

      <div class="breakdowns">
        <Breakdown title="By source" rows={sourceRows} {code} color={(row) => seriesColor(row.key)} />
        <Breakdown title="By app" rows={appRows} {code} color={() => SERIES_COLORS[0]} />
        <Breakdown
          title="By country"
          rows={countryRows}
          {code}
          color={() => SERIES_COLORS[2]}
          empty="No country was attached to this window's money."
        />
      </div>
    {/if}

    {#if vault.error}
      <p class="note error">{vault.error}</p>
    {/if}
  {/if}
</section>

<style>
  .vault {
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
    grid-template-columns: repeat(auto-fit, minmax(min(160px, 100%), 1fr));
    gap: 0.6rem;
    margin-bottom: 0.9rem;
  }

  .card {
    padding: 0.9rem 1rem 0.6rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    margin-bottom: 0.9rem;
  }

  .card-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.6rem;
    margin-bottom: 0.5rem;
  }

  .card-head h3 {
    margin: 0;
    font-size: 0.85rem;
    font-weight: 600;
  }

  .hint {
    font-size: 0.7rem;
    color: var(--text-faint);
  }

  .breakdowns {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(260px, 100%), 1fr));
    gap: 0.6rem;
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
    margin: 0 0 0.6rem;
    font-size: 0.85rem;
    max-width: 62ch;
  }

  .empty .fine {
    font-size: 0.78rem;
    color: var(--text-faint);
    margin-bottom: 0;
  }

  a {
    color: var(--accent);
  }

  code {
    font-family: var(--mono);
    font-size: 0.85em;
    background: var(--panel-2);
    border: 1px solid var(--border-soft);
    border-radius: 5px;
    padding: 0.05rem 0.35rem;
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
