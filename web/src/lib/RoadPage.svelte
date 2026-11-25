<script lang="ts">
  import { untrack } from 'svelte'
  import { road } from './road.svelte'
  import { currentLocale } from './locale'
  import type { FunnelAd, FunnelDay, FunnelNote, FunnelStageSource } from './types'
  import { ROAD_RANGES, currency, dayLabel, integer, percent } from './types'
  import { slots } from './markup'
  import { m } from '../paraglide/messages'

  // Same contract as the vault: mounting starts the polling, leaving stops it.
  $effect(() => untrack(() => road.activate()))

  const report = $derived(road.report)
  const code = $derived(report?.currency || 'USD')
  const totals = $derived(report?.totals)
  /** The page is off until a report says it is on; a failed probe is "off". */
  const off = $derived(report ? !report.enabled : road.enabled === false)

  /** The generic word is a capitalised stage name in a list and a noun mid-sentence. */
  const conversionName = $derived(report?.conversion_label || m.road_conversion_generic())
  const conversionInline = $derived(report?.conversion_label || m.road_conversion_generic_inline())
  /** True when ads ran but none of them reports the chosen conversion at all. */
  const untracked = $derived(!!report && report.ads.length > 0 && report.ads.every((a) => !a.tracked))

  // ------------------------------------------------------------- formatting

  function money(value: number): string {
    return currency(value, code)
  }

  function count(value: number | null): string {
    return value === null ? '—' : integer(value)
  }

  let decimalFormat: Intl.NumberFormat | null = null
  /** A rate like "1.4" (conversions per 100 views): one decimal at most. */
  function decimal(value: number): string {
    decimalFormat ??= new Intl.NumberFormat(currentLocale(), { maximumFractionDigits: 1 })
    return decimalFormat.format(value)
  }

  /** Today as the server's day keys spell it, in this browser's time zone. */
  function isoDay(date: Date): string {
    const y = date.getFullYear()
    const mo = String(date.getMonth() + 1).padStart(2, '0')
    const d = String(date.getDate()).padStart(2, '0')
    return `${y}-${mo}-${d}`
  }

  /**
   * A sentence with markup in it, cut at its named placeholders.
   *
   * `slots()` in markup.ts does this for sentences whose placeholders are all
   * alike. A note can carry two different ones (the ad's name and the
   * conversion's) in whatever order the translation puts them, so here each
   * placeholder is marked with its own name and the template looks it up.
   */
  type Part = { text: string; slot?: undefined } | { slot: string; text?: undefined }

  function named(render: (slot: (name: string) => string) => string): Part[] {
    const names: string[] = []
    const out = render((name) => {
      names.push(name)
      return `${names.length - 1}`
    })
    const parts: Part[] = []
    const re = /(\d+)/g
    let last = 0
    for (let match = re.exec(out); match; match = re.exec(out)) {
      if (match.index > last) parts.push({ text: out.slice(last, match.index) })
      parts.push({ slot: names[Number(match[1])] })
      last = re.lastIndex
    }
    if (last < out.length) parts.push({ text: out.slice(last) })
    return parts
  }

  // ------------------------------------------------------------- latest day

  /** Days oldest first, whatever order they arrived in. */
  const days = $derived([...(report?.days ?? [])].sort((a, b) => a.day.localeCompare(b.day)))

  /**
   * The newest *finished* day the ad platform has reported. Today is still
   * running, and a half-day read as a whole one is the kind of number this
   * page must not show first.
   */
  const latest = $derived.by((): FunnelDay | null => {
    const today = isoDay(new Date())
    for (let i = days.length - 1; i >= 0; i--) {
      if (days[i].day < today && days[i].spend !== null) return days[i]
    }
    return null
  })

  const latestLabel = $derived.by(() => {
    if (!latest) return ''
    const yesterday = new Date()
    yesterday.setDate(yesterday.getDate() - 1)
    const day = dayLabel(latest.day)
    return latest.day === isoDay(yesterday) ? m.road_day_yesterday({ day }) : day
  })

  /** Each sentence about that day, in reading order. */
  const latestLines = $derived.by((): string[] => {
    const d = latest
    if (!d) return []
    const lines: string[] = []
    if (!d.spend && !d.impressions) {
      lines.push(m.road_day_no_ads())
    } else {
      if (d.spend !== null && d.impressions !== null) {
        lines.push(m.road_day_spent({ count: d.impressions, countText: integer(d.impressions), spend: money(d.spend) }))
      }
      if (d.link_clicks !== null) {
        lines.push(
          d.landing_page_views !== null
            ? m.road_day_taps({
                count: d.link_clicks,
                countText: integer(d.link_clicks),
                views: integer(d.landing_page_views),
              })
            : m.road_day_taps_only({ count: d.link_clicks, countText: integer(d.link_clicks) }),
        )
      }
      if (d.conversions !== null && !untracked) {
        lines.push(m.road_day_conversions({ label: conversionName, count: integer(d.conversions) }))
      }
    }
    if (d.installs !== null) {
      lines.push(m.road_day_installs({ count: d.installs, countText: integer(d.installs) }))
    } else {
      lines.push(m.road_day_store_pending())
    }
    if (d.purchases !== null && d.purchases > 0 && d.revenue !== null) {
      lines.push(m.road_day_purchases({ count: d.purchases, countText: integer(d.purchases), revenue: money(d.revenue) }))
    }
    return lines
  })

  // ------------------------------------------------------------------ notes

  interface RenderedNote {
    key: string
    parts: Part[]
    ad: string
  }

  function param(note: FunnelNote, key: string): number | null {
    const value = note.params?.[key]
    return typeof value === 'number' && Number.isFinite(value) ? value : null
  }

  function dayParam(note: FunnelNote, key: string): string | null {
    const value = note.params?.[key]
    return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value) ? value : null
  }

  /**
   * One note as a sentence, or null. A code this build does not know is
   * skipped, and so is a note missing a number its sentence needs: a sentence
   * with a hole in it is worse than one not said.
   */
  function noteParts(note: FunnelNote): Part[] | null {
    const ad = note.ad ?? ''
    switch (note.code) {
      case 'untracked':
        return named((s) => m.road_note_untracked({ label: s('label') }))
      case 'spend_no_conversions': {
        const spend = param(note, 'spend')
        if (!ad || spend === null) return null
        return named((s) => m.road_note_spend_no_conversions({ ad: s('ad'), spend: money(spend), label: s('label') }))
      }
      case 'concentrated': {
        const pct = param(note, 'pct')
        if (!ad || pct === null) return null
        return named((s) => m.road_note_concentrated({ ad: s('ad'), share: percent(pct / 100) }))
      }
      case 'cost_rising': {
        const cost = param(note, 'cost')
        const previous = param(note, 'previous')
        const day = dayParam(note, 'day')
        if (cost === null || previous === null || !day) return null
        return named(() => m.road_note_cost_rising({ cost: money(cost), usual: money(previous), day: dayLabel(day) }))
      }
      case 'store_lag': {
        const day = dayParam(note, 'installs_as_of')
        if (!day) return null
        return named(() => m.road_note_store_lag({ day: dayLabel(day) }))
      }
      case 'budget_check': {
        const spend = param(note, 'spend')
        const rate = param(note, 'rate')
        if (spend === null || rate === null) return null
        return named((s) => m.road_note_budget_check({ spend: money(spend), rate: decimal(rate), label: s('label') }))
      }
      default:
        return null
    }
  }

  const notes = $derived.by((): RenderedNote[] => {
    const out: RenderedNote[] = []
    for (const [i, note] of (report?.notes ?? []).entries()) {
      const parts = noteParts(note)
      if (parts) out.push({ key: `${note.code}:${note.ad_id ?? ''}:${i}`, parts, ad: note.ad ?? '' })
    }
    return out
  })

  // ----------------------------------------------------------------- funnel

  let explain = $state(readExplain())

  function readExplain(): boolean {
    try {
      return localStorage.getItem('loot.road.explain') !== '0'
    } catch {
      return true
    }
  }

  function toggleExplain(): void {
    explain = !explain
    try {
      localStorage.setItem('loot.road.explain', explain ? '1' : '0')
    } catch {
      // Not remembering the choice is survivable.
    }
  }

  interface Stage {
    key: string
    name: string
    explain: string
    value: number | null
    /** The big figure; "no data" handled separately. */
    figure: string
    /** Drawn to scale with the other bar stages. */
    bar: boolean
    /** Under the figure: cost per unit, the step rate, revenue. */
    details: string[]
    /** "through Nov 22" when this stage's source lags the ads. */
    lag: string
    /** Counted across every customer, not only the ones ads sent. */
    everyone: boolean
    /** A word instead of a number (untracked conversions). */
    word: string
  }

  /** Which `as_of` key each stage's numbers come from. */
  const STAGE_SOURCE: Record<string, FunnelStageSource> = {
    spend: 'ads',
    impressions: 'ads',
    link_clicks: 'ads',
    landing_page_views: 'ads',
    conversions: 'ads',
    link_visits: 'links',
    site_events: 'site_events',
    installs: 'installs',
    purchases: 'sales',
  }

  function lagFor(key: string): string {
    const source = STAGE_SOURCE[key]
    const asOf = report?.as_of ?? {}
    const own = asOf[source]
    const ads = asOf.ads
    if (source === 'ads' || !own || !ads || own >= ads) return ''
    return m.road_through({ day: dayLabel(own) })
  }

  function costEach(spend: number | null, n: number | null): string {
    if (spend === null || n === null || n <= 0 || spend <= 0) return ''
    return m.road_cost_each({ cost: money(spend / n) })
  }

  function rate(n: number | null, of: number | null, render: (rate: string) => string): string {
    if (n === null || of === null || of <= 0) return ''
    return render(percent(n / of))
  }

  const stages = $derived.by((): Stage[] => {
    const t = totals
    if (!t) return []
    const list: Stage[] = []
    const add = (s: Omit<Stage, 'lag' | 'figure' | 'bar' | 'everyone' | 'word'> & Partial<Stage>) =>
      list.push({
        lag: lagFor(s.key),
        figure: count(s.value),
        bar: true,
        everyone: false,
        word: '',
        ...s,
        details: s.details.filter(Boolean),
      })

    add({
      key: 'spend',
      name: m.road_stage_spend(),
      explain: m.road_explain_spend(),
      value: t.spend,
      figure: t.spend === null ? '' : money(t.spend),
      bar: false,
      details: [],
    })
    add({
      key: 'impressions',
      name: m.road_stage_impressions(),
      explain: m.road_explain_impressions(),
      value: t.impressions,
      bar: false,
      details: [
        t.spend !== null && t.impressions
          ? m.road_cost_per_thousand({ cost: money((t.spend / t.impressions) * 1000), n: integer(1000) })
          : '',
      ],
    })
    add({
      key: 'link_clicks',
      name: m.road_stage_link_clicks(),
      explain: m.road_explain_link_clicks(),
      value: t.link_clicks,
      details: [
        costEach(t.spend, t.link_clicks),
        rate(t.link_clicks, t.impressions, (r) => m.road_rate_of_shown({ rate: r })),
      ],
    })
    add({
      key: 'link_visits',
      name: m.road_stage_link_visits(),
      explain: m.road_explain_link_visits(),
      value: t.link_visits,
      details: [
        costEach(t.spend, t.link_visits),
        rate(t.link_visits, t.link_clicks, (r) => m.road_rate_of_taps({ rate: r })),
      ],
    })
    add({
      key: 'landing_page_views',
      name: m.road_stage_landing_page_views(),
      explain: m.road_explain_landing_page_views(),
      value: t.landing_page_views,
      details: [
        costEach(t.spend, t.landing_page_views),
        rate(t.landing_page_views, t.link_clicks, (r) => m.road_rate_of_taps({ rate: r })),
      ],
    })
    add({
      key: 'conversions',
      name: conversionName,
      explain: m.road_explain_conversions(),
      value: untracked ? null : t.conversions,
      word: untracked ? m.road_not_tracked() : '',
      details: untracked
        ? []
        : [
            costEach(t.spend, t.conversions),
            rate(t.conversions, t.landing_page_views, (r) => m.road_rate_of_views({ rate: r })),
          ],
    })
    if (report?.site_event) {
      add({
        key: 'site_events',
        name: m.road_stage_site_events({ event: report.site_event }),
        explain: m.road_explain_site_events(),
        value: t.site_events,
        details: [],
        everyone: true,
      })
    }
    add({
      key: 'installs',
      name: m.road_stage_installs(),
      explain: m.road_explain_installs(),
      value: t.installs,
      details: [],
      everyone: true,
    })
    add({
      key: 'purchases',
      name: m.road_stage_purchases(),
      explain: m.road_explain_purchases(),
      value: t.purchases,
      details: [t.revenue !== null && t.purchases ? m.road_worth({ revenue: money(t.revenue) }) : ''],
      everyone: true,
    })
    return list
  })

  /** Bars share one scale: the biggest stage from link taps down. */
  const barTop = $derived(Math.max(1, ...stages.filter((s) => s.bar && s.value !== null).map((s) => s.value as number)))

  const adStages = $derived(stages.filter((s) => !s.everyone))
  const everyoneStages = $derived(stages.filter((s) => s.everyone))

  // --------------------------------------------------------------------- ads

  const ads = $derived([...(report?.ads ?? [])].sort((a, b) => b.spend - a.spend))

  function statusLabel(status: string): string {
    switch (status.toUpperCase()) {
      case 'ACTIVE':
        return m.road_status_active()
      case 'PAUSED':
        return m.road_status_paused()
      default:
        return status.toLowerCase().replace(/_/g, ' ')
    }
  }

  function perView(ad: FunnelAd): string {
    return ad.landing_page_views > 0 ? money(ad.spend / ad.landing_page_views) : '—'
  }

  /** The ad's campaign and ad set, as one quiet line. */
  function adPath(ad: FunnelAd): string {
    return [ad.campaign, ad.adset].filter(Boolean).join(' › ')
  }

  const SPARK_W = 96
  const SPARK_H = 24

  function sparkPath(series: number[]): string {
    if (series.length === 0) return ''
    const top = Math.max(1, ...series)
    const step = series.length > 1 ? (SPARK_W - 2) / (series.length - 1) : 0
    return series
      .map((v, i) => `${i === 0 ? 'M' : 'L'}${(1 + i * step).toFixed(1)},${(SPARK_H - 2 - (v / top) * (SPARK_H - 4)).toFixed(1)}`)
      .join(' ')
  }

  function sparkLabel(series: number[]): string {
    if (series.length === 0 || days.length === 0) return ''
    let list: string
    try {
      list = new Intl.ListFormat(currentLocale(), { style: 'short', type: 'unit' }).format(series.map(integer))
    } catch {
      list = series.map(integer).join(', ')
    }
    return m.road_spark_label({
      from: dayLabel(report?.days[0]?.day ?? ''),
      to: dayLabel(report?.days[report.days.length - 1]?.day ?? ''),
      values: list,
    })
  }

  // ---------------------------------------------------------------- by day

  const newestFirst = $derived([...days].reverse())
  /** Today is still running; its row says so rather than looking like a slow day. */
  const today = isoDay(new Date())

  // ------------------------------------------------------------- freshness

  const freshness = $derived.by((): string[] => {
    const asOf = report?.as_of ?? {}
    const out: string[] = []
    if (asOf.ads) out.push(m.road_fresh_ads({ day: dayLabel(asOf.ads) }))
    if (asOf.links) out.push(m.road_fresh_links({ day: dayLabel(asOf.links) }))
    if (asOf.site_events) out.push(m.road_fresh_site_events({ day: dayLabel(asOf.site_events) }))
    if (asOf.installs) out.push(m.road_fresh_installs({ day: dayLabel(asOf.installs) }))
    if (asOf.sales) out.push(m.road_fresh_sales({ day: dayLabel(asOf.sales) }))
    return out
  })

  const rangeLabel = $derived(report?.from && report?.to ? `${dayLabel(report.from)} → ${dayLabel(report.to)}` : '')
  const offBody = $derived(slots((slot) => m.road_off_body({ doc: slot })))
