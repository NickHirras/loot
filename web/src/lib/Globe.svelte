<script lang="ts">
  import { untrack } from 'svelte'
  import { geoCircle, geoDistance, geoGraticule10, geoInterpolate, geoOrthographic, geoPath } from 'd3-geo'
  import { borders, centroidOf, countries as landFeatures, countryName } from './geo'
  import { rarityColor } from './palette'
  import { prefersReducedMotion } from './route.svelte'
  import { anchorageOf, vesselKind, vesselName, vesselWhy } from './sea'
  import { loot } from './state.svelte'
  import type { Drop, HearthCountry, HearthVessel } from './types'
  import { currency, flagEmoji, integer } from './types'

  let {
    countries,
    fleet = [],
    capital,
    code,
    ambient = false,
  }: {
    /** Every settlement, biggest first. */
    countries: HearthCountry[]
    /**
     * The fleet: one vessel per source that counted people it could not place.
     * They are drawn at sea and are never countries.
     */
    fleet?: HearthVessel[]
    /** ISO2 of the capital, or "" when Loot has never seen a country. */
    capital: string
    /** Display currency, for the tooltip. */
    code: string
    /** Ambient mode draws a starfield and is allowed to steer the globe. */
    ambient?: boolean
  } = $props()

  // --------------------------------------------------------------- constants

  /** One revolution every 90 seconds, in degrees per second. */
  const SPIN = 360 / 90
  /** How long the globe stays still after you stop touching it. */
  const INTERACTION_PAUSE_MS = 5_000
  /** A drop's flight time from its country to the capital. */
  const ARC_MS = 1_600
  /** The fraction of the arc that is still lit behind the comet head. */
  const ARC_TAIL = 0.28
  /** How long an impact ring, an origin pulse or a cursed flash lives. */
  const PULSE_MS = 900
  /** A founding — the ring and the floating label — takes its time. */
  const FOUND_MS = 2_400
  /** Turning the globe to bring an arc's origin into view. */
  const TURN_MS = 1_000
  /** Frame budget: half rate when nothing is moving, to spare the battery. */
  const IDLE_FPS = 30
  const BUSY_FPS = 60

  const OCEAN_INNER = '#101a2e'
  const OCEAN_OUTER = '#05080f'
  const LAND_FILL = '#22304a'
  const LAND_STROKE = '#3a4a68'
  const BORDER_STROKE = 'rgba(140, 162, 205, 0.22)'
  const GRATICULE = 'rgba(110, 231, 255, 0.07)'
  const ATMOSPHERE = 'rgba(110, 231, 255, 1)'
  /** City lights: warm sodium gold, exactly what you see from orbit. */
  const CITY = [245, 196, 81]
  const CAPITAL = [110, 231, 255]
  /**
   * Deck lights: the same idea as a city's, cooled and desaturated. A vessel
   * has to read as "not a settlement" at a glance and from across a room, and
   * cold against warm does that faster than shape alone.
   */
  const VESSEL = [141, 196, 224]

  /** Marker radius per tier index, at the reference scale. */
  const TIER_RADIUS = [1.3, 1.9, 2.6, 3.5, 4.6, 6]
  /** A vessel's half-width per tier index — roughly 8px to 15px of ship. */
  const VESSEL_SIZE = [4, 4.6, 5.3, 6.1, 6.9, 7.6]
  const REFERENCE_SCALE = 260
  /** One slow bob of the swell, in milliseconds. */
  const BOB_MS = 2_600

  // ------------------------------------------------------------------- state

  let host = $state<HTMLDivElement | null>(null)
  let canvas = $state<HTMLCanvasElement | null>(null)
  // Canvas geometry is read by the render loop, never by the template, so it
  // is deliberately plain state: making it reactive would have the resize
  // observer and the effect that installs it chase each other.
  let width = 720
  let height = 520

  /** The rotation the projection is currently at, [yaw, pitch]. */
  let rotation: [number, number] = [-20, -12]
  /** Globe radius in pixels; the fit is recomputed on every resize. */
  let scale = 260
  let fitScale = 260

  let dragging = false
  let dragFrom: { x: number; y: number; rotation: [number, number] } | null = null
  let interactingUntil = 0
  /** Yaw/pitch velocity in degrees per ms, left over from a flick. */
  let velocity: [number, number] = [0, 0]
  let lastMove: { x: number; y: number; t: number } | null = null
  const FLICK_DECAY = 0.0035 // per ms; ~1s of coast

  /** An animated rotation, used to bring an arc's origin into view. */
  let turn: { from: [number, number]; to: [number, number]; start: number } | null = null

  /** A drop in flight from its country to the capital. */
  interface Arc {
    from: [number, number]
    to: [number, number]
    color: string
    start: number
    duration: number
    /** Set once the head has landed, so the impact only bursts once. */
    landed: boolean
  }

  /** A ring expanding out of a point on the globe. */
  interface Pulse {
    at: [number, number]
    color: string
    start: number
    duration: number
    /** A founding also floats a label; anything else is just a ring. */
    label?: string
    /** Cursed drops flash a filled disc rather than a thin ring. */
    filled?: boolean
  }

  /**
   * Arcs and pulses are pruned as they are drawn, so a hidden tab (where the
   * browser stops calling requestAnimationFrame) would otherwise pile up an
   * unbounded queue of animations nobody watched. These caps drop the oldest.
   */
  const MAX_ARCS = 24
  const MAX_PULSES = 48

  let arcs: Arc[] = []
  let pulses: Pulse[] = []

  function push<T>(list: T[], item: T, cap: number): T[] {
    list.push(item)
    return list.length > cap ? list.slice(list.length - cap) : list
  }

  /** The settlement under the pointer, and where to put its tooltip. */
  let hovered = $state<{ country: HearthCountry; x: number; y: number } | null>(null)
  /** The vessel under the pointer. Only ever one of the two is set. */
  let hoveredShip = $state<{ vessel: HearthVessel; x: number; y: number } | null>(null)

  let stars: { x: number; y: number; r: number; a: number }[] = []

  const reduced = prefersReducedMotion()

  // ------------------------------------------------------------- projections

  const projection = geoOrthographic().precision(0.4)
  const graticule = geoGraticule10()
  const sphere = { type: 'Sphere' } as const
  const nightCircle = geoCircle().radius(90)

  /** Every settlement that Loot can actually place on the map. */
  const cities = $derived(
    countries
      .map((country) => ({ country, point: centroidOf(country.country) }))
      .filter((c): c is { country: HearthCountry; point: [number, number] } => c.point !== null),
  )

  const capitalPoint = $derived(capital ? centroidOf(capital) : null)

  /**
   * The fleet, anchored. A vessel is always placeable — its anchorage is a
   * fixed piece of open ocean picked from its source id — so unlike a
   * settlement, none of them can fall off the map.
   *
   * The phase staggers the bobbing, so eight ships do not rise and fall as one.
   */
  const vessels = $derived(
    fleet.map((vessel, i) => ({
      vessel,
      point: anchorageOf(vessel.source),
      phase: (i * 2.3) % (Math.PI * 2),
    })),
  )

  // ------------------------------------------------------------ the sun

  /**
   * The point on Earth the sun is directly above, from the standard low
   * precision solar position formulas (declination plus the equation of time).
   * It is accurate to a fraction of a degree, which is far better than a
   * terminator drawn on a 500 pixel globe can show.
   */
  function subsolarPoint(now: Date): [number, number] {
    const rad = Math.PI / 180
    const days = now.getTime() / 86_400_000 + 2_440_587.5 - 2_451_545.0

    const meanLongitude = (280.46 + 0.9856474 * days) % 360
    const meanAnomaly = ((357.528 + 0.9856003 * days) % 360) * rad
    const eclipticLongitude =
      (meanLongitude + 1.915 * Math.sin(meanAnomaly) + 0.02 * Math.sin(2 * meanAnomaly)) * rad
    const obliquity = (23.439 - 0.0000004 * days) * rad

    const declination = Math.asin(Math.sin(obliquity) * Math.sin(eclipticLongitude)) / rad

    // The equation of time: how far ahead of (or behind) the clock the sun is.
    const rightAscension =
      Math.atan2(Math.cos(obliquity) * Math.sin(eclipticLongitude), Math.cos(eclipticLongitude)) / rad
    const eot = ((meanLongitude - rightAscension + 180) % 360) - 180

    const utcHours = now.getUTCHours() + now.getUTCMinutes() / 60 + now.getUTCSeconds() / 3600
    const longitude = -15 * (utcHours - 12 + (eot * 4) / 60)
    return [((longitude + 540) % 360) - 180, declination]
  }

  // ------------------------------------------------------------ small helpers

  /** The point at the centre of the visible disc, given the current rotation. */
  function viewCentre(): [number, number] {
    return [-rotation[0], -rotation[1]]
  }

  /** False for anything on the far side of the world. */
  function visible(point: [number, number]): boolean {
    return geoDistance(point, viewCentre()) < Math.PI / 2 - 0.02
  }

  function rgba(colour: number[], alpha: number): string {
    return `rgba(${colour[0]}, ${colour[1]}, ${colour[2]}, ${alpha})`
  }

  function markerRadius(tierIndex: number): number {
    const base = TIER_RADIUS[Math.min(tierIndex, TIER_RADIUS.length - 1)] ?? TIER_RADIUS[0]
    return base * (scale / REFERENCE_SCALE)
  }

  /** Half the width of a vessel's hull, on the same tier ladder as a city. */
  function vesselSize(tierIndex: number): number {
    const base = VESSEL_SIZE[Math.min(tierIndex, VESSEL_SIZE.length - 1)] ?? VESSEL_SIZE[0]
    return base * (scale / REFERENCE_SCALE)
  }

  function shortestTurn(from: number, to: number): number {
    return ((((to - from) % 360) + 540) % 360) - 180
  }

  // ------------------------------------------------------------------ drawing

  function draw(now: number) {
    const ctx = canvas?.getContext('2d')
    if (!ctx || !width || !height) return

    projection
      .rotate([rotation[0], rotation[1], 0])
      .scale(scale)
      .translate([width / 2, height / 2])

    const path = geoPath(projection, ctx)
    const centre: [number, number] = [width / 2, height / 2]
    const sun = subsolarPoint(new Date())

    ctx.clearRect(0, 0, width, height)

    if (ambient) drawStars(ctx)
    drawAtmosphere(ctx, centre)

    // Ocean.
    ctx.beginPath()
    path(sphere)
    const ocean = ctx.createRadialGradient(
      centre[0] - scale * 0.35,
      centre[1] - scale * 0.4,
      scale * 0.1,
      centre[0],
      centre[1],
      scale,
    )
    ocean.addColorStop(0, OCEAN_INNER)
    ocean.addColorStop(1, OCEAN_OUTER)
    ctx.fillStyle = ocean
    ctx.fill()

    ctx.beginPath()
    path(graticule)
    ctx.strokeStyle = GRATICULE
    ctx.lineWidth = 0.5
    ctx.stroke()

    ctx.beginPath()
    path(landFeatures)
    ctx.fillStyle = LAND_FILL
    ctx.fill()
    ctx.strokeStyle = LAND_STROKE
    ctx.lineWidth = 0.6
    ctx.stroke()

    ctx.beginPath()
    path(borders)
    ctx.strokeStyle = BORDER_STROKE
    ctx.lineWidth = 0.5
    ctx.stroke()

    drawNight(ctx, path, sun)
    drawCities(ctx, sun, now)
    drawFleet(ctx, sun, now)
    drawArcs(ctx, centre, now)
    drawPulses(ctx, now)

    // The rim goes on last so the night side fades into it rather than
    // being painted over by it.
    ctx.beginPath()
    ctx.arc(centre[0], centre[1], scale, 0, Math.PI * 2)
    ctx.strokeStyle = 'rgba(110, 231, 255, 0.22)'
    ctx.lineWidth = 1
    ctx.stroke()
  }

  function drawStars(ctx: CanvasRenderingContext2D) {
    if (!stars.length) return
    for (const star of stars) {
      ctx.beginPath()
      ctx.arc(star.x * width, star.y * height, star.r, 0, Math.PI * 2)
      ctx.fillStyle = `rgba(215, 228, 255, ${star.a})`
      ctx.fill()
    }
  }

  /** The halo of air around the planet, drawn just outside the sphere. */
  function drawAtmosphere(ctx: CanvasRenderingContext2D, centre: [number, number]) {
    const glow = ctx.createRadialGradient(centre[0], centre[1], scale * 0.94, centre[0], centre[1], scale * 1.18)
    glow.addColorStop(0, ATMOSPHERE.replace('1)', '0.16)'))
    glow.addColorStop(0.45, ATMOSPHERE.replace('1)', '0.07)'))
    glow.addColorStop(1, ATMOSPHERE.replace('1)', '0)'))
    ctx.beginPath()
    ctx.arc(centre[0], centre[1], scale * 1.18, 0, Math.PI * 2)
    ctx.fillStyle = glow
    ctx.fill()
  }

  /**
   * Night. One hard shadow would put a razor across the planet, so the
   * terminator is five nested caps of the same faint ink: each one is a
   * slightly smaller circle around the anti-sun point, and where they overlap
   * the shade deepens into a soft, wide dusk.
   */
  function drawNight(ctx: CanvasRenderingContext2D, path: ReturnType<typeof geoPath>, sun: [number, number]) {
    const antisolar: [number, number] = [((sun[0] + 360) % 360) - 180, -sun[1]]
    for (const radius of [96, 90, 84, 78, 70]) {
      ctx.beginPath()
      path(nightCircle.center(antisolar).radius(radius)())
      ctx.fillStyle = 'rgba(3, 6, 16, 0.19)'
      ctx.fill()
    }
  }

  function drawCities(ctx: CanvasRenderingContext2D, sun: [number, number], now: number) {
    for (const { country, point } of cities) {
      if (!visible(point)) continue
      const projected = projection(point)
      if (!projected) continue

      const [x, y] = projected
      const isCapital = country.country === capital
      const tier = country.tier?.index ?? 0
      const radius = markerRadius(tier)
      // City lights are what you notice from orbit, and only at night.
      const night = geoDistance(point, sun) > Math.PI / 2
      const lit = night ? 1 : 0.55
      // Small settlements are dimmer as well as smaller, so a metropolis
      // reads at a glance.
      const strength = (0.45 + tier * 0.11) * lit

      const colour = isCapital ? CAPITAL : CITY
      const glowRadius = radius * (night ? 5.5 : 3.6)
      const glow = ctx.createRadialGradient(x, y, 0, x, y, glowRadius)
      glow.addColorStop(0, rgba(colour, Math.min(0.85, strength + 0.2)))
      glow.addColorStop(0.4, rgba(colour, strength * 0.35))
      glow.addColorStop(1, rgba(colour, 0))
      ctx.beginPath()
      ctx.arc(x, y, glowRadius, 0, Math.PI * 2)
      ctx.fillStyle = glow
      ctx.fill()

      if (isCapital) {
        drawStar(ctx, x, y, Math.max(4, radius * 1.9), rgba(CAPITAL, 0.95))
        continue
      }

      // A village gets a ring, a town a halo, a city a skyline, a metropolis
      // a halo that breathes.
      if (tier >= 2) {
        ctx.beginPath()
        ctx.arc(x, y, radius * 2.1, 0, Math.PI * 2)
        ctx.strokeStyle = rgba(colour, 0.28 * lit + 0.1)
        ctx.lineWidth = 1
        ctx.stroke()
      }
      if (tier >= 5) {
        const breath = 1 + 0.18 * Math.sin(now / 620)
        ctx.beginPath()
        ctx.arc(x, y, radius * 3.1 * breath, 0, Math.PI * 2)
        ctx.strokeStyle = rgba(colour, 0.16)
        ctx.lineWidth = 1.2
        ctx.stroke()
      }
      if (tier >= 4) {
        // Three ticks standing up out of the dot: a skyline, at this size.
        ctx.strokeStyle = rgba(colour, 0.5 * lit + 0.2)
        ctx.lineWidth = 1
        for (const [dx, h] of [
          [-radius, radius * 1.4],
          [0, radius * 2.1],
          [radius, radius * 1.1],
        ]) {
          ctx.beginPath()
          ctx.moveTo(x + dx, y)
          ctx.lineTo(x + dx, y - h)
          ctx.stroke()
        }
      }

      ctx.beginPath()
      ctx.arc(x, y, radius, 0, Math.PI * 2)
      ctx.fillStyle = rgba(colour, Math.min(1, 0.6 + strength))
      ctx.fill()
    }
  }

  function drawStar(ctx: CanvasRenderingContext2D, x: number, y: number, radius: number, fill: string) {
    ctx.beginPath()
    for (let i = 0; i < 10; i++) {
      const r = i % 2 === 0 ? radius : radius * 0.42
      const angle = (Math.PI / 5) * i - Math.PI / 2
      const px = x + Math.cos(angle) * r
      const py = y + Math.sin(angle) * r
      if (i === 0) ctx.moveTo(px, py)
      else ctx.lineTo(px, py)
    }
    ctx.closePath()
    ctx.fillStyle = fill
    ctx.fill()
  }

  /**
   * The fleet: everybody a source counted but could not place, drawn as a ship
   * at that source's anchorage.
   *
   * They are lit like cities — brighter on the night side, bigger with tier —
   * but cold rather than warm, and a hull rather than a dot, because the one
   * thing this must never say is "a country lives here".
   */
  function drawFleet(ctx: CanvasRenderingContext2D, sun: [number, number], now: number) {
    for (const { vessel, point, phase } of vessels) {
      if (!visible(point)) continue
      const projected = projection(point)
      if (!projected) continue

      const tier = vessel.tier?.index ?? 0
      const size = vesselSize(tier)
      const night = geoDistance(point, sun) > Math.PI / 2
      const lit = night ? 1 : 0.62
      const strength = (0.45 + tier * 0.1) * lit
      // The swell. Nothing here is going anywhere, so it is a rise and fall of
      // a fraction of the hull rather than a journey — and it stops entirely
      // for anyone who asked the page to hold still.
      const bob = reduced ? 0 : Math.sin((now / BOB_MS) * Math.PI * 2 + phase) * size * 0.16
      const x = projected[0]
      const y = projected[1] + bob

      const glowRadius = size * (night ? 3.4 : 2.4)
      const glow = ctx.createRadialGradient(x, y, 0, x, y, glowRadius)
      glow.addColorStop(0, rgba(VESSEL, Math.min(0.7, strength + 0.14)))
      glow.addColorStop(0.45, rgba(VESSEL, strength * 0.3))
      glow.addColorStop(1, rgba(VESSEL, 0))
      ctx.beginPath()
      ctx.arc(x, y, glowRadius, 0, Math.PI * 2)
      ctx.fillStyle = glow
      ctx.fill()

      const ink = rgba(VESSEL, Math.min(1, 0.62 + strength))
      ctx.save()
      ctx.translate(x, y)
      ctx.fillStyle = ink
      ctx.strokeStyle = ink
      ctx.lineWidth = Math.max(0.8, size * 0.16)
      ctx.lineCap = 'round'
      if (vesselKind(vessel.source) === 'rig') drawRig(ctx, size)
      else drawShip(ctx, size)

      // A waterline, so the hull sits *on* something.
      ctx.beginPath()
      ctx.moveTo(-size * 1.25, size * 0.6)
      ctx.lineTo(size * 1.25, size * 0.6)
      ctx.strokeStyle = rgba(VESSEL, 0.22 * lit + 0.08)
      ctx.lineWidth = Math.max(0.6, size * 0.1)
      ctx.stroke()
      ctx.restore()
    }
  }

  /** A hull, a mast and a sail, in as few strokes as read at 12 pixels. */
  function drawShip(ctx: CanvasRenderingContext2D, s: number) {
    ctx.beginPath()
    ctx.moveTo(-s, -s * 0.1)
    ctx.quadraticCurveTo(0, s * 0.8, s, -s * 0.1)
    ctx.closePath()
    ctx.fill()

    ctx.beginPath()
    ctx.moveTo(0, -s * 0.14)
    ctx.lineTo(0, -s * 1.55)
    ctx.stroke()

    ctx.beginPath()
    ctx.moveTo(s * 0.1, -s * 1.42)
    ctx.lineTo(s * 0.1, -s * 0.26)
    ctx.lineTo(s * 0.98, -s * 0.32)
    ctx.closePath()
    ctx.fill()
  }

  /**
   * A rig: a pontoon on legs with two derricks. Snapcraft gets one because a
   * platform in the North Sea is funnier than another boat, and because a rig
   * is exactly what an app store that reports no country is — parked offshore,
   * pumping numbers up from somewhere nobody names.
   */
  function drawRig(ctx: CanvasRenderingContext2D, s: number) {
    ctx.beginPath()
    ctx.rect(-s, s * 0.14, s * 2, s * 0.34)
    ctx.fill()

    ctx.beginPath()
    ctx.moveTo(-s * 0.62, s * 0.14)
    ctx.lineTo(-s * 0.62, -s * 0.42)
    ctx.moveTo(s * 0.62, s * 0.14)
    ctx.lineTo(s * 0.62, -s * 0.42)
    ctx.stroke()

    ctx.beginPath()
    ctx.rect(-s * 0.95, -s * 0.68, s * 1.9, s * 0.28)
    ctx.fill()

    ctx.beginPath()
    for (const dx of [-s * 0.45, s * 0.45]) {
      ctx.moveTo(dx - s * 0.32, -s * 0.68)
      ctx.lineTo(dx, -s * 1.55)
      ctx.lineTo(dx + s * 0.32, -s * 0.68)
    }
    ctx.stroke()
  }

  /**
   * A drop travelling to the capital: a comet head with a fading tail, bowed
   * away from the globe so it reads as flight rather than a scratch on the
   * surface. Samples behind the horizon are dropped, which is what makes an
   * arc slide over the edge of the world instead of through it.
   */
  function drawArcs(ctx: CanvasRenderingContext2D, centre: [number, number], now: number) {
    const alive: Arc[] = []

    for (const arc of arcs) {
      const t = Math.min(1, (now - arc.start) / arc.duration)
      const interpolate = geoInterpolate(arc.from, arc.to)

      const start = projection(arc.from)
      const end = projection(arc.to)
      if (!start || !end) continue

      // Bow the arc perpendicular to its chord, on the side facing away from
      // the middle of the disc, so it always lifts off the planet.
      const vx = end[0] - start[0]
      const vy = end[1] - start[1]
      const length = Math.hypot(vx, vy) || 1
      let px = -vy / length
      let py = vx / length
      const midX = (start[0] + end[0]) / 2 - centre[0]
      const midY = (start[1] + end[1]) / 2 - centre[1]
      if (px * midX + py * midY < 0) {
        px = -px
        py = -py
      }
      const bow = Math.min(scale * 0.4, Math.max(scale * 0.12, length * 0.35))

      const samples = 56
      const tail = Math.max(0, t - ARC_TAIL)
      let previous: [number, number] | null = null

      ctx.lineCap = 'round'
      for (let i = 0; i <= samples; i++) {
        const s = tail + ((t - tail) * i) / samples
        const point = interpolate(s)
        if (!visible(point)) {
          previous = null
          continue
        }
        const projected = projection(point)
        if (!projected) {
          previous = null
          continue
        }
        const lift = Math.sin(Math.PI * s) * bow
        const here: [number, number] = [projected[0] + px * lift, projected[1] + py * lift]

        if (previous) {
          const fade = i / samples
          ctx.beginPath()
          ctx.moveTo(previous[0], previous[1])
          ctx.lineTo(here[0], here[1])
          ctx.strokeStyle = arc.color
          ctx.globalAlpha = 0.12 + 0.78 * fade
          ctx.lineWidth = 1 + 2.6 * fade
          ctx.stroke()
        }
        previous = here
      }
      ctx.globalAlpha = 1

      // The head, while it is still on this side of the world.
      if (previous && t < 1) {
        const head = ctx.createRadialGradient(previous[0], previous[1], 0, previous[0], previous[1], 9)
        head.addColorStop(0, arc.color)
        head.addColorStop(1, 'rgba(0,0,0,0)')
        ctx.beginPath()
        ctx.arc(previous[0], previous[1], 9, 0, Math.PI * 2)
        ctx.fillStyle = head
        ctx.fill()
      }

      if (t >= 1 && !arc.landed) {
        arc.landed = true
        pulses = push(pulses, { at: arc.to, color: arc.color, start: now, duration: PULSE_MS }, MAX_PULSES)
        pulses = push(pulses, { at: arc.from, color: arc.color, start: now, duration: PULSE_MS }, MAX_PULSES)
      }
      // Keep the arc one tail-length past arrival so it fades out rather than
      // vanishing mid-air.
      if (now - arc.start < arc.duration * (1 + ARC_TAIL)) alive.push(arc)
    }

    arcs = alive
  }

  function drawPulses(ctx: CanvasRenderingContext2D, now: number) {
    const alive: Pulse[] = []

    for (const pulse of pulses) {
      const t = (now - pulse.start) / pulse.duration
      if (t >= 1) continue
      alive.push(pulse)
      if (!visible(pulse.at)) continue

      const projected = projection(pulse.at)
      if (!projected) continue
      const [x, y] = projected
      const eased = 1 - (1 - t) * (1 - t)
      const radius = 4 + eased * (pulse.label ? 46 : 26) * (scale / REFERENCE_SCALE)

      if (pulse.filled) {
        ctx.beginPath()
        ctx.arc(x, y, radius * 0.6, 0, Math.PI * 2)
        ctx.fillStyle = pulse.color
        ctx.globalAlpha = 0.5 * (1 - t)
        ctx.fill()
        ctx.globalAlpha = 1
      }

      ctx.beginPath()
      ctx.arc(x, y, radius, 0, Math.PI * 2)
      ctx.strokeStyle = pulse.color
      ctx.globalAlpha = 1 - t
      ctx.lineWidth = pulse.label ? 2 : 1.5
      ctx.stroke()
      ctx.globalAlpha = 1

      if (pulse.label) {
        ctx.font = '600 12px ui-sans-serif, system-ui, sans-serif'
        ctx.textAlign = 'center'
        ctx.globalAlpha = Math.min(1, 2 - 2 * t)
        ctx.fillStyle = pulse.color
        ctx.fillText(pulse.label, x, y - 16 - eased * 34)
        ctx.globalAlpha = 1
      }
    }

    pulses = alive
  }

  // ------------------------------------------------------------- the loop

  let raf = 0
  let lastFrame = 0

  function frame(now: number) {
    raf = requestAnimationFrame(frame)

    const coasting = Math.abs(velocity[0]) > 0.002 || Math.abs(velocity[1]) > 0.002
    const busy = arcs.length > 0 || pulses.length > 0 || dragging || turn !== null || coasting
    const budget = 1000 / (busy ? BUSY_FPS : IDLE_FPS)
    const elapsed = now - lastFrame
    if (elapsed < budget) return
    lastFrame = now

    if (turn) {
      const t = Math.min(1, (now - turn.start) / TURN_MS)
      const eased = t < 0.5 ? 2 * t * t : 1 - (-2 * t + 2) ** 2 / 2
      rotation = [
        turn.from[0] + shortestTurn(turn.from[0], turn.to[0]) * eased,
        turn.from[1] + (turn.to[1] - turn.from[1]) * eased,
      ]
      if (t >= 1) turn = null
    } else if (!dragging && (Math.abs(velocity[0]) > 0.002 || Math.abs(velocity[1]) > 0.002)) {
      const dt = Math.min(elapsed, 100)
      rotation = [rotation[0] + velocity[0] * dt, Math.max(-80, Math.min(80, rotation[1] + velocity[1] * dt))]
      const decay = Math.exp(-FLICK_DECAY * dt)
      velocity = [velocity[0] * decay, velocity[1] * decay]
    } else if (!reduced && !dragging && now > interactingUntil) {
      velocity = [0, 0]
      rotation = [rotation[0] + (SPIN * Math.min(elapsed, 100)) / 1000, rotation[1]]
    }

    draw(now)
  }

  // --------------------------------------------------------------- arrivals

  /**
   * A drop landed. Cursed news flashes red where it happened and goes no
   * further; everything else flies to the capital, and a first-ever country
   * founds itself with a ring and a label on the way.
   *
   * A drop with no country at all sails instead: it leaves from its source's
   * anchorage, which is where that source's people are already drawn. It used
   * to leave from nowhere — the globe simply ignored it — so a Flathub day was
   * invisible on the one screen that is about where your customers are.
   */
  function receive(drop: Drop) {
    // A vessel is never founded. It has no first day and no ring: it is
    // already out there the moment the source counts anybody at all.
    const founding = drop.kind === 'settlement' && drop.country !== ''
    const from = drop.country ? centroidOf(drop.country) : anchorageOf(drop.source)
    if (!from) return

    const now = performance.now()
    const colour = rarityColor(drop.rarity)

    if (drop.rarity === 'cursed') {
      pulses = push(pulses, { at: from, color: colour, start: now, duration: PULSE_MS * 1.3, filled: true }, MAX_PULSES)
      return
    }

    if (founding) {
      pulses = push(pulses, { at: from, color: colour, start: now, duration: FOUND_MS, label: 'New settlement' }, MAX_PULSES)
    }

    // With no capital there is nowhere to send it, so the drop arrives at the
    // sunlit top of the world instead — which is at least somewhere to look.
    const to = capitalPoint ?? ([rotation[0] * -1, 35] as [number, number])
    if (from[0] === to[0] && from[1] === to[1]) {
      pulses = push(pulses, { at: from, color: colour, start: now, duration: PULSE_MS }, MAX_PULSES)
      return
    }

    const launch = () => {
      arcs = push(
        arcs,
        {
          from,
          to,
          color: colour,
          start: performance.now(),
          // Reduced motion still shows where the drop came from; it just does
          // not make anyone watch it travel.
          duration: reduced ? 1 : ARC_MS,
          landed: false,
        },
        MAX_ARCS,
      )
    }

    // An arc from the far side would be invisible. In ambient mode (or on an
    // idle globe) turn the world to face it first; while someone is dragging,
    // the globe belongs to them.
    const behind = !visible(from)
    const maySteer = ambient || (!dragging && now > interactingUntil)
    if (behind && maySteer && !reduced) {
      turn = { from: rotation, to: [-from[0], Math.max(-55, Math.min(55, -from[1]))], start: now }
      setTimeout(launch, TURN_MS)
      return
    }
    launch()
  }

  // ------------------------------------------------------------- interaction

  function pointerDown(event: PointerEvent) {
    dragging = true
    interactingUntil = performance.now() + INTERACTION_PAUSE_MS
    dragFrom = { x: event.clientX, y: event.clientY, rotation: [...rotation] as [number, number] }
    lastMove = { x: event.clientX, y: event.clientY, t: performance.now() }
    velocity = [0, 0]
    turn = null
    ;(event.currentTarget as HTMLCanvasElement).setPointerCapture(event.pointerId)
  }

  function pointerMove(event: PointerEvent) {
    if (dragging && dragFrom) {
      // Degrees per pixel, so a small globe turns as far per swipe as a big one.
      const k = 90 / scale
      const yaw = dragFrom.rotation[0] + (event.clientX - dragFrom.x) * k
      const pitch = dragFrom.rotation[1] - (event.clientY - dragFrom.y) * k
      rotation = [yaw, Math.max(-80, Math.min(80, pitch))]
      const now = performance.now()
      if (lastMove) {
        const dt = Math.max(1, now - lastMove.t)
        // Blend so a jittery last sample doesn't decide the whole flick.
        velocity = [
          velocity[0] * 0.4 + (((event.clientX - lastMove.x) * k) / dt) * 0.6,
          velocity[1] * 0.4 + ((-(event.clientY - lastMove.y) * k) / dt) * 0.6,
        ]
      }
      lastMove = { x: event.clientX, y: event.clientY, t: now }
      interactingUntil = now + INTERACTION_PAUSE_MS
      turn = null
      hovered = hoveredShip = null
      return
    }
    trackHover(event)
  }

  function pointerUp(event: PointerEvent) {
    if (!dragging) return
    dragging = false
    dragFrom = null
    // A stale sample means the pointer paused before release: no flick.
    if (!lastMove || performance.now() - lastMove.t > 80) velocity = [0, 0]
    lastMove = null
    interactingUntil = performance.now() + INTERACTION_PAUSE_MS
    ;(event.currentTarget as HTMLCanvasElement).releasePointerCapture?.(event.pointerId)
  }

  /**
   * Finds the settlement or vessel nearest the pointer, if one is close
   * enough. Both are hunted in the same pass and the closer one wins, so a
   * ship anchored near a coast cannot steal its neighbour's tooltip.
   */
  function trackHover(event: PointerEvent) {
    const rect = canvas?.getBoundingClientRect()
    if (!rect) return
    const x = event.clientX - rect.left
    const y = event.clientY - rect.top

    let best: { country: HearthCountry; x: number; y: number } | null = null
    let bestShip: { vessel: HearthVessel; x: number; y: number } | null = null
    let bestDistance = Infinity
    for (const { country, point } of cities) {
      if (!visible(point)) continue
      const projected = projection(point)
      if (!projected) continue
      const distance = Math.hypot(projected[0] - x, projected[1] - y)
      // Bigger settlements are easier to hit, which is also what you want.
      const reach = Math.max(14, markerRadius(country.tier?.index ?? 0) * 3)
      if (distance < reach && distance < bestDistance) {
        bestDistance = distance
        best = { country, x: projected[0], y: projected[1] }
      }
    }
    for (const { vessel, point } of vessels) {
      if (!visible(point)) continue
      const projected = projection(point)
      if (!projected) continue
      const distance = Math.hypot(projected[0] - x, projected[1] - y)
      const reach = Math.max(16, vesselSize(vessel.tier?.index ?? 0) * 2.2)
      if (distance < reach && distance < bestDistance) {
        bestDistance = distance
        best = null
        bestShip = { vessel, x: projected[0], y: projected[1] }
      }
    }
    hovered = best
    hoveredShip = bestShip
  }

  function wheel(event: WheelEvent) {
    event.preventDefault()
    interactingUntil = performance.now() + INTERACTION_PAUSE_MS
    const next = scale * Math.exp(-event.deltaY * 0.0015)
    scale = Math.max(fitScale * 0.75, Math.min(fitScale * 3.5, next))
  }

  /** Double click puts the world back where it started, over the capital. */
  function reset() {
    scale = fitScale
    velocity = [0, 0]
    turn = {
      from: rotation,
      to: capitalPoint ? [-capitalPoint[0], -capitalPoint[1]] : [-20, -12],
      start: performance.now(),
    }
    interactingUntil = 0
  }

  // ---------------------------------------------------------------- lifecycle

  function resize() {
    if (!host || !canvas) return
    const rect = host.getBoundingClientRect()
    const nextWidth = Math.max(240, Math.round(rect.width))
    const nextHeight = Math.max(240, Math.round(rect.height))
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    if (nextWidth === width && nextHeight === height && canvas.width === Math.round(width * dpr)) return
    const zoom = fitScale ? scale / fitScale : 1
    width = nextWidth
    height = nextHeight

    canvas.width = Math.round(width * dpr)
    canvas.height = Math.round(height * dpr)
    canvas.style.width = `${width}px`
    canvas.style.height = `${height}px`
    const ctx = canvas.getContext('2d')
    ctx?.setTransform(dpr, 0, 0, dpr, 0, 0)

    fitScale = (Math.min(width, height) / 2) * 0.86
    scale = fitScale * zoom
    stars = makeStars()

    // Resizing the canvas clears it, and the render loop is throttled (or
    // paused entirely, in a background tab), so repaint now rather than
    // leaving a blank rectangle behind.
    draw(performance.now())
  }

  function makeStars() {
    if (!ambient) return []
    const count = Math.round((width * height) / 9_000)
    return Array.from({ length: count }, () => ({
      x: Math.random(),
      y: Math.random(),
      r: Math.random() * 1.1 + 0.2,
      a: Math.random() * 0.5 + 0.08,
    }))
  }

  $effect(() => {
    // Only host/canvas binding may (re)start the loop. Everything else the
    // setup touches — draw() reads `cities` and `hovered`, resize() reads
    // `ambient` — must be untracked, or every hover and every data poll would
    // re-run this effect: rotation snapped back to the capital, zoom reset,
    // and the starfield regenerated (which looked like TV static).
    const h = host
    const c = canvas
    if (!h || !c) return
    return untrack(() => {
      resize()

      const observer = new ResizeObserver(() => resize())
      observer.observe(h)
      // Svelte registers onwheel passively, which makes preventDefault a no-op
      // and lets the page scroll under the globe while zooming.
      c.addEventListener('wheel', wheel, { passive: false })

      // Start over the capital rather than mid-Pacific.
      const home = capitalPoint
      if (home) rotation = [-home[0], -home[1]]

      raf = requestAnimationFrame(frame)
      return () => {
        observer.disconnect()
        c.removeEventListener('wheel', wheel)
        cancelAnimationFrame(raf)
      }
    })
  })

  // The websocket is already open in the feed store; the globe just listens.
  $effect(() => loot.onDrop(receive))

  const hoveredName = $derived(hovered ? countryName(hovered.country.country) : '')
