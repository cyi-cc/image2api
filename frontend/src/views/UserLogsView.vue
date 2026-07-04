<script setup>
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { api, generatedUrl, thumbUrl } from '../api'
import { fmtTs } from '../utils/format'
import { copyText } from '../utils/clipboard'
import Icon from '../components/Icon.vue'
import MediaLightbox from '../components/MediaLightbox.vue'

const router = useRouter()

const items = ref([])
const total = ref(0)
const loading = ref(false)
const kindFilter = ref('') // '', 'image', 'video'
const search = ref('')
const page = ref(1)
// 20 per page so a 4-col (lg) / 5-col (xl) grid lays out as clean rows,
// matching the admin 图片管理 (ImagesView) page.
const pageSize = 20
let timer = null

async function load() {
  loading.value = true
  // Server-side pagination over real media only: status=success + has_file=1
  // makes the row count == displayable count, so the numbered pager is accurate.
  // Failed/pending/file-pruned rows live in admin /admin/logs, never here.
  const qs = new URLSearchParams({
    limit: String(pageSize),
    offset: String((page.value - 1) * pageSize),
    status: 'success',
    has_file: '1',
    source: 'user', // 创作记录 = 画图台作品;排除 API(v1,无存储文件)+ 测试
  })
  if (kindFilter.value) qs.set('kind', kindFilter.value)
  const r = await api('/logs?' + qs.toString())
  items.value = (r.data?.data || []).filter((e) => e.status === 'success' && e.file)
  total.value = Number(r.data?.total ?? items.value.length)
  loading.value = false
}

// Search narrows the CURRENT page (same as the admin 日志 page); the numbered
// pager still reflects the full server-side total.
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return items.value
  return items.value.filter((e) =>
    (e.model || '').toLowerCase().includes(q) ||
    (e.prompt || '').toLowerCase().includes(q),
  )
})

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))
function setKind(v) { kindFilter.value = v; page.value = 1; load() }
function goPage(n) {
  const target = Math.max(1, Math.min(totalPages.value, n))
  if (target === page.value) return
  page.value = target
  load()
}
const pageNumbers = computed(() => {
  const n = totalPages.value
  const cur = page.value
  if (n <= 7) return Array.from({ length: n }, (_, i) => i + 1)
  const want = new Set([1, n, cur - 1, cur, cur + 1])
  if (cur <= 3) { want.add(2); want.add(3); want.add(4) }
  if (cur >= n - 2) { want.add(n - 1); want.add(n - 2); want.add(n - 3) }
  const list = [...want].filter((x) => x >= 1 && x <= n).sort((a, b) => a - b)
  const out = []
  for (let i = 0; i < list.length; i++) {
    if (i > 0 && list[i] - list[i - 1] > 1) out.push(null)
    out.push(list[i])
  }
  return out
})

function fmtMs(ms) {
  if (!ms) return ''
  if (ms < 1000) return ms + 'ms'
  return (ms / 1000).toFixed(1) + 's'
}


async function copyLink(name) {
  const u = generatedUrl(name)
  const ok = await copyText(u.startsWith('http') ? u : location.origin + u)
  toast.value = ok ? '链接已复制' : '复制失败'
  setTimeout(() => (toast.value = ''), 1500)
}

async function copyImage(url) {
  try {
    const blob = await (await fetch(url)).blob()
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
    toast.value = '图片已复制'
  } catch {
    toast.value = '复制失败'
  }
  setTimeout(() => (toast.value = ''), 1500)
}

async function copyPrompt(e) {
  if (!e.prompt) return
  const ok = await copyText(e.prompt)
  toast.value = ok ? '指令已复制' : '复制失败'
  setTimeout(() => (toast.value = ''), 1500)
}

const toast = ref('')
const lightbox = ref(null)
// Videos whose first-frame thumbnail is missing (old videos) — fall back to
// the muted <video> preview for those cards.
const thumbFail = reactive({})
function onKey(e) { if (e.key === 'Escape') lightbox.value = null }

onMounted(() => {
  load()
  timer = setInterval(load, 3000)
  window.addEventListener('keydown', onKey)
})
onUnmounted(() => {
  clearInterval(timer)
  window.removeEventListener('keydown', onKey)
})
</script>

