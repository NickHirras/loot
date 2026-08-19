<script lang="ts">
  import type { FeedDrop } from './state.svelte'
  import { flagEmoji, isFlashy, money, timeAgo } from './types'

  let { drop, index = 0 }: { drop: FeedDrop; index?: number } = $props()

  const flashy = $derived(isFlashy(drop.rarity))
  const amount = $derived(money(drop.amount, drop.currency))
  const flag = $derived(flagEmoji(drop.country))

  // Particles are only worth their DOM cost on a genuinely notable drop.
  const particles = $derived(flashy && drop.fresh ? Array.from({ length: 10 }, (_, i) => i) : [])
</script>

<article
  class="card rarity-{drop.rarity}"
  class:fresh={drop.fresh}
  class:flashy
  style="--i: {index}"
  aria-label="{drop.rarity} drop: {drop.title}"
>
  <div class="stripe" aria-hidden="true"></div>

  {#if particles.length}
    <div class="particles" aria-hidden="true">
      {#each particles as p (p)}
        <span class="particle" style="--p: {p}; --angle: {(360 / particles.length) * p}deg"></span>
      {/each}
    </div>
  {/if}

  <div class="body">
    <div class="top">
      <span class="badge">{drop.rarity}</span>
      <span class="source mono">{drop.source}</span>
      {#if drop.kind}<span class="kind mono">{drop.kind}</span>{/if}
      {#if drop.chest_date}
        <span class="chest" title="Came out of the chest for {drop.chest_date}">📦 {drop.chest_date}</span>
      {/if}
      <span class="spacer"></span>
      <span class="xp">+{drop.xp} XP</span>
      <time class="ago" datetime={drop.created_at} title={new Date(drop.created_at).toLocaleString()}>
        {timeAgo(drop.created_at)}
      </time>
    </div>

    <h3 class="title">{drop.title}</h3>

    {#if drop.subtitle}
      <p class="subtitle">{drop.subtitle}</p>
    {/if}

    <div class="meta">
      {#if amount}<span class="chip money">{amount}</span>{/if}
      {#if drop.country}<span class="chip">{flag} {drop.country}</span>{/if}
      {#if drop.app}<span class="chip mono app" title={drop.app}>{drop.app}</span>{/if}
    </div>
  </div>
</article>

<style>
  .card {
    position: relative;
    display: flex;
    gap: 0;
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    overflow: hidden;
    transition:
      border-color 0.2s ease,
      transform 0.2s ease;
  }

  .card:hover {
    border-color: color-mix(in oklab, var(--r) 45%, var(--border));
    transform: translateX(2px);
  }

  .stripe {
    width: 4px;
    flex: 0 0 4px;
    background: var(--r);
    box-shadow: 0 0 12px color-mix(in oklab, var(--r) 70%, transparent);
  }

  .body {
    flex: 1;
    min-width: 0;
    padding: 0.7rem 0.9rem 0.75rem;
  }

  .top {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    font-size: 0.72rem;
  }

  .spacer {
    flex: 1;
  }

  .badge {
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-weight: 700;
    font-size: 0.66rem;
    color: var(--r);
    background: color-mix(in oklab, var(--r) 14%, transparent);
    border: 1px solid color-mix(in oklab, var(--r) 35%, transparent);
    padding: 0.1rem 0.4rem;
    border-radius: 999px;
  }

  .source,
  .kind {
    color: var(--text-faint);
    font-size: 0.7rem;
  }

  .kind::before {
    content: '· ';
  }

  .chest {
    font-size: 0.66rem;
    color: var(--legendary);
    background: color-mix(in oklab, var(--legendary) 12%, transparent);
    border: 1px solid color-mix(in oklab, var(--legendary) 30%, transparent);
    border-radius: 999px;
    padding: 0.02rem 0.4rem;
    white-space: nowrap;
  }

  .xp {
    color: color-mix(in oklab, var(--r) 75%, var(--text));
    font-weight: 600;
    font-size: 0.72rem;
    white-space: nowrap;
  }

  .ago {
    color: var(--text-faint);
    font-size: 0.7rem;
    white-space: nowrap;
    min-width: 3ch;
    text-align: right;
  }

  .title {
    margin: 0.35rem 0 0;
    font-size: 1rem;
    font-weight: 620;
    line-height: 1.3;
    color: var(--text);
    overflow-wrap: anywhere;
  }

  .subtitle {
    margin: 0.15rem 0 0;
    color: var(--text-dim);
    font-size: 0.85rem;
    overflow-wrap: anywhere;
  }

  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
    margin-top: 0.5rem;
  }

  .chip {
    font-size: 0.7rem;
    color: var(--text-dim);
    background: #0e121b;
    border: 1px solid var(--border-soft);
    border-radius: 999px;
    padding: 0.08rem 0.5rem;
  }

  .chip.money {
    color: var(--uncommon);
    border-color: color-mix(in oklab, var(--uncommon) 30%, transparent);
  }

  .chip.app {
    max-width: 22ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* --- arrival animation ------------------------------------------------- */

  .fresh {
    animation: slide-in 0.42s cubic-bezier(0.22, 1, 0.36, 1) both;
  }

  .fresh.flashy {
    border-color: color-mix(in oklab, var(--r) 55%, var(--border));
    animation:
      slide-in 0.42s cubic-bezier(0.22, 1, 0.36, 1) both,
      glow 2s ease-out 0.2s both;
  }

  @keyframes slide-in {
    from {
      opacity: 0;
      transform: translateX(-14px) scale(0.985);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }

  @keyframes glow {
    0% {
      box-shadow: 0 0 0 0 color-mix(in oklab, var(--r) 55%, transparent);
    }
    25% {
      box-shadow: 0 0 26px 2px color-mix(in oklab, var(--r) 45%, transparent);
    }
    100% {
      box-shadow: 0 0 0 0 transparent;
    }
  }

  /* --- particle burst ----------------------------------------------------- */

  .particles {
    position: absolute;
    inset: 0;
    pointer-events: none;
    overflow: hidden;
  }

  .particle {
    position: absolute;
    top: 50%;
    left: 12px;
    width: 5px;
    height: 5px;
    margin: -2.5px 0 0 -2.5px;
    border-radius: 50%;
    background: var(--r);
    box-shadow: 0 0 8px color-mix(in oklab, var(--r) 80%, transparent);
    animation: burst 0.85s ease-out both;
    animation-delay: calc(var(--p) * 12ms);
  }

  @keyframes burst {
    0% {
      opacity: 1;
      transform: rotate(var(--angle)) translateX(0) scale(1);
    }
    100% {
      opacity: 0;
      transform: rotate(var(--angle)) translateX(90px) scale(0.3);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .particles {
      display: none;
    }
  }
</style>
