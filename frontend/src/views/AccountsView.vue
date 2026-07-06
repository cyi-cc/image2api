<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { api, jsonBody } from '../api'
import { fmtTs, fmtIso, fmtDate, fmtClock } from '../utils/format'
import ImportModal from '../components/ImportModal.vue'
import UpstreamModal from '../components/UpstreamModal.vue'
import AccountEditModal from '../components/AccountEditModal.vue'
import AccountTestModal from '../components/AccountTestModal.vue'
import Icon from '../components/Icon.vue'

const rows = ref([])
const loading = ref(false)
const quotaStatus = ref('')
const showImport = ref(false)
const showUpstream = ref(false)
const editingUpstream = ref(null)
function editUpstream(a) { editingUpstream.value = a; showUpstream.value = true }
const editingAccount = ref(null)
function editAccount(a) { editingAccount.value = a }
const testingAccount = ref(null)
function testAccount(a) { testingAccount.value = a }
// 预加载模型列表，让「生图测试」弹窗即开即用(不显示加载中)。
const allModels = ref([])
async function loadModelList() {
  const r = await api('/managed-models')
  allModels.value = r.data?.data || []
}
// Reflect the saved values in the table without a full reload.
function applyEdit(payload) {
  const row = editingAccount.value
  if (!row) return
  if (payload.weight != null) row.weight = payload.weight
  if (payload.concurrency != null) row.concurrency = payload.concurrency
}

const typeFilter = ref('')      // '' | 'openai' | 'adobe' | 'runway' | 'leonardo'
const statusFilter = ref('')    // '' | 'active' | 'quota' | 'disabled'
const search = ref('')

const page = ref(1)
const pageSize = ref(20)
// Typing a search term must jump back to page 1 — otherwise a narrowed result
// set can leave you stranded on a now-empty page.
watch(search, () => { page.value = 1 })

// 每个类型的 成功/失败/限额 三个数(成功=正常可用, 失败=失效/禁用, 限额=额度耗尽)。
const stats = computed(() => {
  const by = (t) => {
    const s = rows.value.filter((r) => r.type === t)
    return {
      n: s.length,
      ok: s.filter((r) => r.status === 'active').length,
      dead: s.filter((r) => r.dead || r.status === 'disabled').length,
      quota: s.filter((r) => r.status === 'quota').length,
    }
  }
  return {
    total: rows.value.length,
    openai: by('openai'), adobe: by('adobe'), runway: by('runway'),
    leonardo: by('leonardo'), krea: by('krea'), imagine: by('imagine'),
    grok: by('grok'),
  }
})

// 异常账号 = 已失效(401)被锁定的号(红色锁定行)。用于「一键删除异常账号」。
const deadCount = computed(() => rows.value.filter((r) => r.dead).length)

function typePill(t) {
  return {
    adobe: 'bg-rose-500/10 text-rose-300 ring-rose-400/30',
    openai: 'bg-emerald-500/10 text-emerald-300 ring-emerald-400/30',
    runway: 'bg-violet-500/10 text-violet-300 ring-violet-400/30',
    leonardo: 'bg-amber-500/10 text-amber-300 ring-amber-400/30',
    krea: 'bg-sky-500/10 text-sky-300 ring-sky-400/30',
    imagine: 'bg-teal-500/10 text-teal-300 ring-teal-400/30',
  }[t] || 'bg-white/[0.06] text-white/70 ring-white/15'
}
const STATUS_LABEL = { active: '正常', quota: '额度耗尽', disabled: '已禁用', pending: '检测中' }

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  const sorted = [...rows.value].sort((a, b) => (b.created_at || 0) - (a.created_at || 0))
  return sorted.filter((a) => {
    if (typeFilter.value && a.type !== typeFilter.value) return false
    if (statusFilter.value && a.status !== statusFilter.value) return false
    if (q && !(
      (a.email || '').toLowerCase().includes(q) ||
      (a.id || '').toLowerCase().includes(q) ||
      (a.type || '').toLowerCase().includes(q)
    )) return false
    return true
  })
})

