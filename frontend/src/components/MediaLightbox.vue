<script setup>
// Full-screen preview: the enlarged image/video on a dark backdrop, plus a
// small top-right action row (copy-to-clipboard for images, download for
// both). Click the dark area (or Esc, handled by the parent) to close.
import { ref, watch } from 'vue'
import Icon from './Icon.vue'

const props = defineProps({
  src: { type: String, required: true },     // resolved media URL
  kind: { type: String, default: 'image' },  // 'image' | 'video'
  prompt: { type: String, default: '' },
  meta: { type: String, default: '' },
  metaSub: { type: String, default: '' },
  downloadName: { type: String, default: '' },
})
const emit = defineEmits(['close'])

// Render the image as a CSS background (not <img>) so Edge/Bing shows no
// "visual search" hover icon. Load it to learn its aspect ratio, then size the
// div to fit the viewport while preserving ratio — mirrors object-contain.
const imgRatio = ref(1)
watch(() => props.src, (src) => {
  if (props.kind !== 'image' || !src) return
  const im = new Image()
  im.onload = () => { if (im.naturalHeight) imgRatio.value = im.naturalWidth / im.naturalHeight }
  im.src = src
}, { immediate: true })

const toast = ref('')
let toastTimer = null
function flash(msg) {
  toast.value = msg
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toast.value = ''), 1800)
}

async function copyImage() {
  try {
    const blob = await (await fetch(props.src)).blob()
    const pngBlob = blob.type === 'image/png'
      ? blob
      : await new Promise((resolve, reject) => {
          createImageBitmap(blob).then((bitmap) => {
            const canvas = document.createElement('canvas')
            canvas.width = bitmap.width
            canvas.height = bitmap.height
            const ctx = canvas.getContext('2d')
            if (!ctx) { reject(new Error('no canvas ctx')); return }
            ctx.drawImage(bitmap, 0, 0)
            canvas.toBlob((out) => {
              if (out) resolve(out)
              else reject(new Error('png convert failed'))
            }, 'image/png')
          }).catch(reject)
        })
    await navigator.clipboard.write([new ClipboardItem({ 'image/png': pngBlob })])
    flash('图片已复制')
  } catch {
    flash('复制失败')
  }
}
</script>

<template>
  <!-- Teleport to <body> so the overlay escapes the layout's `main` (relative
       z-10) stacking context — otherwise the fixed z-index sits BELOW the
       root-level sidebar (z-30) and the logo pokes through the backdrop. -->
  <Teleport to="body">
  <transition name="lb-fade" appear>
    <div class="fixed inset-0 z-[100] bg-black/90 flex items-center justify-center p-4"
         @click.self="emit('close')">
      <video v-if="kind === 'video'" :src="src" controls autoplay
             class="max-h-[94vh] max-w-[96vw] rounded-lg"
             controlslist="nodownload noremoteplayback noplaybackrate"
             disablepictureinpicture disableremoteplayback></video>
      <div v-else
           :style="{ width: `min(96vw, calc(94vh * ${imgRatio}))`, aspectRatio: imgRatio, backgroundImage: `url(${src})` }"
           class="rounded-lg bg-contain bg-center bg-no-repeat"></div>

      <!-- actions: copy (images only) + download -->
      <div class="absolute top-4 right-4 flex gap-2">
        <button v-if="kind !== 'video'" @click.stop="copyImage" title="复制图片"
                class="w-9 h-9 rounded-lg bg-black/60 ring-1 ring-white/15 hover:bg-black/80 text-white grid place-items-center">
          <Icon name="copy" class="w-4 h-4" />
        </button>
        <a :href="src" :download="downloadName || src.split('/').pop()" @click.stop title="下载"
           class="w-9 h-9 rounded-lg bg-black/60 ring-1 ring-white/15 hover:bg-black/80 text-white grid place-items-center">
          <Icon name="download" class="w-4 h-4" />
        </a>
      </div>

      <div v-if="toast"
           class="absolute bottom-6 left-1/2 -translate-x-1/2 bg-slate-900 text-white text-xs px-4 py-2 rounded-lg shadow-lg">
        {{ toast }}
      </div>
    </div>
  </transition>
  </Teleport>
</template>

<style scoped>
.lb-fade-enter-active, .lb-fade-leave-active { transition: opacity 0.18s ease; }
.lb-fade-enter-from, .lb-fade-leave-to { opacity: 0; }
</style>
