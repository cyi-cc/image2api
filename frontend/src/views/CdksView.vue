<script setup>
// Admin CDK (redeem code) management — generate fixed-amount codes, list,
// copy, delete. Amounts are 积分.
import { ref, computed, onMounted, watch } from 'vue'
import { api, jsonBody } from '../api'
import { fmtTs } from '../utils/format'
import Icon from '../components/Icon.vue'

const items = ref([])
const stats = ref({ total: 0, active: 0, redeemed: 0, active_amount: 0, redeemed_amount: 0 })
const loading = ref(false)

// filters
const statusFilter = ref('')   // '' | 'active'(未使用) | 'used'(已使用)
const typeFilter = ref('')     // '' | 'normal' | 'marketing'
const search = ref('')
function setFilter(fn) { fn(); page.value = 1 }
watch(search, () => { page.value = 1 })
const filtered = computed(() => {
  let list = items.value
  if (statusFilter.value === 'active') list = list.filter((c) => c.status === 'active')
  else if (statusFilter.value === 'used') list = list.filter((c) => c.status !== 'active')
  if (typeFilter.value === 'marketing') list = list.filter((c) => c.type === 'marketing')
  else if (typeFilter.value === 'normal') list = list.filter((c) => c.type !== 'marketing')
  const q = search.value.trim().toUpperCase()
  if (q) list = list.filter((c) => (c.code || '').toUpperCase().includes(q))
  return list
})

const form = ref({ amount: 5000, count: 10, type: 'normal' })
const lastBatch = ref([])      // codes from the most recent generate
const flashMsg = ref('')
let flashTimer = null
function flash(m) { flashMsg.value = m; clearTimeout(flashTimer); flashTimer = setTimeout(() => (flashMsg.value = ''), 2000) }

const page = ref(1)
const pageSize = ref(20)

async function load() {
  loading.value = true
  const r = await api('/cdks')
  loading.value = false
  if (r.ok) { items.value = r.data?.data || []; stats.value = r.data?.stats || stats.value }
}
onMounted(load)

async function generate() {
  const amount = Number(form.value.amount), count = Number(form.value.count)
  if (!amount || amount <= 0) { flash('金额必须大于 0'); return }
  if (!count || count <= 0) { flash('数量必须大于 0'); return }
  const r = await api('/cdks', jsonBody('POST', { amount, count, type: form.value.type }))
  if (!r.ok) { flash(r.data?.detail || '生成失败'); return }
  lastBatch.value = (r.data?.created || []).map((c) => c.code)
  flash(`已生成 ${lastBatch.value.length} 个兑换码`)
  page.value = 1
  load()
}

async function del(code) {
  if (!confirm(`删除兑换码 ${code}?`)) return
  const r = await api(`/cdks/${code}`, { method: 'DELETE' })
  if (r.ok) {
    flash('已删除')
    await load()
    // deleting the last row on the last page would otherwise strand us on an
    // empty page past the end — clamp back into range.
    if (page.value > totalPages.value) page.value = totalPages.value
  } else flash(r.data?.detail || '删除失败')
}

// ===== 多选删除 =====
const selected = ref(new Set())
function toggleSelect(code) {
  const s = new Set(selected.value)
  s.has(code) ? s.delete(code) : s.add(code)
  selected.value = s
}
// Header checkbox selects/deselects the CURRENT PAGE only.
const allSelected = computed(() =>
  pagedItems.value.length > 0 && pagedItems.value.every((c) => selected.value.has(c.code)))
function toggleSelectAll() {
  const s = new Set(selected.value)
  if (allSelected.value) pagedItems.value.forEach((c) => s.delete(c.code))
  else pagedItems.value.forEach((c) => s.add(c.code))
  selected.value = s
}
async function delSelected() {
  const codes = [...selected.value]
  if (!codes.length) return
  if (!confirm(`确认删除选中的 ${codes.length} 个兑换码?此操作不可撤销。`)) return
  const r = await api('/cdks/delete-bulk', jsonBody('POST', { codes }))
  if (r.ok) {
    flash(`已删除 ${r.data?.deleted ?? codes.length} 个`)
    selected.value = new Set()
    await load()
    if (page.value > totalPages.value) page.value = totalPages.value
  } else flash(r.data?.detail || '删除失败')
}

async function copy(text) {
  try { await navigator.clipboard.writeText(text); flash('已复制') } catch { flash('复制失败') }
}
function copyBatch() { copy(lastBatch.value.join('\n')) }

