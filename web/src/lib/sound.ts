/**
 * Synthesized drop sounds.
 *
 * Everything here is generated with the Web Audio API — no audio files ship
 * with Loot. Each rarity gets a distinct gesture so you can identify a drop
 * without looking at the screen:
 *
 *   common     a soft sine blip
 *   uncommon   a rising two-note figure
 *   rare       a four-note major arpeggio
 *   epic       a filtered chord swell
 *   legendary  a long fanfare arpeggio drenched in reverb
 *   cursed     a low, detuned buzz that sags downward
 *
 * Browsers refuse to start an AudioContext before a user gesture, so the UI
 * shows a "click to enable sound" banner until `unlock()` succeeds.
 */
import type { Rarity } from './types'

/** Note frequencies in Hz. */
const A4 = 440
const C5 = 523.25
const D5 = 587.33
const E5 = 659.25
const G5 = 783.99
const A5 = 880
const CS5 = 554.37
const C6 = 1046.5
const E6 = 1318.51

export class DropSounds {
  private ctx: AudioContext | null = null
  private master: GainNode | null = null
  private reverb: ConvolverNode | null = null
  private reverbSend: GainNode | null = null

  /** True once an AudioContext exists and is running. */
  get ready(): boolean {
    return this.ctx !== null && this.ctx.state === 'running'
  }

  /**
   * Creates and resumes the AudioContext. Must be called from a user gesture
   * handler; returns false if the browser still refused.
   */
  async unlock(): Promise<boolean> {
    try {
      if (!this.ctx) this.build()
      if (this.ctx && this.ctx.state !== 'running') await this.ctx.resume()
      return this.ready
    } catch {
      return false
    }
  }

  /** Plays the sound for a rarity. A no-op until unlocked. */
  play(rarity: Rarity, volume = 1): void {
    if (!this.ctx || !this.master || this.ctx.state !== 'running') return

    const t = this.ctx.currentTime
    switch (rarity) {
      case 'common':
        this.blip(t, volume)
        break
      case 'uncommon':
        this.twoNote(t, volume)
        break
      case 'rare':
        this.arpeggio(t, [A4, CS5, E5, A5], 0.07, 'triangle', volume, 0.12)
        break
      case 'epic':
        this.chordSwell(t, volume)
        break
      case 'legendary':
        this.fanfare(t, volume)
        break
      case 'cursed':
        this.buzz(t, volume)
        break
    }
  }

  /** Sets the master output level (0 mutes without tearing down the context). */
  setVolume(v: number): void {
    if (!this.master || !this.ctx) return
    this.master.gain.setTargetAtTime(v, this.ctx.currentTime, 0.01)
  }

  close(): void {
    this.ctx?.close().catch(() => {})
    this.ctx = null
    this.master = null
  }

  // ---------------------------------------------------------------- internals

  private build(): void {
    const Ctor = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
    if (!Ctor) return

    const ctx = new Ctor()
    const master = ctx.createGain()
    master.gain.value = 0.9
    master.connect(ctx.destination)

    // A short synthesized impulse response gives the legendary fanfare its tail
    // without shipping an impulse file.
    const reverb = ctx.createConvolver()
    reverb.buffer = impulseResponse(ctx, 1.8, 2.5)
    const send = ctx.createGain()
    send.gain.value = 0
    send.connect(reverb)
    reverb.connect(master)

    this.ctx = ctx
    this.master = master
    this.reverb = reverb
    this.reverbSend = send
  }

  /** One oscillator with an ADSR-ish envelope, optionally sent to the reverb. */
  private voice(opts: {
    at: number
    freq: number
    type: OscillatorType
    duration: number
    peak: number
    attack?: number
    detune?: number
    reverb?: number
    filter?: { type: BiquadFilterType; from: number; to: number; q?: number }
    glideTo?: number
  }): void {
    const ctx = this.ctx
    const master = this.master
    if (!ctx || !master) return

    const { at, freq, type, duration, peak, attack = 0.005, detune = 0, reverb = 0, filter, glideTo } = opts

    const osc = ctx.createOscillator()
    osc.type = type
    osc.frequency.setValueAtTime(freq, at)
    if (detune) osc.detune.setValueAtTime(detune, at)
    if (glideTo) osc.frequency.exponentialRampToValueAtTime(glideTo, at + duration)

    const gain = ctx.createGain()
    gain.gain.setValueAtTime(0.0001, at)
    gain.gain.exponentialRampToValueAtTime(Math.max(peak, 0.0002), at + attack)
    gain.gain.exponentialRampToValueAtTime(0.0001, at + duration)

    let node: AudioNode = osc
    if (filter) {
      const biquad = ctx.createBiquadFilter()
      biquad.type = filter.type
      biquad.frequency.setValueAtTime(filter.from, at)
      biquad.frequency.exponentialRampToValueAtTime(filter.to, at + duration)
      if (filter.q) biquad.Q.value = filter.q
      osc.connect(biquad)
      node = biquad
    }

    node.connect(gain)
    gain.connect(master)

    if (reverb && this.reverbSend && this.reverb) {
      const wet = ctx.createGain()
      wet.gain.value = reverb
      gain.connect(wet)
      wet.connect(this.reverb)
    }

    osc.start(at)
    osc.stop(at + duration + 0.05)
  }

