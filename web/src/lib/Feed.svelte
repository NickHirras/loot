<script lang="ts">
  import DropCard from './DropCard.svelte'
  import { loot } from './state.svelte'

  // Re-render relative timestamps once a minute without touching the data.
  let tick = $state(0)
  $effect(() => {
    const id = setInterval(() => (tick += 1), 60_000)
    return () => clearInterval(id)
  })

  let sentinel: HTMLDivElement | null = $state(null)

  // Infinite scroll: load the next page when the bottom marker comes into view.
  $effect(() => {
    if (!sentinel) return
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) void loot.loadMore()
      },
      { rootMargin: '300px' },
    )
    observer.observe(sentinel)
    return () => observer.disconnect()
  })
</script>

<section class="feed" aria-label="Drop feed">
  {#if loot.loading}
    <p class="note">Opening the vault…</p>
  {:else if loot.error && loot.drops.length === 0}
    <p class="note error">{loot.error}</p>
  {:else if loot.drops.length === 0}
    <div class="empty">
      <p class="big">No loot yet.</p>
      <p>
        Point a RevenueCat webhook at <code>/hooks/revenuecat</code>, add Flathub apps to your config, or run with
        <code>dev.enabled</code> and fire a test drop.
      </p>
    </div>
  {:else}
    {#key tick}
      <ul class="list">
        {#each loot.drops as drop, i (drop.id)}
          <li>
            <DropCard {drop} index={i} />
          </li>
        {/each}
      </ul>
    {/key}

    <div class="sentinel" bind:this={sentinel}></div>

    {#if loot.loadingMore}
      <p class="note">Digging deeper…</p>
    {:else if !loot.nextBefore && loot.drops.length > 20}
      <p class="note">That is the whole hoard.</p>
    {/if}
  {/if}
</section>

<style>
  .feed {
    padding: 1rem 1.1rem 4rem;
  }

  .list {
    list-style: none;
    margin: 0 auto;
    padding: 0;
    max-width: 780px;
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
  }

  .note {
    max-width: 780px;
    margin: 1.5rem auto;
    text-align: center;
    color: var(--text-faint);
    font-size: 0.85rem;
  }

  .note.error {
    color: var(--cursed);
  }

  .empty {
    max-width: 520px;
    margin: 4rem auto;
    text-align: center;
    color: var(--text-dim);
  }

  .empty .big {
    font-size: 1.3rem;
    color: var(--text);
    margin-bottom: 0.4rem;
  }

  code {
    font-family: var(--mono);
    font-size: 0.85em;
    background: var(--panel-2);
    border: 1px solid var(--border-soft);
    border-radius: 5px;
    padding: 0.05rem 0.35rem;
    color: var(--accent);
  }

  .sentinel {
    height: 1px;
  }
</style>
