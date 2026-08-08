<script setup>
// 邀请好友 — its own page (moved out of 设置). Invite data is real, from the
// logged-in account (auth.user). The inviter earns INVITE_REWARD 积分 once each
// invited friend completes their FIRST generation (生图).
import { ref, computed, onMounted } from 'vue'
import { auth, refreshMe } from '../auth'
import { api } from '../api'
import { fmtIso } from '../utils/format'
import Icon from '../components/Icon.vue'
import Mascot from '../components/Mascot.vue'

// Reward per completed invite — comes from the backend (credits.invite_reward),
// falling back to 3 until the response lands.
const INVITE_REWARD = ref(3)

const records = ref([])
const loading = ref(false)
const page = ref(1)
const pageSize = 10
async function loadRecords() {
  loading.value = true
  const r = await api('/auth/invites')
  loading.value = false
  if (r.ok) {
    records.value = r.data?.data || []
    if (r.data?.reward != null) INVITE_REWARD.value = Number(r.data.reward)
    if ((page.value - 1) * pageSize >= records.value.length) page.value = 1
  }
}

// Client-side numbered pagination — matches the admin 日志/图片管理 .pg strip.
const totalPages = computed(() => Math.max(1, Math.ceil(records.value.length / pageSize)))
const paged = computed(() => records.value.slice((page.value - 1) * pageSize, page.value * pageSize))
function goPage(n) { page.value = Math.max(1, Math.min(totalPages.value, n)) }
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

onMounted(async () => {
  await refreshMe()   // latest invite_count / invite_earned
  loadRecords()
})

const inviteCode = computed(() => auth.user?.invite_code || '')
const inviteCount = computed(() => Number(auth.user?.invite_count || 0))
const inviteEarned = computed(() => Number(auth.user?.invite_earned || 0))
const inviteUrl = computed(() => `${location.origin}/?ref=${inviteCode.value}`)

async function copyInvite() {
  try { await navigator.clipboard.writeText(inviteUrl.value); toast('邀请链接已复制') }
  catch { toast('复制失败') }
}
async function copyCode() {
  try { await navigator.clipboard.writeText(inviteCode.value); toast('邀请码已复制') }
  catch { toast('复制失败') }
}

// ---- Toast ----
const toastMsg = ref('')
let toastTimer = null
function toast(m) {
  toastMsg.value = m
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toastMsg.value = ''), 2200)
}
</script>

