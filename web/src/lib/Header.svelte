<script lang="ts">
  import { loot } from './state.svelte'
  import { RARITIES } from './types'

  const xp = $derived(loot.stats?.total_xp ?? 0)
  const totalDrops = $derived(loot.stats?.total_drops ?? 0)
  const countries = $derived(loot.stats?.countries_count ?? 0)

  /** A soft level curve: every level costs a bit more than the last. */
  const level = $derived(Math.max(1, Math.floor(Math.sqrt(xp / 100)) + 1))
  const levelFloor = $derived((level - 1) ** 2 * 100)
  const levelCeil = $derived(level ** 2 * 100)
  const progress = $derived(
    levelCeil > levelFloor ? Math.min(100, ((xp - levelFloor) / (levelCeil - levelFloor)) * 100) : 0,
  )

  function statusLabel(source: { last_error: string; last_poll_at: string | null; mode: string }): string {
    if (source.last_error) return 'error'
    if (source.mode === 'webhook') return 'listening'
    return source.last_poll_at ? 'ok' : 'waiting'
  }
</script>

<header>
  <div class="bar">
    <div class="brand">
      <span class="gem" aria-hidden="true">◆</span>
      <h1>Loot</h1>
      <span class="conn" class:live={loot.connected} title={loot.connected ? 'Live' : 'Reconnecting…'}>
        <span class="dot"></span>{loot.connected ? 'live' : 'offline'}
      </span>
    </div>

    <div class="right">
      <button
        class="mute"
        onclick={() => loot.toggleMute()}
        aria-pressed={loot.muted}
        title={loot.muted ? 'Unmute drop sounds' : 'Mute drop sounds'}
      >
        {loot.muted ? '🔇' : '🔊'}
        <span class="mute-label">{loot.muted ? 'Muted' : 'Sound'}</span>
      </button>
    </div>
  </div>

  <div class="stats">
    <div class="xp-block">
      <div class="xp-top">
        <span class="level">LVL {level}</span>
        <span class="xp-total">{xp.toLocaleString()} XP</span>
      </div>
      <div class="xp-bar" role="progressbar" aria-valuenow={Math.round(progress)} aria-valuemin="0" aria-valuemax="100">
        <div class="xp-fill" style="width: {progress}%"></div>
      </div>
      <div class="xp-sub">
        {totalDrops.toLocaleString()} drops · {countries} {countries === 1 ? 'country' : 'countries'}
      </div>
    </div>

    <ul class="rarities">
      {#each RARITIES as rarity (rarity)}
        <li class="rarity rarity-{rarity}" title="{loot.byRarity[rarity] ?? 0} {rarity} drops">
          <span class="count">{loot.byRarity[rarity] ?? 0}</span>
          <span class="name">{rarity}</span>
        </li>
      {/each}
    </ul>

    <ul class="sources">
      {#each loot.sources as source (source.name)}
        <li class="source" class:err={!!source.last_error} title={source.last_error || statusLabel(source)}>
          <span class="s-dot"></span>
          <span class="s-name mono">{source.name}</span>
          <span class="s-meta">{source.mode === 'poll' ? source.poll_interval : 'webhook'}</span>
          <span class="s-events">{source.events.toLocaleString()}</span>
        </li>
      {:else}
        <li class="source empty">no sources configured</li>
      {/each}
    </ul>
  </div>
</header>

<style>
  header {
    position: sticky;
    top: 0;
    z-index: 10;
    backdrop-filter: blur(14px);
    background: color-mix(in oklab, var(--bg) 82%, transparent);
    border-bottom: 1px solid var(--border-soft);
  }

  .bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.7rem 1.1rem 0.4rem;
  }

  .brand {
    display: flex;
    align-items: baseline;
    gap: 0.55rem;
  }

  .gem {
    color: var(--legendary);
    font-size: 1.1rem;
    text-shadow: 0 0 14px color-mix(in oklab, var(--legendary) 60%, transparent);
  }

  h1 {
    margin: 0;
    font-size: 1.15rem;
    letter-spacing: 0.02em;
    font-weight: 700;
    background: linear-gradient(92deg, var(--legendary), var(--epic) 60%, var(--rare));
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }

  .conn {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.68rem;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-faint);
  }

  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--cursed);
  }

  .conn.live .dot {
    background: var(--uncommon);
    box-shadow: 0 0 8px var(--uncommon);
    animation: pulse 2.4s ease-in-out infinite;
  }

  @keyframes pulse {
    50% {
      opacity: 0.45;
    }
  }

  .mute {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.78rem;
  }

  .stats {
    display: grid;
    grid-template-columns: minmax(200px, 1fr) auto auto;
    gap: 1rem 1.4rem;
    align-items: center;
    padding: 0.2rem 1.1rem 0.8rem;
  }

  .xp-top {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
  }

  .level {
    font-weight: 700;
    font-size: 0.8rem;
    color: var(--legendary);
    letter-spacing: 0.05em;
  }

  .xp-total {
    font-size: 0.8rem;
    color: var(--text-dim);
  }

  .xp-bar {
    height: 6px;
    margin-top: 0.3rem;
    border-radius: 999px;
    background: #0d111a;
    border: 1px solid var(--border-soft);
    overflow: hidden;
  }

  .xp-fill {
    height: 100%;
    background: linear-gradient(90deg, var(--rare), var(--epic), var(--legendary));
    transition: width 0.6s cubic-bezier(0.22, 1, 0.36, 1);
  }

  .xp-sub {
    margin-top: 0.28rem;
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .rarities {
    display: flex;
    gap: 0.4rem;
    list-style: none;
    margin: 0;
    padding: 0;
    flex-wrap: wrap;
  }

  .rarity {
    min-width: 62px;
    text-align: center;
    padding: 0.25rem 0.45rem;
    border-radius: var(--radius-sm);
    background: color-mix(in oklab, var(--r) 8%, #0d111a);
    border: 1px solid color-mix(in oklab, var(--r) 26%, transparent);
  }

  .rarity .count {
    display: block;
    font-weight: 700;
    font-size: 0.95rem;
    color: var(--r);
    line-height: 1.15;
  }

  .rarity .name {
    display: block;
    font-size: 0.6rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-faint);
  }

  .sources {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.72rem;
  }

  .source {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    color: var(--text-dim);
  }

  .s-dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: var(--uncommon);
  }

  .source.err .s-dot {
    background: var(--cursed);
  }

  .source.err .s-name {
    color: var(--cursed);
  }

  .s-meta {
    color: var(--text-faint);
  }

  .s-events {
    color: var(--text-faint);
  }

  .source.empty {
    color: var(--text-faint);
    font-style: italic;
  }

  @media (max-width: 780px) {
    .stats {
      grid-template-columns: 1fr;
      gap: 0.7rem;
    }
    .mute-label {
      display: none;
    }
  }
</style>
