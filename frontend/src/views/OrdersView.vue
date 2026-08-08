<script setup>
// Front-end 订单 page — the signed-in user's own recharge orders. Same light look
// as the 日志 page: filter pills + search + numbered pagination. Unpaid/cancelled
// orders can be resumed via 继续支付.
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { api, jsonBody } from '../api'
import { openPayment } from '../payment'
import { refreshMe } from '../auth'
import Icon from '../components/Icon.vue'
import Mascot from '../components/Mascot.vue'

const router = useRouter()
const items = ref([])
const total = ref(0)
const loading = ref(false)
const status = ref('')   // '' | pending | paid | cancelled
const search = ref('')
const page = ref(1)
const pageSize = 20

const STATUS = { pending: '待支付', paid: '已支付', cancelled: '已取消' }
const METHOD = { wxpay: '微信', alipay: '支付宝' }
const statusPill = (s) => ({
  paid: 'bg-emerald-50 text-emerald-700 ring-emerald-200',
  pending: 'bg-amber-50 text-amber-700 ring-amber-200',
  cancelled: 'bg-slate-100 text-slate-500 ring-slate-200',
}[s] || 'bg-slate-100 text-slate-500 ring-slate-200')
const statusDot = (s) => ({ paid: 'bg-emerald-500', pending: 'bg-amber-500', cancelled: 'bg-slate-400' }[s] || 'bg-slate-400')

