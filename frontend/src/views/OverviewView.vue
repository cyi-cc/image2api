<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { fmtTs, fmtSize } from '../utils/format'
import Icon from '../components/Icon.vue'

const providers = ref([])
const stats = ref({ generated_count: 0, generated_size_bytes: 0 })
const userStats = ref({ total: 0, active: 0, disabled: 0, admins: 0, credits_total: 0, new_24h: 0, new_7d: 0, active_24h: 0 })
// Everything time-windowed now comes pre-aggregated from /dashboard (server-side
// SQL) instead of being recomputed in the browser from the last 200 logs — which
// silently undercounted week / DAU / trend / top-N once volume grew past 200.
const dash = ref(null)
const logs = ref([])        // recent-activity feed only (small page)
const models = ref([])      // managed models, for the model-count card
const range = ref('day')    // analytics window toggle: 'day' (24h) | 'week' (7d)
let timer = null

async function refreshAll() {
  const [p, s, u, d, l, m] = await Promise.all([
    api('/providers'),
    api('/stats'),
    api('/users'),
    api('/dashboard'),
    api('/logs?limit=20'),
    api('/managed-models'),
  ])
  providers.value = p.data?.data || []
  stats.value = s.data || {}
  userStats.value = u.data?.stats || {}
  dash.value = d.data || null
  logs.value = l.data?.data || []
  models.value = m.data?.data || []
}

// ---- windowed event aggregates (from /dashboard) ----
const EMPTY_WINDOW = { total: 0, success: 0, failed: 0, pending: 0, image: 0, video: 0, api: 0, web: 0, spent: 0 }
const day = computed(() => dash.value?.day || EMPTY_WINDOW)
const week = computed(() => dash.value?.week || EMPTY_WINDOW)
// All-time persistent counters (stat_counters) — independent of log retention.
const lifetime = computed(() => dash.value?.lifetime || {})
const successRate = computed(() => (day.value.total ? Math.round((day.value.success / day.value.total) * 100) : 0))

// Direction vs the previous 24h (24–48h ago) — a quiet day after a busy week is
// worth seeing. prev_day_total is computed server-side.
const dayDelta = computed(() => {
  const cur = day.value.total
  const prev = dash.value?.prev_day_total || 0
  if (!prev) return cur ? { pct: null, dir: 'up' } : { pct: null, dir: 'flat' }
  const pct = Math.round(((cur - prev) / prev) * 100)
  return { pct, dir: cur > prev ? 'up' : cur < prev ? 'down' : 'flat' }
})

const dau = computed(() => dash.value?.dau || 0)
const avg24hMs = computed(() => stats.value?.avg_elapsed_ms_24h ?? null)

// ---- range-toggled top-N analytics (both windows ship in the payload, so the
// 24h/7d switch is instant — no re-fetch) ----
const rangeLabel = computed(() => (range.value === 'week' ? '近 3 天' : '近 24h'))
const analytics = computed(() => dash.value?.analytics?.[range.value] || { models: [], failures: [], top_users: [] })
const modelUsage = computed(() => analytics.value.models || [])
const usageMax = computed(() => Math.max(1, ...modelUsage.value.map((m) => m.count)))
const failures = computed(() => analytics.value.failures || [])
const topUsers = computed(() => analytics.value.top_users || [])
const topUserMax = computed(() => Math.max(1, ...topUsers.value.map((u) => u.spent)))

// ---- 24h trend (always 24h) ----
const hourBuckets = computed(() => dash.value?.hourly || Array.from({ length: 24 }, () => ({ image: 0, video: 0 })))
const hourMax = computed(() => Math.max(1, ...hourBuckets.value.map((b) => b.image + b.video)))

// ---- operations cards ----
const cdk = computed(() => dash.value?.cdk || {})
const invites = computed(() => dash.value?.invites || {})
const checkin = computed(() => dash.value?.checkin || {})

// ---- token / model / provider summaries (from /providers + /managed-models) ----
const tokens = computed(() => {
  let active = 0, total = 0
  for (const p of providers.value) {
    active += p.tokens_active || 0
    total += (p.tokens_active || 0) + (p.tokens_disabled || 0) + (p.tokens_quota || 0)
  }
  return { active, total }
})

