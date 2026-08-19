<script lang="ts">
  import MysteryCard from './MysteryCard.svelte'
  import QuestCard from './QuestCard.svelte'
  import { questsState } from './quests.svelte'
  import { loot } from './state.svelte'
  import type { Metric, NewQuest } from './types'
  import { METRICS, METRIC_LABEL, dayLabel, timeAgo } from './types'
  import { vault } from './vault.svelte'

  // The page owns the polling: mounting starts it, leaving stops it.
  $effect(() => questsState.activate())

  const code = $derived(loot.stats?.display_currency ?? 'USD')
  const board = $derived(questsState.board)
  const casebook = $derived(questsState.casebook)

  // --- the new quest form ---------------------------------------------------

  let showForm = $state(false)
  let metric = $state<Metric>('revenue')
  let target = $state<number | null>(null)
  let questWindow = $state<'week' | 'month'>('week')
  let app = $state('')
  let source = $state('')
  let title = $state('')
  let formError = $state('')
  let saving = $state(false)

  /** Apps and sources you actually have data for, so the form offers real
   * choices rather than a free-text field nobody can spell. */
  const apps = $derived((vault.summary?.by_app ?? []).map((a) => a.app).filter(Boolean).sort())
  const sources = $derived(Object.keys(loot.stats?.by_source ?? {}).sort())

  function openForm(): void {
    showForm = true
    formError = ''
    // The app list comes from the vault summary; load it if the Vault tab has
    // not been visited this session.
    if (!vault.summary) void vault.load()
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault()
    if (!target || target <= 0) {
      formError = 'Give it a target above zero.'
      return
    }
    saving = true
    formError = ''
    const req: NewQuest = { metric, target, window: questWindow }
    if (app) req.app = app
    if (source) req.source = source
    if (title.trim()) req.title = title.trim()
    try {
      await questsState.create(req)
      showForm = false
      target = null
      title = ''
      app = ''
      source = ''
    } catch (err) {
      formError = err instanceof Error ? err.message : String(err)
    } finally {
      saving = false
    }
  }
</script>