function fmt(unix) {
  if (!unix) return '—'
  const d = new Date(unix * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

async function load() {
  loading.value = true
  const qs = new URLSearchParams({ limit: String(pageSize), offset: String((page.value - 1) * pageSize) })
  if (status.value) qs.set('status', status.value)
  if (search.value.trim()) qs.set('q', search.value.trim())
  const r = await api('/pay/orders?' + qs.toString())
  loading.value = false
  if (r.ok) {
    items.value = r.data?.data || []
    total.value = Number(r.data?.total ?? items.value.length)
  }
}
onMounted(load)

// 搜索走服务端(跨页)，直接展示服务端返回的当页结果。
const displayed = computed(() => items.value)
function doSearch() { page.value = 1; load() }
const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))
const pageStart = computed(() => total.value === 0 ? 0 : (page.value - 1) * pageSize + 1)
const pageEnd = computed(() => Math.min(total.value, page.value * pageSize))
function setStatus(v) { status.value = v; page.value = 1; load() }
const pageNumbers = computed(() => {
  const n = totalPages.value, cur = page.value
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
  page.value = t; load()
}

const continuingId = ref('')
async function cont(o) {
  if (continuingId.value) return
  continuingId.value = o.id
  try {
    const r = await api(`/pay/orders/${o.id}/continue`, jsonBody('POST', {}))
    if (!r.ok) return
    openPayment(r.data, { onPaid: () => { refreshMe(); router.push('/settings') } })
  } finally {
    continuingId.value = ''
  }
}
</script>

<template>
  <section class="space-y-5 log-page">
    <div class="flex items-end justify-between flex-wrap gap-3">
      <div>
        <h1 class="text-2xl font-semibold tracking-tight text-slate-900">订单</h1>
        <p class="text-sm text-slate-500 mt-1">{{ total }} 笔充值订单 · 未支付可继续支付</p>
      </div>
      <button @click="router.push('/settings')" class="btn-primary"><Icon name="spark" class="w-4 h-4" /> 去充值</button>
    </div>

    <!-- Filter bar -->
    <div class="card p-3 flex items-center gap-3 flex-wrap">
      <div class="flex items-center gap-1.5">
        <button v-for="s in [['','全部'],['pending','待支付'],['paid','已支付'],['cancelled','已取消']]" :key="s[0]"
                @click="setStatus(s[0])"
                class="text-xs rounded-lg px-2.5 py-1.5 transition-colors"
                :class="status === s[0] ? 'chip-on' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">{{ s[1] }}</button>
      </div>
      <div class="flex-1 min-w-[180px]">
        <input v-model="search" @keyup.enter="doSearch" @change="doSearch"
               class="field !py-1.5 text-xs" placeholder="搜索 订单号 / 金额 / 方式…" />
      </div>
      <button @click="load" class="btn-soft"><Icon name="refresh" class="w-3.5 h-3.5" /> 刷新</button>
    </div>

    <div v-if="loading && !items.length" class="card text-center text-sm text-slate-400 py-24">加载中…</div>
    <div v-else-if="!total" class="anime-card relative overflow-hidden flex flex-col items-center gap-3 text-[color:var(--fg-3)] py-20">
      <div class="anime-stars"></div>
      <Mascot :size="120" />
      <span class="text-sm">还没有充值订单～</span>
    </div>

    <div v-else class="card overflow-hidden !p-0">
      <table class="w-full text-sm log-table">
        <thead>
          <tr class="text-[10px] uppercase tracking-[0.18em] text-slate-400 border-b border-slate-200">
            <th class="text-left px-4 py-3 font-medium">订单号</th>
            <th class="text-left px-3 py-3 font-medium">下单时间</th>
            <th class="text-left px-3 py-3 font-medium">支付时间</th>
            <th class="text-right px-3 py-3 font-medium">金额</th>
            <th class="text-right px-3 py-3 font-medium">充值积分</th>
            <th class="text-left px-3 py-3 font-medium">状态</th>
            <th class="text-right px-4 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="o in displayed" :key="o.id" class="log-row">
            <td class="px-4 py-3 align-middle font-mono text-xs text-slate-700">{{ o.id }}</td>
            <td class="px-3 py-3 align-middle text-xs text-slate-500 whitespace-nowrap">{{ fmt(o.created_at) }}</td>
            <td class="px-3 py-3 align-middle text-xs text-slate-500 whitespace-nowrap">{{ fmt(o.paid_at) }}</td>
            <td class="px-3 py-3 align-middle text-right tabular-nums text-slate-800 font-medium">¥{{ o.amount }}</td>
            <td class="px-3 py-3 align-middle text-right tabular-nums text-violet-600 font-medium">{{ o.points }}</td>
            <td class="px-3 py-3 align-middle">
              <span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium ring-1 whitespace-nowrap" :class="statusPill(o.status)">
                <span class="w-1.5 h-1.5 rounded-full" :class="statusDot(o.status)"></span>{{ STATUS[o.status] }}
                <span class="text-slate-400">· {{ METHOD[o.pay_type] || o.pay_type }}</span>
              </span>
            </td>
            <td class="px-4 py-3 align-middle text-right">
              <button v-if="o.status === 'pending'" @click="cont(o)" :disabled="continuingId === o.id"
                      class="rounded-lg bg-violet-600 text-white hover:bg-violet-500 disabled:opacity-60 disabled:cursor-not-allowed px-3 py-1.5 text-xs font-medium transition-colors inline-flex items-center gap-1.5">
                <span v-if="continuingId === o.id" class="w-3 h-3 rounded-full border-2 border-white/40 border-t-white animate-spin"></span>
                {{ continuingId === o.id ? '处理中…' : '继续支付' }}
              </button>
              <span v-else class="text-xs text-slate-300">—</span>
            </td>
          </tr>
        </tbody>
      </table>

      <div v-if="total && totalPages > 1"
           class="flex items-center justify-between gap-3 border-t border-slate-200 px-5 py-3 text-xs text-slate-500">
        <div><span class="tabular-nums text-slate-700">{{ pageStart }}–{{ pageEnd }}</span><span class="ml-1">/ {{ total }} 笔</span></div>
        <div class="flex items-center gap-1">
          <template v-for="(n, i) in pageNumbers" :key="i">
            <span v-if="n === null" class="px-1 text-slate-300">…</span>
            <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
          </template>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.log-table { border-collapse: separate; border-spacing: 0; }
.log-row td { border-bottom: 1px solid rgb(15 23 42 / 0.06); transition: background-color 0.15s ease, box-shadow 0.15s ease; }
.log-row:hover td { background: rgb(15 23 42 / 0.025); }
.log-row:hover td:first-child { box-shadow: inset 2px 0 0 rgb(124 58 237 / 0.6); }
.log-row:last-child td { border-bottom: none; }
.pg {
  min-width: 1.75rem; padding: 0.3rem 0.55rem; font-size: 0.72rem; font-weight: 500; text-align: center;
  border-radius: 0.45rem; color: rgb(71 85 105); background: rgb(241 245 249);
  box-shadow: inset 0 0 0 1px rgb(15 23 42 / 0.06); transition: background 0.15s, color 0.15s, box-shadow 0.15s;
}
.pg:hover:not(.pg-on) { background: rgb(226 232 240); color: rgb(15 23 42); }
.pg-on { background: rgb(15 23 42); color: white; box-shadow: none; }
</style>
