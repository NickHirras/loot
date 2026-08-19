<script lang="ts">
  import Sparkline from './Sparkline.svelte'
  import { rarityColor, tierColor } from './palette'
  import type { Recap } from './types'
  import { currency, dayLabel, flagEmoji, integer, percent } from './types'

  let { recap }: { recap: Recap } = $props()

  const code = $derived(recap.display_currency || 'USD')
  // The poster takes its colour from the best thing that happened in the
  // month. A quiet month is blue, a month with a legendary in it is gold.
  const accent = $derived(rarityColor(recap.top_rarity || 'rare'))

  /** "month" or "year", so every sentence on the card reads in the right unit. */
  const unit = $derived(recap.period.kind === 'season' ? 'year' : 'month')

  /**
   * The delta line, stated as a fact and nothing else: what it was, and which
   * way it moved. A month that earned less says so in the same grey as a month
   * that earned more — there is no target here to have missed.
   */
  const revenueLine = $derived(
    !recap.revenue_delta.has_basis
      ? recap.revenue_base > 0
        ? `first ${unit} with revenue`
        : ''
      : `${recap.revenue_delta.direction === 'down' ? '−' : '+'}${percent(
          Math.abs(recap.revenue_delta.pct),
        )} · ${currency(recap.revenue_delta.previous, code, 0)} the ${unit} before`,
  )

  const countries = $derived(recap.new_countries ?? [])
  const trophies = $derived(recap.achievements_unlocked ?? [])
  const series = $derived(recap.series ?? [])

  let copied = $state(false)
  let copyError = $state('')

  /**
   * The whole recap as plain text. Deliberately not an image: an image export
   * means a canvas renderer and a font pipeline, and a paste into a chat
   * window is what people actually do with a month's numbers.
   */
  function summaryText(): string {
    const lines: string[] = []
    lines.push(`◆ Loot — ${recap.period.label}${recap.period.partial ? ' (so far)' : ''}`)
    lines.push('')
    lines.push(`Revenue  ${currency(recap.revenue_base, code, 0)}${revenueLine ? `  (${revenueLine})` : ''}`)
    lines.push(`Units    ${integer(recap.units)}${recap.refunds ? ` · ${integer(recap.refunds)} refunds` : ''}`)
    if (recap.installs) lines.push(`Installs ${integer(recap.installs)}`)
    lines.push(`Drops    ${integer(recap.drops)} · ${integer(recap.xp)} XP`)
    if (recap.era_start !== recap.era_end) lines.push(`Era      ${recap.era_start} → ${recap.era_end}`)
    else lines.push(`Level    ${recap.level_start}${recap.level_end > recap.level_start ? ` → ${recap.level_end}` : ''}`)
    if (countries.length) {
      lines.push(`Settled  ${countries.map((c) => c.country).join(', ')}`)
    }
    if (recap.highlights.length) {
      lines.push('')
      for (const h of recap.highlights) lines.push(`• ${h}`)
    }
    if (trophies.length) {
      lines.push('')
      lines.push(`Achievements: ${trophies.map((t) => t.title).join(', ')}`)
    }
    return lines.join('\n')
  }

  function copied2s(): void {
    copied = true
    setTimeout(() => (copied = false), 2200)
  }

  /**
   * The old textarea trick, kept as a fallback. The async clipboard refuses
   * outside a secure context and whenever the document is not focused, which
   * covers a Loot served over plain http on a LAN — exactly where a
   * self-hosted dashboard tends to live.
   */
  function copyViaTextarea(text: string): boolean {
    const area = document.createElement('textarea')
    area.value = text
    area.setAttribute('readonly', '')
    area.style.position = 'fixed'
    area.style.top = '-1000px'
    area.style.opacity = '0'
    document.body.appendChild(area)
    area.select()
    let ok = false
    try {
      ok = document.execCommand('copy')
    } catch {
      ok = false
    }
    document.body.removeChild(area)
    return ok
  }

  async function copy(): Promise<void> {
    copyError = ''
    const text = summaryText()
    try {
      await navigator.clipboard.writeText(text)
      copied2s()
      return
    } catch {
      // Fall through to the textarea below rather than giving up.
    }
    if (copyViaTextarea(text)) {
      copied2s()
      return
    }
    copyError = 'This browser would not let Loot use the clipboard.'
  }
</script>

