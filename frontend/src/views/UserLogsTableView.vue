<script setup>
// Front-end "日志" page — a row-per-event log of the signed-in user's OWN
// generations (success / failed / pending), surfacing failure reasons that the
// image-only 记录 gallery hides. Uses the same /logs endpoint (auto-scoped to
// the caller), just without the success-only filter.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { api, generatedUrl, thumbUrl } from '../api'
import { fmtDate, fmtClock, fmtTs } from '../utils/format'
import { copyText } from '../utils/clipboard'
import { points } from '../credits'
import Icon from '../components/Icon.vue'
import MediaLightbox from '../components/MediaLightbox.vue'

const router = useRouter()
const items = ref([])          // current server page
const total = ref(0)           // server-side total (matches current filters)
const stats = ref({ total: 0, success: 0, failed: 0, pending: 0 })  // 本人统计
const loading = ref(false)
const statusFilter = ref('')   // '' | success | failed | pending
const sourceFilter = ref('')   // '' | api | web   (api = key 调用, web = 画图台)
const search = ref('')
const page = ref(1)
const pageSize = 20
const lightbox = ref(null)
// Video rows whose first-frame thumbnail is missing (old videos) — fall back
// to the muted <video> preview for those.
const thumbFail = reactive({})

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

// 来源筛选走服务端:画图台 = source "user",API = source "v1"。
const SOURCE_PARAM = { web: 'user', api: 'v1' }

// 服务端分页 —— 不再只拉前 200 条;按页向后端取,可翻到全部历史。
async function load() {
  loading.value = true
  const qs = new URLSearchParams({
    limit: String(pageSize),
    offset: String((page.value - 1) * pageSize),
  })
  if (statusFilter.value) qs.set('status', statusFilter.value)
  if (SOURCE_PARAM[sourceFilter.value]) qs.set('source', SOURCE_PARAM[sourceFilter.value])
  const r = await api('/logs?' + qs.toString())
  loading.value = false
  if (r.ok) {
    items.value = r.data?.data || []
    total.value = Number(r.data?.total ?? items.value.length)
    if (r.data?.stats) stats.value = r.data.stats
  }
}
onMounted(load)

// Source: backend stamps "v1" for API-key calls, "user"/"admin" for the
// playground/test page. Collapse to two buckets the user cares about.
const isApi = (e) => e.source === 'v1'
const sourceLabel = (e) => (isApi(e) ? 'API' : '画图台')
const sourcePill = (e) => (isApi(e)
  ? 'bg-violet-50 text-violet-700 ring-violet-200'
  : 'bg-sky-50 text-sky-700 ring-sky-200')

// 搜索只在当前页内过滤(状态/来源已由服务端筛选并分页)。
const displayed = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return items.value
  return items.value.filter((e) =>
    (e.model || '').toLowerCase().includes(q) ||
    (e.prompt || '').toLowerCase().includes(q) ||
    (e.error || '').toLowerCase().includes(q))
})
const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))
const pageStart = computed(() => total.value === 0 ? 0 : (page.value - 1) * pageSize + 1)
const pageEnd = computed(() => Math.min(total.value, page.value * pageSize))
function setStatus(v) { statusFilter.value = v; page.value = 1; load() }
function setSource(v) { sourceFilter.value = v; page.value = 1; load() }

// Numbered pagination strip — first + last + a window around current; gaps
// collapse to null ("…"). Mirrors the admin 日志 page so both look the same.
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
function goPage(n) {
  const t = Math.max(1, Math.min(totalPages.value, n))
  if (t === page.value) return
  page.value = t
  load()
}

const statusLabel = (s) => ({ success: '成功', failed: '失败', pending: '进行中' }[s] || s)
const statusPill = (s) => ({
  success: 'bg-emerald-50 text-emerald-700 ring-emerald-200',
  failed: 'bg-rose-50 text-rose-700 ring-rose-200',
  pending: 'bg-amber-50 text-amber-700 ring-amber-200',
}[s] || 'bg-slate-100 text-slate-500 ring-slate-200')
const statusDot = (s) => ({
  success: 'bg-emerald-500', failed: 'bg-rose-500', pending: 'bg-amber-500',
}[s] || 'bg-slate-400')
// Match the admin 日志 params exactly: 比例 · 画质 · [时长] · [参考 N].
const params = (e) => {
  const parts = [e.ratio || '—', e.resolution || '—']
  if (e.duration) parts.push(e.duration)
  if (e.refs > 0) parts.push(`参考 ${e.refs}`)
  return parts.join(' · ')
}
</script>