const totalPages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize.value)))
const pagedItems = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return filtered.value.slice(start, start + pageSize.value)
})
function goPage(n) {
  const target = Math.max(1, Math.min(totalPages.value, n))
  if (target !== page.value) page.value = target
}
function setFilter(fn) { fn(); page.value = 1 }
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

let pendingTimer = null

async function loadAccounts() {
  loading.value = true
  quotaStatus.value = ''
  const r = await api('/accounts')
  rows.value = r.data?.data || []
  loading.value = false
  if (rows.value.length) reconcile()
  schedulePendingPoll()
}

// While any imported account is still being checked server-side, re-fetch the
// list so it flips pending → active/dead on its own (no manual refresh).
function schedulePendingPoll() {
  if (pendingTimer) { clearTimeout(pendingTimer); pendingTimer = null }
  if (!rows.value.some((r) => r.pending)) return
  pendingTimer = setTimeout(async () => {
    const r = await api('/accounts')
    rows.value = r.data?.data || []
    schedulePendingPoll()
  }, 2000)
}

// Background reconciliation: openai → live quota; adobe → reset_after;
// adobe without email → fetch email. Only ACTIVE accounts — pending ones are
// handled by the import worker, dead/disabled ones need no re-check.
//
// Scope: ONLY the accounts visible on the current page. Probing all 100+ rows on
// every open floods the backend; the user only ever sees ~20 at a time, so we
// re-check just those and re-run when the page (or filters) change. New imports
// are hydrated server-side by the import worker and surfaced via the pending
// poll reading the store — they don't need a frontend probe.
let reconcileToken = 0
async function reconcile() {
  const myToken = ++reconcileToken   // supersede any in-flight run (fast page flips)
  const visible = pagedItems.value
  // NEW accounts (still pending the import worker's server-side check) are never
  // probed here — the pending poll just reads the store until the worker writes
  // their quota/email. OLD accounts (active) get a real live /quota probe for
  // up-to-date remaining + refresh time.
  const quotaRows = visible.filter((r) => !r.pending && r.status === 'active' && (r.type === 'openai' || r.type === 'adobe' || r.type === 'runway' || r.type === 'leonardo' || r.type === 'krea' || r.type === 'imagine' || r.type === 'grok'))
  const adobeNeedEmail = visible.filter((r) => !r.pending && r.type === 'adobe' && !r.email)
  const total = quotaRows.length + adobeNeedEmail.length
  if (total === 0) { quotaStatus.value = ''; return }

  let done = 0, updates = 0
  quotaStatus.value = `后台校对… 0/${total}`
  const bump = () => {
    done++
    if (myToken !== reconcileToken) return  // a newer page-flip superseded us
    quotaStatus.value = `后台校对… ${done}/${total}${updates ? ` · 更新 ${updates}` : ''}`
  }

  // Build thunks (NOT immediately-invoked) so the pool controls how many run at
  // once. Each /accounts/.../quota probe is a *synchronous* backend call to
  // OpenAI/Adobe. We only probe the visible page (≤ pageSize rows), so the
  // limit below is effectively bounded by that — no full-list flood.
  const jobs = []
  for (const row of quotaRows) {
    jobs.push(async () => {
      const result = await fetchOneQuota(row.pool, row.id)
      if (result && result.auth_failed) {
        // backend auto-disabled this dead (401) token — reflect it immediately
        row.status = result.status || 'disabled'
        row.dead = true
        row.remaining = null
        row._unknown = true
        updates++
      } else if (result && result.unchanged === false) {
        if (row.type === 'adobe') row.reset_after = result.reset_after
        else applyQuota(row, result)
        updates++
      }
      bump()
    })
  }
  for (const row of adobeNeedEmail) {
    jobs.push(async () => {
      const result = await fetchOneEmail(row.pool, row.id)
      if (result && result.email && (!row.email || row.email === '—')) {
        row.email = result.email
        updates++
      }
      bump()
    })
  }
  await runWithLimit(jobs, Infinity)   // no JS-side cap — fire all visible-page probes at once (browser still limits ~6 conns/origin)
  // clear the indicator when done — but only if we're still the current run
  // (a page flip mid-reconcile starts a fresh one that owns the indicator).
  if (myToken === reconcileToken) quotaStatus.value = ''
}

