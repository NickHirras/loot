import { mount } from 'svelte'
import App from './App.svelte'
import './app.css'

const target = document.getElementById('app')
if (!target) throw new Error('#app not found')

// Only in a real build: under `vite dev` a service worker would sit between the
// page and HMR and cache assets that are supposed to change every keystroke.
// Registration failing is not worth bothering anyone about — the dashboard
// works identically without it, it just is not installable.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch((err) => {
      console.warn('service worker registration failed', err)
    })
  })
}

export default mount(App, { target })
