<script lang="ts">
  import { questsState } from './quests.svelte'
  import type { Quest } from './types'
  import { METRIC_ICON, METRIC_LABEL, currency, integer, percent } from './types'

  let {
    quest,
    code,
    flashing = false,
  }: {
    quest: Quest
    /** Display currency, for the money metrics. */
    code: string
    /** True for a few seconds after the completion drop lands. */
    flashing?: boolean
  } = $props()

  function format(value: number): string {
    return quest.metric === 'revenue' ? currency(value, code, 0) : integer(value)
  }

  const done = $derived(quest.status === 'completed')
  const ended = $derived(quest.status === 'expired')

  /** Ended quests read as a plain fact — "ended · 62%" — never as a failure. */
  const statusLine = $derived(
    done
      ? `completed${quest.xp ? ` · +${integer(quest.xp)} XP` : ''}`
      : ended
        ? `ended · ${percent(quest.pct)}`
        : quest.days_left <= 0
          ? 'last day'
          : quest.days_left === 1
            ? '1 day left'
            : `${quest.days_left} days left`,
  )

  /** A window of four weeks or more is a month's quest, exactly as the server
   * decides when it chooses between a rare and an epic completion drop. */
  const windowDays = $derived(
    Math.round(
      (Date.parse(`${quest.window_end}T00:00:00Z`) - Date.parse(`${quest.window_start}T00:00:00Z`)) / 86_400_000,
    ) + 1,
  )
  const windowLabel = $derived(windowDays >= 28 ? 'this month' : windowDays === 7 ? 'this week' : `${windowDays} days`)
</script>

<article class="quest" class:done class:ended class:flashing>
  <div class="head">
    <span class="icon" aria-hidden="true">{METRIC_ICON[quest.metric] ?? '◆'}</span>
    <h4>{quest.title}</h4>
    {#if quest.kind === 'custom'}
      <span class="tag">custom</span>
    {/if}
    {#if quest.kind === 'custom' && quest.status === 'active'}
      <button
        class="remove"
        title="Remove this quest"
        aria-label="Remove quest {quest.title}"
        disabled={questsState.isBusy(quest.id)}
        onclick={() => questsState.remove(quest.id)}>×</button
      >
    {/if}
  </div>

  <div class="track" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(quest.pct * 100)}>
    <div class="bar" style="width: {Math.max(1.5, quest.pct * 100)}%"></div>
  </div>

  <div class="foot">
    <span class="figures">
      <strong>{format(quest.value)}</strong>
      <span class="of">/ {format(quest.target)}</span>
      <span class="metric">{METRIC_LABEL[quest.metric] ?? quest.metric}</span>
    </span>
    <span class="status">{statusLine}</span>
  </div>

  <div class="scope">
    {#if quest.app}<span class="chip">{quest.app}</span>{/if}
    {#if quest.source}<span class="chip">{quest.source}</span>{/if}
    <span class="chip">{windowLabel}</span>
  </div>
</article>

<style>
  .quest {
    padding: 0.7rem 0.85rem 0.75rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    min-width: 0;
  }

  /* A completed quest is quietly gold; an ended one is quietly grey. Neither
     shouts, and nothing here is ever red. */
  .quest.done {
    border-color: color-mix(in oklab, var(--legendary) 40%, transparent);
  }

  .quest.ended {
    background: #0e121b;
    border-style: dashed;
    color: var(--text-dim);
  }

  .quest.flashing {
    animation: land 5s ease-out;
  }

  @keyframes land {
    0% {
      box-shadow: 0 0 0 0 color-mix(in oklab, var(--legendary) 70%, transparent);
      border-color: var(--legendary);
    }
    40% {
      box-shadow: 0 0 26px 2px color-mix(in oklab, var(--legendary) 45%, transparent);
    }
    100% {
      box-shadow: 0 0 0 0 transparent;
    }
  }

  .head {
    display: flex;
    align-items: baseline;
    gap: 0.45rem;
  }

  .icon {
    color: var(--legendary);
    font-size: 0.85rem;
  }

  h4 {
    margin: 0;
    font-size: 0.9rem;
    font-weight: 600;
    line-height: 1.3;
    overflow-wrap: anywhere;
  }

  .ended h4 {
    font-weight: 500;
    color: var(--text-dim);
  }

  .tag {
    font-size: 0.58rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    padding: 0.05rem 0.35rem;
    border-radius: 999px;
    color: var(--accent);
    border: 1px solid color-mix(in oklab, var(--accent) 40%, transparent);
  }

  .remove {
    margin-left: auto;
    border: 0;
    background: transparent;
    color: var(--text-faint);
    padding: 0 0.2rem;
    font-size: 1rem;
    line-height: 1;
  }

  .remove:hover {
    color: var(--text);
    background: transparent;
  }

  .track {
    height: 8px;
    margin: 0.55rem 0 0.4rem;
    border-radius: 999px;
    background: #0d111a;
    border: 1px solid var(--border-soft);
    overflow: hidden;
  }

  .bar {
    height: 100%;
    background: linear-gradient(90deg, var(--rare), var(--epic) 62%, var(--legendary));
    transition: width 0.6s cubic-bezier(0.22, 1, 0.36, 1);
  }

  .ended .bar {
    background: var(--common);
    opacity: 0.55;
  }

  .done .bar {
    background: linear-gradient(90deg, var(--epic), var(--legendary));
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

  .metric {
    margin-left: 0.3rem;
    color: var(--text-faint);
  }

  .status {
    color: var(--text-faint);
  }

  .done .status {
    color: var(--legendary);
  }

  .scope {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    margin-top: 0.45rem;
  }

  .chip {
    font-size: 0.66rem;
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    background: var(--panel-2);
    border: 1px solid var(--border-soft);
    color: var(--text-faint);
  }
</style>