// Re-check the newly visible accounts whenever the page or filters change.
// Only the on-screen page is ever probed (see reconcile), so flipping pages is
// what triggers checking the rest — never all rows at once.
watch([page, typeFilter, statusFilter], () => {
  if (rows.value.length) reconcile()
})

// Bounded-concurrency runner: keeps at most `limit` thunks in flight at once.
async function runWithLimit(thunks, limit) {
  let next = 0
  const workers = Array.from({ length: Math.min(limit, thunks.length) }, async () => {
    while (next < thunks.length) {
      const idx = next++
      await thunks[idx]()
    }
  })
  await Promise.all(workers)
}

async function fetchOneQuota(pool, id) {
  try { return (await api(`/accounts/${pool}/${id}/quota`)).data || {} }
  catch (e) { return { error: String(e) } }
}
async function fetchOneEmail(pool, id) {
  try { return (await api(`/accounts/${pool}/${id}/email`)).data || {} }
  catch (e) { return { error: String(e) } }
}

function applyQuota(row, result) {
  // A transient probe error (e.g. connection reset when OpenAI is unreachable
  // without a proxy) must NOT blank the cached number — keep the last-known
  // value so a network blip doesn't turn the whole column into "—".
  if (result.error) { row._quotaError = result.error; return }
  row._quotaError = null
  if (result.unknown && result.remaining === null) { row.remaining = null; row._unknown = true; return }
  row._unknown = false
  row.remaining = result.remaining
  row.reset_after = result.reset_after
}

async function toggleAccountStatus(pool, id, current) {
  const row = rows.value.find((r) => r.pool === pool && r.id === id)
  const next = current === 'active' ? 'disabled' : 'active'
  // Optimistic: flip the switch instantly so the UI never waits on the network.
  // The PATCH itself is a cheap in-memory update server-side; the old 5s lag came
  // from the follow-up loadAccounts() → reconcile() probing every account's quota.
  if (row) row.status = next
  try {
    const r = await api(`/tokens/${pool}/${id}`, jsonBody('PATCH', { status: next }))
    if (!r.ok && row) row.status = current  // revert on server rejection
  } catch (e) {
    if (row) row.status = current           // revert on network error
  }
}

async function deleteAccount(pool, id) {
  if (!confirm(`确认删除 ${pool} / ${id}?`)) return
  await api(`/tokens/${pool}/${id}`, { method: 'DELETE' })
  loadAccounts()
}

// 一键删除全部异常(已失效/红色锁定)账号。逐个走与单删相同的 DELETE 接口。
async function deleteDeadAccounts() {
  const dead = rows.value.filter((r) => r.dead)
  if (!dead.length) return
  if (!confirm(`确认删除全部 ${dead.length} 个异常(已失效)账号?此操作不可撤销。`)) return
  await Promise.all(dead.map((r) => api(`/tokens/${r.pool}/${r.id}`, { method: 'DELETE' })))
  loadAccounts()
}

// ===== 多选删除 =====
const selected = ref(new Set())
function toggleSelect(id) {
  const s = new Set(selected.value)
  s.has(id) ? s.delete(id) : s.add(id)
  selected.value = s
}
// Header checkbox selects/deselects the CURRENT PAGE only.
const allSelected = computed(() =>
  pagedItems.value.length > 0 && pagedItems.value.every((a) => selected.value.has(a.id)))
function toggleSelectAll() {
  const s = new Set(selected.value)
  if (allSelected.value) pagedItems.value.forEach((a) => s.delete(a.id))
  else pagedItems.value.forEach((a) => s.add(a.id))
  selected.value = s
}
async function deleteSelected() {
  const ids = [...selected.value]
  if (!ids.length) return
  if (!confirm(`确认删除选中的 ${ids.length} 个账号?此操作不可撤销。`)) return
  const r = await api('/tokens/delete-bulk', jsonBody('POST', { ids }))
  if (r.ok) {
    selected.value = new Set()
    loadAccounts()
  }
}

onMounted(() => { loadAccounts(); loadModelList() })
</script>

