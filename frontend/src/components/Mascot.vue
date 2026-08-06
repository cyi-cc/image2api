<script setup>
// Vivi — the platform's original chibi mascot. Pure inline SVG (no raster
// asset, no external dependency), so it scales crisply and inherits the
// pink/violet/sky brand palette. `size` sets the square px box; the halo of
// twin-tails + a camera-lens hair ring ties her to the B-ring logo.
import { computed } from 'vue'
const props = defineProps({
  size: { type: [Number, String], default: 160 },
  float: { type: Boolean, default: true },
  uid: { type: String, default: () => 'm' + Math.random().toString(36).slice(2, 8) },
})
const px = computed(() => (typeof props.size === 'number' ? props.size + 'px' : props.size))
const g = (k) => `${props.uid}-${k}`
</script>

<template>
  <svg :width="px" :height="px" viewBox="0 0 200 200" fill="none"
       :class="['mascot', { 'mascot-float': float }]" aria-hidden="true">
    <defs>
      <linearGradient :id="g('hair')" x1="0" y1="0" x2="1" y2="1">
        <stop offset="0" stop-color="#f0abfc" />
        <stop offset="0.55" stop-color="#c084fc" />
        <stop offset="1" stop-color="#818cf8" />
      </linearGradient>
      <linearGradient :id="g('eye')" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stop-color="#7dd3fc" />
        <stop offset="1" stop-color="#a855f7" />
      </linearGradient>
      <linearGradient :id="g('ring')" x1="0" y1="0" x2="1" y2="1">
        <stop offset="0" stop-color="#f472b6" />
        <stop offset="0.5" stop-color="#a78bfa" />
        <stop offset="1" stop-color="#38bdf8" />
      </linearGradient>
      <radialGradient :id="g('glow')" cx="0.5" cy="0.5" r="0.5">
        <stop offset="0" stop-color="#e879f9" stop-opacity="0.55" />
        <stop offset="1" stop-color="#e879f9" stop-opacity="0" />
      </radialGradient>
    </defs>

    <!-- soft aura -->
    <circle cx="100" cy="104" r="86" :fill="`url(#${g('glow')})`" />

    <!-- back twin-tail -->
    <path d="M56 78 C22 84 20 140 44 168 C58 150 60 120 74 104 Z" :fill="`url(#${g('hair')})`" opacity="0.92" />
    <path d="M144 78 C178 84 180 140 156 168 C142 150 140 120 126 104 Z" :fill="`url(#${g('hair')})`" opacity="0.92" />

    <!-- hair back mass -->
    <path d="M100 34 C60 34 44 66 46 100 C47 116 56 128 62 132 L138 132 C144 128 153 116 154 100 C156 66 140 34 100 34 Z"
          :fill="`url(#${g('hair')})`" />

    <!-- face -->
    <path d="M64 92 C64 66 80 52 100 52 C120 52 136 66 136 92 C136 118 120 134 100 134 C80 134 64 118 64 92 Z"
          fill="#fff5fb" />
    <!-- fringe -->
    <path d="M62 92 C60 66 78 50 100 50 C122 50 140 66 138 92 C132 78 120 74 118 90 C112 74 100 72 100 72 C100 72 88 74 82 90 C80 74 68 78 62 92 Z"
          :fill="`url(#${g('hair')})`" />

    <!-- ears blush -->
    <circle cx="76" cy="106" r="7" fill="#fbb6ce" opacity="0.75" />
    <circle cx="124" cy="106" r="7" fill="#fbb6ce" opacity="0.75" />

    <!-- eyes -->
    <g>
      <ellipse cx="83" cy="98" rx="9" ry="12" :fill="`url(#${g('eye')})`" />
      <ellipse cx="117" cy="98" rx="9" ry="12" :fill="`url(#${g('eye')})`" />
      <circle cx="85.5" cy="93.5" r="3.2" fill="#fff" />
      <circle cx="119.5" cy="93.5" r="3.2" fill="#fff" />
      <circle cx="81" cy="101" r="1.6" fill="#fff" opacity="0.85" />
      <circle cx="115" cy="101" r="1.6" fill="#fff" opacity="0.85" />
    </g>
    <!-- brows / smile -->
    <path d="M74 84 q9 -5 18 -1" stroke="#c084fc" stroke-width="2.4" stroke-linecap="round" />
    <path d="M108 83 q9 -4 18 1" stroke="#c084fc" stroke-width="2.4" stroke-linecap="round" />
    <path d="M92 114 q8 7 16 0" stroke="#f472b6" stroke-width="2.6" stroke-linecap="round" fill="none" />

    <!-- camera-lens hair ring (echoes the logo) -->
    <g transform="translate(140 58)">
      <circle r="15" fill="none" :stroke="`url(#${g('ring')})`" stroke-width="4"
              stroke-linecap="round" stroke-dasharray="70 24" transform="rotate(-55)" />
      <circle r="5.5" fill="#38bdf8" />
      <circle r="2.2" fill="#fff" opacity="0.9" />
    </g>

    <!-- sparkles -->
    <g fill="#fef08a">
      <path d="M46 60 l2.2 5 5 2.2 -5 2.2 -2.2 5 -2.2 -5 -5 -2.2 5 -2.2 Z" />
      <path d="M158 128 l1.6 3.6 3.6 1.6 -3.6 1.6 -1.6 3.6 -1.6 -3.6 -3.6 -1.6 3.6 -1.6 Z" />
    </g>
    <circle cx="52" cy="150" r="2.4" fill="#7dd3fc" />
    <circle cx="150" cy="44" r="2" fill="#f0abfc" />
  </svg>
</template>

<style scoped>
.mascot { display: block; }
.mascot-float { animation: mascotFloat 5s ease-in-out infinite; transform-origin: center; }
@keyframes mascotFloat {
  0%, 100% { transform: translateY(0) rotate(-1deg); }
  50% { transform: translateY(-8px) rotate(1.5deg); }
}
@media (prefers-reduced-motion: reduce) { .mascot-float { animation: none; } }
</style>