const modelTypes = computed(() => {
  const image = models.value.filter((m) => m.type === 'image').length
  const video = models.value.filter((m) => m.type === 'video').length
  return { image, video, total: models.value.length }
})

const providerHealth = computed(() =>
  providers.value.map((p) => {
    const total = (p.tokens_active || 0) + (p.tokens_disabled || 0) + (p.tokens_quota || 0)
    let status = 'down'
    if ((p.tokens_active || 0) > 0) status = 'healthy'
    else if (total > 0) status = 'warning'
    return { ...p, status, total }
  })
)

// One-glance system health badge derived from provider token availability.
const overallHealth = computed(() => {
  const list = providerHealth.value
  if (!list.length) return { label: '未配置 Provider', tone: 'down' }
  if (list.some((p) => p.status === 'down')) return { label: 'Provider 异常', tone: 'down' }
  if (list.some((p) => p.status === 'warning')) return { label: 'Provider 告警', tone: 'warning' }
  return { label: '系统健康', tone: 'healthy' }
})

const recentLogs = computed(() => logs.value.slice(0, 12))

// ---- formatters ----
function statusLabel(s) { return s === 'healthy' ? '健康' : s === 'warning' ? '告警' : '未配置' }
function statusDot(s) { return s === 'healthy' ? 'bg-emerald-400' : s === 'warning' ? 'bg-amber-400' : 'bg-rose-500' }
function statusPill(s) {
  if (s === 'healthy') return 'bg-emerald-500/10 text-emerald-300 ring-emerald-400/30'
  if (s === 'warning') return 'bg-amber-500/10 text-amber-300 ring-amber-400/30'
  return 'bg-rose-500/10 text-rose-300 ring-rose-400/30'
}
function logDot(status) {
  if (status === 'success') return 'bg-emerald-400'
  if (status === 'pending') return 'bg-amber-400'
  return 'bg-rose-500'
}
function fmtMs(ms) {
  if (!ms) return '—'
  if (ms < 1000) return ms + 'ms'
  return (ms / 1000).toFixed(1) + 's'
}
function fmtInt(n) { return (n ?? 0).toLocaleString('zh-CN') }
function fmtCredits(n) {
  const v = Number(n || 0)
  if (v >= 10000) return (v / 10000).toFixed(1) + ' 万'
  return fmtInt(Math.round(v))
}

