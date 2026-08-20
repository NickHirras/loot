import { defineConfig, type Plugin } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * Go's `//go:embed all:dist` needs the directory to exist even before the first
 * frontend build, and Vite's emptyOutDir wipes the placeholder. Put it back so a
 * clean checkout still compiles.
 */
function keepDist(): Plugin {
  return {
    name: 'loot-keep-dist',
    closeBundle() {
      writeFileSync(resolve(__dirname, 'dist/.gitkeep'), '')
    },
  }
}

/**
 * The service worker lives in public/ so it is served from the origin root (a
 * worker can only claim clients at or below its own path). Its cache names need
 * to change on every build, though, or an old shell would outlive the deploy it
 * belongs to — so the placeholder gets stamped here, after public/ is copied.
 */
function stampServiceWorker(): Plugin {
  return {
    name: 'loot-stamp-sw',
    apply: 'build',
    closeBundle() {
      const sw = resolve(__dirname, 'dist/sw.js')
      const stamp = new Date().toISOString().replace(/[-:.TZ]/g, '').slice(0, 14)
      writeFileSync(sw, readFileSync(sw, 'utf8').replace(/__LOOT_BUILD__/g, stamp))
    },
  }
}

const backend = process.env.LOOT_BACKEND ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [svelte(), keepDist(), stampServiceWorker()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // The dashboard is small; one chunk keeps the embedded binary simple.
    chunkSizeWarningLimit: 1024,
  },
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': { target: backend, changeOrigin: true },
      '/hooks': { target: backend, changeOrigin: true },
      '/ws': { target: backend.replace(/^http/, 'ws'), ws: true },
    },
  },
})
