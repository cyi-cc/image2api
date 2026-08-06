<script setup>
// Vivid mark: an open spectrum ring (lens/aperture) around a folded "V", with a
// shutter dot closing the gap — image + video in one glyph. Transparent
// background so it reads on both the dark and light shells.
// Each instance gets unique gradient ids (useId) so multiple logos on one
// page don't share/clobber defs.
import { useId } from 'vue'

defineProps({ size: { type: [Number, String], default: 40 } })

const uid = useId()
const gRing = `vring-${uid}`
const gV = `vv-${uid}`
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 48 48" fill="none"
       xmlns="http://www.w3.org/2000/svg" class="shrink-0">
    <defs>
      <linearGradient :id="gRing" x1="6" y1="6" x2="42" y2="42" gradientUnits="userSpaceOnUse">
        <stop stop-color="#e879f9" />
        <stop offset="0.5" stop-color="#8b5cf6" />
        <stop offset="1" stop-color="#38bdf8" />
      </linearGradient>
      <linearGradient :id="gV" x1="14" y1="14" x2="34" y2="34" gradientUnits="userSpaceOnUse">
        <stop stop-color="#f0abfc" />
        <stop offset="1" stop-color="#8b5cf6" />
      </linearGradient>
    </defs>

    <g transform="translate(24 24)">
      <!-- open ring: dash gap leaves room for the shutter dot -->
      <circle r="18.5" fill="none" :stroke="`url(#${gRing})`" stroke-width="3.2"
              stroke-linecap="round" stroke-dasharray="86 30" transform="rotate(-58)" />
      <!-- folded V -->
      <path d="M-8 -7.5 L0 9 L8 -7.5" fill="none" :stroke="`url(#${gV})`" stroke-width="4.8"
            stroke-linecap="round" stroke-linejoin="round" />
      <circle cx="13" cy="-13" r="3.4" fill="#38bdf8" />
    </g>
  </svg>
</template>