onMounted(() => {
  refreshAll()
  timer = setInterval(refreshAll, 10000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section class="space-y-4">
    <!-- ===== Toolbar: overall health + refresh ===== -->
    <div class="flex items-center justify-between gap-3">
      <span class="inline-flex items-center gap-2 rounded-full px-3 py-1.5 text-xs font-medium ring-1 tabular-nums"
            :class="statusPill(overallHealth.tone)">
        <span class="w-1.5 h-1.5 rounded-full" :class="statusDot(overallHealth.tone)"></span>
        {{ overallHealth.label }}
      </span>
      <button @click="refreshAll" class="btn-ghost">刷新</button>
    </div>

    <!-- ===== KPI strip ===== -->
    <div class="grid grid-cols-2 lg:grid-cols-5 gap-3">
      <!-- 用户 -->
      <div class="card p-4">
        <div class="flex items-center justify-between">
          <span class="text-xs text-white/55">用户</span>
          <span class="w-7 h-7 rounded-lg bg-indigo-500/15 text-indigo-300 grid place-items-center ring-1 ring-indigo-400/20">
            <Icon name="accounts" class="w-3.5 h-3.5" />
          </span>
        </div>
        <div class="text-2xl font-semibold tabular-nums mt-2">{{ fmtInt(userStats.total) }}</div>
        <div class="text-[11px] text-white/45 mt-1">{{ userStats.active }} 活跃 · {{ userStats.admins }} 管理员</div>
        <div class="text-[11px] text-emerald-300/80 mt-0.5">
          今日新增 <span class="tabular-nums font-medium">{{ fmtInt(userStats.new_24h) }}</span>
          · 7日 <span class="tabular-nums font-medium">{{ fmtInt(userStats.new_7d) }}</span>
        </div>
      </div>

      <!-- 24h -->
      <div class="card p-4">
        <div class="flex items-center justify-between">
          <span class="text-xs text-white/55">近 24 小时生成</span>
          <span class="w-7 h-7 rounded-lg bg-violet-500/15 text-violet-300 grid place-items-center ring-1 ring-violet-400/20">
            <Icon name="spark" class="w-3.5 h-3.5" />
          </span>
        </div>
        <div class="text-2xl font-semibold tabular-nums mt-2 flex items-baseline gap-2">
          <span>{{ fmtInt(day.total) }}</span>
          <span v-if="dayDelta.pct != null"
                class="text-[11px] font-medium tabular-nums"
                :class="dayDelta.dir === 'up' ? 'text-emerald-300' : dayDelta.dir === 'down' ? 'text-rose-300' : 'text-white/45'">
            {{ dayDelta.dir === 'up' ? '↑' : dayDelta.dir === 'down' ? '↓' : '·' }}{{ Math.abs(dayDelta.pct) }}%
          </span>
        </div>
        <div class="text-[11px] mt-1 flex flex-wrap gap-x-2">
          <span class="text-emerald-300 tabular-nums">{{ day.success }} 成功</span>
          <span v-if="day.failed" class="text-rose-300 tabular-nums">{{ day.failed }} 失败</span>
          <span v-if="day.pending" class="text-amber-300 tabular-nums">{{ day.pending }} 进行中</span>
        </div>
        <div v-if="day.total" class="text-[10px] text-white/40 mt-1 tabular-nums">
          Web {{ day.web }} · API {{ day.api }} · 图 {{ day.image }} · 视 {{ day.video }}
        </div>
      </div>

      <!-- 累计生成(全部 · 持久计数,不随日志清理变化) -->
      <div class="card p-4">
        <div class="flex items-center justify-between">
          <span class="text-xs text-white/55">累计生成</span>
          <span class="w-7 h-7 rounded-lg bg-indigo-500/15 text-indigo-300 grid place-items-center ring-1 ring-indigo-400/20">
            <Icon name="overview" class="w-3.5 h-3.5" />
          </span>
        </div>
        <div class="text-2xl font-semibold tabular-nums mt-2">{{ fmtInt(lifetime.total || 0) }}</div>
        <div class="text-[11px] mt-1 flex flex-wrap gap-x-2">
          <span class="text-emerald-300 tabular-nums">{{ lifetime.success || 0 }} 成功</span>
          <span v-if="lifetime.failed" class="text-rose-300 tabular-nums">{{ lifetime.failed }} 失败</span>
        </div>
        <div class="text-[10px] text-white/40 mt-1 tabular-nums">
          API {{ lifetime.api || 0 }} · 图 {{ lifetime.image || 0 }} · 视 {{ lifetime.video || 0 }}
        </div>
      </div>

      <!-- 平均耗时 -->
      <div class="card p-4">
        <div class="flex items-center justify-between">
          <span class="text-xs text-white/55">平均耗时</span>
          <span class="w-7 h-7 rounded-lg bg-emerald-500/15 text-emerald-300 grid place-items-center ring-1 ring-emerald-400/20">
            <Icon name="refresh" class="w-3.5 h-3.5" />
          </span>
        </div>
        <div class="text-2xl font-semibold tabular-nums mt-2">{{ fmtMs(avg24hMs) }}</div>
        <div class="text-[11px] text-white/45 mt-1">{{ successRate }}% 成功率 · 24h</div>
      </div>

      <!-- 存储 -->
      <div class="card p-4">
        <div class="flex items-center justify-between">
          <span class="text-xs text-white/55">产物存储</span>
          <span class="w-7 h-7 rounded-lg bg-amber-500/15 text-amber-300 grid place-items-center ring-1 ring-amber-400/20">
            <Icon name="files" class="w-3.5 h-3.5" />
          </span>
        </div>
        <div class="text-2xl font-semibold tabular-nums mt-2">{{ fmtSize(stats.generated_size_bytes || 0) }}</div>
        <div class="text-[11px] text-white/45 mt-1">{{ fmtInt(stats.generated_count) }} 个文件</div>
      </div>
    </div>

    <!-- ===== Secondary stat row ===== -->
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3">
      <div class="card p-4">
        <div class="text-xs text-white/55">系统积分总和</div>
        <div class="text-xl font-semibold tabular-nums mt-2">{{ fmtCredits(userStats.credits_total) }}</div>
        <div class="text-[11px] text-white/45 mt-1">
          所有用户余额累加 · <span class="text-amber-300">24h 消耗 {{ fmtCredits(day.spent) }}</span>
        </div>
      </div>
      <div class="card p-4">
        <div class="text-xs text-white/55">Token</div>
        <div class="text-xl font-semibold tabular-nums mt-2 flex items-baseline gap-1">
          <span class="text-emerald-300">{{ tokens.active }}</span>
          <span class="text-white/30">/</span>
          <span>{{ tokens.total }}</span>
        </div>
        <div class="text-[11px] text-white/45 mt-1">活跃 / 已配置</div>
      </div>
      <div class="card p-4">
        <div class="text-xs text-white/55">模型</div>
        <div class="text-xl font-semibold tabular-nums mt-2">{{ modelTypes.total }}</div>
        <div class="text-[11px] text-white/45 mt-1">图像 {{ modelTypes.image }} · 视频 {{ modelTypes.video }}</div>
      </div>
      <div class="card p-4">
        <div class="text-xs text-white/55">活跃用户 · 24h</div>
        <div class="text-xl font-semibold tabular-nums mt-2">{{ fmtInt(dau) }}</div>
        <div class="text-[11px] text-white/45 mt-1">近 3 天累计生成 {{ fmtInt(week.total) }}</div>
      </div>
    </div>

    <!-- ===== Operations row: CDK / 邀请 / 签到 ===== -->
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
      <!-- 兑换码 -->
      <div class="card p-4">
        <div class="flex items-center justify-between">
          <span class="text-xs text-white/55">兑换码 CDK</span>
          <span class="w-7 h-7 rounded-lg bg-sky-500/15 text-sky-300 grid place-items-center ring-1 ring-sky-400/20">
            <Icon name="spark" class="w-3.5 h-3.5" />
          </span>
        </div>
        <div class="text-xl font-semibold tabular-nums mt-2 flex items-baseline gap-1">
          <span class="text-emerald-300">{{ fmtInt(cdk.active) }}</span>
          <span class="text-white/30 text-sm">未用 /</span>
          <span class="text-white/70 text-base">{{ fmtInt(cdk.redeemed) }} 已兑</span>
        </div>
        <div class="text-[11px] text-white/45 mt-1">
          待兑积分 <span class="text-amber-300 tabular-nums">{{ fmtCredits(cdk.active_amount) }}</span>
          · 已发出 <span class="tabular-nums">{{ fmtCredits(cdk.redeemed_amount) }}</span>
        </div>
      </div>

      <!-- 邀请 -->
      <div class="card p-4">
        <div class="flex items-center justify-between">
          <span class="text-xs text-white/55">邀请</span>
          <span class="w-7 h-7 rounded-lg bg-fuchsia-500/15 text-fuchsia-300 grid place-items-center ring-1 ring-fuchsia-400/20">
            <Icon name="accounts" class="w-3.5 h-3.5" />
          </span>
        </div>
        <div class="text-xl font-semibold tabular-nums mt-2 flex items-baseline gap-1">
          <span>{{ fmtInt(invites.total) }}</span>
          <span class="text-white/40 text-sm">邀请注册</span>
        </div>
        <div class="text-[11px] text-white/45 mt-1">
          <span class="text-emerald-300 tabular-nums">{{ fmtInt(invites.completed) }}</span> 已达成奖励
          · 已发 <span class="text-amber-300 tabular-nums">{{ fmtCredits(invites.reward_paid) }}</span> 积分
        </div>
      </div>

      <!-- 签到 -->
      <div class="card p-4">
        <div class="flex items-center justify-between">
          <span class="text-xs text-white/55">今日签到</span>
          <span class="w-7 h-7 rounded-lg bg-teal-500/15 text-teal-300 grid place-items-center ring-1 ring-teal-400/20">
            <Icon name="refresh" class="w-3.5 h-3.5" />
          </span>
        </div>
        <div class="text-xl font-semibold tabular-nums mt-2 flex items-baseline gap-1">
          <span>{{ fmtInt(checkin.today) }}</span>
          <span class="text-white/40 text-sm">人</span>
        </div>
        <div class="text-[11px] text-white/45 mt-1">
          发放 <span class="text-amber-300 tabular-nums">{{ fmtCredits(checkin.awarded_today) }}</span> 积分
        </div>
      </div>
    </div>

    <!-- ===== Provider health + 24h trend ===== -->
    <div class="grid lg:grid-cols-2 gap-3">
      <div class="card">
        <div class="px-5 py-3 border-b border-white/[0.06] flex items-center justify-between">
          <h2 class="text-sm font-semibold">Provider 健康</h2>
          <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1"
                :class="statusPill(overallHealth.tone)">
            <span class="w-1.5 h-1.5 rounded-full" :class="statusDot(overallHealth.tone)"></span>
            {{ overallHealth.label }}
          </span>
        </div>
        <div class="p-3">
          <div v-if="!providerHealth.length" class="text-center text-xs text-white/40 py-8">未注册 Provider</div>
          <div v-else class="space-y-0.5">
            <div v-for="p in providerHealth" :key="p.name"
                 class="flex items-center gap-3 px-2 py-2 rounded-lg hover:bg-white/[0.04] transition-colors">
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium capitalize truncate">{{ p.name }}</div>
                <div class="text-[11px] text-white/45 mt-0.5">
                  {{ p.model_count }} 模型 · token {{ p.tokens_active }}/{{ p.total || 0 }} 活跃
                  <span v-if="p.tokens_quota"> · {{ p.tokens_quota }} 限额</span>
                  <span v-if="p.tokens_disabled"> · {{ p.tokens_disabled }} 停用</span>
                </div>
              </div>
              <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1 tabular-nums"
                    :class="statusPill(p.status)">
                <span class="w-1.5 h-1.5 rounded-full" :class="statusDot(p.status)"></span>
                {{ statusLabel(p.status) }}
              </span>
            </div>
          </div>
        </div>
      </div>

      <div class="card flex flex-col">
        <div class="px-5 py-3 border-b border-white/[0.06] flex items-baseline justify-between">
          <h2 class="text-sm font-semibold">24 小时生成趋势</h2>
          <div class="text-[11px] text-white/45 flex items-center gap-3">
            <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-sm bg-indigo-400/80"></span>图像</span>
            <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-sm bg-fuchsia-400/80"></span>视频</span>
            <span class="tabular-nums">峰值 {{ hourMax }}/h</span>
          </div>
        </div>
        <div class="p-5 flex-1 flex flex-col">
          <div class="flex items-end gap-[3px] flex-1 min-h-[8rem]">
            <div v-for="(b, i) in hourBuckets" :key="i"
                 class="group/bar relative flex-1 flex flex-col justify-end rounded-t overflow-visible"
                 :style="{ height: Math.max(4, ((b.image + b.video) / hourMax) * 100) + '%' }">
              <!-- hover tooltip -->
              <div class="pointer-events-none absolute -top-9 left-1/2 -translate-x-1/2 z-10 hidden group-hover/bar:block
                          whitespace-nowrap rounded-md bg-black/90 ring-1 ring-white/10 px-2 py-1 text-[10px] text-white/90 tabular-nums">
                {{ 23 - i }}h 前 · 图 {{ b.image }} / 视 {{ b.video }}
              </div>
              <div v-if="b.video" class="bg-fuchsia-400/80 group-hover/bar:bg-fuchsia-400" :style="{ flex: b.video }"></div>
              <div v-if="b.image" class="bg-indigo-400/80 group-hover/bar:bg-indigo-400" :style="{ flex: b.image }"></div>
              <div v-if="!b.image && !b.video" class="bg-white/[0.06] group-hover/bar:bg-white/15 flex-1 rounded-t"></div>
            </div>
          </div>
          <div class="flex justify-between text-[10px] text-white/40 mt-2 tabular-nums">
            <span>-24h</span><span>-18h</span><span>-12h</span><span>-6h</span><span>现在</span>
          </div>
        </div>
      </div>
    </div>

    <!-- ===== Analytics (range-toggled): top models / failures / spenders ===== -->
    <div class="flex items-center justify-between gap-3 pt-1">
      <h2 class="text-sm font-semibold text-white/80">使用分析 · <span class="text-white/45 font-normal">{{ rangeLabel }}</span></h2>
      <div class="inline-flex rounded-lg bg-white/[0.04] ring-1 ring-white/[0.08] p-0.5 text-xs">
        <button @click="range = 'day'"
                class="px-3 py-1 rounded-md transition-colors"
                :class="range === 'day' ? 'bg-white/10 text-white font-medium' : 'text-white/50 hover:text-white/80'">
          近 24h
        </button>
        <button @click="range = 'week'"
                class="px-3 py-1 rounded-md transition-colors"
                :class="range === 'week' ? 'bg-white/10 text-white font-medium' : 'text-white/50 hover:text-white/80'">
          近 3d
        </button>
      </div>
    </div>

    <div class="grid lg:grid-cols-2 gap-3">
      <div class="card">
        <div class="px-5 py-3 border-b border-white/[0.06]">
          <h2 class="text-sm font-semibold">热门模型</h2>
        </div>
        <div class="p-5">
          <div v-if="!modelUsage.length" class="text-center text-xs text-white/40 py-6">尚无生成记录</div>
          <div v-else class="space-y-3">
            <div v-for="m in modelUsage" :key="m.model" class="text-sm">
              <div class="flex items-baseline justify-between gap-3 mb-1">
                <span class="font-mono text-[12px] text-white/85 truncate">{{ m.model }}</span>
                <span class="flex items-baseline gap-2 shrink-0">
                  <span v-if="m.avg_ms" class="text-[10px] text-white/40 tabular-nums">{{ fmtMs(m.avg_ms) }}</span>
                  <span class="tabular-nums text-xs font-semibold">{{ m.count }}</span>
                </span>
              </div>
              <div class="h-1.5 rounded-full bg-white/[0.06] overflow-hidden">
                <div class="h-full rounded-full bg-gradient-to-r from-violet-400 to-fuchsia-500"
                     :style="{ width: ((m.count / usageMax) * 100) + '%' }"></div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="px-5 py-3 border-b border-white/[0.06]">
          <h2 class="text-sm font-semibold">失败原因 Top</h2>
        </div>
        <div class="p-5">
          <div v-if="!failures.length" class="text-center text-xs text-white/40 py-6">{{ rangeLabel }}内没有失败 — 一切正常</div>
          <div v-else class="space-y-2">
            <div v-for="f in failures" :key="f.reason"
                 class="flex items-start gap-3 px-2 py-2 rounded-lg hover:bg-white/[0.04]">
              <span class="w-1.5 h-1.5 mt-1.5 rounded-full bg-rose-400 shrink-0"></span>
              <div class="flex-1 min-w-0">
                <div class="text-xs text-white/85 break-all leading-snug">{{ f.reason }}</div>
              </div>
              <span class="tabular-nums text-xs font-semibold text-rose-300 shrink-0">×{{ f.count }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- ===== User consumption + spend summary ===== -->
    <div class="grid lg:grid-cols-2 gap-3">
      <div class="card">
        <div class="px-5 py-3 border-b border-white/[0.06]">
          <h2 class="text-sm font-semibold">用户消耗 Top · {{ rangeLabel }}</h2>
        </div>
        <div class="p-5">
          <div v-if="!topUsers.length" class="text-center text-xs text-white/40 py-6">{{ rangeLabel }}内无消耗记录</div>
          <div v-else class="space-y-3">
            <div v-for="u in topUsers" :key="u.user_id || u.name" class="text-sm">
              <div class="flex items-baseline justify-between gap-3 mb-1">
                <span class="text-[12px] text-white/85 truncate">{{ u.name }}</span>
                <span class="flex items-baseline gap-2 shrink-0">
                  <span class="text-[10px] text-white/40 tabular-nums">{{ u.count }} 次</span>
                  <span class="tabular-nums text-xs font-semibold text-amber-300">{{ fmtCredits(u.spent) }}</span>
                </span>
              </div>
              <div class="h-1.5 rounded-full bg-white/[0.06] overflow-hidden">
                <div class="h-full rounded-full bg-gradient-to-r from-amber-400 to-orange-500"
                     :style="{ width: ((u.spent / topUserMax) * 100) + '%' }"></div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="px-5 py-3 border-b border-white/[0.06]">
          <h2 class="text-sm font-semibold">积分消耗概览</h2>
        </div>
        <div class="p-5 grid grid-cols-2 gap-4">
          <div>
            <div class="text-xs text-white/55">近 24 小时</div>
            <div class="text-2xl font-semibold tabular-nums mt-1 text-amber-300">{{ fmtCredits(day.spent) }}</div>
            <div class="text-[11px] text-white/40 mt-1">{{ day.success }} 次成功生成</div>
          </div>
          <div>
            <div class="text-xs text-white/55">近 3 天</div>
            <div class="text-2xl font-semibold tabular-nums mt-1 text-amber-300">{{ fmtCredits(week.spent) }}</div>
            <div class="text-[11px] text-white/40 mt-1">{{ week.success }} 次成功生成</div>
          </div>
          <div>
            <div class="text-xs text-white/55">单次均价</div>
            <div class="text-xl font-semibold tabular-nums mt-1">{{ day.success ? fmtCredits(Math.round(day.spent / day.success)) : '—' }}</div>
            <div class="text-[11px] text-white/40 mt-1">24h 平均</div>
          </div>
          <div>
            <div class="text-xs text-white/55">消耗用户数</div>
            <div class="text-xl font-semibold tabular-nums mt-1">{{ topUsers.length }}</div>
            <div class="text-[11px] text-white/40 mt-1">{{ rangeLabel }}有消耗</div>
          </div>
        </div>
      </div>
    </div>

    <!-- ===== Recent activity (full width) ===== -->
    <div class="card">
      <div class="px-5 py-3 border-b border-white/[0.06] flex items-baseline justify-between">
        <h2 class="text-sm font-semibold">最近活动</h2>
        <router-link to="/admin/logs" class="text-[11px] text-white/55 hover:text-white">查看全部 →</router-link>
      </div>
      <div class="p-3">
        <div v-if="!recentLogs.length" class="text-center text-xs text-white/40 py-6">尚无活动</div>
        <div v-else>
          <div v-for="e in recentLogs" :key="e.id"
               class="flex items-center gap-3 px-2 py-2 text-xs rounded-lg hover:bg-white/[0.03]">
            <span class="w-1.5 h-1.5 rounded-full shrink-0" :class="logDot(e.status)"></span>
            <span class="text-[10px] uppercase tracking-wider font-medium w-9"
                  :class="e.kind === 'video' ? 'text-fuchsia-300' : 'text-indigo-300'">
              {{ e.kind === 'video' ? '视频' : '图像' }}
            </span>
            <span class="font-mono text-white/85 truncate w-40 shrink-0">{{ e.model }}</span>
            <span class="text-white/55 truncate flex-1 min-w-0">{{ e.prompt }}</span>
            <span class="text-white/40 tabular-nums whitespace-nowrap w-12 text-right">{{ fmtMs(e.elapsed_ms) }}</span>
            <span class="text-white/40 whitespace-nowrap text-right tabular-nums">{{ fmtTs(e.ts) }}</span>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