<template>
  <section class="space-y-5 log-page">
    <!-- Header -->
    <div class="flex items-end justify-between flex-wrap gap-3">
      <div>
        <h1 class="text-2xl font-semibold tracking-tight text-slate-900">生成日志</h1>
        <p class="text-sm text-slate-500 mt-1">{{ total }} 条记录 · 含失败原因</p>
      </div>
      <button @click="router.push('/user')" class="btn-primary">
        <Icon name="spark" class="w-4 h-4" /> 去画图
      </button>
    </div>

    <!-- KPI 统计(本人累计) -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-slate-400">总计</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-slate-900">{{ stats.total }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-emerald-600/80">成功</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-emerald-600">{{ stats.success }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-rose-600/80">失败</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-rose-600">{{ stats.failed }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-amber-600/80">进行中</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-amber-600">{{ stats.pending }}</div>
      </div>
    </div>

    <!-- Filter bar -->
    <div class="card p-3 flex items-center gap-3 flex-wrap">
      <div class="flex items-center gap-1.5">
        <button v-for="s in [['','全部'],['success','成功'],['failed','失败'],['pending','进行中']]" :key="s[0]"
                @click="setStatus(s[0])"
                class="text-xs rounded-lg px-2.5 py-1.5 transition-colors"
                :class="statusFilter === s[0] ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">{{ s[1] }}</button>
      </div>
      <div class="w-px h-5 bg-slate-200"></div>
      <div class="flex items-center gap-1.5">
        <button v-for="s in [['','全部来源'],['web','画图台'],['api','API']]" :key="s[0]"
                @click="setSource(s[0])"
                class="text-xs rounded-lg px-2.5 py-1.5 transition-colors"
                :class="sourceFilter === s[0] ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">{{ s[1] }}</button>
      </div>
      <div class="flex-1 min-w-[180px]">
        <input v-model="search" class="field !py-1.5 text-xs" placeholder="搜索 提示词 / 模型 / 错误…" />
      </div>
      <button @click="load" class="btn-soft"><Icon name="refresh" class="w-3.5 h-3.5" /> 刷新</button>
    </div>

    <!-- States -->
    <div v-if="loading && !items.length" class="card text-center text-sm text-slate-400 py-24">加载中…</div>
    <div v-else-if="!total" class="card flex flex-col items-center gap-3 text-slate-400 py-24">
      <span class="w-14 h-14 rounded-2xl bg-slate-100 grid place-items-center"><Icon name="log" class="w-6 h-6" /></span>
      <span class="text-sm">还没有生成日志</span>
    </div>

    <!-- Table -->
    <div v-else class="card overflow-hidden !p-0">
      <table class="w-full text-sm table-fixed log-table">
        <colgroup>
          <col class="w-16" />     <!-- preview -->
          <col class="w-28" />     <!-- time -->
          <col class="w-24" />     <!-- status -->
          <col class="w-28" />     <!-- user/account -->
          <col class="w-56" />     <!-- model -->
          <col />                  <!-- prompt/error -->
          <col class="w-40" />     <!-- params -->
          <col class="w-14" />     <!-- cost -->
          <col class="w-16" />     <!-- elapsed -->
        </colgroup>
        <thead>
          <tr class="text-[10px] uppercase tracking-[0.18em] text-slate-400 border-b border-slate-200">
            <th class="text-center px-3 py-3 font-medium">预览</th>
            <th class="text-left px-3 py-3 font-medium">时间</th>
            <th class="text-left px-3 py-3 font-medium">状态</th>
            <th class="text-left px-3 py-3 font-medium">用户 / 账号</th>
            <th class="text-left px-3 py-3 font-medium">模型</th>
            <th class="text-left px-3 py-3 font-medium">提示词 / 错误</th>
            <th class="text-left px-3 py-3 font-medium">参数</th>
            <th class="text-right px-3 py-3 font-medium">积分</th>
            <th class="text-right px-3 py-3 font-medium">耗时</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in displayed" :key="e.id" class="log-row">
            <td class="px-3 py-3 align-middle text-center">
              <!-- API(v1) videos are no-store: file is an external provider URL
                   (not a RustFS path), so it can't be previewed in-browser — show —. -->
              <button v-if="e.status === 'success' && e.file && !e.file.startsWith('http')" @click="lightbox = e"
                      class="block w-11 h-11 mx-auto rounded-lg overflow-hidden ring-1 ring-slate-200 hover:ring-fuchsia-300 transition-all">
                <img v-if="e.kind !== 'video' || !thumbFail[e.id]" :src="thumbUrl(e.file)" loading="lazy" class="w-full h-full object-cover"
                     @error="e.kind === 'video' && (thumbFail[e.id] = true)" />
                <video v-else :src="generatedUrl(e.file)" muted preload="metadata" class="w-full h-full object-cover" />
              </button>
              <span v-else class="text-slate-300">—</span>
            </td>
            <td class="px-3 py-3 align-middle text-xs whitespace-nowrap" :title="fmtTs(e.ts)">
              <div v-if="e.ts" class="leading-tight">
                <div class="text-slate-600 tabular-nums">{{ fmtDate(e.ts) }}</div>
                <div class="text-slate-400 tabular-nums">{{ fmtClock(e.ts) }}</div>
              </div>
              <span v-else class="text-slate-300">—</span>
            </td>
            <td class="px-3 py-3 align-middle">
              <span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium ring-1 whitespace-nowrap" :class="statusPill(e.status)">
                <span class="w-1.5 h-1.5 rounded-full" :class="statusDot(e.status)"></span>{{ statusLabel(e.status) }}
              </span>
            </td>
            <td class="px-3 py-3 align-middle min-w-0">
              <div class="text-xs text-slate-700 truncate" :title="e.user_name || '匿名'">{{ e.user_name || '匿名' }}</div>
              <div v-if="e.account" class="mt-0.5 text-[11px] text-slate-400 truncate" :title="e.account">{{ e.account }}</div>
            </td>
            <td class="px-3 py-3 align-middle min-w-0">
              <div class="font-mono text-xs text-slate-800 break-all" :title="e.model">{{ e.model }}</div>
              <div class="mt-0.5 flex items-center gap-1.5">
                <span class="text-[10px] uppercase tracking-wider font-medium"
                      :class="e.kind === 'video' ? 'text-fuchsia-600' : 'text-indigo-600'">
                  {{ e.kind === 'video' ? '视频' : '图像' }}
                </span>
                <span class="inline-flex items-center rounded px-1.5 py-px text-[10px] font-medium ring-1 whitespace-nowrap"
                      :class="sourcePill(e)">{{ sourceLabel(e) }}</span>
              </div>
            </td>
            <td class="px-3 py-3 align-middle min-w-0">
              <div class="text-xs text-slate-700 truncate transition-colors"
                   :class="e.prompt ? 'cursor-pointer hover:text-slate-900' : ''"
                   :title="e.prompt ? '点击复制提示词' : ''"
                   @click="e.prompt && copyPrompt(e)">{{ e.prompt || '—' }}</div>
              <div v-if="e.error" class="mt-1 text-[11px] text-rose-600 truncate cursor-pointer hover:text-rose-700 transition-colors"
                   :title="e.error + ' — 点击复制'"
                   @click.stop="copyError(e)">⚠ {{ e.error }}</div>
            </td>
            <td class="px-3 py-3 align-middle text-xs text-slate-500 tabular-nums">{{ params(e) || '—' }}</td>
            <td class="px-3 py-3 align-middle text-right text-xs text-slate-700 tabular-nums">{{ e.cost ? points(e.cost) : '—' }}</td>
            <td class="px-3 py-3 align-middle text-right text-xs text-slate-500 tabular-nums">{{ e.elapsed_ms ? (e.elapsed_ms / 1000).toFixed(1) + 's' : '—' }}</td>
          </tr>
        </tbody>
      </table>

      <!-- Pagination — numbered with ellipsis, inside the card footer exactly
           like the admin 日志 page (top border + px-5 py-3). -->
      <div v-if="total && totalPages > 1"
           class="flex items-center justify-between gap-3 border-t border-slate-200 px-5 py-3 text-xs text-slate-500">
        <div>
          <span class="tabular-nums text-slate-700">{{ pageStart }}–{{ pageEnd }}</span>
          <span class="ml-1">/ {{ total }} 条</span>
        </div>
        <div class="flex items-center gap-1">
          <template v-for="(n, i) in pageNumbers" :key="i">
            <span v-if="n === null" class="px-1 text-slate-300">…</span>
            <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
          </template>
        </div>
      </div>
    </div>

    <MediaLightbox
      v-if="lightbox"
      :src="generatedUrl(lightbox.file)"
      :kind="lightbox.kind"
      :prompt="lightbox.prompt"
      :meta="[lightbox.model, lightbox.ratio, lightbox.resolution, lightbox.duration].filter(Boolean).join(' · ')"
      :download-name="lightbox.file"
      @close="lightbox = null" />

    <div v-if="toast"
         class="fixed bottom-6 left-1/2 -translate-x-1/2 z-[60] bg-slate-900 text-white text-xs px-4 py-2 rounded-lg shadow-lg">
      {{ toast }}
    </div>
  </section>
</template>

<style scoped>
/* Row hover — light-theme twin of the admin 日志 page's .log-row: a subtle
   tint plus a violet accent bar on the left edge of the hovered row. */
.log-table { border-collapse: separate; border-spacing: 0; }
.log-row td {
  border-bottom: 1px solid rgb(15 23 42 / 0.06);
  transition: background-color 0.15s ease, box-shadow 0.15s ease;
}
.log-row:hover td { background: rgb(15 23 42 / 0.025); }
.log-row:hover td:first-child { box-shadow: inset 2px 0 0 rgb(124 58 237 / 0.6); }
.log-row:last-child td { border-bottom: none; }

/* Numbered pagination buttons — light-theme twin of the admin 日志 page's .pg */
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
.pg-on {
  background: rgb(15 23 42);
  color: white;
  box-shadow: none;
}
</style>
