import { untrack } from 'svelte'
import { fetchReviews } from './api'
import { scope } from './scope.svelte'
import type { ReviewsRange, ReviewsSummary, ReviewStarFilter } from './types'
import { REVIEWS_RANGES } from './types'

const RANGE_KEY = 'loot.reviews.range'
const REFRESH_MS = 120_000

function readRange(): ReviewsRange {
  try {
    const saved = localStorage.getItem(RANGE_KEY)
    if (saved && (REVIEWS_RANGES as readonly string[]).includes(saved)) return saved as ReviewsRange
  } catch {
    // ignore
  }
  return '90d'
}

class ReviewsState {
  range = $state<ReviewsRange>(readRange())
  source = $state('')
  stars = $state<ReviewStarFilter>('all')
  data = $state<ReviewsSummary | null>(null)
  loading = $state(false)
  error = $state('')

  #seq = 0
  #timer: ReturnType<typeof setInterval> | null = null

  activate(): () => void {
    return untrack(() => {
      void this.load()
      this.#timer = setInterval(() => void this.load(), REFRESH_MS)
      const unscope = scope.onChange(() => void this.load())
      return () => {
        if (this.#timer) clearInterval(this.#timer)
        this.#timer = null
        unscope()
      }
    })
  }

  setRange(next: ReviewsRange): void {
    if (next === this.range) return
    this.range = next
    try {
      localStorage.setItem(RANGE_KEY, next)
    } catch {
      // ignore
    }
    void this.load()
  }

  setSource(next: string): void {
    if (next === this.source) return
    this.source = next
    void this.load()
  }

  setStars(next: ReviewStarFilter): void {
    if (next === this.stars) return
    this.stars = next
    void this.load()
  }

  async load(): Promise<void> {
    const seq = ++this.#seq
    this.loading = true
    const range = untrack(() => this.range)
    const source = untrack(() => this.source)
    const stars = untrack(() => this.stars)
    try {
      const res = await fetchReviews(range, source, stars)
      if (seq !== this.#seq) return
      this.data = res
      this.error = ''
    } catch (err) {
      if (seq !== this.#seq) return
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      if (seq === this.#seq) this.loading = false
    }
  }
}

export const reviews = new ReviewsState()
