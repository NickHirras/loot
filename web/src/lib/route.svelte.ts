/**
 * The whole router. Loot has two pages, so a hash and a `$state` are enough —
 * no dependency, no history juggling, and it survives being served from any
 * sub-path because the hash never reaches the server.
 */
export const TABS = [
  { id: 'feed', label: 'Feed', hash: '#/' },
  { id: 'vault', label: 'Vault', hash: '#/vault' },
] as const

export type Tab = (typeof TABS)[number]['id']

function parse(hash: string): Tab {
  return hash.replace(/^#\/?/, '').split('?')[0] === 'vault' ? 'vault' : 'feed'
}

class Router {
  tab = $state<Tab>(parse(typeof location === 'undefined' ? '' : location.hash))

  /** Starts listening for hash changes; returns the matching teardown. */
  start(): () => void {
    const sync = () => (this.tab = parse(location.hash))
    sync()
    addEventListener('hashchange', sync)
    return () => removeEventListener('hashchange', sync)
  }

  go(tab: Tab): void {
    location.hash = tab === 'vault' ? '#/vault' : '#/'
    this.tab = tab
  }
}

export const router = new Router()

/** True when the user asked the OS to keep animation to a minimum. */
export function prefersReducedMotion(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
}
