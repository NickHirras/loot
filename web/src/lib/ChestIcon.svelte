<script lang="ts">
  /**
   * A treasure chest drawn as SVG so it can be animated, tinted and scaled
   * without shipping an asset. The lid is a separate group rotating about its
   * hinge, which is the whole trick.
   */
  let {
    size = 22,
    open = false,
    idle = false,
    glow = false,
  }: { size?: number; open?: boolean; idle?: boolean; glow?: boolean } = $props()
</script>

<svg
  class="chest"
  class:open
  class:idle
  class:glow
  width={size}
  height={size * (52 / 64)}
  viewBox="0 0 64 52"
  fill="none"
  aria-hidden="true"
>
  <!-- Body -->
  <path class="body" d="M6 24h52a2 2 0 0 1 2 2v20a4 4 0 0 1-4 4H8a4 4 0 0 1-4-4V26a2 2 0 0 1 2-2Z" />
  <path class="band" d="M4 34h56" />
  <rect class="lock" x="27" y="29" width="10" height="12" rx="2" />
  <circle class="keyhole" cx="32" cy="34" r="1.6" />

  <!-- Lid: rotates about the hinge at the back-left of the body. -->
  <g class="lid">
    <path class="body" d="M6 24a26 18 0 0 1 52 0 2 2 0 0 1-2 2H8a2 2 0 0 1-2-2Z" />
    <path class="band" d="M32 7v17" />
    <rect class="clasp" x="28" y="19" width="8" height="8" rx="2" />
  </g>

  <!-- Loot spilling out, only while open. -->
  <g class="spill">
    <circle cx="20" cy="20" r="2.2" />
    <circle cx="32" cy="15" r="2.8" />
    <circle cx="44" cy="21" r="2" />
  </g>
</svg>

<style>
  .chest {
    display: block;
    overflow: visible;
  }

  .body {
    fill: color-mix(in oklab, var(--chest, var(--legendary)) 22%, #10141d);
    stroke: var(--chest, var(--legendary));
    stroke-width: 3;
    stroke-linejoin: round;
  }

  .band,
  .lock,
  .clasp,
  .keyhole {
    stroke: var(--chest, var(--legendary));
    stroke-width: 3;
    fill: none;
    stroke-linecap: round;
  }

  .lock,
  .clasp {
    fill: #10141d;
  }

  .keyhole {
    fill: var(--chest, var(--legendary));
    stroke: none;
  }

  .lid {
    transform-origin: 6px 26px;
    transform-box: view-box;
    transition: transform 0.7s cubic-bezier(0.34, 1.4, 0.5, 1);
  }

  .open .lid {
    transform: rotate(-42deg) translateY(-2px);
  }

  .spill {
    fill: var(--legendary);
    opacity: 0;
  }

  .open .spill {
    animation: spill 0.9s 0.25s ease-out both;
  }

  .glow {
    filter: drop-shadow(0 0 6px color-mix(in oklab, var(--chest, var(--legendary)) 55%, transparent));
  }

  .idle {
    animation: wobble 3.4s ease-in-out infinite;
    transform-origin: 50% 90%;
  }

  @keyframes wobble {
    0%,
    72%,
    100% {
      transform: rotate(0deg);
    }
    78% {
      transform: rotate(-5deg);
    }
    84% {
      transform: rotate(4deg);
    }
    90% {
      transform: rotate(-2deg);
    }
  }

  @keyframes spill {
    0% {
      opacity: 0;
      transform: translateY(6px) scale(0.4);
    }
    45% {
      opacity: 1;
    }
    100% {
      opacity: 0.9;
      transform: translateY(-2px) scale(1);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .idle {
      animation: none;
    }
    .spill {
      opacity: 0.9;
    }
    .open .spill {
      animation: none;
    }
  }
</style>
