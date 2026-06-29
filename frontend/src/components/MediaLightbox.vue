<script setup>
// Shared full-screen preview for a generated image/video. One look across the
// admin (图片管理 / 日志) and the user-facing (画图记录) surfaces. Parent controls
// mount via v-if and passes the resolved media URL + meta; the component owns the
// overlay shell, image/video element, prompt + meta block, and action buttons.
import { ref } from 'vue'
import { copyText } from '../utils/clipboard'
import Icon from './Icon.vue'

const props = defineProps({
  src: { type: String, required: true },     // resolved media URL (generatedUrl)
  kind: { type: String, default: 'image' },  // 'image' | 'video'
  prompt: { type: String, default: '' },
  meta: { type: String, default: '' },        // primary meta line (mono)
  metaSub: { type: String, default: '' },     // optional second meta line
  downloadName: { type: String, default: '' },
})
const emit = defineEmits(['close'])

const toast = ref('')
let toastTimer = null
async function copyPrompt() {
  if (!props.prompt) return
  toast.value = (await copyText(props.prompt)) ? '指令已复制' : '复制失败'
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toast.value = ''), 1800)
}
</script>

<template>
  <transition name="lb-fade" appear>
    <div class="media-card fixed inset-0 z-50 bg-slate-950/85 backdrop-blur-sm flex items-center justify-center p-6"
         @click.self="emit('close')">
      <div v-if="toast"
           class="fixed bottom-6 left-1/2 -translate-x-1/2 z-[60] bg-slate-900 text-white text-xs px-4 py-2 rounded-lg shadow-lg ring-1 ring-white/10">
        {{ toast }}
      </div>
      <!-- Wrapper shrinks to the media's rendered width, so the info row below
           lines up flush with the image's left & right edges (one clean column). -->
      <div class="flex flex-col max-h-full max-w-full">
        <video v-if="kind === 'video'" :src="src" controls autoplay
               class="max-h-[76vh] max-w-[88vw] rounded-xl shadow-2xl object-contain"></video>
        <img v-else :src="src" class="max-h-[76vh] max-w-[88vw] rounded-xl shadow-2xl object-contain" />

        <div class="mt-3 flex items-start justify-between gap-4 text-white">
          <div class="min-w-0 flex-1">
            <div v-if="prompt" @click="copyPrompt" title="点击复制提示词"
                 class="text-sm font-medium leading-snug line-clamp-3 break-words cursor-pointer transition-colors hover:text-white/75">{{ prompt }}</div>
            <div v-if="meta" class="text-xs text-white/60 mt-1 font-mono break-all">{{ meta }}</div>
            <div v-if="metaSub" class="text-xs text-white/45 mt-1">{{ metaSub }}</div>
          </div>
          <div class="flex items-center gap-2 shrink-0">
            <a :href="src" target="_blank"
               class="inline-flex items-center gap-1.5 rounded-lg bg-white/10 hover:bg-white/20 px-3 py-1.5 text-xs transition-colors">
              <Icon name="open" class="w-3.5 h-3.5" /> 原图
            </a>
            <a :href="src" :download="downloadName"
               class="inline-flex items-center gap-1.5 rounded-lg bg-white text-slate-900 hover:bg-slate-100 px-3 py-1.5 text-xs font-medium transition-colors">
              <Icon name="download" class="w-3.5 h-3.5" /> 下载
            </a>
            <button @click="emit('close')"
               class="w-8 h-8 rounded-lg bg-white/10 hover:bg-white/20 grid place-items-center transition-colors">
              <Icon name="close" class="w-4 h-4" />
            </button>
          </div>
        </div>
      </div>
    </div>
  </transition>
</template>

<style scoped>
.lb-fade-enter-active, .lb-fade-leave-active { transition: opacity 0.18s ease; }
.lb-fade-enter-from, .lb-fade-leave-to { opacity: 0; }
</style>