</script>

{#snippet noteSentence(parts: Part[], ad: string)}
  {#each parts as part, i (i)}
    {#if part.slot === 'ad'}<strong class="ad-name">{ad}</strong
      >{:else if part.slot === 'label'}<em>{conversionInline}</em>{:else}{part.text}{/if}
  {/each}
{/snippet}

{#snippet stageRow(stage: Stage)}
  <li class="stage" class:nodata={stage.value === null && !stage.word}>
    <div class="stage-name">
      <span class="name">{stage.name}</span>
      {#if explain}<span class="explain">{stage.explain}</span>{/if}
    </div>
    <div class="stage-figures">
      {#if stage.word}
        <span class="figure word">{stage.word}</span>
      {:else if stage.key === 'spend' && stage.figure}
        <span class="figure">{stage.figure}</span>
      {:else if stage.value === null}
        <span class="figure none">{m.road_no_data()}</span>
      {:else}
        <span class="figure">{stage.figure}</span>
      {/if}
      {#if stage.lag}<span class="lag">{stage.lag}</span>{/if}
    </div>
    {#if stage.bar}
      <div class="track" aria-hidden="true">
        {#if stage.value !== null}
          <div class="bar" class:all={stage.everyone} style="width: {Math.max(1.5, (stage.value / barTop) * 100)}%"></div>
        {/if}
      </div>
    {/if}
    {#if stage.details.length}
      <div class="details">
        {#each stage.details as detail (detail)}<span>{detail}</span>{/each}
      </div>
    {/if}
  </li>
{/snippet}

{#snippet spark(series: number[])}
  {#if series.length > 1}
    <svg
      class="spark"
      viewBox="0 0 {SPARK_W} {SPARK_H}"
      width={SPARK_W}
      height={SPARK_H}
      role="img"
      aria-label={sparkLabel(series)}
    >
      <path d={sparkPath(series)} />
    </svg>
  {:else}
    <span class="faint">—</span>
  {/if}
{/snippet}

<section class="road" aria-label={m.road_title()}>
  <div class="bar-top">
    <div class="titles">
      <h2>{m.road_title()}</h2>
      <span class="sub">{m.road_subtitle()}</span>
      {#if rangeLabel && !off}<span class="range mono">{rangeLabel}</span>{/if}
    </div>

    {#if !off}
      <div class="picker" role="group" aria-label={m.road_range_label()}>
        {#each ROAD_RANGES as range (range)}
          <button
            class="range-btn"
            class:current={road.range === range}
            aria-pressed={road.range === range}
            onclick={() => road.setRange(range)}
          >
            {m.road_range_option({ count: range, countText: integer(range) })}
          </button>
        {/each}
      </div>
    {/if}
  </div>

  {#if off}
    <div class="empty">
      <h3>{m.road_off_title()}</h3>
      <p>
        {offBody[0]}<a href="https://github.com/nickhirras/loot/blob/main/docs/funnel.md" target="_blank" rel="noreferrer"
          ><code>docs/funnel.md</code></a
        >{offBody[1]}
      </p>
    </div>
  {:else if road.error && !report}
    <p class="note error">{road.error}</p>
  {:else if !report}
    <p class="note">{m.road_loading()}</p>
  {:else}
    <div class="card lead">
      <h3>
        {m.road_day_heading()}{#if latest}<span class="lead-day"><span aria-hidden="true">·</span>{latestLabel}</span>{/if}
      </h3>
      {#if latest}
        <p class="lead-text">{latestLines.join(' ')}</p>
      {:else}
        <p class="lead-text">{m.road_day_none()}</p>
      {/if}
    </div>

    <section class="card notes" aria-labelledby="road-notes">
      <h3 id="road-notes">{m.road_notes_title()}</h3>
      {#if notes.length}
        <ul>
          {#each notes as note (note.key)}
            <li><span class="note-mark" aria-hidden="true">◆</span><p>{@render noteSentence(note.parts, note.ad)}</p></li>
          {/each}
        </ul>
      {:else}
        <p class="quiet">{m.road_notes_none()}</p>
      {/if}
    </section>

    <section class="card" aria-labelledby="road-funnel">
      <div class="card-head">
        <h3 id="road-funnel">{m.road_funnel_title()}</h3>
        <button class="explain-btn" aria-pressed={explain} onclick={toggleExplain}>
          {explain ? m.road_explain_hide() : m.road_explain_show()}
        </button>
      </div>
      <ol class="stages">
        {#each adStages as stage (stage.key)}{@render stageRow(stage)}{/each}
      </ol>
      <p class="everyone">{m.road_everyone()}</p>
      <ol class="stages" start={adStages.length + 1}>
        {#each everyoneStages as stage (stage.key)}{@render stageRow(stage)}{/each}
      </ol>
      <p class="hint bars-note">{m.road_funnel_bars()}</p>
    </section>

    <section class="card" aria-labelledby="road-ads">
      <div class="card-head">
        <h3 id="road-ads">{m.road_ads_title()}</h3>
        <span class="hint">{m.road_ads_hint()}</span>
      </div>
      {#if ads.length === 0}
        <p class="quiet">{m.road_ads_none()}</p>
      {:else}
        <div class="ads-table">
          <table>
            <thead>
              <tr>
                <th scope="col">{m.road_col_ad()}</th>
                <th scope="col">{m.road_col_status()}</th>
                <th scope="col" class="num">{m.road_col_spend()}</th>
                <th scope="col" class="num">{m.road_col_views()}</th>
                <th scope="col" class="num">{m.road_col_cost_per_view()}</th>
                <th scope="col" class="num">{conversionName}</th>
                <th scope="col" class="num">{m.road_col_link_visits()}</th>
                <th scope="col">{m.road_col_trend()}</th>
              </tr>
            </thead>
            <tbody>
              {#each ads as ad (ad.source + ad.ad_id)}
                <tr>
                  <th scope="row" class="ad-cell">
                    <span class="ad-title">{ad.ad}</span>
                    {#if adPath(ad)}<span class="ad-path">{adPath(ad)}</span>{/if}
                  </th>
                  <td><span class="status" data-status={ad.status.toUpperCase()}>{statusLabel(ad.status)}</span></td>
                  <td class="num">{money(ad.spend)}</td>
                  <td class="num">{integer(ad.landing_page_views)}</td>
                  <td class="num">{perView(ad)}</td>
                  <td class="num">
                    {#if ad.tracked}{integer(ad.conversions)}{:else}<span class="faint">{m.road_not_tracked()}</span>{/if}
                  </td>
                  <td class="num">{count(ad.link_visits)}</td>
                  <td>{@render spark(ad.series)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        <!-- The same rows as cards on a phone, where eight columns cannot fit
             and sideways scrolling would hide the ad's name from its numbers. -->
        <ul class="ads-cards">
          {#each ads as ad (ad.source + ad.ad_id)}
            <li>
              <div class="ad-card-top">
                <div class="ad-cell">
                  <span class="ad-title">{ad.ad}</span>
                  {#if adPath(ad)}<span class="ad-path">{adPath(ad)}</span>{/if}
                </div>
                <span class="status" data-status={ad.status.toUpperCase()}>{statusLabel(ad.status)}</span>
              </div>
              <dl>
                <div><dt>{m.road_col_spend()}</dt><dd>{money(ad.spend)}</dd></div>
                <div><dt>{m.road_col_views()}</dt><dd>{integer(ad.landing_page_views)}</dd></div>
                <div><dt>{m.road_col_cost_per_view()}</dt><dd>{perView(ad)}</dd></div>
                <div>
                  <dt>{conversionName}</dt>
                  <dd>{#if ad.tracked}{integer(ad.conversions)}{:else}<span class="faint">{m.road_not_tracked()}</span>{/if}</dd>
                </div>
                <div><dt>{m.road_col_link_visits()}</dt><dd>{count(ad.link_visits)}</dd></div>
                <div><dt>{m.road_col_trend()}</dt><dd>{@render spark(ad.series)}</dd></div>
              </dl>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <section class="card" aria-labelledby="road-days">
      <div class="card-head">
        <h3 id="road-days">{m.road_days_title()}</h3>
        <span class="hint">{m.road_days_hint()}</span>
      </div>
      <!-- Scrolls sideways inside the card on a phone. A scrollable region
           has to be focusable or a keyboard cannot scroll it, which is the
           one case where a tabindex on a non-control is the accessible
           choice rather than a mistake. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="days-scroll" role="region" aria-labelledby="road-days" tabindex="0">
        <table class="days">
          <thead>
            <tr>
              <th scope="col">{m.road_col_day()}</th>
              <th scope="col" class="num">{m.road_col_spend()}</th>
              <th scope="col" class="num">{m.road_col_impressions()}</th>
              <th scope="col" class="num">{m.road_col_taps()}</th>
              <th scope="col" class="num">{m.road_col_views()}</th>
              <th scope="col" class="num">{conversionName}</th>
              <th scope="col" class="num">{m.road_col_link_visits()}</th>
              {#if report.site_event}<th scope="col" class="num">{report.site_event}</th>{/if}
              <th scope="col" class="num">{m.road_col_installs()}</th>
              <th scope="col" class="num">{m.road_col_purchases()}</th>
            </tr>
          </thead>
          <tbody>
            {#each newestFirst as d (d.day)}
              <tr>
                <th scope="row">
                  {dayLabel(d.day)}{#if d.day === today}<span class="partial">{m.road_day_partial()}</span>{/if}
                </th>
                <td class="num">{d.spend === null ? '—' : money(d.spend)}</td>
                <td class="num">{count(d.impressions)}</td>
                <td class="num">{count(d.link_clicks)}</td>
                <td class="num">{count(d.landing_page_views)}</td>
                <td class="num">{untracked ? '—' : count(d.conversions)}</td>
                <td class="num">{count(d.link_visits)}</td>
                {#if report.site_event}<td class="num">{count(d.site_events)}</td>{/if}
                <td class="num">{count(d.installs)}</td>
                <td class="num">{count(d.purchases)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    {#if freshness.length}
      <div class="fresh">
        <span class="fresh-title">{m.road_fresh_title()}</span>
        <ul>
          {#each freshness as line (line)}<li>{line}</li>{/each}
        </ul>
      </div>
    {/if}

    {#if road.error}
      <p class="note error">{road.error}</p>
    {/if}
  {/if}
</section>

<style>
  .road {
    max-width: 980px;
    margin: 0 auto;
    padding: 1rem 1.1rem 4rem;
  }

  .bar-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.6rem;
    margin-bottom: 0.9rem;
  }

  .titles {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 0.2rem 0.6rem;
  }

  h2 {
    margin: 0;
    font-size: 1.1rem;
  }

  .sub {
    font-size: 0.8rem;
    color: var(--text-dim);
  }

  .range {
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .picker {
    display: inline-flex;
    gap: 0.2rem;
    padding: 0.15rem;
    border: 1px solid var(--border-soft);
    border-radius: 999px;
    background: #0d111a;
  }

  .range-btn {
    border: 1px solid transparent;
    background: transparent;
    border-radius: 999px;
    padding: 0.2rem 0.65rem;
    font-size: 0.78rem;
    color: var(--text-dim);
  }

  .range-btn:hover {
    background: var(--panel-2);
    color: var(--text);
  }

  .range-btn.current {
    color: var(--text);
    background: color-mix(in oklab, var(--accent) 14%, var(--panel-2));
    border-color: color-mix(in oklab, var(--accent) 38%, transparent);
  }

  .card {
    padding: 0.9rem 1rem 0.9rem;
    border: 1px solid var(--border-soft);
    border-radius: var(--radius);
    background: linear-gradient(180deg, var(--panel), var(--panel-2));
    margin-bottom: 0.9rem;
    min-width: 0;
  }

  .card h3 {
    margin: 0;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-faint);
    font-weight: 600;
  }

  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.4rem 0.6rem;
    margin-bottom: 0.7rem;
  }

  .hint {
    font-size: 0.7rem;
    color: var(--text-faint);
  }

  /* ------------------------------------------------------------ lead */

  .lead {
    border-color: color-mix(in oklab, var(--accent) 22%, var(--border-soft));
  }

  .lead-day {
    display: inline-flex;
    gap: 0.4rem;
    margin-left: 0.4rem;
    text-transform: none;
    letter-spacing: 0;
    color: var(--text-dim);
    font-weight: 500;
    font-size: 0.8rem;
  }

  .lead-text {
    margin: 0.45rem 0 0;
    font-size: 1.02rem;
    line-height: 1.55;
    max-width: 68ch;
  }

  /* ----------------------------------------------------------- notes */

  .notes ul {
    list-style: none;
    margin: 0.6rem 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
  }

  .notes li {
    display: flex;
    gap: 0.55rem;
    align-items: baseline;
  }

  .notes li p {
    margin: 0;
    font-size: 0.88rem;
    color: var(--text-dim);
    max-width: 72ch;
  }

  .note-mark {
    flex: none;
    font-size: 0.6rem;
    color: var(--legendary);
  }

  .ad-name {
    color: var(--text);
    font-weight: 600;
  }

  em {
    font-style: normal;
    color: var(--text);
  }

  .quiet {
    margin: 0.5rem 0 0;
    font-size: 0.85rem;
    color: var(--text-faint);
  }

  /* ---------------------------------------------------------- funnel */

  .explain-btn {
    font-size: 0.72rem;
    padding: 0.2rem 0.6rem;
    border-radius: 999px;
    color: var(--text-dim);
  }

  .stages {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .stage {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    column-gap: 1rem;
    row-gap: 0.25rem;
  }

  .stage-name {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .name {
    font-size: 0.9rem;
    font-weight: 600;
  }

  .explain {
    font-size: 0.74rem;
    color: var(--text-faint);
    max-width: 60ch;
  }

  .stage-figures {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    text-align: right;
  }

  .figure {
    font-size: 1.05rem;
    font-weight: 650;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  .figure.none,
  .figure.word {
    font-size: 0.8rem;
    font-weight: 500;
    font-style: italic;
    color: var(--text-faint);
  }

  .lag {
    font-size: 0.68rem;
    color: var(--legendary);
    white-space: nowrap;
  }

  /* A stage with no reading in the whole range is dimmed and says "no data"
     in words; the dashed track is the third cue, so none of it rests on
     colour. */
  .stage.nodata {
    opacity: 0.6;
  }

  .stage.nodata .name {
    color: var(--text-dim);
  }

  .track {
    grid-column: 1 / -1;
    height: 8px;
    border-radius: 4px;
    background: #0c1017;
    overflow: hidden;
  }

  .stage.nodata .track {
    background: repeating-linear-gradient(90deg, #0c1017 0 6px, transparent 6px 10px);
    opacity: 0.7;
  }

  .bar {
    height: 100%;
    border-radius: 0 4px 4px 0;
    background: var(--accent);
  }

  /* Everyone's numbers get a different colour *and* their own block under a
     sentence saying so, so the colour is never the only thing telling them
     apart. */
  .bar.all {
    background: var(--epic);
  }

  .details {
    grid-column: 1 / -1;
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.9rem;
    font-size: 0.72rem;
    color: var(--text-dim);
    font-variant-numeric: tabular-nums;
  }

  .everyone {
    margin: 1rem 0 0.75rem;
    padding-top: 0.75rem;
    border-top: 1px dashed var(--border);
    font-size: 0.76rem;
    color: var(--text-dim);
  }

  .bars-note {
    margin: 0.9rem 0 0;
  }

  /* ------------------------------------------------------------- ads */

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.8rem;
    font-variant-numeric: tabular-nums;
  }

  th,
  td {
    padding: 0.45rem 0.5rem;
    text-align: left;
    border-bottom: 1px solid var(--border-soft);
    vertical-align: middle;
  }

  thead th {
    font-size: 0.66rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-faint);
    font-weight: 600;
    white-space: nowrap;
  }

  tbody tr:last-child > * {
    border-bottom: 0;
  }

  tbody th {
    font-weight: 500;
  }

  .num {
    text-align: right;
    white-space: nowrap;
  }

  .ad-cell {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .ad-title {
    font-weight: 600;
    color: var(--text);
  }

  .ad-path {
    font-size: 0.7rem;
    color: var(--text-faint);
  }

  .status {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.74rem;
    color: var(--text-dim);
    white-space: nowrap;
  }

  .status::before {
    content: '';
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--common);
  }

  .status[data-status='ACTIVE']::before {
    background: var(--uncommon);
  }

  .spark {
    display: block;
  }

  .spark path {
    fill: none;
    stroke: var(--accent);
    stroke-width: 1.5;
    stroke-linejoin: round;
  }

  .faint {
    color: var(--text-faint);
  }

  .ads-cards {
    display: none;
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .ads-cards li {
    padding: 0.7rem 0;
    border-bottom: 1px solid var(--border-soft);
  }

  .ads-cards li:first-child {
    padding-top: 0;
  }

  .ads-cards li:last-child {
    border-bottom: 0;
    padding-bottom: 0;
  }

  .ad-card-top {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 0.6rem;
  }

  dl {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 0.5rem 0.6rem;
    margin: 0.55rem 0 0;
  }

  dt {
    font-size: 0.62rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-faint);
  }

  dd {
    margin: 0;
    font-size: 0.85rem;
    font-variant-numeric: tabular-nums;
  }

  @media (max-width: 640px) {
    .ads-table {
      display: none;
    }

    .ads-cards {
      display: block;
    }
  }

  /* ------------------------------------------------------------- days */

  .days-scroll {
    overflow-x: auto;
    overscroll-behavior-x: contain;
    -webkit-overflow-scrolling: touch;
    margin: 0 -0.3rem;
  }

  .days-scroll:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }

  .days th,
  .days td {
    padding: 0.35rem 0.5rem;
    white-space: nowrap;
  }

  .partial {
    margin-left: 0.4rem;
    font-size: 0.66rem;
    font-weight: 400;
    font-style: italic;
    color: var(--text-faint);
  }

  /* ---------------------------------------------------------- footer */

  .fresh {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.2rem 0.6rem;
    margin: 0.2rem 0 0;
    font-size: 0.72rem;
    color: var(--text-faint);
  }

  .fresh ul {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.6rem;
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .fresh li + li::before {
    content: '·';
    margin-right: 0.6rem;
  }

  .empty {
    padding: 1.4rem 1.3rem 1.6rem;
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    background: #0e121b;
    color: var(--text-dim);
  }

  .empty h3 {
    margin: 0 0 0.5rem;
    font-size: 1rem;
    color: var(--text);
  }

  .empty p {
    margin: 0;
    font-size: 0.85rem;
    max-width: 62ch;
  }

  a {
    color: var(--accent);
  }

  code {
    font-family: var(--mono);
    font-size: 0.85em;
    background: var(--panel-2);
    border: 1px solid var(--border-soft);
    border-radius: 5px;
    padding: 0.05rem 0.35rem;
  }

  .note {
    margin: 1.5rem auto;
    text-align: center;
    color: var(--text-faint);
    font-size: 0.85rem;
  }

  .note.error {
    color: var(--cursed);
  }
</style>
