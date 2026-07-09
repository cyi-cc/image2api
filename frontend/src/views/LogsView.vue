<script setup>
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { fmtTs, fmtDate, fmtClock } from '../utils/format'
import { copyText } from '../utils/clipboard'
import { generatedUrl, thumbUrl } from '../api'
import Icon from '../components/Icon.vue'
import MediaLightbox from '../components/MediaLightbox.vue'

const items = ref([])
const stats = ref({ total: 0, success: 0, failed: 0, pending: 0 })
const loading = ref(false)
const kindFilter = ref('')     // '' | 'image' | 'video'
const statusFilter = ref('')   // '' | 'success' | 'failed' | 'pending'
const sourceFilter = ref('')   // '' | 'v1' | 'user' | 'admin'
const search = ref('')
const userSearch = ref('')   // 服务端用户搜索：名称 / 邮箱 / ID，跨页生效
const page = ref(1)
const pageSize = ref(15)
const total = ref(0)
const toast = ref('')
let toastTimer = null
async function copyPrompt(e) {
  if (!e.prompt) return
  toast.value = (await copyText(e.prompt)) ? '指令已复制' : '复制失败'
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toast.value = ''), 1800)
}
async function copyError(e) {
  if (!e.error) return
  toast.value = (await copyText(e.error)) ? '错误已复制' : '复制失败'
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toast.value = ''), 1800)
}

async function load() {
  loading.value = true
  const offset = (page.value - 1) * pageSize.value
  const qs = new URLSearchParams({ limit: String(pageSize.value), offset: String(offset) })
  // Admin 日志 page: request the full cross-user view. The backend only honors
  // scope=all for admins; without it /logs returns the caller's own records.
  qs.set('scope', 'all')
  if (kindFilter.value) qs.set('kind', kindFilter.value)
  if (statusFilter.value) qs.set('status', statusFilter.value)
  if (sourceFilter.value) qs.set('source', sourceFilter.value)
  if (userSearch.value.trim()) qs.set('user', userSearch.value.trim())
  if (search.value.trim()) qs.set('q', search.value.trim())
  const r = await api('/logs?' + qs.toString())
  items.value = r.data?.data || []
  total.value = Number(r.data?.total ?? items.value.length)
  stats.value = r.data?.stats || { total: 0, success: 0, failed: 0, pending: 0 }
  loading.value = false
}

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const pageStart = computed(() => total.value === 0 ? 0 : (page.value - 1) * pageSize.value + 1)
const pageEnd = computed(() => Math.min(total.value, page.value * pageSize.value))