  private blip(t: number, v: number): void {
    this.voice({ at: t, freq: E5, type: 'sine', duration: 0.09, peak: 0.16 * v })
  }

  private twoNote(t: number, v: number): void {
    this.voice({ at: t, freq: D5, type: 'triangle', duration: 0.1, peak: 0.18 * v })
    this.voice({ at: t + 0.09, freq: A5, type: 'triangle', duration: 0.16, peak: 0.16 * v })
  }

  private arpeggio(
    t: number,
    notes: number[],
    step: number,
    type: OscillatorType,
    v: number,
    tail = 0.15,
    reverb = 0,
  ): void {
    notes.forEach((freq, i) => {
      const last = i === notes.length - 1
      this.voice({
        at: t + i * step,
        freq,
        type,
        duration: last ? tail + 0.2 : step + tail,
        peak: (last ? 0.2 : 0.16) * v,
        reverb,
      })
    })
  }

  private chordSwell(t: number, v: number): void {
    // A slow filter sweep over a stacked chord: the "something big happened" cue.
    const chord = [C5 / 2, E5 / 2, G5 / 2, C5]
    for (const freq of chord) {
      this.voice({
        at: t,
        freq,
        type: 'sawtooth',
        duration: 1.1,
        peak: 0.07 * v,
        attack: 0.22,
        detune: (Math.random() - 0.5) * 8,
        reverb: 0.25,
        filter: { type: 'lowpass', from: 320, to: 3800, q: 6 },
      })
    }
    this.voice({ at: t + 0.5, freq: C6, type: 'triangle', duration: 0.7, peak: 0.1 * v, reverb: 0.3 })
  }

  private fanfare(t: number, v: number): void {
    // Rising arpeggio, an octave double, and a long reverb tail.
    const notes = [C5, E5, G5, C6, E6]
    this.arpeggio(t, notes, 0.085, 'triangle', v, 0.5, 0.35)

    notes.forEach((freq, i) => {
      this.voice({
        at: t + i * 0.085,
        freq: freq / 2,
        type: 'square',
        duration: 0.3,
        peak: 0.05 * v,
        reverb: 0.2,
        filter: { type: 'lowpass', from: 1800, to: 700 },
      })
    })

    // Final held chord under the tail.
    for (const freq of [C6, E6, G5 * 2]) {
      this.voice({ at: t + 0.42, freq, type: 'triangle', duration: 1.5, peak: 0.08 * v, attack: 0.05, reverb: 0.5 })
    }
  }

  private buzz(t: number, v: number): void {
    // Two detuned saws sagging downward: unmistakably a loss.
    for (const detune of [-14, 12]) {
      this.voice({
        at: t,
        freq: 92,
        glideTo: 58,
        type: 'sawtooth',
        duration: 0.65,
        peak: 0.13 * v,
        attack: 0.02,
        detune,
        filter: { type: 'lowpass', from: 900, to: 220, q: 8 },
      })
    }
  }
}

/** Exponentially decaying noise: a cheap but convincing reverb impulse. */
function impulseResponse(ctx: AudioContext, seconds: number, decay: number): AudioBuffer {
  const rate = ctx.sampleRate
  const length = Math.max(1, Math.floor(rate * seconds))
  const buffer = ctx.createBuffer(2, length, rate)

  for (let channel = 0; channel < 2; channel++) {
    const data = buffer.getChannelData(channel)
    for (let i = 0; i < length; i++) {
      data[i] = (Math.random() * 2 - 1) * Math.pow(1 - i / length, decay)
    }
  }
  return buffer
}

export const sounds = new DropSounds()
