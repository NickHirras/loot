<script lang="ts">
  import ChestOverlay from './lib/ChestOverlay.svelte'
  import CodexPage from './lib/CodexPage.svelte'
  import DevPanel from './lib/DevPanel.svelte'
  import Feed from './lib/Feed.svelte'
  import Header from './lib/Header.svelte'
  import HearthPage from './lib/HearthPage.svelte'
  import QuestsPage from './lib/QuestsPage.svelte'
  import VaultPage from './lib/VaultPage.svelte'
  import { router } from './lib/route.svelte'
  import { scope } from './lib/scope.svelte'
  import { loot } from './lib/state.svelte'

  $effect(() => {
    void loot.start()
    return () => loot.stop()
  })

  $effect(() => {
    const stopRouter = router.start()
    // A scoped link, or the back button, changes the hash without going
    // through the selector; adopting it here is what makes both work.
    const syncScope = () => scope.syncFromHash()
    addEventListener('hashchange', syncScope)
    return () => {
      stopRouter()
      removeEventListener('hashchange', syncScope)
    }
  })

  // Browsers will not start an AudioContext without a gesture, so the banner
  // stays until the user clicks (or explicitly mutes).
  const needsUnlock = $derived(!loot.audioReady && !loot.muted)

  // Ambient mode is the Hearth alone on the screen: no header, no footer, no
  // dev panel — nothing that would spoil a globe left running on a spare
  // monitor. The sound banner still shows, because a muted ambient globe is
  // half the experience and one click fixes it.
  const bare = $derived(router.ambient)
</script>

<svelte:window
  onkeydown={(e) => {
    if (e.key === 'm' && !e.metaKey && !e.ctrlKey && !(e.target instanceof HTMLInputElement)) loot.toggleMute()
  }}
/>

{#if needsUnlock}
  <button class="unlock" onclick={() => loot.enableAudio()}>
    <span class="speaker" aria-hidden="true">🔈</span>
    Click to enable drop sounds
    <span class="dismiss" onclick={(e) => { e.stopPropagation(); loot.toggleMute() }} role="none">no thanks</span>
  </button>
{/if}

{#if !bare}
  <Header />
{/if}

{#if router.tab === 'hearth'}
  <HearthPage />
{:else if router.tab === 'vault'}
  <VaultPage />
{:else if router.tab === 'quests'}
  <QuestsPage />
{:else if router.tab === 'codex'}
  <CodexPage />
{:else}
  <Feed />
{/if}

{#if loot.chestOverlay}
  <ChestOverlay />
{/if}

{#if loot.devEnabled && !bare}
  <DevPanel />
{/if}

{#if !bare}
  <footer>
    <span>Loot · self-hosted loot tracking for indie devs</span>
    <span class="hint">press <kbd>m</kbd> to mute</span>
  </footer>
{/if}

<style>
  .unlock {
    position: sticky;
    top: 0;
    z-index: 30;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.6rem;
    width: 100%;
    border: 0;
    border-bottom: 1px solid color-mix(in oklab, var(--legendary) 35%, transparent);
    border-radius: 0;
    padding: 0.5rem 1rem;
    font-size: 0.8rem;
    color: #1a1405;
    background: linear-gradient(90deg, var(--legendary), #ffd97a);
    font-weight: 600;
  }

  .unlock:hover {
    background: linear-gradient(90deg, #ffcf5c, var(--legendary));
  }

  .speaker {
    font-size: 0.95rem;
  }

  .dismiss {
    font-weight: 500;
    text-decoration: underline;
    opacity: 0.75;
    margin-left: 0.4rem;
  }

  .dismiss:hover {
    opacity: 1;
  }

  footer {
    display: flex;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.5rem;
    max-width: 780px;
    margin: 0 auto;
    padding: 1.2rem 1.1rem 2.5rem;
    border-top: 1px solid var(--border-soft);
    color: var(--text-faint);
    font-size: 0.72rem;
  }

  kbd {
    font-family: var(--mono);
    font-size: 0.9em;
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-bottom-width: 2px;
    border-radius: 4px;
    padding: 0 0.3rem;
  }
</style>
