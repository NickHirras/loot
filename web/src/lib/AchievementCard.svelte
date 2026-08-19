<script lang="ts">
  import { tierColor } from './palette'
  import type { Achievement } from './types'
  import { TIER_RARITY, TIER_XP, currency, dayLabel, integer } from './types'

  let {
    achievement,
    code,
  }: {
    achievement: Achievement
    /** Display currency, for the money achievements. */
    code: string
  } = $props()

  // The description is the reward for looking closer, so it is revealed on
  // hover and on tap. Tap matters: a phone has no hover, and a trophy whose
  // story you cannot read is just a coloured square.
  let open = $state(false)

  const unlocked = $derived(!!achievement.unlocked_at)
  const backfilled = $derived(achievement.meta?.backfilled === true)
  const color = $derived(tierColor(achievement.tier))

  function format(value: number): string {
    return achievement.money ? currency(value, code, 0) : integer(value)
  }

  /** "18 / 25 countries" for a ladder; nothing for a one-off. */
  const progressLine = $derived(
    achievement.progress_target > 1
      ? `${format(achievement.progress_value)} / ${format(achievement.progress_target)}${
          achievement.unit ? ` ${achievement.unit}` : ''
        }`
      : '',
  )

  const earned = $derived(
    achievement.unlocked_at ? dayLabel(achievement.unlocked_at.slice(0, 10), true) : '',
  )
</script>

<div
  class="ach tier-{achievement.tier}"
  class:unlocked
  class:open
  style="--tier: {color}"
  role="button"
  tabindex="0"
  aria-expanded={open}
  onclick={() => (open = !open)}
  onkeydown={(e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      open = !open
    }
  }}
>
  <div class="head">
    <span class="medal" aria-hidden="true">{unlocked ? '★' : '☆'}</span>
    <h4>{achievement.title}</h4>
    <span class="tier" title="{achievement.tier} · pays a {TIER_RARITY[achievement.tier]} drop worth {TIER_XP[achievement.tier]} XP">
      {achievement.tier}
    </span>
  </div>

  {#if unlocked}
    <p class="when">
      {earned}
      {#if backfilled}
        <span class="backfill" title="Earned before Loot was watching; dated to the day it happened">backfilled</span>
      {/if}
    </p>
  {:else if progressLine}
    <div class="track" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(achievement.pct * 100)}>
      <div class="bar" style="width: {Math.max(1.5, achievement.pct * 100)}%"></div>
    </div>
    <p class="when progress">{progressLine}</p>
  {:else}
    <p class="when faint">not yet</p>
  {/if}

  <p class="desc">{achievement.description}</p>
</div>

<style>
  .ach {
    position: relative;
    padding: 0.6rem 0.7rem 0.65rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: #0e121b;
    min-width: 0;
    cursor: default;
    /* A locked trophy is dim, never absent: the wall is a map of what there is
       to win, and hiding the unwon half would make it a list of one. */
    opacity: 0.72;
    transition:
      opacity 0.15s ease,
      border-color 0.15s ease,
      transform 0.08s ease;
  }

  .ach:hover,
  .ach:focus-visible {
    opacity: 1;
    border-color: var(--border);
  }

  .ach.unlocked {
    opacity: 1;
    border-color: color-mix(in oklab, var(--tier) 45%, transparent);
    background:
      radial-gradient(120% 90% at 0% 0%, color-mix(in oklab, var(--tier) 14%, transparent), transparent 60%),
      linear-gradient(180deg, var(--panel), var(--panel-2));
  }

  /* The legendary tier gets the one gradient on the wall, because it should be
     obvious across a room which trophies are the hard ones. */
  .ach.unlocked.tier-legendary {
    border-color: color-mix(in oklab, var(--tier) 60%, transparent);
    box-shadow: 0 0 22px -8px color-mix(in oklab, var(--tier) 70%, transparent);
    background:
      radial-gradient(130% 100% at 0% 0%, color-mix(in oklab, var(--tier) 22%, transparent), transparent 62%),
      linear-gradient(180deg, var(--panel), #191426);
  }

  .head {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
  }

  .medal {
    font-size: 0.85rem;
    color: var(--text-faint);
    line-height: 1;
  }

  .unlocked .medal {
    color: var(--tier);
    text-shadow: 0 0 10px color-mix(in oklab, var(--tier) 65%, transparent);
  }

  h4 {
    margin: 0;
    font-size: 0.86rem;
    font-weight: 600;
    line-height: 1.3;
    overflow-wrap: anywhere;
    color: var(--text-dim);
  }

  .unlocked h4 {
    color: var(--text);
  }

  .tier {
    margin-left: auto;
    font-size: 0.56rem;
    text-transform: uppercase;
    letter-spacing: 0.09em;
    font-weight: 700;
    padding: 0.05rem 0.35rem;
    border-radius: 999px;
    white-space: nowrap;
    color: var(--text-faint);
    border: 1px solid var(--border);
    cursor: help;
  }

  .unlocked .tier {
    color: var(--tier);
    border-color: color-mix(in oklab, var(--tier) 45%, transparent);
    background: color-mix(in oklab, var(--tier) 10%, transparent);
  }

  .when {
    margin: 0.35rem 0 0;
    font-size: 0.7rem;
    color: var(--text-faint);
  }

  .unlocked .when {
    color: color-mix(in oklab, var(--tier) 70%, var(--text-dim));
  }

  .when.progress {
    font-family: var(--mono);
    font-size: 0.68rem;
  }

  .when.faint {
    font-style: italic;
  }

  .backfill {
    margin-left: 0.35rem;
    font-size: 0.58rem;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-faint);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 0.3rem;
    cursor: help;
  }

  .track {
    height: 5px;
    margin: 0.5rem 0 0.1rem;
    border-radius: 999px;
    background: #0a0d15;
    border: 1px solid var(--border-soft);
    overflow: hidden;
  }

  .bar {
    height: 100%;
    background: color-mix(in oklab, var(--tier) 60%, var(--common));
    transition: width 0.6s cubic-bezier(0.22, 1, 0.36, 1);
  }

  /* The description is the story. It costs no layout until it is wanted. */
  .desc {
    margin: 0;
    font-size: 0.72rem;
    line-height: 1.35;
    color: var(--text-dim);
    max-height: 0;
    opacity: 0;
    overflow: hidden;
    transition:
      max-height 0.2s ease,
      opacity 0.2s ease,
      margin 0.2s ease;
  }

  .ach:hover .desc,
  .ach:focus-visible .desc,
  .ach.open .desc {
    max-height: 6rem;
    opacity: 1;
    margin-top: 0.4rem;
  }
</style>
