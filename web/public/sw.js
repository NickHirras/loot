/**
 * Loot's service worker: enough to make the dashboard installable and to give
 * an offline tab something honest to say. Deliberately not a data cache.
 *
 * Rules, in one breath:
 *   - /api, /ws and /hooks are never touched. Loot's whole point is live
 *     numbers; a stale revenue figure served from disk would be a bug that
 *     looks like a fact. Those requests fall through to the network untouched.
 *   - Navigations are network-first, so an online reload always gets the
 *     freshly built index.html. Offline, they fall back to the last good shell
 *     and then to /offline.html.
 *   - Only /assets/* is cache-first. Vite content-hashes those filenames, so a
 *     given URL's bytes never change; a new build simply asks for new URLs.
 *
 * BUILD is stamped at build time by the loot-stamp-sw plugin in vite.config.ts,
 * which makes every deploy a fresh set of caches and drops the old ones on
 * activate.
 */
const BUILD = '__LOOT_BUILD__'
const SHELL_CACHE = `loot-shell-${BUILD}`
const ASSET_CACHE = `loot-assets-${BUILD}`
const CURRENT = new Set([SHELL_CACHE, ASSET_CACHE])

const OFFLINE_URL = '/offline.html'
const SHELL_URL = '/'

/** Paths that belong to the server, not to the app. Never cached, never intercepted. */
const PASSTHROUGH = ['/api/', '/hooks/', '/ws']

function isPassthrough(url) {
  return PASSTHROUGH.some((prefix) => url.pathname === prefix || url.pathname.startsWith(prefix))
}

self.addEventListener('install', (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(SHELL_CACHE)
      // The offline page is the only thing worth precaching: it is tiny, it has
      // no dependencies, and it is the one document that must work when the
      // network does not.
      await cache.add(new Request(OFFLINE_URL, { cache: 'reload' }))
      await self.skipWaiting()
    })(),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      const names = await caches.keys()
      await Promise.all(
        names.filter((name) => name.startsWith('loot-') && !CURRENT.has(name)).map((name) => caches.delete(name)),
      )
      await self.clients.claim()
    })(),
  )
})

self.addEventListener('fetch', (event) => {
  const request = event.request

  if (request.method !== 'GET') return

  let url
  try {
    url = new URL(request.url)
  } catch {
    return
  }

  // Someone else's origin, the API, the websocket, the webhooks, or a range
  // request (media seeking): let the browser do exactly what it would have done
  // without a service worker installed.
  if (url.origin !== self.location.origin) return
  if (isPassthrough(url)) return
  if (request.headers.has('range')) return

  if (request.mode === 'navigate') {
    event.respondWith(handleNavigation(request))
    return
  }

  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(handleImmutableAsset(request))
  }
})

/**
 * Network-first. Online, this is a plain fetch plus a copy of the shell kept
 * for later, which is what keeps a reload from ever showing stale HTML.
 */
async function handleNavigation(request) {
  try {
    const response = await fetch(request)
    if (response && response.ok) {
      const copy = response.clone()
      const cache = await caches.open(SHELL_CACHE)
      // Store under a stable key so any route can be recovered offline; the
      // SPA reads the path from the URL bar once it boots.
      await cache.put(SHELL_URL, copy)
    }
    return response
  } catch {
    const cache = await caches.open(SHELL_CACHE)
    const shell = await cache.match(SHELL_URL)
    if (shell) return shell
    const offline = await cache.match(OFFLINE_URL)
    if (offline) return offline
    return new Response('Offline', {
      status: 503,
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    })
  }
}

/** Cache-first, safe only because Vite content-hashes these filenames. */
async function handleImmutableAsset(request) {
  const cache = await caches.open(ASSET_CACHE)
  const hit = await cache.match(request)
  if (hit) return hit

  const response = await fetch(request)
  if (response && response.ok && response.type === 'basic') {
    await cache.put(request, response.clone())
  }
  return response
}
