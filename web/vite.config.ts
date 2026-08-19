import { defineConfig, type Plugin } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { writeFileSync } from 'node:fs'
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

const backend = process.env.LOOT_BACKEND ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [svelte(), keepDist()],
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
