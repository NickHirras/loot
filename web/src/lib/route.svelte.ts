/**
 * The whole router. Loot has five pages, so a hash and a `$state` are enough —
 * no dependency, no history juggling, and it survives being served from any
 * sub-path because the hash never reaches the server.
 */
export const TABS = [
  { id: 'feed', label: 'Feed', hash: '#/' },
  { id: 'vault', label: 'Vault', hash: '#/vault' },
  { id: 'hearth', label: 'Hearth', hash: '#/hearth' },
  { id: 'quests', label: 'Quests', hash: '#/quests' },
  { id: 'codex', label: 'Codex', hash: '#/codex' },
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

  const tab: Tab =
    path === 'vault'
      ? 'vault'
      : path === 'hearth'
        ? 'hearth'
        : path === 'quests'
          ? 'quests'
          : path === 'codex'
            ? 'codex'
            : 'feed'
  const ambient = tab === 'hearth' && new URLSearchParams(query ?? '').get('ambient') === '1'
  return { tab, ambient }
}

const AMBIENT_HASH = '#/hearth?ambient=1'

/** The `app` query parameter of a hash, or "". */
export function appOf(hash: string): string {
  const [, query] = hash.replace(/^#\/?/, '').split('?')
  return new URLSearchParams(query ?? '').get('app') ?? ''
}

/**
 * Rewrites a hash so it carries the given app scope, keeping its path and any
 * other parameters (ambient mode) intact.
 *
 * The scope lives in the hash rather than only in localStorage so a link is
 * shareable: "look at the Vault for Nistis" is a URL, not an instruction to
 * click something after opening one.
 */
export function withApp(hash: string, app: string): string {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash
  const [path, query] = raw.split('?')
  const params = new URLSearchParams(query ?? '')
  if (app) params.set('app', app)
  else params.delete('app')
  const rendered = params.toString()
  return `#${path || '/'}${rendered ? `?${rendered}` : ''}`
}

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

  /** Changes tab, carrying the app scope with it so it survives navigation. */
  go(tab: Tab): void {
    const hash = TABS.find((t) => t.id === tab)?.hash ?? '#/'
    location.hash = withApp(hash, appOf(location.hash))
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
