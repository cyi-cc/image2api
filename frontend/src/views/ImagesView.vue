<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { api, generatedUrl } from '../api'
import { fmtTs, fmtSize } from '../utils/format'
import { copyText } from '../utils/clipboard'
import Icon from '../components/Icon.vue'
import MediaLightbox from '../components/MediaLightbox.vue'

const items = ref([])
const total = ref(0)
const stats = ref({ total: 0, image: 0, video: 0, size_bytes: 0 })
const loading = ref(false)
const kind = ref('')          // '' | 'image' | 'video'
const selected = ref(null)
const toast = ref('')

const page = ref(1)
// 20 per page so a 4-col (lg) or 5-col (xl) grid lays out as clean rows of
// 5×4 or 4×5 instead of a half-empty trailing row.
const pageSize = ref(20)


async function load() {
  loading.value = true
  const qs = new URLSearchParams({
    limit: String(pageSize.value),
    offset: String((page.value - 1) * pageSize.value),
  })
  if (kind.value) qs.set('kind', kind.value)
  const r = await api('/images?' + qs.toString())
  items.value = r.data?.data || []
  total.value = Number(r.data?.total ?? items.value.length)
  // Stats arrive with the same payload so the KPI strip stays cheap.
  stats.value = r.data?.stats || { total: 0, image: 0, video: 0, size_bytes: 0 }
  loading.value = false
}

function absUrl(name) {
  const u = generatedUrl(name)
  return u.startsWith('http') ? u : location.origin + u
}

async function copyLink(name) {
  flash(await copyText(absUrl(name)) ? '链接已复制' : '复制失败')
}

async function copyPrompt(f) {
  if (!f.prompt) return
  flash(await copyText(f.prompt) ? '指令已复制' : '复制失败')
}

let toastTimer = null
function flash(msg) {
  toast.value = msg
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toast.value = ''), 1800)
}

function setKind(v) { kind.value = v; page.value = 1; load() }

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
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

