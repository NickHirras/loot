/**
 * Re-shoots every screenshot in this directory against a local demo server.
 *
 * The demo world is generated from a fixed seed, so this is reproducible: the
 * same seed gives the same history, the same chest and the same globe. What is
 * *not* fixed is the wall clock, so the waits below are tuned rather than
 * arbitrary — see the comments at each one.
 *
 *   make build
 *   ./bin/loot serve --demo --demo-reset --listen :8082   # in another terminal
 *
 *   mkdir -p /tmp/loot-shots && cd /tmp/loot-shots        # outside the repo,
 *   npm init -y && npm i -D playwright                    # so no lockfile here
 *   npx playwright install chromium                       # churns
 *   node /path/to/loot/docs/screenshots/capture.mjs
 *
 * Then squeeze the PNGs (both are `brew install`able, both are optional):
 *
 *   pngquant --quality 85-100 --speed 1 --force --ext .png ./*.png
 *   oxipng -o 4 --strip safe ./*.png
 *
 * --demo-reset matters: the chest shots need yesterday's chest still shut, and
 * opening it is a one-way door.
 */
import { chromium } from 'playwright'
import { fileURLToPath } from 'node:url'
import { dirname } from 'node:path'

const BASE = process.env.BASE ?? 'http://localhost:8082'
const OUT = process.env.OUT ?? dirname(fileURLToPath(import.meta.url))

// The "click to enable drop sounds" banner is a browser autoplay artefact,
// not part of the product, so it never belongs in a screenshot.
const HIDE_CHROME = `.unlock { display: none !important; }`
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const browser = await chromium.launch()
const ctx = await browser.newContext({
  viewport: { width: 1400, height: 900 },
  deviceScaleFactor: 1,
  colorScheme: 'dark',
})
// The vault remembers its range; seed it before any page script runs.
await ctx.addInitScript(() => localStorage.setItem('loot.vault.range', '90d'))
const page = await ctx.newPage()

async function go(hash, settle = 2500) {
  // A hash-only navigation would not restart the page, and the globe's
  // rotation clock has to start from zero for the waits below to mean
  // anything. So every page here is a real document load.
  await page.goto('about:blank')
  await page.goto(BASE + '/' + hash, { waitUntil: 'load' })
  await page.addStyleTag({ content: HIDE_CHROME })
  await sleep(settle)
}
async function shot(name) {
  await page.screenshot({ path: `${OUT}/${name}.png` })
  console.log('  wrote', name + '.png')
}

// -- feed, first, while the chest badge is still in the header.
await go('#/', 4000)
console.log('chest badge present:', await page.locator('.chest-badge').count())
await shot('feed')

// -- the cascade. The lid takes about 1.4 s, then one drop every 600 ms,
// ordered cursed -> legendary, so five and a half seconds in is past the
// commons and onto something worth photographing.
await page.locator('.chest-badge').click()
await sleep(900)
await page.locator('.scrim button.primary').click()
await sleep(5600)
console.log('cascade at:', await page.locator('.scrim .counter').textContent().catch(() => '?'))
await shot('chest')
await sleep(9000)
await shot('chest-haul')
await page.keyboard.press('Escape')
await page.locator('.scrim .close').click().catch(() => {})
await sleep(600)

// -- vault, ninety days
await go('#/vault', 2500)
const range = page.locator('button.range-btn', { hasText: '90d' })
if (await range.count()) await range.first().click()
await sleep(2500)
await shot('vault')

// -- the globe turns once every 90 s. The Hearth starts pointed at the
// capital (the US, in the demo world), so it takes most of a revolution to
// bring the European cluster round; ambient starts over the Atlantic already.
await go('#/hearth', 72000)
await shot('hearth')
await go('#/ambient', 11000)
await shot('ambient')

// -- quests
await go('#/quests', 3000)
await shot('quests')

await browser.close()
console.log('done')
