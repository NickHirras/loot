<script lang="ts">
  import { fireFakeDrop } from './api'
  import type { FakeDropRequest } from './api'
  import { RARITIES } from './types'

  let open = $state(false)
  let busy = $state('')
  let error = $state('')
  /** Optional country code, so the "new settlement" floor rule is testable. */
  let country = $state('')

  /** The rarities a fake chest is filled with: dull first, so it builds. */
  const CHEST_MIX = ['common', 'uncommon', 'rare', 'cursed', 'legendary'] as const

  async function run(tag: string, requests: FakeDropRequest[]) {
    busy = tag
    error = ''
    try {
      for (const req of requests) {
        await fireFakeDrop({ country: country.trim().toUpperCase(), ...req })
      }
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      busy = ''
    }
  }

  const fire = (rarity: string) => run(rarity, [{ rarity }])

  /** Five chest-bound drops on today's chest: the reveal cascade, on demand. */
  const fakeChest = () => run('chest', CHEST_MIX.map((rarity) => ({ rarity, chest: true })))

  /**
   * A ledger day the way a store reports one: a silent revenue row plus a
   * chest-bound `sales_day` summary. This is what fills the vault.
   */
  const fakeSalesDay = () =>
    run('sales_day', [
      { kind: 'sales_day', amount: 250, currency: 'EUR', country: 'DE', quantity: 62, chest: true },
    ])

  function randomCountry() {
    const pool = ['JP', 'BR', 'DE', 'NG', 'IN', 'CA', 'AU', 'FR', 'MX', 'ZA', 'SE', 'KR']
    country = pool[Math.floor(Math.random() * pool.length)]
  }
</script>

<aside class="dev" class:open>
  <button class="toggle" onclick={() => (open = !open)} aria-expanded={open}>
    <span class="wrench" aria-hidden="true">⚙</span> dev
  </button>

  {#if open}
    <div class="panel">
      <p class="hint">Fire a synthetic drop through the real pipeline.</p>

      <div class="grid">
        {#each RARITIES as rarity (rarity)}
          <button class="fire rarity-{rarity}" onclick={() => fire(rarity)} disabled={busy === rarity}>
            {rarity}
          </button>
        {/each}
      </div>

      <div class="scenarios">
        <button class="scenario" onclick={fakeChest} disabled={busy === 'chest'}>
          📦 Fake chest (5 drops)
        </button>
        <button class="scenario" onclick={fakeSalesDay} disabled={busy === 'sales_day'}>
          💶 Fake sales day (EUR 250)
        </button>
      </div>

      <div class="country">
        <input
          bind:value={country}
          placeholder="country (optional)"
          maxlength="2"
          aria-label="Country code for the fake drop"
        />
        <button onclick={randomCountry} title="Pick a random country">🎲</button>
        <button onclick={() => (country = '')} title="Clear the country">✕</button>
      </div>

      {#if error}
        <p class="error">{error}</p>
      {/if}
    </div>
  {/if}
</aside>

<style>
  .dev {
    position: fixed;
    right: 1rem;
    bottom: 1rem;
    bottom: max(1rem, env(safe-area-inset-bottom, 0px));
    left: auto;
    z-index: 20;
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 0.5rem;
  }

  /* A 260px panel plus a 1rem margin does not fit a 375px phone with anything
     to spare, so on a narrow screen it spans what is left of the width. */
  @media (max-width: 420px) {
    .dev {
      left: 1rem;
    }

    .panel {
      width: 100%;
    }
  }

  .toggle {
    font-size: 0.75rem;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--text-dim);
    background: color-mix(in oklab, var(--panel) 92%, transparent);
    backdrop-filter: blur(8px);
  }

  .wrench {
    color: var(--accent);
  }

  .panel {
    order: -1;
    width: 260px;
    padding: 0.75rem;
    background: color-mix(in oklab, var(--panel) 96%, transparent);
    backdrop-filter: blur(14px);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: 0 18px 40px rgb(0 0 0 / 0.45);
  }

  .hint {
    margin: 0 0 0.6rem;
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.35rem;
  }

  .fire {
    font-size: 0.75rem;
    text-transform: capitalize;
    color: var(--r);
    border-color: color-mix(in oklab, var(--r) 35%, transparent);
    background: color-mix(in oklab, var(--r) 10%, #0d111a);
  }

  .fire:hover {
    background: color-mix(in oklab, var(--r) 20%, #0d111a);
    border-color: var(--r);
  }

  .fire:disabled {
    opacity: 0.5;
    cursor: wait;
  }

  .scenarios {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    margin-top: 0.45rem;
  }

  .scenario {
    font-size: 0.75rem;
    text-align: left;
    color: var(--text-dim);
    border-color: color-mix(in oklab, var(--legendary) 28%, transparent);
    background: color-mix(in oklab, var(--legendary) 8%, #0d111a);
  }

  .scenario:hover {
    color: var(--text);
    border-color: color-mix(in oklab, var(--legendary) 55%, transparent);
  }

  .scenario:disabled {
    opacity: 0.5;
    cursor: wait;
  }

  .country {
    display: flex;
    gap: 0.3rem;
    margin-top: 0.5rem;
  }

  input {
    flex: 1;
    min-width: 0;
    font: inherit;
    font-size: 0.75rem;
    text-transform: uppercase;
    color: var(--text);
    background: #0d111a;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius-sm);
    padding: 0.3rem 0.5rem;
  }

  input:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }

  .error {
    margin: 0.5rem 0 0;
    font-size: 0.72rem;
    color: var(--cursed);
  }
</style>
