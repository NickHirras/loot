<script lang="ts">
  import Sparkline from './Sparkline.svelte'
  import { bossesState } from './bosses.svelte'
  import type { Boss } from './types'
  import { dayLabel, integer } from './types'

  let {
    boss,
    flashing = false,
  }: {
    boss: Boss
    /** True for a few seconds after the kill drop lands. */
    flashing?: boolean
  } = $props()

  const alive = $derived(boss.status === 'alive')
  const slain = $derived(boss.status === 'slain')
  const faded = $derived(boss.status === 'faded')

  const busy = $derived(bossesState.isBusy(boss.id))
  const confirming = $derived(bossesState.confirming === boss.id)

  /** "8 days" reads better than "8 days alive" under a name. */
  const dayCount = $derived(boss.days_alive === 1 ? '1 day' : `${boss.days_alive} days`)

  /**
   * The one line that says how the fight is going. It is deliberately a
   * *statement*, never a verdict: a boss that has not moved says so plainly,
   * and a boss that is still standing after a fortnight is still standing.
   */
  const statusLine = $derived(
    slain
      ? `slain in ${dayCount}${boss.xp_awarded ? ` · +${integer(boss.xp_awarded)} XP` : ''}`
      : faded
        ? `faded after ${dayCount} — the source stopped reporting`
        : boss.down_pct >= 0.01
          ? `−${Math.round(boss.down_pct * 100)}% since it appeared`
          : boss.enraged
            ? 'stronger than when it appeared'
            : 'holding at full strength',
  )

  const unit = $derived(boss.unit || 'crashes')
</script>