</script>

<div class="globe" bind:this={host} class:ambient>
  <canvas
    bind:this={canvas}
    onpointerdown={pointerDown}
    onpointermove={pointerMove}
    onpointerup={pointerUp}
    onpointercancel={pointerUp}
    onpointerleave={() => (hovered = hoveredShip = null)}
    ondblclick={reset}
    aria-label="A globe of every country you have sold in, and the fleet at sea"
  ></canvas>

  {#if hovered}
    <div
      class="tip"
      style="left: {hovered.x}px; top: {hovered.y}px"
      class:below={hovered.y < 140}
    >
      <div class="tip-head">
        <span class="flag">{flagEmoji(hovered.country.country)}</span>
        <span class="name">{hoveredName}</span>
        <span class="tier">{hovered.country.tier?.name ?? 'outpost'}</span>
      </div>
      <dl>
        <div><dt>Population</dt><dd>{integer(hovered.country.population)}</dd></div>
        <div><dt>Revenue</dt><dd>{currency(hovered.country.revenue_base, code)}</dd></div>
        <div><dt>Drops</dt><dd>{integer(hovered.country.drops)}</dd></div>
        <div><dt>First customer</dt><dd>{hovered.country.first_seen || '—'}</dd></div>
      </dl>
    </div>
  {/if}

  {#if hoveredShip}
    <div
      class="tip"
      style="left: {hoveredShip.x}px; top: {hoveredShip.y}px"
      class:below={hoveredShip.y < 160}
    >
      <div class="tip-head">
        <span class="flag">⚓</span>
        <span class="name">{vesselName(hoveredShip.vessel.source)}</span>
      </div>
      <p class="why">{vesselWhy(hoveredShip.vessel.source)}</p>
      <dl>
        <div><dt>Aboard</dt><dd>{integer(hoveredShip.vessel.population)}</dd></div>
        {#if hoveredShip.vessel.revenue_base}
          <div><dt>Revenue</dt><dd>{currency(hoveredShip.vessel.revenue_base, code)}</dd></div>
        {/if}
        <div><dt>Sailing since</dt><dd>{hoveredShip.vessel.first_seen || '—'}</dd></div>
      </dl>
    </div>
  {/if}
</div>

<style>
  .globe {
    position: relative;
    width: 100%;
    height: 100%;
    min-height: 320px;
  }

  canvas {
    display: block;
    /* The globe eats the gesture: a drag turns the world, it does not scroll. */
    touch-action: none;
    cursor: grab;
  }

  /*
   * On a phone the globe is most of the page, and a canvas that swallows every
   * swipe would strand the reader above the panel. Narrow screens keep the
   * vertical gesture for scrolling and give the globe the horizontal one —
   * which is the axis worth turning anyway.
   */
  @media (max-width: 900px) {
    canvas {
      touch-action: pan-y;
    }
  }

  canvas:active {
    cursor: grabbing;
  }

  .tip {
    position: absolute;
    z-index: 5;
    transform: translate(-50%, calc(-100% - 14px));
    min-width: 168px;
    padding: 0.5rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: color-mix(in oklab, var(--panel) 92%, transparent);
    backdrop-filter: blur(10px);
    box-shadow: 0 10px 30px rgb(0 0 0 / 45%);
    pointer-events: none;
    font-size: 0.75rem;
  }

  .tip.below {
    transform: translate(-50%, 14px);
  }

  .tip-head {
    display: flex;
    align-items: baseline;
    gap: 0.35rem;
    margin-bottom: 0.35rem;
  }

  .flag {
    font-size: 0.95rem;
  }

  .name {
    font-weight: 650;
  }

  .why {
    margin: 0 0 0.35rem;
    max-width: 210px;
    font-size: 0.66rem;
    line-height: 1.35;
    color: var(--text-faint);
  }

  .tier {
    margin-left: auto;
    font-size: 0.62rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--legendary);
  }

  dl {
    margin: 0;
    display: grid;
    gap: 0.1rem;
  }

  dl div {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
  }

  dt {
    color: var(--text-faint);
  }

  dd {
    margin: 0;
    font-variant-numeric: tabular-nums;
  }
</style>