// Numbered pagination strip: always shows first + last + a window around
// the current page; gaps collapse to `null` (rendered as "…").
const pageNumbers = computed(() => {
  const n = totalPages.value
  const cur = page.value
  if (n <= 7) return Array.from({ length: n }, (_, i) => i + 1)
  const want = new Set([1, n, cur - 1, cur, cur + 1])
  // pad the second slot from each end so 1 2 … X … N-1 N feels balanced
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

function goPage(n) {
  const target = Math.max(1, Math.min(totalPages.value, n))
  if (target === page.value) return
  page.value = target
  load()
}

// Filters reset the cursor so a narrower view always starts on page 1.
function setKind(v) { kindFilter.value = v; page.value = 1; load() }
function setStatus(v) { statusFilter.value = v; page.value = 1; load() }
function setSource(v) { sourceFilter.value = v; page.value = 1; load() }
function doUserSearch() { page.value = 1; load() }

// 搜索全部走服务端(跨页)，页面直接展示服务端返回的当页结果。
const filtered = computed(() => items.value)

function fmtMs(ms) {
  if (!ms) return '—'
  if (ms < 1000) return ms + 'ms'
  return Math.round(ms / 1000) + 's'
}

// One-line timestamp: within 3 days show a relative phrase ("12h 前"),
// older entries collapse to a full Y-M-D H:M:S so the row stays compact.
function fmtWhen(ts) {
  if (!ts) return '—'
  return fmtTs(ts)
}

// Video rows whose first-frame thumbnail is missing (old videos) — fall back
// to the muted <video> preview for those.
const thumbFail = reactive({})

const previewing = ref(null)   // entry whose generated file is open in the lightbox
function openPreview(e) {
  // API (v1) outputs aren't persisted/served by us (image=b64 inline, video=an
  // upstream URL for /content) — no in-log preview, same as images. Skip them.
  if (e.status !== 'success' || !e.file || e.source === 'v1') return
  previewing.value = e
}
function closePreview() { previewing.value = null }
function onKey(ev) { if (ev.key === 'Escape') closePreview() }

// 日志不支持手动清空(清空按钮已移除);仅由后台保留期策略自动清理。

// Auto-refresh removed: the admin can hit 刷新 / change a filter to reload.
onMounted(() => {
  load()
  window.addEventListener('keydown', onKey)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
})

// ---- chip helpers ----
const statusLabel = (s) => ({ success: '成功', failed: '失败', pending: '进行中' }[s] || s)
const statusPill = (s) => ({
  success: 'bg-emerald-500/10 text-emerald-300 ring-emerald-400/30',
  failed:  'bg-rose-500/10 text-rose-300 ring-rose-400/30',
  pending: 'bg-amber-500/10 text-amber-300 ring-amber-400/30',
}[s] || 'bg-white/[0.06] text-white/65 ring-white/15')
const statusDot = (s) => ({
  success: 'bg-emerald-400',
  failed:  'bg-rose-400',
  pending: 'bg-amber-400',
}[s] || 'bg-white/40')

// Source: backend stamps "v1" (API key), "user" (画图台), "admin" (后台测试模型).
const sourceLabel = (s) => ({ v1: 'API', user: '画图台', admin: '测试' }[s] || '画图台')
const sourcePill = (s) => ({
  v1:    'bg-violet-500/15 text-violet-300 ring-violet-400/30',
  admin: 'bg-amber-500/15 text-amber-300 ring-amber-400/30',
  user:  'bg-sky-500/15 text-sky-300 ring-sky-400/30',
}[s] || 'bg-sky-500/15 text-sky-300 ring-sky-400/30')
</script>

<template>
  <section class="space-y-4">
    <!-- KPI strip — dense pills aligned with the dashboard tints -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-white/45">总计</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums">{{ stats.total }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-emerald-300/80">成功</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-emerald-300">{{ stats.success }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-rose-300/80">失败</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-rose-300">{{ stats.failed }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-amber-300/80">进行中</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-amber-300">{{ stats.pending }}</div>
      </div>
    </div>

    <!-- Toolbar -->
    <div class="card p-3 flex items-center gap-3 flex-wrap">
      <div class="flex items-center gap-1">
        <button @click="setKind('')" class="fp" :class="kindFilter === '' && 'fp-on'">全部</button>
        <button @click="setKind('image')" class="fp" :class="kindFilter === 'image' && 'fp-on'">图像</button>
        <button @click="setKind('video')" class="fp" :class="kindFilter === 'video' && 'fp-on'">视频</button>
      </div>
      <div class="w-px h-5 bg-white/10"></div>
      <div class="flex items-center gap-1">
        <button @click="setStatus('')" class="fp" :class="statusFilter === '' && 'fp-on'">所有状态</button>
        <button @click="setStatus('success')" class="fp" :class="statusFilter === 'success' && 'fp-emerald'">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>成功
        </button>
        <button @click="setStatus('failed')" class="fp" :class="statusFilter === 'failed' && 'fp-rose'">
          <span class="w-1.5 h-1.5 rounded-full bg-rose-400"></span>失败
        </button>
        <button @click="setStatus('pending')" class="fp" :class="statusFilter === 'pending' && 'fp-amber'">
          <span class="w-1.5 h-1.5 rounded-full bg-amber-400"></span>进行中
        </button>
      </div>
      <div class="w-px h-5 bg-white/10"></div>
      <div class="flex items-center gap-1">
        <button @click="setSource('')" class="fp" :class="sourceFilter === '' && 'fp-on'">所有来源</button>
        <button @click="setSource('user')" class="fp" :class="sourceFilter === 'user' && 'fp-on'">画图台</button>
        <button @click="setSource('v1')" class="fp" :class="sourceFilter === 'v1' && 'fp-on'">API</button>
        <button @click="setSource('admin')" class="fp" :class="sourceFilter === 'admin' && 'fp-on'">测试</button>
      </div>
      <div class="w-44">
        <input v-model="userSearch" @keyup.enter="doUserSearch" @change="doUserSearch"
               class="field !py-1.5 text-xs" placeholder="搜索用户 名称/邮箱/ID…" />
      </div>
      <div class="flex-1 min-w-[200px]">
        <input v-model="search" @keyup.enter="doUserSearch" @change="doUserSearch"
               class="field !py-1.5 text-xs" placeholder="搜索 模型 / 提示词 / 错误…" />
      </div>
      <button @click="load" class="btn-soft">
        <Icon name="refresh" class="w-3.5 h-3.5" /> 刷新
      </button>
    </div>

    <!-- Table -->
    <div class="card overflow-hidden">
      <div v-if="loading && !items.length" class="text-center text-sm text-white/40 py-20">加载中…</div>
      <div v-else-if="!filtered.length" class="flex flex-col items-center gap-3 text-white/40 py-20">
        <span class="w-14 h-14 rounded-2xl bg-white/[0.04] grid place-items-center"><Icon name="files" class="w-6 h-6" /></span>
        <span class="text-sm">{{ (search.trim() || userSearch.trim()) ? '没有匹配的记录' : '还没有日志' }}</span>
      </div>

      <!-- Each row is a thumbnail + a stack of model/prompt + a meta line.
           Beats a 9-column table for scanability — the eye lands on the
           image first, then reads the model + intent, then params. -->
      <table v-else class="w-full text-sm table-fixed log-table">
        <colgroup>
          <col class="w-20" />        <!-- preview -->
          <col class="w-32" />        <!-- time -->
          <col class="w-24" />        <!-- status -->
          <col class="w-28" />        <!-- user -->
          <col class="w-56" />        <!-- model -->
          <col />                     <!-- prompt + error -->
          <col class="w-48" />        <!-- params -->
          <col class="w-16" />        <!-- credits -->
          <col class="w-16" />        <!-- elapsed -->
        </colgroup>
        <thead>
          <tr class="text-[10px] uppercase tracking-[0.2em] text-white/40 border-b border-white/[0.06]">
            <th class="text-center px-4 py-3 font-medium">预览</th>
            <th class="text-left px-4 py-3 font-medium">时间</th>
            <th class="text-left px-3 py-3 font-medium">状态</th>
            <th class="text-left px-3 py-3 font-medium">用户 / 账号</th>
            <th class="text-left px-3 py-3 font-medium">模型</th>
            <th class="text-left px-3 py-3 font-medium">提示词 / 错误</th>
            <th class="text-left px-3 py-3 font-medium">参数</th>
            <th class="text-right px-3 py-3 font-medium">积分</th>
            <th class="text-right px-4 py-3 font-medium">耗时</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in filtered" :key="e.id" class="log-row">
            <td class="px-4 py-3.5 align-middle text-center">
              <button v-if="e.status === 'success' && e.file && e.source !== 'v1'"
                      @click="openPreview(e)"
                      class="block w-12 h-12 mx-auto rounded-lg overflow-hidden ring-1 ring-white/10 hover:ring-fuchsia-400/60 transition-all">
                <img v-if="e.kind !== 'video' || !thumbFail[e.id]" :src="thumbUrl(e.file)" loading="lazy"
                     class="w-full h-full object-cover"
                     @error="e.kind === 'video' && (thumbFail[e.id] = true)" />
                <video v-else :src="generatedUrl(e.file)" muted loop preload="metadata" playsinline
                       class="w-full h-full object-cover"
                       @mouseenter="$event.target.play && $event.target.play()"
                       @mouseleave="$event.target.pause && $event.target.pause()" />
              </button>
              <div v-else-if="e.status === 'pending'" class="w-12 h-12 mx-auto rounded-lg bg-amber-500/10 ring-1 ring-amber-400/30 grid place-items-center">
                <span class="w-2 h-2 rounded-full bg-amber-400 animate-pulse"></span>
              </div>
              <!-- failed (and any non-success/non-pending) rows: no thumbnail, just a dash.
                   The 状态 column already flags the failure. -->
              <span v-else class="text-white/20">—</span>
            </td>
            <td class="px-4 py-3.5 align-middle text-xs whitespace-nowrap" :title="fmtTs(e.ts)">
              <div v-if="e.ts" class="leading-tight">
                <div class="text-white/80 tabular-nums">{{ fmtDate(e.ts) }}</div>
                <div class="text-white/45 tabular-nums">{{ fmtClock(e.ts) }}</div>
              </div>
              <span v-else class="text-white/25">—</span>
            </td>
            <td class="px-3 py-3.5 align-middle">
              <span class="chip ring-1" :class="statusPill(e.status)">
                <span class="w-1.5 h-1.5 rounded-full" :class="statusDot(e.status)"></span>
                {{ statusLabel(e.status) }}
              </span>
            </td>
            <td class="px-3 py-3.5 align-middle min-w-0">
              <div class="text-xs text-white/80 truncate" :title="e.user_name || '匿名'">{{ e.user_name || '匿名' }}</div>
              <div v-if="e.account" class="mt-0.5 text-[11px] text-white/45 truncate" :title="e.account">{{ e.account }}</div>
            </td>
            <td class="px-3 py-3.5 align-middle min-w-0">
              <div class="font-mono text-xs text-white/90 break-all" :title="e.model">{{ e.model }}</div>
              <div class="mt-1 flex items-center gap-1.5 min-w-0">
                <span class="text-[10px] uppercase tracking-wider font-medium truncate min-w-0"
                      :class="e.kind === 'video' ? 'text-fuchsia-300/80' : 'text-indigo-300/80'">
                  {{ e.kind === 'video' ? '视频' : '图像' }}
                  <span v-if="e.provider" class="text-white/30 ml-1">· {{ e.provider }}</span>
                </span>
                <span class="inline-flex items-center rounded px-1.5 py-px text-[10px] font-medium ring-1 whitespace-nowrap shrink-0"
                      :class="sourcePill(e.source)">{{ sourceLabel(e.source) }}</span>
              </div>
            </td>
            <!-- Prompt with error inline; the error reads as a follow-up rather
                 than wasting a whole column when there's nothing to show. -->
            <td class="px-3 py-3.5 align-middle min-w-0">
              <div class="text-xs text-white/80 truncate transition-colors"
                   :class="e.prompt ? 'cursor-pointer hover:text-white' : ''"
                   :title="e.prompt ? '点击复制提示词' : ''"
                   @click="e.prompt && copyPrompt(e)">{{ e.prompt || '—' }}</div>
              <div v-if="e.error" class="mt-1 text-[11px] text-rose-300/85 truncate flex items-center gap-1.5 cursor-pointer hover:text-rose-200 transition-colors"
                   :title="e.error + ' — 点击复制'"
                   @click.stop="copyError(e)">
                {{ e.error }}
              </div>
            </td>
            <!-- Compact single-line params, dot-separated. -->
            <td class="px-3 py-3.5 align-middle text-[11px] text-white/55 font-mono whitespace-nowrap tabular-nums">
              <span>{{ e.ratio || '—' }}</span>
              <span class="text-white/25 mx-1.5">·</span>
              <span>{{ e.resolution || '—' }}</span>
              <template v-if="e.duration">
                <span class="text-white/25 mx-1.5">·</span>
                <span>{{ e.duration }}</span>
              </template>
              <template v-if="e.refs > 0">
                <span class="text-white/25 mx-1.5">·</span>
                <span class="text-white/40">参考 {{ e.refs }}</span>
              </template>
              <template v-if="e.deai">
                <span class="text-white/25 mx-1.5">·</span>
                <span class="text-violet-300/70">去AI特征</span>
              </template>
            </td>
            <td class="px-3 py-3.5 text-right text-xs tabular-nums align-middle whitespace-nowrap">
              <span v-if="e.cost > 0" class="text-amber-300 font-medium">{{ e.cost }}</span>
              <span v-else class="text-white/25">0</span>
            </td>
            <td class="px-4 py-3.5 text-right text-xs tabular-nums align-middle whitespace-nowrap text-white/85">
              {{ fmtMs(e.elapsed_ms) }}
            </td>
          </tr>
        </tbody>
      </table>

      <!-- pagination — numbered with ellipsis, no prev/next buttons -->
      <div v-if="!loading && total > 0"
           class="flex items-center justify-between gap-3 border-t border-white/[0.06] px-5 py-3 text-xs text-white/55">
        <div>
          <span class="tabular-nums text-white/85">{{ pageStart }}–{{ pageEnd }}</span>
          <span class="ml-1">/ {{ total }} 条</span>
        </div>
        <div class="flex items-center gap-1">
          <template v-for="(n, i) in pageNumbers" :key="i">
            <span v-if="n === null" class="px-1 text-white/35">…</span>
            <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
          </template>
        </div>
      </div>
    </div>

    <!-- Lightbox (shared component) -->
    <MediaLightbox
      v-if="previewing"
      :src="generatedUrl(previewing.file)"
      :kind="previewing.kind"
      :prompt="previewing.prompt"
      :meta="[previewing.model, previewing.ratio, previewing.resolution, previewing.duration, fmtMs(previewing.elapsed_ms)].filter(Boolean).join(' · ')"
      :download-name="previewing.file"
      @close="closePreview" />

    <div v-if="toast"
         class="fixed bottom-6 left-1/2 -translate-x-1/2 z-[60] bg-slate-900 text-white text-xs px-4 py-2 rounded-lg shadow-lg">
      {{ toast }}
    </div>
  </section>
</template>

<style scoped>
/* --- filter pills --- */
.fp {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  padding: 0.35rem 0.7rem;
  font-size: 0.72rem;
  border-radius: 0.55rem;
  color: rgb(255 255 255 / 0.65);
  background: rgb(255 255 255 / 0.05);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.06);
  transition: background 0.15s, color 0.15s, box-shadow 0.15s;
}
.fp:hover { background: rgb(255 255 255 / 0.09); color: white; }
.fp-on {
  background: rgb(255 255 255 / 0.92);
  color: rgb(15 23 42);
  box-shadow: none;
}
.fp-emerald {
  background: rgb(16 185 129 / 0.22);
  color: rgb(110 231 183);
  box-shadow: inset 0 0 0 1px rgb(110 231 183 / 0.45);
}
.fp-rose {
  background: rgb(244 63 94 / 0.22);
  color: rgb(253 164 175);
  box-shadow: inset 0 0 0 1px rgb(253 164 175 / 0.45);
}
.fp-amber {
  background: rgb(245 158 11 / 0.22);
  color: rgb(252 211 77);
  box-shadow: inset 0 0 0 1px rgb(252 211 77 / 0.45);
}

/* --- type / status chip used inside table rows --- */
.chip {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.18rem 0.55rem;
  font-size: 0.7rem;
  font-weight: 500;
  border-radius: 9999px;
  white-space: nowrap;
}

/* --- "danger" variant for the 清空 button --- */
.btn-soft.danger {
  color: rgb(253 164 175);
  background: rgb(244 63 94 / 0.12);
  box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3);
}
.btn-soft.danger:hover {
  color: white;
  background: rgb(244 63 94 / 0.25);
}

.fade-enter-active, .fade-leave-active { transition: opacity 0.15s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

/* --- log table: subtle row separators + a barely-there hover tint that
       extends a soft violet accent on the left of the row (read as a
       focus indicator without being noisy). --- */
.log-table { border-collapse: separate; border-spacing: 0; }
.log-row td {
  border-bottom: 1px solid rgb(255 255 255 / 0.04);
  transition: background-color 0.15s ease, box-shadow 0.15s ease;
}
.log-row:hover td { background: rgb(255 255 255 / 0.025); }
.log-row:hover td:first-child {
  box-shadow: inset 2px 0 0 rgb(167 139 250 / 0.55);
}
.log-row:last-child td { border-bottom: none; }

/* --- pagination buttons --- */
.pg {
  min-width: 1.75rem;
  padding: 0.3rem 0.55rem;
  font-size: 0.72rem;
  font-weight: 500;
  text-align: center;
  border-radius: 0.45rem;
  color: rgb(255 255 255 / 0.7);
  background: rgb(255 255 255 / 0.04);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
  transition: background 0.15s, color 0.15s, box-shadow 0.15s;
}
.pg:hover:not(.pg-on) { background: rgb(255 255 255 / 0.1); color: white; }
.pg-on {
  background: rgb(255 255 255 / 0.92);
  color: rgb(15 23 42);
  box-shadow: none;
}
</style>
