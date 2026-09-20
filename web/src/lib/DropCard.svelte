<script lang="ts">
  import type { FeedDrop } from './state.svelte'
  import { flagEmoji, isFlashy, money, timeAgo } from './types'
  import { rarityLabel } from './labels'
  import { m } from '../paraglide/messages'

  let {
    drop,
    index = 0,
    /**
     * The feed's clock, bumped once a minute. Reading it here — rather than
     * keying the whole list on it — is what keeps "3m" honest while leaving
     * every card's DOM, and its arrival animation, untouched.
     */
    now = Date.now(),
  }: { drop: FeedDrop; index?: number; now?: number } = $props()

  const ago = $derived(timeAgo(drop.created_at, now))
  const flashy = $derived(isFlashy(drop.rarity))
  const amount = $derived(money(drop.amount, drop.currency))
  const flag = $derived(flagEmoji(drop.country))

  // Where the card points. The server derives this per drop and already
  // refuses anything that is not http(s) or one of Loot's own hash routes,
  // but the shape is checked again here: part of a drop's link can come from
  // a payload someone POSTed to /hooks/webhook, and a `javascript:` href one
  // layer of validation away from the DOM is one layer too few. Anything that
  // passes neither test is ignored and the card renders exactly as it always
  // did, unlinked.
  const link = $derived(drop.link ?? '')
  const external = $derived(/^https?:\/\//i.test(link))
  const internal = $derived(link.startsWith('#/'))
  const href = $derived(external || internal ? link : '')

  // Particles are only worth their DOM cost on a genuinely notable drop.
  const particles = $derived(flashy && drop.fresh ? Array.from({ length: 10 }, (_, i) => i) : [])
</script>

<article
  class="card rarity-{drop.rarity}"
  class:fresh={drop.fresh}
  class:flashy
  style="--i: {index}"
  aria-label={m.drop_aria_label({ rarity: rarityLabel(drop.rarity), title: drop.title })}
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
      <span class="badge">{rarityLabel(drop.rarity)}</span>
      <span class="source mono">{drop.source}</span>
      {#if drop.kind}<span class="kind mono">{drop.kind}</span>{/if}
      {#if drop.chest_date}
        <span class="chest" title={m.drop_chest_title({ date: drop.chest_date })}>📦 {drop.chest_date}</span>
      {/if}
      <span class="spacer"></span>
      <span class="xp">{m.drop_xp({ xp: drop.xp })}</span>
      <time class="ago" datetime={drop.created_at} title={new Date(drop.created_at).toLocaleString()}>
        {ago}
      </time>
      <!-- The affordance: faint until the card is hovered or focused, so a
           feed of linked drops does not read as a column of arrows. -->
      {#if external}<span class="out" aria-hidden="true">↗</span>{/if}
    </div>

    <h3 class="title">
      {#if href}
        <!-- The "stretched link": one ordinary link, named by the title, whose
             ::after covers the whole card. Screen readers get a single sensible
             link instead of a clickable <article>, and the mouse gets the card. -->
        <a
          class="title-link"
          {href}
          target={external ? '_blank' : null}
          rel={external ? 'noopener noreferrer' : null}
          title={external ? m.drop_open_source() : m.drop_open_in_loot()}>{drop.title}</a>
      {:else}
        {drop.title}
      {/if}
    </h3>

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

  /* Keyboard parity with the hover state: the ring goes on the card rather
     than on the title text, because the card is what the link actually is. */
  .card:has(.title-link:focus-visible) {
    outline: 2px solid color-mix(in oklab, var(--r) 70%, var(--text));
    outline-offset: 2px;
    border-color: color-mix(in oklab, var(--r) 45%, var(--border));
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

  .out {
    color: var(--text-faint);
    font-size: 0.72rem;
    line-height: 1;
    opacity: 0.4;
    transition:
      opacity 0.2s ease,
      color 0.2s ease;
  }

  .card:hover .out,
  .card:focus-within .out {
    opacity: 1;
    color: color-mix(in oklab, var(--r) 65%, var(--text-dim));
  }

  .title {
    margin: 0.35rem 0 0;
    font-size: 1rem;
    font-weight: 620;
    line-height: 1.3;
    color: var(--text);
    overflow-wrap: anywhere;
  }

  /* A linked title is still just the title: same colour, no underline until
     you are actually pointing at it. */
  .title-link {
    color: inherit;
    text-decoration: none;
    cursor: pointer;
  }

  .title-link:hover {
    text-decoration: underline;
  }

  /* The ring is drawn on the card instead; see .card:has(…) above. */
  .title-link:focus-visible {
    outline: none;
  }

  /* This is the whole stretched-link trick: an empty box over the entire card,
     belonging to the anchor, so a click anywhere on the card follows it. */
  .title-link::after {
    content: '';
    position: absolute;
    inset: 0;
  }

  /* …and these are the exceptions that have to stay on top of it, because a
     `title` tooltip only appears for the element the pointer is really over.
     They are small, so the card stays clickable everywhere that matters. */
  .chest,
  .ago,
  .chip.app {
    position: relative;
    z-index: 1;
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