<template>
  <div class="theme-text space-y-10">
    <!-- header -->
    <header>
      <span class="sticker">🎀 奖励</span>
      <h1 class="mt-3 text-4xl md:text-5xl font-bold tracking-tight">邀请好友</h1>
      <p class="text-white/45 mt-2">好友用你的链接注册,并完成首次生图后,你得 {{ INVITE_REWARD }} 积分。</p>
    </header>

    <section class="relative rounded-3xl ring-1 ring-white/[0.08] p-7 md:p-8 overflow-hidden"
             style="background: radial-gradient(at 70% 30%,rgba(251,191,36,0.16) 0%, transparent 55%),linear-gradient(180deg,rgba(255,255,255,0.04),rgba(255,255,255,0.02))">
      <div class="inline-grid w-10 h-10 rounded-xl bg-amber-500/15 ring-1 ring-amber-400/30 grid place-items-center text-amber-300">
        <Icon name="accounts" class="w-4 h-4" />
      </div>

      <!-- summary -->
      <div class="mt-6 grid grid-cols-2 gap-3">
        <div class="rounded-xl bg-white/[0.04] ring-1 ring-white/[0.06] px-4 py-3">
          <div class="text-2xl font-bold tabular-nums">{{ inviteCount }}</div>
          <div class="text-[10px] text-white/40 mt-1 uppercase tracking-widest">已邀请</div>
        </div>
        <div class="rounded-xl bg-white/[0.04] ring-1 ring-white/[0.06] px-4 py-3">
          <div class="text-2xl font-bold tabular-nums">{{ inviteEarned.toLocaleString('en-US') }}</div>
          <div class="text-[10px] text-white/40 mt-1 uppercase tracking-widest">累计积分</div>
        </div>
      </div>

      <!-- code -->
      <div class="mt-6">
        <label class="block text-xs text-white/50 mb-2">邀请码</label>
        <div class="flex gap-2">
          <button @click="copyCode"
                  class="flex-1 rounded-xl bg-white/[0.05] ring-1 ring-white/10 hover:ring-white/30 hover:bg-white/[0.08] px-4 py-3 text-sm font-mono text-left transition-all">
            {{ inviteCode || '—' }}
          </button>
          <button @click="copyInvite"
                  class="rounded-xl bg-white text-black hover:bg-white/90 px-5 py-3 text-sm font-semibold transition-colors">
            复制链接
          </button>
        </div>
        <div class="mt-2 text-[11px] text-white/35 break-all font-mono">{{ inviteUrl }}</div>
      </div>
    </section>

    <!-- records -->
    <section>
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-lg font-semibold">邀请记录</h2>
        <button @click="loadRecords" class="text-xs text-white/50 hover:text-white inline-flex items-center gap-1.5 transition-colors">
          <Icon name="refresh" class="w-3.5 h-3.5" /> 刷新
        </button>
      </div>

      <div class="card overflow-hidden">
        <div v-if="loading && !records.length" class="text-center text-sm text-white/40 py-16">加载中…</div>
        <div v-else-if="!records.length" class="relative overflow-hidden flex flex-col items-center gap-3 text-white/40 py-14">
          <div class="anime-stars"></div>
          <Mascot :size="110" />
          <span class="text-sm">还没有人通过你的链接注册～快去分享吧</span>
        </div>

        <table v-else class="w-full text-sm">
          <colgroup>
            <col />
            <col class="w-24" />
            <col class="w-44" />
            <col class="w-44" />
            <col class="w-28" />
          </colgroup>
          <thead>
            <tr class="text-[10px] uppercase tracking-[0.2em] text-white/40 border-b border-white/[0.06]">
              <th class="text-left px-5 py-3 font-medium">注册用户名</th>
              <th class="text-right px-3 py-3 font-medium">奖励</th>
              <th class="text-left px-3 py-3 font-medium">注册时间</th>
              <th class="text-left px-3 py-3 font-medium">完成时间</th>
              <th class="text-right px-5 py-3 font-medium">状态</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(r, i) in paged" :key="i"
                class="border-b border-white/[0.04] hover:bg-white/[0.03] transition-colors">
              <td class="px-5 py-3.5 align-middle font-medium text-white/90 truncate">{{ r.name }}</td>
              <td class="px-3 py-3.5 align-middle text-right tabular-nums whitespace-nowrap"
                  :class="r.reward ? 'text-emerald-300' : 'text-white/25'">
                {{ r.reward ? '+' + r.reward : '—' }}
              </td>
              <td class="px-3 py-3.5 align-middle text-xs text-white/55 tabular-nums whitespace-nowrap">
                {{ r.registered_at ? fmtIso(r.registered_at) : '—' }}
              </td>
              <td class="px-3 py-3.5 align-middle text-xs text-white/55 tabular-nums whitespace-nowrap">
                {{ r.completed_at ? fmtIso(r.completed_at) : '—' }}
              </td>
              <td class="px-5 py-3.5 align-middle text-right">
                <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1"
                      :class="r.status === 'completed'
                        ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-400/30'
                        : 'bg-amber-500/10 text-amber-300 ring-amber-400/30'">
                  <span class="w-1.5 h-1.5 rounded-full"
                        :class="r.status === 'completed' ? 'bg-emerald-400' : 'bg-amber-400'"></span>
                  {{ r.status === 'completed' ? '已完成' : '待生图' }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>

        <!-- pagination — numbered with ellipsis, same as the admin pages -->
        <div v-if="records.length && totalPages > 1"
             class="flex items-center justify-between gap-3 border-t border-white/[0.06] px-5 py-3 text-xs text-white/55">
          <div>
            <span class="tabular-nums text-white/85">{{ (page - 1) * pageSize + 1 }}–{{ Math.min(records.length, page * pageSize) }}</span>
            <span class="ml-1">/ {{ records.length }} 条</span>
          </div>
          <div class="flex items-center gap-1">
            <template v-for="(n, i) in pageNumbers" :key="i">
              <span v-if="n === null" class="px-1 text-white/35">…</span>
              <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
            </template>
          </div>
        </div>
      </div>
    </section>

    <!-- toast -->
    <transition name="fade">
      <div v-if="toastMsg"
           class="fixed bottom-8 left-1/2 -translate-x-1/2 z-50 bg-white text-black text-sm font-medium px-5 py-2.5 rounded-full shadow-2xl">
        {{ toastMsg }}
      </div>
    </transition>
  </div>
</template>

<style scoped>
/* Numbered pagination buttons — dark base (matches the admin pages); the global
   .theme-text rules in style.css recolor these for light mode automatically. */
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
