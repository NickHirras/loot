/**
 * The whole router. Loot has three pages, so a hash and a `$state` are enough —
 * no dependency, no history juggling, and it survives being served from any
 * sub-path because the hash never reaches the server.
 */
export const TABS = [
  { id: 'feed', label: 'Feed', hash: '#/' },
  { id: 'vault', label: 'Vault', hash: '#/vault' },
  { id: 'hearth', label: 'Hearth', hash: '#/hearth' },
] as const

export type Tab = (typeof TABS)[number]['id']

/** Where a hash points: which tab, and whether the chrome is hidden. */
interface Route {
  tab: Tab
  ambient: boolean
}

/**
 * Ambient mode is the Hearth with everything else taken away — for a spare
 * monitor or an iPad on the shelf. `#/ambient` is the short spelling of
 * `#/hearth?ambient=1`; both work, and both leave by dropping the flag.
 */
function parse(hash: string): Route {
  const [path, query] = hash.replace(/^#\/?/, '').split('?')
  if (path === 'ambient') return { tab: 'hearth', ambient: true }

  const tab: Tab = path === 'vault' ? 'vault' : path === 'hearth' ? 'hearth' : 'feed'
  const ambient = tab === 'hearth' && new URLSearchParams(query ?? '').get('ambient') === '1'
  return { tab, ambient }
}

const AMBIENT_HASH = '#/hearth?ambient=1'

class Router {
  #initial = parse(typeof location === 'undefined' ? '' : location.hash)

  tab = $state<Tab>(this.#initial.tab)
  ambient = $state(this.#initial.ambient)

  /** Starts listening for hash changes; returns the matching teardown. */
  start(): () => void {
    const sync = () => {
      const route = parse(location.hash)
      this.tab = route.tab
      this.ambient = route.ambient
    }
    sync()
    addEventListener('hashchange', sync)
    return () => removeEventListener('hashchange', sync)
  }

  go(tab: Tab): void {
    location.hash = TABS.find((t) => t.id === tab)?.hash ?? '#/'
    this.tab = tab
    this.ambient = false
  }

  enterAmbient(): void {
    location.hash = AMBIENT_HASH
    this.tab = 'hearth'
    this.ambient = true
  }

  /** Esc, or the button: back to the Hearth with its chrome on. */
  exitAmbient(): void {
    if (!this.ambient) return
    this.go('hearth')
  }
}

export const router = new Router()

/** True when the user asked the OS to keep animation to a minimum. */
export function prefersReducedMotion(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
}