<section class="quests" aria-label="Quests">
  <div class="bar">
    <div class="titles">
      <h2>Quests</h2>
      <span class="sub">goals from your own history · nothing here can be failed</span>
    </div>
    <button class="new" onclick={() => (showForm ? (showForm = false) : openForm())} aria-expanded={showForm}>
      {showForm ? 'Cancel' : '+ New quest'}
    </button>
  </div>

  {#if showForm}
    <form class="form" onsubmit={submit}>
      <label>
        <span>Metric</span>
        <select bind:value={metric}>
          {#each METRICS as m (m)}
            <option value={m}>{METRIC_LABEL[m]}</option>
          {/each}
        </select>
      </label>

      <label>
        <span>Target</span>
        <input type="number" min="0" step="any" bind:value={target} placeholder="1000" required />
      </label>

      <label>
        <span>Window</span>
        <select bind:value={questWindow}>
          <option value="week">this week</option>
          <option value="month">this month</option>
        </select>
      </label>

      <label>
        <span>App</span>
        <select bind:value={app}>
          <option value="">every app</option>
          {#each apps as a (a)}
            <option value={a}>{a}</option>
          {/each}
        </select>
      </label>

      <label>
        <span>Source</span>
        <select bind:value={source}>
          <option value="">every source</option>
          {#each sources as s (s)}
            <option value={s}>{s}</option>
          {/each}
        </select>
      </label>

      <label class="wide">
        <span>Title <em>optional</em></span>
        <input type="text" bind:value={title} placeholder="Beat last week's revenue" />
      </label>

      <div class="actions">
        {#if formError}<span class="form-error">{formError}</span>{/if}
        <button type="submit" class="save" disabled={saving}>{saving ? 'Setting…' : 'Set quest'}</button>
      </div>
    </form>
  {/if}

  {#if questsState.error}
    <p class="note error">{questsState.error}</p>
  {/if}

  {#if questsState.loading}
    <p class="note">Reading the board…</p>
  {:else}
    <h3 class="section">Active</h3>
    {#if board.active.length === 0}
      <p class="empty">
        Nothing running. Loot writes a week's quests from your own history — one week with data in it is all it needs —
        and you can set your own with <strong>+ New quest</strong>.
      </p>
    {:else}
      <div class="grid">
        {#each board.active as quest (quest.id)}
          <QuestCard {quest} {code} flashing={questsState.flashing.includes(quest.id)} />
        {/each}
      </div>
    {/if}

    {#if board.recent.length > 0}
      <h3 class="section">Recent</h3>
      <div class="grid">
        {#each board.recent as quest (quest.id)}
          <QuestCard {quest} {code} flashing={questsState.flashing.includes(quest.id)} />
        {/each}
      </div>
    {/if}

    <h3 class="section">
      Mysteries
      {#if casebook.open.length > 0}<span class="count">{casebook.open.length}</span>{/if}
    </h3>
    <p class="lede">
      Days your numbers did something the days around them do not explain. They are optional puzzles: solving one means
      writing down what you think happened — which pays a drop, and leaves you a notebook of what actually moves your
      numbers.
    </p>

    {#if casebook.open.length === 0}
      <p class="empty">
        Nothing unexplained. Loot re-reads the last fortnight every hour and flags a day only when it breaks away from
        its own 28-day baseline.
      </p>
    {:else}
      <div class="grid wide-grid">
        {#each casebook.open as mystery (mystery.id)}
          <MysteryCard {mystery} {code} />
        {/each}
      </div>
    {/if}

    {#if casebook.resolved.length > 0}
      <h3 class="section">Notebook</h3>
      <ul class="notebook">
        {#each casebook.resolved as mystery (mystery.id)}
          <li class:dismissed={mystery.status === 'dismissed'}>
            <div class="nb-head">
              <span class="nb-title">{mystery.title}</span>
              <span class="nb-when">{mystery.resolved_at ? timeAgo(mystery.resolved_at) : dayLabel(mystery.day)}</span>
            </div>
            {#if mystery.note}
              <p class="nb-note">“{mystery.note}”</p>
            {:else}
              <p class="nb-note faint">dismissed without a note</p>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</section>

<style>
  .quests {
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
    flex-wrap: wrap;
  }

  h2 {
    margin: 0;
    font-size: 1.1rem;
  }

  .sub {
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .new {
    font-size: 0.8rem;
    border-color: color-mix(in oklab, var(--accent) 35%, transparent);
    background: color-mix(in oklab, var(--accent) 10%, #0d111a);
  }

  .form {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    gap: 0.6rem;
    padding: 0.85rem 0.9rem 0.9rem;
    margin-bottom: 1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: #0e121b;
  }

  .form label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-faint);
    min-width: 0;
  }

  .form label em {
    font-style: normal;
    text-transform: none;
    letter-spacing: 0;
    opacity: 0.7;
  }

  .form .wide {
    grid-column: 1 / -1;
  }

  .form input,
  .form select {
    font: inherit;
    font-size: 0.82rem;
    text-transform: none;
    letter-spacing: 0;
    color: var(--text);
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.32rem 0.5rem;
    min-width: 0;
  }

  .actions {
    grid-column: 1 / -1;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 0.6rem;
  }

  .save {
    font-size: 0.8rem;
    border-color: color-mix(in oklab, var(--uncommon) 45%, transparent);
    background: color-mix(in oklab, var(--uncommon) 12%, #0d111a);
  }

  .form-error {
    font-size: 0.75rem;
    color: var(--cursed);
  }

  .section {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    margin: 1.4rem 0 0.6rem;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-faint);
    font-weight: 600;
  }

  .count {
    font-size: 0.66rem;
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    color: var(--accent);
    border: 1px solid color-mix(in oklab, var(--accent) 40%, transparent);
  }

  .lede {
    margin: -0.2rem 0 0.7rem;
    font-size: 0.78rem;
    color: var(--text-dim);
    max-width: 68ch;
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 0.6rem;
  }

  .wide-grid {
    grid-template-columns: repeat(auto-fit, minmax(330px, 1fr));
  }

  .empty {
    padding: 0.9rem 1rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    background: #0e121b;
    color: var(--text-dim);
    font-size: 0.82rem;
    max-width: 72ch;
  }

  .notebook {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }

  .notebook li {
    padding: 0.55rem 0.75rem 0.6rem;
    border: 1px solid var(--border-soft);
    border-left: 2px solid color-mix(in oklab, var(--uncommon) 55%, transparent);
    border-radius: var(--radius-sm);
    background: var(--panel);
  }

  .notebook li.dismissed {
    border-left-color: var(--border);
  }

  .nb-head {
    display: flex;
    justify-content: space-between;
    gap: 0.6rem;
    font-size: 0.78rem;
  }

  .nb-when {
    color: var(--text-faint);
    font-size: 0.7rem;
    white-space: nowrap;
  }

  .nb-note {
    margin: 0.15rem 0 0;
    font-size: 0.8rem;
    color: var(--text-dim);
  }

  .nb-note.faint {
    color: var(--text-faint);
    font-style: italic;
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