<template>
  <section class="space-y-4">
    <!-- KPI strip — 每个类型显示 成功/失败/限额 三个数(绿/红/琥珀) -->
    <div class="grid grid-cols-2 md:grid-cols-4 xl:grid-cols-7 gap-3">
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-white/45">账号总数</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums">{{ stats.total }}</div>
        <div class="text-[10px] text-white/35 mt-0.5">成功/失败/限额</div>
      </div>
      <div v-for="t in [['openai','OpenAI','text-emerald-300/80'],['adobe','Adobe','text-rose-300/80'],['runway','Runway','text-violet-300/80'],['leonardo','Leonardo','text-amber-300/80'],['krea','Krea','text-sky-300/80'],['imagine','Imagine','text-teal-300/80'],['grok','Grok','text-slate-300/80']]"
           :key="t[0]" class="card p-4">
        <div class="text-[11px] uppercase tracking-wider" :class="t[2]">{{ t[1] }}</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums">
          <span class="text-emerald-300">{{ stats[t[0]].ok }}</span><span class="text-white/30">/</span><span class="text-rose-300">{{ stats[t[0]].dead }}</span><span class="text-white/30">/</span><span class="text-amber-300">{{ stats[t[0]].quota }}</span>
        </div>
        <div class="text-[10px] text-white/35 mt-0.5">共 {{ stats[t[0]].n }}</div>
      </div>
    </div>

    <!-- Toolbar -->
    <div class="card p-3 flex items-center gap-3 flex-wrap">
      <div class="flex items-center gap-1">
        <button @click="setFilter(() => typeFilter = '')" class="fp" :class="typeFilter === '' && 'fp-on'">全部类型</button>
        <button @click="setFilter(() => typeFilter = 'openai')" class="fp" :class="typeFilter === 'openai' && 'fp-emerald'">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>OpenAI
        </button>
        <button @click="setFilter(() => typeFilter = 'adobe')" class="fp" :class="typeFilter === 'adobe' && 'fp-rose'">
          <span class="w-1.5 h-1.5 rounded-full bg-rose-400"></span>Adobe
        </button>
        <button @click="setFilter(() => typeFilter = 'runway')" class="fp" :class="typeFilter === 'runway' && 'fp-violet'">
          <span class="w-1.5 h-1.5 rounded-full bg-violet-400"></span>Runway
        </button>
        <button @click="setFilter(() => typeFilter = 'leonardo')" class="fp" :class="typeFilter === 'leonardo' && 'fp-amber'">
          <span class="w-1.5 h-1.5 rounded-full bg-amber-400"></span>Leonardo
        </button>
        <button @click="setFilter(() => typeFilter = 'krea')" class="fp" :class="typeFilter === 'krea' && 'fp-sky'">
          <span class="w-1.5 h-1.5 rounded-full bg-sky-400"></span>Krea
        </button>
        <button @click="setFilter(() => typeFilter = 'imagine')" class="fp" :class="typeFilter === 'imagine' && 'fp-teal'">
          <span class="w-1.5 h-1.5 rounded-full bg-teal-400"></span>Imagine
        </button>
        <button @click="setFilter(() => typeFilter = 'grok')" class="fp" :class="typeFilter === 'grok' && 'fp-on'">
          <span class="w-1.5 h-1.5 rounded-full bg-slate-400"></span>Grok
        </button>
      </div>
      <div class="w-px h-5 bg-white/10"></div>
      <div class="flex items-center gap-1">
        <button @click="setFilter(() => statusFilter = '')" class="fp" :class="statusFilter === '' && 'fp-on'">所有状态</button>
        <button @click="setFilter(() => statusFilter = 'active')" class="fp" :class="statusFilter === 'active' && 'fp-emerald'">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>正常
        </button>
        <button @click="setFilter(() => statusFilter = 'quota')" class="fp" :class="statusFilter === 'quota' && 'fp-amber'">
          <span class="w-1.5 h-1.5 rounded-full bg-amber-400"></span>额度耗尽
        </button>
        <button @click="setFilter(() => statusFilter = 'disabled')" class="fp" :class="statusFilter === 'disabled' && 'fp-rose'">
          <span class="w-1.5 h-1.5 rounded-full bg-rose-400"></span>已禁用
        </button>
      </div>
      <div class="flex-1 min-w-[200px]">
        <input v-model="search" class="field !py-1.5 text-xs" placeholder="搜索 邮箱 / ID / 类型…" />
      </div>
      <button v-if="selected.size" @click="deleteSelected" class="btn-soft danger" title="删除选中的账号">
        <Icon name="trash" class="w-3.5 h-3.5" /> 删除选中 ({{ selected.size }})
      </button>
      <button v-if="deadCount" @click="deleteDeadAccounts" class="btn-soft danger" title="删除全部已失效(401)账号">
        <Icon name="trash" class="w-3.5 h-3.5" /> 删除异常账号 ({{ deadCount }})
      </button>
      <button @click="loadAccounts" class="btn-soft">
        <Icon name="refresh" class="w-3.5 h-3.5" /> 刷新
      </button>
      <button @click="showImport = true" class="btn-primary">
        <Icon name="plus" class="w-3.5 h-3.5" /> 导入账号
      </button>
      <button @click="editingUpstream = null; showUpstream = true" class="btn-soft">
        <Icon name="plus" class="w-3.5 h-3.5" /> 添加上游
      </button>
    </div>

    <!-- Table -->
    <div class="card overflow-x-auto">
      <div v-if="loading && !rows.length" class="text-center text-sm text-white/40 py-20">加载中…</div>
      <div v-else-if="!filtered.length" class="flex flex-col items-center gap-3 text-white/40 py-20">
        <span class="w-14 h-14 rounded-2xl bg-white/[0.04] grid place-items-center">
          <Icon name="accounts" class="w-6 h-6" />
        </span>
        <span class="text-sm">{{ rows.length ? '没有匹配的账号' : '还没有账号' }}</span>
        <button v-if="!rows.length" @click="showImport = true" class="btn-soft mt-1">导入第一个</button>
      </div>

      <table v-else class="w-full text-sm table-fixed min-w-[1080px]">
        <colgroup>
          <col class="w-9" />      <!-- select -->
          <col />                  <!-- identity (flex) -->
          <col class="w-20" />     <!-- type -->
          <col class="w-24" />     <!-- remaining -->
          <col class="w-16" />     <!-- weight -->
          <col class="w-16" />     <!-- concurrency -->
          <col class="w-32" />     <!-- reset -->
          <col class="w-28" />     <!-- created -->
          <col class="w-28" />     <!-- last used -->
          <col class="w-40" />     <!-- inflight/success/fail -->
          <col class="w-16" />     <!-- status switch -->
          <col class="w-32" />     <!-- actions -->
        </colgroup>
        <thead>
          <tr class="text-[10px] uppercase tracking-[0.2em] text-white/40 border-b border-white/[0.06]">
            <th class="text-center px-3 py-3 font-medium">
              <input type="checkbox" :checked="allSelected" @change="toggleSelectAll"
                     class="chk" title="全选" />
            </th>
            <th class="text-left px-5 py-3 font-medium">账户</th>
            <th class="text-left px-3 py-3 font-medium">类型</th>
            <th class="text-right px-3 py-3 font-medium">额度</th>
            <th class="text-center px-3 py-3 font-medium">权重</th>
            <th class="text-center px-3 py-3 font-medium">并发</th>
            <th class="text-left px-3 py-3 font-medium">恢复时间</th>
            <th class="text-left px-3 py-3 font-medium">创建时间</th>
            <th class="text-left px-3 py-3 font-medium">最后使用</th>
            <th class="text-center px-3 py-3 font-medium">在途 / 成功 / 失败</th>
            <th class="text-left px-3 py-3 font-medium">状态</th>
            <th class="text-right px-3 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in pagedItems" :key="a.pool + '/' + a.id"
              class="border-b border-white/[0.04] hover:bg-white/[0.03] transition-colors"
              :class="a.dead && 'dead-row'">
            <!-- select -->
            <td class="px-3 py-3.5 align-middle text-center">
              <input type="checkbox" :checked="selected.has(a.id)" @change="toggleSelect(a.id)" @click.stop
                     class="chk" />
            </td>
            <!-- identity -->
            <td class="px-5 py-3.5 align-middle">
              <!-- email + per-kind quota markers on one line. Both-limited shows as
                   额度耗尽 in the status column, so here we only surface the single
                   case. -->
              <div class="flex items-center gap-2 min-w-0">
                <span class="text-sm text-white/90 truncate" :title="a.email || '-'">{{ a.email || '-' }}</span>
                <span v-if="a.image_limited && a.status !== 'quota'"
                      class="shrink-0 inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium bg-amber-500/15 text-amber-300 ring-1 ring-amber-400/20"
                      title="图片额度耗尽，仅视频可用">图片限额</span>
                <span v-if="a.video_limited && a.status !== 'quota'"
                      class="shrink-0 inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium bg-amber-500/15 text-amber-300 ring-1 ring-amber-400/20"
                      title="视频额度耗尽，仅图片可用">视频限额</span>
              </div>
            </td>
            <!-- type -->
            <td class="px-3 py-3.5 align-middle">
              <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1 whitespace-nowrap"
                    :class="typePill(a.type)">{{ a.type }}</span>
            </td>
            <!-- remaining -->
            <td class="px-3 py-3.5 align-middle text-right text-sm tabular-nums whitespace-nowrap">
              <!-- quota column: 数字 / —  (never "未知"/"失败"/"检测中") -->
              <!-- remaining === -1 is the provider "unlimited" sentinel → show — not a scary red -1 -->
              <span v-if="(a.type === 'openai' || a.type === 'runway' || a.type === 'leonardo' || a.type === 'krea' || a.type === 'imagine' || a.type === 'grok') && a.remaining != null && a.remaining !== -1"
                    class="font-mono font-semibold"
                    :class="a.remaining > 0 ? 'text-emerald-300' : 'text-rose-300'">{{ a.remaining }}{{ a.type === 'grok' ? '%' : '' }}</span>
              <span v-else class="text-white/25" :title="a._quotaError || ''">—</span>
            </td>
            <!-- weight (edit via modal) -->
            <td class="px-3 py-3.5 align-middle text-center whitespace-nowrap text-xs tabular-nums text-white/70">
              {{ a.weight ?? 0 }}
            </td>
            <!-- concurrency (custom = configured value; others = system fixed) -->
            <td class="px-3 py-3.5 align-middle text-center whitespace-nowrap text-xs tabular-nums">
              <span v-if="a.type === 'custom'" class="text-white/70">{{ a.concurrency || 1 }}</span>
              <span v-else class="text-white/25" title="系统固定">{{ a.type === 'grok' ? 10 : 1 }}</span>
            </td>
            <!-- reset_after -->
            <td class="px-3 py-3.5 align-middle text-xs whitespace-nowrap">
              <div v-if="a.reset_after" class="leading-tight" :title="fmtIso(a.reset_after)">
                <div class="text-white/65 tabular-nums">{{ fmtDate(a.reset_after) }}</div>
                <div class="text-white/35 tabular-nums">{{ fmtClock(a.reset_after) }}</div>
              </div>
              <span v-else class="text-white/25">—</span>
            </td>
            <!-- created_at -->
            <td class="px-3 py-3.5 align-middle text-xs whitespace-nowrap">
              <div class="leading-tight" :title="fmtTs(a.created_at)">
                <div class="text-white/65 tabular-nums">{{ fmtDate(a.created_at) }}</div>
                <div class="text-white/35 tabular-nums">{{ fmtClock(a.created_at) }}</div>
              </div>
            </td>
            <!-- last_used_at -->
            <td class="px-3 py-3.5 align-middle text-xs whitespace-nowrap">
              <div v-if="a.last_used_at" class="leading-tight" :title="fmtTs(a.last_used_at)">
                <div class="text-white/65 tabular-nums">{{ fmtDate(a.last_used_at) }}</div>
                <div class="text-white/35 tabular-nums">{{ fmtClock(a.last_used_at) }}</div>
              </div>
              <span v-else class="text-white/25">从未</span>
            </td>
            <!-- inflight / success / fail -->
            <td class="px-3 py-3.5 align-middle">
              <div class="flex items-center justify-center gap-1.5 text-xs tabular-nums">
                <span class="px-1.5 py-0.5 rounded"
                      :class="a.in_flight ? 'bg-indigo-500/15 text-indigo-300 font-semibold' : 'text-white/25'"
                      title="在途">{{ a.in_flight || 0 }}</span>
                <span class="text-white/20">/</span>
                <span class="px-1.5 py-0.5 rounded text-emerald-300 font-medium" title="成功">{{ a.success_total || 0 }}</span>
                <span class="text-white/20">/</span>
                <span class="px-1.5 py-0.5 rounded"
                      :class="a.fail_total ? 'bg-rose-500/15 text-rose-300 font-medium' : 'text-white/25'"
                      title="失败">{{ a.fail_total || 0 }}</span>
              </div>
            </td>
            <!-- status (switch) -->
            <td class="px-3 py-3.5 align-middle">
              <button class="sw"
                      :class="{ 'sw-on': a.status === 'active', 'sw-dead': a.dead, 'sw-pending': a.status === 'pending', 'sw-quota': a.status === 'quota', 'sw-locked': a.dead || a.status === 'pending' || a.status === 'quota' }"
                      :disabled="a.dead || a.status === 'pending' || a.status === 'quota'"
                      :aria-pressed="a.status === 'active'"
                      :title="a.status === 'pending' ? '正在检测额度…（暂不调度）' : (a.dead ? '号已失效(401) · 已锁定（删除后重新导入有效令牌）' : (a.status === 'quota' ? '额度耗尽 · 已锁定，到恢复时间自动解开' : (a.status === 'active' ? '点击禁用' : '点击启用')))"
                      @click="!(a.dead || a.status === 'pending' || a.status === 'quota') && toggleAccountStatus(a.pool, a.id, a.status)">
                <span class="sw-thumb"></span>
              </button>
            </td>
            <!-- actions -->
            <td class="px-3 py-3.5 align-middle whitespace-nowrap">
              <div class="flex items-center justify-end gap-2">
                <button @click="testAccount(a)" class="act" title="生图测试">
                  <Icon name="spark" class="w-3.5 h-3.5" />
                </button>
                <button v-if="a.type !== 'custom'" @click="editAccount(a)" class="act" title="编辑">
                  <Icon name="config" class="w-3.5 h-3.5" />
                </button>
                <button v-if="a.type === 'custom'" @click="editUpstream(a)" class="act" title="编辑上游">
                  <Icon name="config" class="w-3.5 h-3.5" />
                </button>
                <button @click="deleteAccount(a.pool, a.id)" class="act danger" title="删除">
                  <Icon name="trash" class="w-3.5 h-3.5" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>

      <!-- pagination -->
      <div v-if="!loading && totalPages > 1"
           class="flex items-center justify-between gap-3 border-t border-white/[0.06] px-5 py-3 text-xs text-white/55">
        <div>
          <span class="tabular-nums text-white/85">{{ (page - 1) * pageSize + 1 }}–{{ Math.min(filtered.length, page * pageSize) }}</span>
          <span class="ml-1">/ {{ filtered.length }} 条</span>
        </div>
        <div class="flex items-center gap-1">
          <template v-for="(n, i) in pageNumbers" :key="i">
            <span v-if="n === null" class="px-1 text-white/35">…</span>
            <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
          </template>
        </div>
      </div>
    </div>

    <ImportModal v-if="showImport" @close="showImport = false" @imported="loadAccounts" />
    <UpstreamModal v-if="showUpstream" :account="editingUpstream" @close="showUpstream = false; editingUpstream = null" @imported="loadAccounts" />
    <AccountEditModal v-if="editingAccount" :account="editingAccount" @saved="applyEdit" @close="editingAccount = null" />
    <AccountTestModal v-if="testingAccount" :account="testingAccount" :all-models="allModels" @close="testingAccount = null" />
  </section>