<template>
  <section class="space-y-5">
    <!-- Header -->
    <div class="flex items-end justify-between flex-wrap gap-3">
      <div>
        <h1 class="text-2xl font-semibold tracking-tight text-slate-900">我的创作记录</h1>
        <p class="text-sm text-slate-500 mt-1">
          {{ total }} 条作品
        </p>
      </div>
      <button @click="router.push('/user')" class="btn-primary">
        <Icon name="spark" class="w-4 h-4" /> 去画图
      </button>
    </div>

    <!-- Filter bar -->
    <div class="card p-3 flex items-center gap-3 flex-wrap">
      <div class="flex items-center gap-1.5">
        <button @click="setKind('')" class="text-xs rounded-lg px-2.5 py-1.5 transition-colors"
                :class="kindFilter === '' ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">全部</button>
        <button @click="setKind('image')" class="text-xs rounded-lg px-2.5 py-1.5 transition-colors"
                :class="kindFilter === 'image' ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">图像</button>
        <button @click="setKind('video')" class="text-xs rounded-lg px-2.5 py-1.5 transition-colors"
                :class="kindFilter === 'video' ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">视频</button>
      </div>
      <div class="flex-1 min-w-[180px]">
        <input v-model="search" class="field !py-1.5 text-xs" placeholder="搜索提示词或模型…" />
      </div>
    </div>

    <!-- Empty -->
    <div v-if="loading && !items.length" class="card text-center text-sm text-slate-400 py-24">加载中…</div>
    <div v-else-if="!filtered.length"
         class="card flex flex-col items-center gap-3 text-slate-400 py-24">
      <span class="w-14 h-14 rounded-2xl bg-slate-100 grid place-items-center"><Icon name="spark" class="w-6 h-6" /></span>
      <span class="text-sm">还没有创作记录</span>
      <button @click="router.push('/user')" class="btn-soft mt-2">开始第一张</button>
    </div>

    <!-- Cards — gallery layout, matching 图片管理 (ImagesView) -->
    <div v-else class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 gap-3">
      <div v-for="e in filtered" :key="e.id"
           class="group relative rounded-xl overflow-hidden ring-1 ring-slate-200 bg-slate-100 aspect-[4/5]"
           :class="(e.status === 'success' && e.file) && 'cursor-zoom-in'"
           @click="(e.status === 'success' && e.file) && (lightbox = e)">
        <!-- media -->
        <template v-if="e.status === 'success' && e.file">
          <!-- first-frame still as background-image (same as image cards); the
               hidden probe img flips to the <video> fallback for old videos. -->
          <div v-if="e.kind === 'video' && !thumbFail[e.id]" :style="{ backgroundImage: `url(${thumbUrl(e.file)})` }"
               class="absolute inset-0 w-full h-full bg-cover bg-center transition-transform duration-300 group-hover:scale-105">
            <img :src="thumbUrl(e.file)" class="hidden" @error="thumbFail[e.id] = true" />
          </div>
          <video v-else-if="e.kind === 'video'" :src="generatedUrl(e.file)" muted loop preload="metadata"
                 class="absolute inset-0 w-full h-full object-cover"
                 @mouseenter="$event.target.play && $event.target.play()"
                 @mouseleave="$event.target.pause && $event.target.pause()" />
          <!-- background-image (not <img>) so Edge shows no 视觉搜索 overlay icon. -->
          <div v-else :style="{ backgroundImage: `url(${thumbUrl(e.file)})` }"
               class="absolute inset-0 w-full h-full bg-cover bg-center transition-transform duration-300 group-hover:scale-105"></div>
          <div class="absolute inset-x-0 bottom-0 h-1/2 bg-gradient-to-t from-black/85 via-black/40 to-transparent pointer-events-none"></div>
        </template>
        <!-- pending / failed placeholders -->
        <div v-else-if="e.status === 'pending'" class="absolute inset-0 grid place-items-center text-slate-400 text-xs">
          <div class="flex flex-col items-center gap-2">
            <span class="w-10 h-10 rounded-xl bg-white grid place-items-center animate-pulse"><Icon name="spark" class="w-4 h-4" /></span>
            生成中…
          </div>
        </div>
        <div v-else class="absolute inset-0 grid place-items-center text-rose-500 text-xs px-4 text-center">
          <div>
            <Icon name="close" class="w-6 h-6 mx-auto mb-2 opacity-60" />
            <div>生成失败</div>
            <div v-if="e.error" class="text-[10px] text-rose-400 line-clamp-2 mt-1">{{ e.error }}</div>
          </div>
        </div>

        <!-- kind chip -->
        <span class="absolute top-3 left-3 inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium uppercase tracking-wider ring-1"
              :class="e.kind === 'video' ? 'bg-fuchsia-500/20 text-fuchsia-200 ring-fuchsia-400/30' : 'bg-indigo-500/20 text-indigo-200 ring-indigo-400/30'">
          {{ e.kind === 'video' ? '视频' : '图像' }}
        </span>

        <!-- hover actions (only when there's a file) -->
        <div v-if="e.status === 'success' && e.file"
             class="absolute top-3 right-3 flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
          <button v-if="e.kind !== 'video'" @click.stop.prevent="copyImage(generatedUrl(e.file))" title="复制图片"
                  class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center">
            <Icon name="copy" class="w-3.5 h-3.5" />
          </button>
          <a :href="generatedUrl(e.file)" :download="e.file.split('/').pop()" @click.stop title="下载"
             class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center">
            <Icon name="download" class="w-3.5 h-3.5" />
          </a>
        </div>

        <!-- caption (over a real image) -->
        <div v-if="e.status === 'success' && e.file" class="absolute inset-x-0 bottom-0 p-3 pointer-events-none">
          <div class="text-[12px] leading-tight text-white font-medium line-clamp-2 mb-1 transition-colors"
               :class="e.prompt ? 'pointer-events-auto cursor-pointer hover:text-white/75' : ''"
               :title="e.prompt ? '点击复制提示词' : ''"
               @click.stop="copyPrompt(e)">{{ e.prompt }}</div>
          <div class="text-[10px] text-white/55 flex items-center justify-between gap-2 tabular-nums">
            <span class="break-all" :title="e.model || ''">{{ e.model || '—' }}</span>
            <span class="shrink-0 flex items-center gap-1">
              <span v-if="e.resolution" class="text-emerald-300/90">{{ e.resolution }}</span>
              <span v-if="e.ratio" class="text-white/40">{{ e.ratio }}</span>
              <span v-if="e.kind === 'video' && e.duration" class="text-fuchsia-300/80">{{ e.duration }}</span>
            </span>
          </div>
          <div class="text-[10px] text-white/35 mt-0.5 tabular-nums">{{ fmtTs(e.ts) }}<span v-if="e.elapsed_ms"> · {{ fmtMs(e.elapsed_ms) }}</span></div>
        </div>
      </div>
    </div>

    <!-- Pagination — its own card, exactly like 图片管理 (ImagesView) -->
    <div v-if="total && totalPages > 1" class="card !p-3 flex items-center justify-between gap-3">
      <div class="text-xs text-slate-500 tabular-nums px-2">
        <span class="text-slate-700">{{ (page - 1) * pageSize + 1 }}–{{ Math.min(total, page * pageSize) }}</span>
        / {{ total }} 张
      </div>
      <div class="flex items-center gap-1">
        <template v-for="(n, i) in pageNumbers" :key="i">
          <span v-if="n === null" class="px-1 text-slate-300">…</span>
          <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
        </template>
      </div>
    </div>

    <!-- Lightbox (shared component) -->
    <MediaLightbox
      v-if="lightbox"
      :src="generatedUrl(lightbox.file)"
      :kind="lightbox.kind"
      :prompt="lightbox.prompt"
      :meta="[lightbox.model, lightbox.ratio, lightbox.resolution, lightbox.duration, fmtMs(lightbox.elapsed_ms)].filter(Boolean).join(' · ')"
      :download-name="lightbox.file"
      @close="lightbox = null" />

    <!-- Toast -->
    <transition name="fade">
      <div v-if="toast"
           class="fixed bottom-6 left-1/2 -translate-x-1/2 z-[60] bg-slate-900 text-white text-xs px-4 py-2 rounded-lg shadow-lg">
        {{ toast }}
      </div>
    </transition>
  </section>
</template>

<style scoped>
.line-clamp-2 { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

/* Numbered pagination buttons — light-theme twin of the admin .pg */
.pg {
  min-width: 1.75rem;
  padding: 0.3rem 0.55rem;
  font-size: 0.72rem;
  font-weight: 500;
  text-align: center;
  border-radius: 0.45rem;
  color: rgb(71 85 105);
  background: rgb(241 245 249);
  box-shadow: inset 0 0 0 1px rgb(15 23 42 / 0.06);
  transition: background 0.15s, color 0.15s, box-shadow 0.15s;
}
.pg:hover:not(.pg-on) { background: rgb(226 232 240); color: rgb(15 23 42); }
.pg-on { background: rgb(15 23 42); color: white; box-shadow: none; }
</style>
