import { untrack } from 'svelte'
import { fetchCodex, fetchRecap } from './api'
import type { CodexBoard, Recap, RecapPeriod } from './types'

/** How often the page re-reads the wall while it is open. */
const REFRESH_MS = 60_000
/** A cascade of unlocks should cause one refresh, not one per trophy. */
const STALE_DEBOUNCE_MS = 1_200

/** Which trophies the wall is showing. */
export type CodexFilter = 'all' | 'unlocked' | 'locked'

/**
 * The Codex page's data: the trophy wall with its records and totals, and one
 * season recap at a time.
 *
 * Kept apart from the feed state for the same reason the vault and the quest
 * board are: the page refreshes on its own clock, and importing it never drags
 * the websocket along.
 */
class CodexState {
  board = $state<CodexBoard | null>(null)
  loading = $state(true)
  error = $state('')

  /** The filter chips: all, unlocked, or still to win. */
  filter = $state<CodexFilter>('all')

  /** The recap being shown, and the months the picker offers. */
  recap = $state<Recap | null>(null)
  periods = $state<RecapPeriod[]>([])
  /** "" means "the last complete month", which is what the server defaults to. */
  period = $state('')
  recapLoading = $state(false)
  recapError = $state('')

  #active = false
  #timer: ReturnType<typeof setInterval> | null = null
  #staleTimer: ReturnType<typeof setTimeout> | null = null
  /**
   * The in-flight guards, deliberately *not* `$state`. `activate()` is called
   * straight out of an `$effect`, so anything reactive the load reads on its
   * synchronous path would become a dependency of that effect — and `loading`
   * is written when the fetch settles, which would restart the poller and
   * fetch again, forever.
   */
  #loading = false
  #again = false
  #loadingRecap = false

  /** How many trophies are won, and out of how many. */
  get unlocked(): number {
    return this.board?.unlocked ?? 0
  }

  get total(): number {
    return this.board?.total ?? 0
  }

  /**
   * Called by the page while it is mounted: loads now, then polls. The whole
   * body is untracked so calling it from an `$effect` can never subscribe that
   * effect to state the load itself writes.
   */
  activate(): () => void {
    return untrack(() => {
      this.#active = true
      void this.load()
      void this.loadRecap()
      this.#timer = setInterval(() => void this.load(), REFRESH_MS)
      return () => {
        this.#active = false
        if (this.#timer) clearInterval(this.#timer)
        if (this.#staleTimer) clearTimeout(this.#staleTimer)
        this.#timer = this.#staleTimer = null
      }
    })
  }

  /**
   * Something happened the wall does not know about — an achievement unlocked,
   * money landed. Refresh soon, but only while the page is on screen;
   * otherwise its own load-on-mount covers it.
   */
  markStale(): void {
    if (!this.#active) return
    if (this.#staleTimer) clearTimeout(this.#staleTimer)
    this.#staleTimer = setTimeout(() => {
      void this.load()
      void this.loadRecap()
    }, STALE_DEBOUNCE_MS)
  }

  async load(): Promise<void> {
    if (this.#loading) {
      this.#again = true
      return
    }
    this.#loading = true
    try {
      this.board = await fetchCodex()
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

  setFilter(filter: CodexFilter): void {
    this.filter = filter
  }

  /** Shows a different month (or year). "" is the last complete month. */
  setPeriod(period: string): void {
    if (period === this.period) return
    this.period = period
    void this.loadRecap()
  }

  async loadRecap(): Promise<void> {
    if (this.#loadingRecap) return
    this.#loadingRecap = true
    this.recapLoading = true
    const wanted = untrack(() => this.period)
    try {
      const { recap, periods } = await fetchRecap(wanted)
      // A period change mid-flight must not overwrite the newer request.
      if (wanted !== this.period) return
      this.recap = recap
      this.periods = periods
      // The server decides what "" means; adopt its answer so the picker can
      // highlight the month it actually chose.
      if (!this.period) this.period = recap.period.key
      this.recapError = ''
    } catch (err) {
      this.recapError = err instanceof Error ? err.message : String(err)
    } finally {
      this.recapLoading = false
      this.#loadingRecap = false
    }
  }
}

export const codexState = new CodexState()