</template>

<style scoped>
/* --- filter pills (mirrors LogsView/UsersView/ModelsView) */
.fp {
  display: inline-flex; align-items: center; gap: 0.35rem;
  padding: 0.35rem 0.7rem; font-size: 0.72rem;
  border-radius: 0.55rem;
  color: rgb(255 255 255 / 0.65);
  background: rgb(255 255 255 / 0.05);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.06);
  transition: background 0.15s, color 0.15s, box-shadow 0.15s;
}
.fp:hover { background: rgb(255 255 255 / 0.09); color: white; }
.fp-on { background: rgb(255 255 255 / 0.92); color: rgb(15 23 42); box-shadow: none; }
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
  color: rgb(253 224 71);
  box-shadow: inset 0 0 0 1px rgb(253 224 71 / 0.4);
}
.fp-violet {
  background: rgb(139 92 246 / 0.22);
  color: rgb(196 181 253);
  box-shadow: inset 0 0 0 1px rgb(196 181 253 / 0.45);
}
.fp-sky {
  background: rgb(56 189 248 / 0.22);
  color: rgb(125 211 252);
  box-shadow: inset 0 0 0 1px rgb(125 211 252 / 0.45);
}
.fp-teal {
  background: rgb(20 184 166 / 0.22);
  color: rgb(94 234 212);
  box-shadow: inset 0 0 0 1px rgb(94 234 212 / 0.45);
}

