import { untrack } from 'svelte'
import { setScope } from './api'
import { appOf, withApp } from './route.svelte'

const SCOPE_KEY = 'loot.scope'

/**
 * The app scope: which of your apps the whole dashboard is currently about.
 *
 * One product at a time, or all of them. Every store reads it through the API
 * layer (which appends `?app=`), so this module owns exactly two things: what
 * the scope *is*, and who to tell when it changes.
 *
 * It lives in three places at once, on purpose:
 *
 *   - the URL hash, so a scoped view is a link somebody can send;
 *   - localStorage, so the app you were looking at yesterday is the one you
 *     open on tomorrow;
 *   - here, so the UI can react.
 *
 * The hash wins on load. A link is an explicit request and must beat a
 * remembered preference, or following one would silently show you something
 * else.
 */

function readStored(): string {
  try {
    return localStorage.getItem(SCOPE_KEY) ?? ''
  } catch {
    return ''
  }
}

function readInitial(): string {
  if (typeof location === 'undefined') return ''
  return appOf(location.hash) || readStored()
}

/** Everything that has to be re-read when the scope changes. */
type Reloader = () => void

class ScopeState {
  /** The product everything is scoped to, or "" for all apps. */
  current = $state(readInitial())
  /** Every product this Loot knows about; the stats poll keeps it fresh. */
  products = $state<string[]>([])
  /**
   * How many drops have landed for *other* apps since the scope was last
   * changed.
   *
   * Scoping means focus: an out-of-scope drop does not render and does not
   * play. But it did happen, and a dashboard that silently swallowed it would
   * be lying by omission — so it becomes a small "+3 elsewhere" pill on the
   * selector, which is enough to know you missed something and where to look.
   * It clears the moment the scope changes.
   */
  elsewhere = $state(0)
  /** True while the selector's dropdown is open. */
  open = $state(false)

  #reloaders = new Set<Reloader>()

  constructor() {
    // The API layer is the one that actually has to know, and it is a plain
    // module rather than reactive state, so it is pushed rather than pulled.
    setScope(untrack(() => this.current))
  }

  /** True when the dashboard is showing one app rather than all of them. */
  get active(): boolean {
    return this.current !== ''
  }

  /** What the selector's button reads. */
  get label(): string {
    return this.current || 'All apps'
  }

  /**
   * Whether a drop belongs in the current scope. Realm-wide drops — an
   * achievement, a global quest — carry no product and belong everywhere:
   * they are about the whole hoard, not about one app.
   */
  includes(product: string | undefined): boolean {
    if (!this.current) return true
    return !product || product === this.current
  }

  /** Counts a drop that landed for another app. */
  noteElsewhere(): void {
    this.elsewhere += 1
  }

  /**
   * Registers something to re-read when the scope changes. Returns the
   * unregister function, so a store can hand it straight back from a teardown.
   */
  onChange(reload: Reloader): () => void {
    this.#reloaders.add(reload)
    return () => this.#reloaders.delete(reload)
  }

  /** Renders a tab's hash with the current scope on it, so links keep it. */
  link(hash: string): string {
    return withApp(hash, this.current)
  }

  /** Switches scope: persists it, puts it in the URL, and refetches the lot. */
  set(product: string): void {
    const next = product
    this.open = false
    if (next === this.current) {
      // Picking the scope you are already in is not a change; it is a way of
      // dismissing the "+N elsewhere" pill, and nothing needs refetching.
      this.elsewhere = 0
      return
    }
    this.current = next
    this.elsewhere = 0

    setScope(next)
    try {
      if (next) localStorage.setItem(SCOPE_KEY, next)
      else localStorage.removeItem(SCOPE_KEY)
    } catch {
      // Private browsing without storage: the scope just will not persist.
    }
    if (typeof location !== 'undefined') {
      const wanted = withApp(location.hash || '#/', next)
      if (location.hash !== wanted) location.hash = wanted
    }
    this.#reload()
  }

  /**
   * Adopts a scope that arrived from the URL (a shared link, the back
   * button). Same as `set` minus the URL write, which has already happened.
   */
  syncFromHash(): void {
    if (typeof location === 'undefined') return
    const fromHash = appOf(location.hash)
    if (fromHash === this.current) return
    this.current = fromHash
    this.elsewhere = 0
    setScope(fromHash)
    try {
      if (fromHash) localStorage.setItem(SCOPE_KEY, fromHash)
      else localStorage.removeItem(SCOPE_KEY)
    } catch {
      // As above.
    }
    this.#reload()
  }

  /** Keeps the selector's options current from the stats poll. */
  setProducts(products: string[] | undefined): void {
    if (!products) return
    const next = products.filter((p) => p)
    if (next.length === this.products.length && next.every((p, i) => p === this.products[i])) return
    this.products = next
  }

  #reload(): void {
    // untrack: `set` is called from a click handler, but `syncFromHash` runs
    // inside the router's effect, and a reloader that writes reactive state
    // would otherwise make that effect depend on its own output — the loop the
    // stores' own `activate()` comments warn about.
    untrack(() => {
      for (const reload of this.#reloaders) {
        try {
          reload()
        } catch {
          // One page failing to refresh must not stop the others.
        }
      }
    })
  }
}

export const scope = new ScopeState()