function onKey(e) {
  if (e.key === 'Escape') selected.value = null
}
onMounted(() => { load(); window.addEventListener('keydown', onKey) })
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <section class="space-y-4">
    <!-- KPI strip — same shape as the LogsView so the admin shell stays
         consistent. /images returns all four numbers in one payload. -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-white/45">总计</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums">{{ stats.total }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-indigo-300/80">图像</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-indigo-300">{{ stats.image }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-fuchsia-300/80">视频</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-fuchsia-300">{{ stats.video }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-amber-300/80">存储</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-amber-300">{{ fmtSize(stats.size_bytes) }}</div>
      </div>
    </div>

    <!-- toolbar -->
    <div class="card p-3 flex items-center justify-between gap-3 flex-wrap">
      <div class="flex items-center gap-1">
        <button @click="setKind('')" class="fp" :class="kind === '' && 'fp-on'">全部</button>
        <button @click="setKind('image')" class="fp" :class="kind === 'image' && 'fp-on'">图像</button>
        <button @click="setKind('video')" class="fp" :class="kind === 'video' && 'fp-on'">视频</button>
      </div>
      <button @click="load" class="btn-soft">
        <Icon name="refresh" class="w-3.5 h-3.5" /> 刷新
      </button>
    </div>

    <!-- grid -->
    <div v-if="loading && !items.length" class="text-center text-sm text-white/40 py-20">加载中…</div>
    <div v-else-if="!items.length" class="card flex flex-col items-center gap-3 text-white/40 py-20">
      <span class="w-14 h-14 rounded-2xl bg-white/[0.04] grid place-items-center">
        <Icon name="files" class="w-6 h-6" />
      </span>
      <span class="text-sm">还没有生成过任何图片</span>
    </div>

    <div v-else class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 gap-3">
      <div v-for="f in items" :key="f.name"
           class="media-card group relative rounded-xl overflow-hidden ring-1 ring-white/[0.06] bg-white/[0.03] aspect-[4/5] cursor-zoom-in"
           @click="selected = f">
        <!-- media -->
        <template v-if="f.kind === 'video'">
          <video :src="generatedUrl(f.name)" muted loop preload="metadata"
                 class="absolute inset-0 w-full h-full object-cover"
                 @mouseenter="$event.target.play && $event.target.play()"
                 @mouseleave="$event.target.pause && $event.target.pause()" />
        </template>
        <img v-else :src="generatedUrl(f.name)" loading="lazy"
             class="absolute inset-0 w-full h-full object-cover transition-transform duration-300 group-hover:scale-105" />

        <!-- gradient veil (always visible so the prompt overlay reads) -->
        <div class="absolute inset-x-0 bottom-0 h-1/2 bg-gradient-to-t from-black/85 via-black/40 to-transparent pointer-events-none"></div>

        <!-- kind chip -->
        <span class="absolute top-3 left-3 inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium uppercase tracking-wider ring-1"
              :class="f.kind === 'video' ? 'bg-fuchsia-500/20 text-fuchsia-200 ring-fuchsia-400/30' : 'bg-indigo-500/20 text-indigo-200 ring-indigo-400/30'">
          {{ f.kind === 'video' ? '视频' : '图像' }}
        </span>

        <!-- quick actions, hover-revealed; same style as 首页内容 -->
        <div class="absolute top-3 right-3 flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
          <a :href="generatedUrl(f.name)" target="_blank" @click.stop title="新标签打开"
             class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center">
            <Icon name="open" class="w-3.5 h-3.5" />
          </a>
          <button @click.stop="copyLink(f.name)" title="复制链接"
             class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center">
            <Icon name="copy" class="w-3.5 h-3.5" />
          </button>
          <a :href="generatedUrl(f.name)" :download="f.name.split('/').pop()" @click.stop title="下载"
             class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center">
            <Icon name="download" class="w-3.5 h-3.5" />
          </a>
        </div>

        <!-- caption: prompt (truncated 2 lines) + meta line -->
        <div class="absolute inset-x-0 bottom-0 p-3 pointer-events-none">
          <div class="text-[12px] leading-tight text-white font-medium line-clamp-2 mb-1 transition-colors"
               :class="f.prompt ? 'pointer-events-auto cursor-pointer hover:text-white/75' : ''"
               :title="f.prompt ? '点击复制提示词' : f.name"
               @click.stop="copyPrompt(f)">
            {{ f.prompt || f.name.split('/').pop() }}
          </div>
          <div class="text-[10px] text-white/55 flex items-center justify-between gap-2 tabular-nums">
            <span class="truncate" :title="f.model || ''">{{ f.model || '—' }}</span>
            <span class="shrink-0 flex items-center gap-1">
              <span v-if="f.resolution" class="text-emerald-300/90">{{ f.resolution }}</span>
              <span v-if="f.ratio" class="text-white/40">{{ f.ratio }}</span>
              <span v-if="f.kind === 'video' && f.duration" class="text-fuchsia-300/80">{{ f.duration }}</span>
            </span>
          </div>
          <div class="text-[10px] text-white/35 mt-0.5 tabular-nums">{{ fmtSize(f.size) }} · {{ fmtTs(f.mtime) }}</div>
        </div>
      </div>
    </div>

    <!-- pagination — hidden when everything fits on one page -->
    <div v-if="!loading && totalPages > 1" class="card !p-3 flex items-center justify-between gap-3">
      <div class="text-xs text-white/55 tabular-nums px-2">
        <span class="text-white/85">{{ (page - 1) * pageSize + 1 }}–{{ Math.min(total, page * pageSize) }}</span>
        / {{ total }} 张
      </div>
      <div class="flex items-center gap-1">
        <template v-for="(n, i) in pageNumbers" :key="i">
          <span v-if="n === null" class="px-1 text-white/35">…</span>
          <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
        </template>
      </div>
    </div>

    <!-- Lightbox (shared component) -->
    <MediaLightbox
      v-if="selected"
      :src="generatedUrl(selected.name)"
      :kind="selected.kind"
      :prompt="selected.prompt"
      :meta="[selected.model, selected.name].filter(Boolean).join(' · ')"
      :meta-sub="[selected.resolution, selected.ratio, (selected.kind === 'video' ? selected.duration : ''), fmtSize(selected.size), fmtTs(selected.mtime)].filter(Boolean).join(' · ')"
      :download-name="selected.name.split('/').pop()"
      @close="selected = null" />

    <!-- toast -->
    <transition name="fade">
      <div v-if="toast"
           class="fixed bottom-6 left-1/2 -translate-x-1/2 z-[60] bg-slate-900 text-white text-xs px-4 py-2 rounded-lg shadow-lg">
        {{ toast }}
      </div>
    </transition>
  </section>
</template>

<style scoped>
.fade-enter-active, .fade-leave-active { transition: opacity 0.18s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

.line-clamp-2 {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

/* filter pill — same shape as LogsView so the admin shell stays consistent */
.fp {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  padding: 0.35rem 0.7rem;
  font-size: 0.72rem;
  border-radius: 0.55rem;
  color: var(--fg-3);
  background: var(--surface-2);
  box-shadow: inset 0 0 0 1px var(--hairline);
  transition: background 0.15s, color 0.15s;
}
.fp:hover { background: var(--hover); color: var(--fg); }
.fp-on { background: rgb(15 23 42); color: white; box-shadow: none; }
html.dark .fp-on { background: rgb(255 255 255 / 0.92); color: rgb(15 23 42); }

/* numbered pagination buttons */
.pg {
  min-width: 1.75rem;
  padding: 0.3rem 0.55rem;
  font-size: 0.72rem;
  font-weight: 500;
  text-align: center;
  border-radius: 0.45rem;
  color: var(--fg-2);
  background: var(--surface-2);
  box-shadow: inset 0 0 0 1px var(--hairline);
  transition: background 0.15s, color 0.15s;
}
.pg:hover:not(.pg-on) { background: var(--hover); color: var(--fg); }
.pg-on { background: rgb(15 23 42); color: white; box-shadow: none; }
html.dark .pg-on { background: rgb(255 255 255 / 0.92); color: rgb(15 23 42); }
</style>
