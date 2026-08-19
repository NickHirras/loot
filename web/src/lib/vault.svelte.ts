import { untrack } from 'svelte'
import { fetchVaultSummary } from './api'
import type { VaultRange, VaultSummary } from './types'
import { VAULT_RANGES } from './types'

const RANGE_KEY = 'loot.vault.range'
/** How often the vault re-reads the summary while its page is open. */
const REFRESH_MS = 60_000
/** A cascade of drops should cause one refresh, not one per drop. */
const STALE_DEBOUNCE_MS = 1_200

function readRange(): VaultRange {
  try {
    const saved = localStorage.getItem(RANGE_KEY)
    if (saved && (VAULT_RANGES as readonly string[]).includes(saved)) return saved as VaultRange
  } catch {
    // Private browsing without storage: fall through to the default.
  }
  return '30d'
}

/**
 * The vault page's data. Kept apart from the feed state so the money view can
 * refresh on its own clock without re-rendering the feed, and so importing it
 * never drags the websocket along.
 */
class VaultState {
  range = $state<VaultRange>(readRange())
  summary = $state<VaultSummary | null>(null)
  loading = $state(false)
  error = $state('')

  #active = false
  /** Set when a load was asked for while one was already in flight. */
  #again = false
  /**
   * The in-flight guard, deliberately *not* `$state`. `activate()` is called
   * straight out of an `$effect`, so anything reactive that `load()` reads on
   * its synchronous path becomes a dependency of that effect — and `loading`
   * is written when the fetch settles, which would re-run the effect, restart
   * the poller and fetch again, forever. `loading` stays reactive for the UI;
   * only this shadow decides whether a load is already running.
   */
  #loading = false
  #timer: ReturnType<typeof setInterval> | null = null
  #staleTimer: ReturnType<typeof setTimeout> | null = null

  /**
   * Called by the page while it is mounted: loads now, then polls. The whole
   * body is untracked so that calling this from an `$effect` never subscribes
   * that effect to anything touched during setup — notably `range`, which the
   * load reads — and the caller gets a plain teardown function and nothing
   * else.
   */
  activate(): () => void {
    return untrack(() => {
      this.#active = true
      void this.load()
      this.#timer = setInterval(() => void this.load(), REFRESH_MS)
      return () => {
        this.#active = false
        if (this.#timer) clearInterval(this.#timer)
        if (this.#staleTimer) clearTimeout(this.#staleTimer)
        this.#timer = this.#staleTimer = null
      }
    })
  }

  setRange(range: VaultRange): void {
    if (range === this.range) return
    this.range = range
    try {
      localStorage.setItem(RANGE_KEY, range)
    } catch {
      // Not persisting the range is survivable.
    }
    void this.load()
  }

  /**
   * Something happened that the summary does not know about — ledger money
   * landed, or a chest was opened. Refresh soon, but only while the page is
   * actually on screen; otherwise its own load-on-mount covers it.
   */
  markStale(): void {
    if (!this.#active) return
    if (this.#staleTimer) clearTimeout(this.#staleTimer)
    this.#staleTimer = setTimeout(() => void this.load(), STALE_DEBOUNCE_MS)
  }

  async load(): Promise<void> {
    if (this.#loading) {
      this.#again = true
      return
    }
    this.#loading = true
    this.loading = true
    const wanted = untrack(() => this.range)
    try {
      const summary = await fetchVaultSummary(wanted)
      // A range change mid-flight must not overwrite the newer request.
      if (wanted !== this.range) return
      this.summary = summary
      this.error = ''
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      this.loading = false
      this.#loading = false
      if (this.#again) {
        this.#again = false
        void this.load()
      }
    }
  }
}

export const vault = new VaultState()
