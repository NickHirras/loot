import { untrack } from 'svelte'
import { fetchFunnel } from './api'
import { scope } from './scope.svelte'
import type { FunnelReport, RoadRange } from './types'
import { ROAD_RANGES } from './types'

const RANGE_KEY = 'loot.road.range'
/**
 * How often the page re-reads the funnel while it is open. Ad platforms and
 * stores report a few times a day at most, so this is slower than the vault:
 * it is there so a page left open overnight is not yesterday's page.
 */
const REFRESH_MS = 5 * 60_000

function readRange(): RoadRange {
  try {
    const saved = Number(localStorage.getItem(RANGE_KEY))
    if ((ROAD_RANGES as readonly number[]).includes(saved)) return saved as RoadRange
  } catch {
    // Private browsing without storage: fall through to the default.
  }
  return 14
}

/**
 * The Road page's data: the acquisition funnel, from ad spend to the stores.
 *
 * It also answers one question for the header — whether there is a Road at
 * all. A Loot with no ads source never shows the tab, so `enabled` starts as
 * "unknown" (null) and the tab stays hidden until a report says otherwise;
 * hidden-until-known is what keeps it from flashing in and out on load.
 */
class RoadState {
  range = $state<RoadRange>(readRange())
  report = $state<FunnelReport | null>(null)
  /** null until the first answer; false also when the endpoint is missing. */
  enabled = $state<boolean | null>(null)
  loading = $state(false)
  error = $state('')

  #again = false
  /** The in-flight guard; not `$state`, for the reason vault.svelte.ts gives. */
  #loading = false
  #timer: ReturnType<typeof setInterval> | null = null
  #watching = false

  /**
   * Called once by the app shell: asks whether there is a funnel to show, and
   * asks again whenever the scope changes, since one app may run ads and
   * another not. The answer is the report itself, so whichever page opens
   * first has its data already.
   */
  watch(): () => void {
    return untrack(() => {
      if (this.#watching) return () => {}
      this.#watching = true
      void this.load()
      const unscope = scope.onChange(() => void this.load())
      return () => {
        this.#watching = false
        unscope()
      }
    })
  }

  /** Called by the page while it is mounted: loads now, then polls. */
  activate(): () => void {
    return untrack(() => {
      void this.load()
      this.#timer = setInterval(() => void this.load(), REFRESH_MS)
      return () => {
        if (this.#timer) clearInterval(this.#timer)
        this.#timer = null
      }
    })
  }

  setRange(range: RoadRange): void {
    if (range === this.range) return
    this.range = range
    try {
      localStorage.setItem(RANGE_KEY, String(range))
    } catch {
      // Not persisting the range is survivable.
    }
    void this.load()
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
      const report = await fetchFunnel(wanted)
      // A range change mid-flight must not overwrite the newer request.
      if (wanted !== this.range) return
      this.report = report
      this.enabled = report.enabled
      this.error = ''
    } catch (err) {
      // A server without the endpoint answers 404, which is "no Road" rather
      // than an error worth showing anyone who never asked for one.
      if (this.enabled === null) this.enabled = false
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

export const road = new RoadState()