/* --- icon-only action buttons */
.act {
  display: inline-flex; align-items: center; justify-content: center;
  width: 1.9rem; height: 1.9rem;
  border-radius: 0.5rem;
  color: rgb(255 255 255 / 0.7);
  background: rgb(255 255 255 / 0.04);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
  transition: background 0.15s, color 0.15s;
}
.act:hover { background: rgb(255 255 255 / 0.1); color: white; }
.act.danger {
  color: rgb(253 164 175);
  background: rgb(244 63 94 / 0.12);
  box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3);
}
.act.danger:hover { color: white; background: rgb(244 63 94 / 0.25); }

/* toolbar 「删除异常账号」按钮 — rose 变体 */
.btn-soft.danger {
  color: rgb(253 164 175);
  background: rgb(244 63 94 / 0.12);
  box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3);
}
.btn-soft.danger:hover { color: white; background: rgb(244 63 94 / 0.25); }

/* iOS-style switch (mirrors UsersView/ModelsView) */
.sw {
  position: relative;
  width: 2.25rem; height: 1.3rem;
  border-radius: 9999px;
  background: rgb(255 255 255 / 0.12);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
  transition: background 0.18s ease;
}
.sw-thumb {
  position: absolute;
  top: 2px; left: 2px;
  width: calc(1.3rem - 4px); height: calc(1.3rem - 4px);
  border-radius: 9999px;
  background: white;
  box-shadow: 0 1px 2px rgb(15 23 42 / 0.3);
  transition: transform 0.18s ease;
}
.sw-on {
  background: rgb(16 185 129 / 0.7);
  box-shadow: inset 0 0 0 1px rgb(16 185 129 / 0.5);
}
.sw-on .sw-thumb { transform: translateX(calc(2.25rem - 1.3rem)); }
/* dead account (401) — red, thumb stays left */
.sw-dead {
  background: rgb(244 63 94 / 0.8);
  box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.6);
}
/* pending (import quota probe in flight) — neutral indigo, thumb stays left */
.sw-pending {
  background: rgb(99 102 241 / 0.45);
  box-shadow: inset 0 0 0 1px rgb(99 102 241 / 0.4);
}
/* quota exhausted — amber (NOT red/dead), thumb stays left, locked until reset */
.sw-quota {
  background: rgb(245 158 11 / 0.5);
  box-shadow: inset 0 0 0 1px rgb(245 158 11 / 0.45);
}
/* dead / pending toggle is locked — can't be flipped */
.sw-locked { cursor: not-allowed; }
/* tint the whole row so a dead account is obvious at a glance */
.dead-row { background: rgb(244 63 94 / 0.07); }
.dead-row:hover { background: rgb(244 63 94 / 0.12); }

/* --- numbered pagination buttons */
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
  transition: background 0.15s, color 0.15s;
}
.pg:hover:not(.pg-on) { background: rgb(255 255 255 / 0.1); color: white; }
.pg-on { background: rgb(255 255 255 / 0.92); color: rgb(15 23 42); box-shadow: none; }
</style>