// Client-side pagination over the full list (CDK volumes are bounded by
// how many the admin generates — comfortably small).
const totalPages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize.value)))
const pagedItems = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return filtered.value.slice(start, start + pageSize.value)
})
function goPage(n) {
  const target = Math.max(1, Math.min(totalPages.value, n))
  if (target !== page.value) page.value = target
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
</script>

<template>
  <section class="space-y-4">
    <!-- KPI strip — same shape as LogsView / InvitesAdminView -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-white/45">总数</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums">{{ stats.total }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-emerald-300/80">未使用</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-emerald-300">{{ stats.active }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-white/35">已使用</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-white/60">{{ stats.redeemed }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-fuchsia-300/80">未使用面额</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-fuchsia-300">{{ Number(stats.active_amount || 0).toLocaleString('en-US') }}</div>
      </div>
    </div>

    <!-- generate -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">生成兑换码</h2>
      </div>
      <div class="flex flex-wrap items-end gap-3">
        <div>
          <label class="block text-xs text-white/55 mb-1.5">单个金额 (积分)</label>
          <input v-model.number="form.amount" type="number" min="1" step="1" class="field w-40" />
        </div>
        <div>
          <label class="block text-xs text-white/55 mb-1.5">数量</label>
          <input v-model.number="form.count" type="number" min="1" max="500" step="1" class="field w-28" />
        </div>
        <div>
          <label class="block text-xs text-white/55 mb-1.5">类型</label>
          <div class="flex items-center gap-1">
            <button type="button" @click="form.type = 'normal'" class="fp" :class="form.type === 'normal' && 'fp-on'">普通</button>
            <button type="button" @click="form.type = 'marketing'" class="fp" :class="form.type === 'marketing' && 'fp-fuchsia'">营销</button>
          </div>
        </div>
        <button @click="generate" class="btn-primary"><Icon name="plus" class="w-3.5 h-3.5" /> 生成</button>
      </div>
      <p v-if="form.type === 'marketing'" class="text-[11px] text-fuchsia-300/80 mt-2">营销兑换码:同一批次每个用户只能兑换一次。</p>

      <!-- last batch -->
      <div v-if="lastBatch.length" class="mt-4 rounded-xl bg-white/[0.04] ring-1 ring-white/10 p-4">
        <div class="flex items-center justify-between mb-2">
          <span class="text-xs font-medium text-white/75">刚生成 {{ lastBatch.length }} 个 — 请复制保存</span>
          <button @click="copyBatch" class="text-xs btn-soft"><Icon name="copy" class="w-3.5 h-3.5" /> 全部复制</button>
        </div>
        <div class="font-mono text-xs text-white/85 space-y-0.5">
          <div v-for="code in lastBatch" :key="code">{{ code }}</div>
        </div>
      </div>
    </div>

    <!-- Toolbar -->
    <div class="card p-3 flex items-center gap-3 flex-wrap">
      <div class="flex items-center gap-1">
        <button @click="setFilter(() => typeFilter = '')" class="fp" :class="typeFilter === '' && 'fp-on'">全部类型</button>
        <button @click="setFilter(() => typeFilter = 'normal')" class="fp" :class="typeFilter === 'normal' && 'fp-on'">普通</button>
        <button @click="setFilter(() => typeFilter = 'marketing')" class="fp" :class="typeFilter === 'marketing' && 'fp-fuchsia'">
          <span class="w-1.5 h-1.5 rounded-full bg-fuchsia-400"></span>营销
        </button>
      </div>
      <div class="w-px h-5 bg-white/10"></div>
      <div class="flex items-center gap-1">
        <button @click="setFilter(() => statusFilter = '')" class="fp" :class="statusFilter === '' && 'fp-on'">所有状态</button>
        <button @click="setFilter(() => statusFilter = 'active')" class="fp" :class="statusFilter === 'active' && 'fp-emerald'">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>未使用
        </button>
        <button @click="setFilter(() => statusFilter = 'used')" class="fp" :class="statusFilter === 'used' && 'fp-on'">已使用</button>
      </div>
      <div class="flex-1 min-w-[160px]">
        <input v-model="search" class="field !py-1.5 text-xs" placeholder="搜索兑换码…" />
      </div>
      <button v-if="selected.size" @click="delSelected" class="btn-soft danger" title="删除选中的兑换码">
        <Icon name="trash" class="w-3.5 h-3.5" /> 删除选中 ({{ selected.size }})
      </button>
      <button @click="load" class="btn-soft">
        <Icon name="refresh" class="w-3.5 h-3.5" /> 刷新
      </button>
    </div>

    <!-- table -->
    <div class="card overflow-hidden">
      <div v-if="loading && !items.length" class="text-center text-sm text-white/40 py-16">加载中…</div>
      <div v-else-if="!items.length" class="text-center text-sm text-white/40 py-16">还没有兑换码</div>
      <div v-else-if="!filtered.length" class="text-center text-sm text-white/40 py-16">没有匹配的兑换码</div>
      <table v-else class="w-full text-sm">
        <colgroup>
          <col class="w-9" />
          <col />
          <col class="w-28" />
          <col class="w-28" />
          <col class="w-44" />
          <col class="w-44" />
          <col class="w-40" />
          <col class="w-20" />
        </colgroup>
        <thead>
          <tr class="text-[10px] uppercase tracking-[0.2em] text-white/40 border-b border-white/[0.06]">
            <th class="text-center px-3 py-3 font-medium">
              <input type="checkbox" :checked="allSelected" @change="toggleSelectAll"
                     class="chk" title="全选" />
            </th>
            <th class="text-left px-5 py-3 font-medium">兑换码</th>
            <th class="text-right px-3 py-3 font-medium">金额</th>
            <th class="text-left px-3 py-3 font-medium">状态</th>
            <th class="text-left px-3 py-3 font-medium">创建时间</th>
            <th class="text-left px-3 py-3 font-medium">使用时间</th>
            <th class="text-left px-3 py-3 font-medium">使用者</th>
            <th class="text-right px-5 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in pagedItems" :key="c.code"
              class="border-b border-white/[0.04] hover:bg-white/[0.03] transition-colors">
            <td class="px-3 py-3.5 align-middle text-center">
              <input type="checkbox" :checked="selected.has(c.code)" @change="toggleSelect(c.code)" @click.stop
                     class="chk" />
            </td>
            <td class="px-5 py-3.5 align-middle font-mono text-xs text-white/90 truncate" :title="c.code">
              <span class="inline-flex items-center gap-2">
                <span class="truncate">{{ c.code }}</span>
                <span v-if="c.type === 'marketing'" class="shrink-0 inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-sans font-medium bg-fuchsia-500/15 text-fuchsia-300 ring-1 ring-fuchsia-400/25">营销</span>
              </span>
            </td>
            <td class="px-3 py-3.5 align-middle text-right tabular-nums text-white/85 whitespace-nowrap">
              {{ Number(c.amount).toLocaleString('en-US') }}
            </td>
            <td class="px-3 py-3.5 align-middle">
              <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1"
                    :class="c.status === 'active'
                      ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-400/30'
                      : 'bg-white/[0.06] text-white/55 ring-white/15'">
                <span class="w-1.5 h-1.5 rounded-full"
                      :class="c.status === 'active' ? 'bg-emerald-400' : 'bg-slate-400'"></span>
                {{ c.status === 'active' ? '未使用' : '已使用' }}
              </span>
            </td>
            <td class="px-3 py-3.5 align-middle text-xs text-white/55 tabular-nums whitespace-nowrap">{{ fmtTs(c.created_at) }}</td>
            <td class="px-3 py-3.5 align-middle text-xs text-white/55 tabular-nums whitespace-nowrap">{{ c.redeemed_at ? fmtTs(c.redeemed_at) : '—' }}</td>
            <td class="px-3 py-3.5 align-middle text-xs text-white/55 truncate">{{ c.redeemed_by_name || '—' }}</td>
            <td class="px-5 py-3.5 align-middle text-right">
              <div class="inline-flex items-center gap-1">
                <button @click="copy(c.code)" class="act" title="复制"><Icon name="copy" class="w-3.5 h-3.5" /></button>
                <button @click="del(c.code)" class="act danger" title="删除"><Icon name="trash" class="w-3.5 h-3.5" /></button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>

      <!-- pagination -->
      <div v-if="!loading && totalPages > 1"
           class="flex items-center justify-between gap-3 border-t border-white/[0.06] px-5 py-3 text-xs text-white/55">
        <div>
          <span class="tabular-nums text-white/85">{{ (page - 1) * pageSize + 1 }}–{{ Math.min(items.length, page * pageSize) }}</span>
          <span class="ml-1">/ {{ items.length }} 条</span>
        </div>
        <div class="flex items-center gap-1">
          <template v-for="(n, i) in pageNumbers" :key="i">
            <span v-if="n === null" class="px-1 text-white/35">…</span>
            <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
          </template>
        </div>
      </div>
    </div>

    <transition name="fade">
      <div v-if="flashMsg" class="fixed bottom-8 left-1/2 -translate-x-1/2 z-50 bg-slate-900 text-white text-sm font-medium px-5 py-2.5 rounded-full shadow-2xl">{{ flashMsg }}</div>
    </transition>
  </section>
</template>

<style scoped>
.fade-enter-active, .fade-leave-active { transition: opacity 0.18s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

.btn-soft.danger {
  color: rgb(253 164 175);
  background: rgb(244 63 94 / 0.12);
  box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3);
}
.btn-soft.danger:hover {
  color: white;
  background: rgb(244 63 94 / 0.25);
}

/* row icon action buttons — mirror 账号管理 for a consistent look */
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
.fp-fuchsia {
  background: rgb(217 70 239 / 0.22);
  color: rgb(245 208 254);
  box-shadow: inset 0 0 0 1px rgb(245 208 254 / 0.45);
}

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
.pg-on {
  background: rgb(255 255 255 / 0.92);
  color: rgb(15 23 42);
  box-shadow: none;
}
</style>