<article class="poster" style="--accent: {accent}">
  <header>
    <div class="titles">
      <h3>{recap.period.label}</h3>
      <span class="sub">
        the {unit} in loot
        {#if recap.period.partial}· so far{/if}
      </span>
    </div>
    <button class="copy" onclick={copy} title="Copy this recap as plain text">
      {copied ? 'Copied' : 'Copy summary'}
    </button>
  </header>

  {#if recap.empty}
    <p class="empty">
      Nothing landed in {recap.period.label}. Loot has no opinion about that — it is simply a {unit} with no
      numbers in it. Pick another from the list above.
    </p>
  {:else}
    <div class="headline">
      <div class="big">
        <span class="amount">{currency(recap.revenue_base, code, 0)}</span>
        <span class="label">revenue</span>
      </div>
      {#if revenueLine}
        <span class="delta dir-{recap.revenue_delta.direction}">{revenueLine}</span>
      {/if}
    </div>

    {#if series.length > 1}
      <Sparkline points={series} flagDay={recap.best_day.day} unit="money" {code} />
    {/if}

    <ul class="figures">
      <li><span class="v">{integer(recap.units)}</span><span class="k">units</span></li>
      {#if recap.installs > 0}
        <li><span class="v">{integer(recap.installs)}</span><span class="k">installs</span></li>
      {/if}
      <li><span class="v">{integer(recap.drops)}</span><span class="k">drops</span></li>
      <li><span class="v">{integer(recap.xp)}</span><span class="k">XP</span></li>
      <li><span class="v">{integer(recap.chests_opened)}</span><span class="k">chests</span></li>
      {#if recap.refunds > 0}
        <li><span class="v">{integer(recap.refunds)}</span><span class="k">refunds</span></li>
      {/if}
    </ul>

    {#if recap.highlights.length > 0}
      <ul class="highlights">
        {#each recap.highlights as line, i (line + i)}
          <li>{line}</li>
        {/each}
      </ul>
    {/if}

    {#if countries.length > 0}
      <div class="row">
        <span class="row-label">New settlements</span>
        <div class="flags">
          {#each countries as c (c.country)}
            <span class="flag" title="{c.country} · {dayLabel(c.day)}">{flagEmoji(c.country)}</span>
          {/each}
        </div>
      </div>
    {/if}

    {#if trophies.length > 0}
      <div class="row">
        <span class="row-label">Achievements</span>
        <div class="trophies">
          {#each trophies as t (t.key)}
            <span class="trophy" style="--tier: {tierColor(t.tier)}" title={t.description}>★ {t.title}</span>
          {/each}
        </div>
      </div>
    {/if}

    <footer>
      <span>
        {recap.era_start === recap.era_end
          ? `${recap.era_end} era · level ${recap.level_end}`
          : `${recap.era_start} → ${recap.era_end} · level ${recap.level_start} → ${recap.level_end}`}
      </span>
      <span class="tops">
        {#if recap.top_app.key}{recap.top_app.key}{/if}
        {#if recap.top_source.key}· {recap.top_source.key}{/if}
        {#if recap.top_country.key}· {flagEmoji(recap.top_country.key)} {recap.top_country.key}{/if}
      </span>
    </footer>

    {#if copyError}<p class="copy-error">{copyError}</p>{/if}
  {/if}
</article>

<style>
  .poster {
    position: relative;
    overflow: hidden;
    padding: 1.1rem 1.2rem 1rem;
    border: 1px solid color-mix(in oklab, var(--accent) 35%, var(--border));
    border-radius: var(--radius);
    background:
      radial-gradient(120% 80% at 100% 0%, color-mix(in oklab, var(--accent) 18%, transparent), transparent 60%),
      radial-gradient(90% 70% at 0% 100%, color-mix(in oklab, var(--accent) 10%, transparent), transparent 65%),
      linear-gradient(165deg, #131826, #0d111a 70%);
  }

  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 0.8rem;
    flex-wrap: wrap;
  }

  .titles {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
  }

  h3 {
    margin: 0;
    font-size: 1.25rem;
    font-weight: 700;
    letter-spacing: 0.01em;
  }

  .sub {
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .copy {
    font-size: 0.75rem;
    border-color: color-mix(in oklab, var(--accent) 40%, transparent);
    background: color-mix(in oklab, var(--accent) 10%, #0d111a);
  }

  .headline {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 0.7rem;
    margin: 0.9rem 0 0.2rem;
  }

  .big {
    display: flex;
    align-items: baseline;
    gap: 0.45rem;
  }

  .amount {
    font-size: clamp(2rem, 7vw, 2.9rem);
    font-weight: 700;
    line-height: 1;
    letter-spacing: -0.02em;
    background: linear-gradient(92deg, var(--text), color-mix(in oklab, var(--accent) 75%, var(--text)));
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }

  .label {
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    color: var(--text-faint);
  }

  /* Both directions are the same grey. A month that earned less is a fact, not
     a failing, and colouring it red would make Loot a report card. */
  .delta {
    font-size: 0.76rem;
    color: var(--text-dim);
  }

  .figures {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem 1.1rem;
    list-style: none;
    margin: 0.7rem 0 0;
    padding: 0;
  }

  .figures li {
    display: flex;
    align-items: baseline;
    gap: 0.3rem;
  }

  .figures .v {
    font-weight: 700;
    font-size: 0.95rem;
  }

  .figures .k {
    font-size: 0.68rem;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-faint);
  }

  .highlights {
    list-style: none;
    margin: 0.85rem 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.22rem;
  }

  .highlights li {
    position: relative;
    padding-left: 0.95rem;
    font-size: 0.83rem;
    color: var(--text);
  }

  .highlights li::before {
    content: '◆';
    position: absolute;
    left: 0;
    top: 0.05em;
    font-size: 0.6rem;
    color: var(--accent);
  }

  .row {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin-top: 0.8rem;
  }

  .row-label {
    font-size: 0.62rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-faint);
    white-space: nowrap;
  }

  .flags {
    display: flex;
    flex-wrap: wrap;
    gap: 0.22rem;
    font-size: 1.05rem;
    line-height: 1.2;
  }

  .flag {
    cursor: help;
  }

  .trophies {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }

  .trophy {
    font-size: 0.68rem;
    padding: 0.08rem 0.42rem;
    border-radius: 999px;
    color: var(--tier);
    border: 1px solid color-mix(in oklab, var(--tier) 45%, transparent);
    background: color-mix(in oklab, var(--tier) 10%, transparent);
    cursor: help;
  }

  footer {
    display: flex;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 1rem;
    padding-top: 0.6rem;
    border-top: 1px solid var(--border-soft);
    font-size: 0.7rem;
    color: var(--text-faint);
  }

  .tops {
    font-family: var(--mono);
  }

  .empty {
    margin: 0.9rem 0 0.2rem;
    font-size: 0.84rem;
    color: var(--text-dim);
    max-width: 60ch;
  }

  .copy-error {
    margin: 0.6rem 0 0;
    font-size: 0.72rem;
    color: var(--text-faint);
  }
</style>
