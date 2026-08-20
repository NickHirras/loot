<script lang="ts">
  import Sparkline from './Sparkline.svelte'
  import { questsState } from './quests.svelte'
  import type { Mystery } from './types'
  import { MYSTERY_KIND_LABEL, currency, dayLabel, integer } from './types'

  let {
    mystery,
    code,
  }: {
    mystery: Mystery
    /** Display currency, for the money series. */
    code: string
  } = $props()

  let note = $state('')

  const detail = $derived(mystery.detail)
  const unit = $derived(detail?.unit ?? 'count')

  function format(v: number): string {
    return unit === 'money' ? currency(v, code, 0) : integer(v)
  }

  const busy = $derived(questsState.isBusy(mystery.id))
  const label = $derived(MYSTERY_KIND_LABEL[mystery.kind] ?? mystery.kind)
</script>

<article class="mystery kind-{mystery.kind}">
  <div class="head">
    <span class="badge">{label}</span>
    <h4>{mystery.title}</h4>
  </div>

  <div class="chips">
    {#if mystery.source}<span class="chip">{mystery.source}</span>{/if}
    {#if mystery.app}<span class="chip">{mystery.app}</span>{/if}
    <span class="chip">{dayLabel(mystery.day, true)}</span>
  </div>

  {#if detail?.series?.length}
    <Sparkline
      points={detail.series}
      flagDay={mystery.day}
      baseline={detail.baseline}
      {unit}
      {code}
    />
  {/if}

  <dl class="figures">
    <div>
      <dt>observed</dt>
      <dd class="observed">{format(mystery.observed)}</dd>
    </div>
    <div>
      <dt>expected</dt>
      <dd>{format(mystery.expected)}</dd>
    </div>
    {#if detail?.ratio}
      <div>
        <dt>ratio</dt>
        <dd>{detail.ratio.toFixed(1)}×</dd>
      </div>
    {/if}
  </dl>

  {#if detail?.why}
    <p class="why">{detail.why}</p>
  {/if}

  <form
    class="answer"
    onsubmit={(e) => {
      e.preventDefault()
      void questsState.solve(mystery.id, note)
    }}
  >
    <input
      type="text"
      placeholder="What do you think happened?"
      bind:value={note}
      aria-label="Your explanation for: {mystery.title}"
      disabled={busy}
    />
    <button type="submit" class="solve" disabled={busy}>Solve</button>
    <button type="button" class="dismiss" disabled={busy} onclick={() => questsState.dismiss(mystery.id)}>
      Dismiss
    </button>
  </form>
</article>

<style>
  .mystery {
    padding: 0.8rem 0.9rem 0.85rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    min-width: 0;
  }

  .head {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
  }

  h4 {
    margin: 0;
    font-size: 0.92rem;
    font-weight: 600;
    line-height: 1.35;
    overflow-wrap: anywhere;
  }

  .badge {
    flex: none;
    font-size: 0.58rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-weight: 700;
    padding: 0.1rem 0.4rem;
    border-radius: 999px;
    color: var(--accent);
    border: 1px solid color-mix(in oklab, var(--accent) 40%, transparent);
    background: color-mix(in oklab, var(--accent) 10%, transparent);
  }

  /* A dip or a refund wave is news, not a telling-off: amber, never red. */
  .kind-dip .badge,
  .kind-refund_spike .badge,
  .kind-silence .badge {
    color: var(--legendary);
    border-color: color-mix(in oklab, var(--legendary) 45%, transparent);
    background: color-mix(in oklab, var(--legendary) 10%, transparent);
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    margin-top: 0.4rem;
  }

  .chip {
    font-size: 0.66rem;
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    background: var(--panel-2);
    border: 1px solid var(--border-soft);
    color: var(--text-faint);
  }

  .figures {
    display: flex;
    gap: 1.2rem;
    margin: 0.3rem 0 0;
  }

  .figures div {
    display: flex;
    flex-direction: column;
  }

  dt {
    font-size: 0.62rem;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-faint);
  }

  dd {
    margin: 0;
    font-size: 0.95rem;
    font-weight: 600;
    color: var(--text-dim);
  }

  dd.observed {
    color: var(--text);
  }

  .why {
    margin: 0.45rem 0 0;
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .answer {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
    margin-top: 0.6rem;
  }

  input {
    flex: 1 1 12rem;
    min-width: 0;
    font: inherit;
    font-size: 0.8rem;
    color: var(--text);
    background: #0d111a;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.35rem 0.55rem;
  }

  input:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }

  button {
    font-size: 0.78rem;
  }

  .solve {
    border-color: color-mix(in oklab, var(--uncommon) 45%, transparent);
    background: color-mix(in oklab, var(--uncommon) 12%, #0d111a);
  }

  .solve:hover {
    border-color: var(--uncommon);
    background: color-mix(in oklab, var(--uncommon) 20%, #0d111a);
  }

  button:disabled {
    opacity: 0.55;
    cursor: default;
  }

  /* A 12rem input plus two buttons is one item too many for a phone row, and
     what wrapped was the Dismiss button, alone and off to the left. Give the
     question its own line and let the two answers share the next. */
  @media (max-width: 560px) {
    input {
      flex: 1 1 100%;
    }

    .solve,
    .dismiss {
      flex: 1 1 0;
    }
  }
</style>
