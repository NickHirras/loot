<script lang="ts">
  import { fetchApps } from './api'
  import { scope } from './scope.svelte'
  import type { AppProduct } from './types'

  /**
   * The app scope selector: "All apps ▾", and a list of everything this Loot
   * could show you instead.
   *
   * It is the smallest control in the header and the one that changes the most
   * — every other panel is an answer to a question this button asks. So: no
   * icon, no colour of its own until a scope is on, and a width that does not
   * move the tabs when the name changes.
   */

  let products = $state<AppProduct[]>([])
  let loaded = $state(false)
  let el = $state<HTMLDivElement | null>(null)

  /**
   * The list is fetched when the menu is first opened rather than on mount.
   * The stats poll already carries the names (see `scope.products`), which is
   * all the button needs; the richer answer — which products are configured,
   * how many events each has — is only worth a request once somebody is
   * actually choosing.
   */
  async function load(): Promise<void> {
    try {
      const apps = await fetchApps()
      products = apps.products
      loaded = true
    } catch {
      // The fallback below still lists every name the stats poll knows, which
      // is enough to change scope with.
    }
  }

  function toggle(): void {
    scope.open = !scope.open
    if (scope.open && !loaded) void load()
  }

  // Names from the stats poll, used until (or instead of) the richer fetch.
  const fallback = $derived(
    scope.products.map((name) => ({ name, configured: true, events: 0 }) as unknown as AppProduct),
  )
  const all = $derived(loaded && products.length ? products : fallback)
  const mapped = $derived(all.filter((p) => p.configured))
  const unmapped = $derived(all.filter((p) => !p.configured))
</script>

<svelte:window
  onclick={(e) => {
    // Click-outside closes the menu. The button's own handler runs first and
    // stops propagation, so this never fights the toggle.
    if (scope.open && el && e.target instanceof Node && !el.contains(e.target)) scope.open = false
  }}
  onkeydown={(e) => {
    if (e.key === 'Escape' && scope.open) scope.open = false
  }}
/>

<div class="scope" bind:this={el}>
  <button
    class="trigger"
    class:scoped={scope.active}
    onclick={(e) => {
      e.stopPropagation()
      toggle()
    }}
    aria-haspopup="listbox"
    aria-expanded={scope.open}
    title={scope.active ? `Showing ${scope.current} only — click to change` : 'Showing every app'}
  >
    <span class="name">{scope.label}</span>
    <span class="caret" aria-hidden="true">▾</span>
    {#if scope.elsewhere > 0}
      <!-- Something landed for another app while you were looking at this
           one. It did not render and did not play a sound; this is the only
           trace it leaves, and picking a scope clears it. -->
      <span class="elsewhere" title="{scope.elsewhere} drop(s) landed for other apps">
        +{scope.elsewhere} elsewhere
      </span>
    {/if}
  </button>

  {#if scope.open}
    <ul class="menu" role="listbox" aria-label="App scope">
      <li>
        <button class="option" class:current={!scope.active} onclick={() => scope.set('')} role="option"
          aria-selected={!scope.active}>
          <span class="opt-name">All apps</span>
          <span class="opt-meta">everything you ship</span>
        </button>
      </li>
      {#each mapped as product (product.name)}
        <li>
          <button
            class="option"
            class:current={scope.current === product.name}
            onclick={() => scope.set(product.name)}
            role="option"
            aria-selected={scope.current === product.name}
          >
            <span class="opt-name">{product.name}</span>
            {#if product.events}
              <span class="opt-meta">{product.events.toLocaleString()} events</span>
            {/if}
          </button>
        </li>
      {/each}
      {#if unmapped.length}
        <!-- Apps a source reported that no `apps:` entry claims. They are
             still selectable: the mapping is a convenience, not a gate. -->
        <li class="divider"><span>unmapped</span></li>
        {#each unmapped as product (product.name)}
          <li>
            <button
              class="option"
              class:current={scope.current === product.name}
              onclick={() => scope.set(product.name)}
              role="option"
              aria-selected={scope.current === product.name}
            >
              <span class="opt-name mono">{product.name}</span>
              {#if product.events}
                <span class="opt-meta">{product.events.toLocaleString()} events</span>
              {/if}
            </button>
          </li>
        {/each}
      {/if}
    </ul>
  {/if}
</div>

<style>
  .scope {
    position: relative;
  }

  .trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.22rem 0.5rem;
    font-size: 0.76rem;
    color: var(--text-dim);
    max-width: 15rem;
  }

  .trigger.scoped {
    color: var(--text);
    border-color: color-mix(in oklab, var(--accent) 40%, transparent);
    background: color-mix(in oklab, var(--accent) 12%, #0d111a);
  }

  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .caret {
    font-size: 0.6rem;
    opacity: 0.7;
  }

  .elsewhere {
    font-size: 0.6rem;
    font-weight: 700;
    line-height: 1;
    padding: 0.15rem 0.32rem;
    border-radius: 999px;
    color: var(--legendary);
    background: color-mix(in oklab, var(--legendary) 16%, #0d111a);
    border: 1px solid color-mix(in oklab, var(--legendary) 40%, transparent);
  }

  .menu {
    position: absolute;
    top: calc(100% + 0.35rem);
    left: 0;
    z-index: 20;
    min-width: 13rem;
    /* Never wider than the window, whichever edge it is anchored to. */
    max-width: calc(100vw - 1.2rem);
    max-height: 60vh;
    overflow-y: auto;
    list-style: none;
    margin: 0;
    padding: 0.25rem;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    background: var(--panel);
    box-shadow: 0 12px 30px rgb(0 0 0 / 45%);
  }

  .option {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.6rem;
    width: 100%;
    text-align: left;
    border: 0;
    border-radius: 6px;
    background: transparent;
    padding: 0.3rem 0.45rem;
    font-size: 0.78rem;
    color: var(--text-dim);
  }

  .option:hover {
    background: var(--panel-2);
    color: var(--text);
  }

  .option.current {
    color: var(--text);
    background: color-mix(in oklab, var(--accent) 16%, transparent);
  }

  .opt-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .opt-meta {
    font-size: 0.66rem;
    color: var(--text-faint);
    white-space: nowrap;
  }

  /*
   * On a narrow screen the selector lives at the right end of the header row,
   * so a menu hung from its left edge runs off the side of the page — and
   * took the whole document's horizontal scroll with it. Hang it from the
   * right edge instead, where there is room.
   */
  @media (max-width: 900px) {
    .menu {
      left: auto;
      right: 0;
      /* Hung from the right edge of a button that is itself near the right of
         a 360px screen, the menu has only so much room before it runs off the
         *other* side. The names ellipsize; the panel stays on the page. */
      min-width: 0;
      max-width: min(13rem, calc(100vw - 1.6rem));
    }
  }

  .divider {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0.35rem 0.2rem 0.2rem;
    font-size: 0.6rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-faint);
  }

  .divider::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--border-soft);
  }
</style>