<article class="boss" class:alive class:slain class:faded class:flashing class:enraged={boss.enraged && alive}>
  <div class="head">
    <h4 class="name">{boss.name}</h4>
    {#if boss.enraged && alive}
      <span class="rage" title="It got worse before it got better">enraged</span>
    {/if}
    {#if slain}
      <span class="tomb" aria-hidden="true">†</span>
    {/if}
  </div>

  <p class="title">{boss.title}</p>

  <div class="chips">
    {#if boss.app}<span class="chip">{boss.app}</span>{/if}
    {#if boss.version}<span class="chip mono">{boss.version}</span>{/if}
    {#if boss.kind === 'anr'}<span class="chip">ANR</span>{/if}
    <span class="chip">{boss.source}</span>
  </div>

  <div
    class="track"
    role="progressbar"
    aria-label="{boss.name} hit points"
    aria-valuemin="0"
    aria-valuemax={Math.round(boss.hp_max)}
    aria-valuenow={Math.round(boss.hp)}
  >
    <div class="hp" style="width: {Math.max(alive ? 2 : 0, boss.pct * 100)}%"></div>
  </div>

  <div class="foot">
    <span class="figures">
      <strong>{integer(boss.hp)}</strong>
      <span class="of">/ {integer(boss.hp_max)}</span>
      <span class="unit">{unit}</span>
    </span>
    <span class="status">{statusLine}</span>
  </div>

  {#if boss.series.length}
    <Sparkline points={boss.series} flagDay={boss.peak_day} unit="count" />
  {/if}

  <dl class="figures-row">
    {#if boss.users_affected > 0}
      <div>
        <dt>people hit</dt>
        <dd>{integer(boss.users_affected)}</dd>
      </div>
    {/if}
    <div>
      <dt>{alive ? 'fighting for' : 'lasted'}</dt>
      <dd>{dayCount}</dd>
    </div>
    <div>
      <dt>appeared</dt>
      <dd>{dayLabel(boss.spawned_day)}</dd>
    </div>
  </dl>

  <div class="actions">
    {#if boss.url}
      <a class="out" href={boss.url} target="_blank" rel="noreferrer noopener">Open the issue ↗</a>
    {/if}
    {#if alive}
      {#if confirming}
        <span class="confirm">Fixed it?</span>
        <button class="slay yes" disabled={busy} onclick={() => bossesState.slay(boss.id)}>
          {busy ? 'Landing the blow…' : 'Yes, slay it'}
        </button>
        <button class="cancel" disabled={busy} onclick={() => bossesState.cancelConfirm()}>Not yet</button>
      {:else}
        <button class="slay" disabled={busy} onclick={() => bossesState.askConfirm(boss.id)}>Mark slain</button>
      {/if}
    {/if}
  </div>
</article>

<style>
  .boss {
    padding: 0.85rem 0.95rem 0.9rem;
    border: 1px solid color-mix(in oklab, var(--cursed) 30%, var(--border-soft));
    border-radius: var(--radius);
    background: linear-gradient(180deg, color-mix(in oklab, var(--cursed) 6%, var(--panel)), var(--panel-2));
    min-width: 0;
  }

  /* A won fight goes quiet and gold-edged; a faded one goes plain grey. Nothing
     here is ever scolding — the only red in the card is the health bar. */
  .boss.slain {
    border-color: color-mix(in oklab, var(--legendary) 35%, transparent);
    background: #0e121b;
  }

  .boss.faded {
    border-style: dashed;
    border-color: var(--border);
    background: #0e121b;
  }

  .boss.enraged {
    border-color: color-mix(in oklab, var(--cursed) 60%, transparent);
  }

  .boss.flashing {
    animation: victory 5s ease-out;
  }

  @keyframes victory {
    0% {
      box-shadow: 0 0 0 0 color-mix(in oklab, var(--legendary) 70%, transparent);
      border-color: var(--legendary);
    }
    40% {
      box-shadow: 0 0 30px 2px color-mix(in oklab, var(--legendary) 45%, transparent);
    }
    100% {
      box-shadow: 0 0 0 0 transparent;
    }
  }

  .head {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  /* The name is the whole point of the feature, so it gets the biggest,
     most ominous type on the page. */
  .name {
    margin: 0;
    font-size: 1.12rem;
    font-weight: 700;
    line-height: 1.2;
    letter-spacing: 0.005em;
    overflow-wrap: anywhere;
    background: linear-gradient(96deg, var(--cursed), #ff9a6b 72%, var(--legendary));
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }

  .slain .name,
  .faded .name {
    background: none;
    color: var(--text-dim);
    -webkit-text-fill-color: currentColor;
  }

  .rage {
    font-size: 0.58rem;
    text-transform: uppercase;
    letter-spacing: 0.09em;
    font-weight: 700;
    padding: 0.1rem 0.4rem;
    border-radius: 999px;
    color: var(--cursed);
    border: 1px solid color-mix(in oklab, var(--cursed) 50%, transparent);
    background: color-mix(in oklab, var(--cursed) 12%, transparent);
  }

  .tomb {
    color: var(--legendary);
    font-size: 0.95rem;
  }

  .title {
    margin: 0.2rem 0 0.4rem;
    font-size: 0.82rem;
    color: var(--text-dim);
    overflow-wrap: anywhere;
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }

  .chip {
    font-size: 0.66rem;
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    background: var(--panel-2);
    border: 1px solid var(--border-soft);
    color: var(--text-faint);
  }

  .chip.mono {
    font-family: var(--mono);
  }

  .track {
    height: 10px;
    margin: 0.6rem 0 0.4rem;
    border-radius: 999px;
    background: #0d111a;
    border: 1px solid var(--border-soft);
    overflow: hidden;
  }

  .hp {
    height: 100%;
    background: linear-gradient(90deg, #8c1b28, var(--cursed));
    transition: width 0.6s cubic-bezier(0.22, 1, 0.36, 1);
  }

  .slain .hp,
  .faded .hp {
    background: var(--common);
    opacity: 0.5;
  }

  .foot {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.4rem;
    font-size: 0.75rem;
    color: var(--text-dim);
  }

  .figures strong {
    font-size: 0.95rem;
    color: var(--text);
  }

  .of {
    color: var(--text-faint);
  }

  .unit {
    margin-left: 0.3rem;
    color: var(--text-faint);
  }

  .status {
    color: var(--text-faint);
  }

  .slain .status {
    color: var(--legendary);
  }

  .figures-row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 1.1rem;
    margin: 0.3rem 0 0;
  }

  .figures-row div {
    min-width: 0;
  }

  .figures-row dt {
    font-size: 0.62rem;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-faint);
  }

  .figures-row dd {
    margin: 0;
    font-size: 0.82rem;
    color: var(--text-dim);
  }

  .actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin-top: 0.6rem;
  }

  .out {
    font-size: 0.75rem;
    color: var(--rare);
    text-decoration: none;
    margin-right: auto;
  }

  .out:hover {
    text-decoration: underline;
  }

  .confirm {
    font-size: 0.75rem;
    color: var(--text-dim);
  }

  .slay,
  .cancel {
    font-size: 0.76rem;
  }

  .slay.yes {
    border-color: color-mix(in oklab, var(--legendary) 50%, transparent);
    background: color-mix(in oklab, var(--legendary) 14%, #0d111a);
    color: var(--legendary);
  }
</style>
