<script setup>
import { ref, computed, onMounted } from 'vue'
import { api, jsonBody } from '../api'
import { fmtTs, fmtDate, fmtClock } from '../utils/format'
import Icon from '../components/Icon.vue'
import SelectMenu from '../components/SelectMenu.vue'
import { points } from '../credits'

const items = ref([])
const stats = ref({ total: 0, active: 0, disabled: 0, admins: 0, credits_total: 0 })
const loading = ref(false)
const search = ref('')
const roleFilter = ref('')      // '' | 'admin' | 'user'
const statusFilter = ref('')    // '' | 'active' | 'disabled'

const page = ref(1)
const pageSize = ref(20)

const showAdd = ref(false)
const editing = ref(null)
const toast = ref('')

const addForm = ref({ email: '', name: '', password: '', role: 'user', credits: 0, notes: '', concurrency_group_id: '' })

const STATUS_OPTIONS = [
  { value: 'active', label: '正常' },
  { value: 'disabled', label: '禁用' },
]

// 代理 = 走代理价的客户(不享管理权限)。
// 管理员唯一:不能通过用户管理创建/改成管理员,所以选项只给 普通用户 / 代理。
const ROLE_OPTIONS = [
  { value: 'user', label: '普通用户' },
  { value: 'agent', label: '代理' },
]
const roleLabel = (r) => ({ user: '用户', agent: '代理', admin: '管理员' }[r] || '用户')

// Concurrency groups — resolve a user's group id → name/limit for the table,
// and offer them in the edit form.
const cgroups = ref([])
const cgroupOptions = computed(() => cgroups.value.map((g) => ({ value: g.id, label: g.name })))
const cgroupById = computed(() => Object.fromEntries(cgroups.value.map((g) => [g.id, g])))
function cgroupLabel(id) {
  const g = cgroupById.value[id]
  if (!g) return '—'
  return g.max_concurrency > 0 ? `${g.name} · ${g.max_concurrency}` : `${g.name} · 不限`
}
async function loadGroups() {
  const r = await api('/concurrency-groups')
  cgroups.value = r.data?.data || []
  // Default the 新建用户 form to the default registration group.
  if (!addForm.value.concurrency_group_id) {
    const def = cgroups.value.find((g) => g.is_default) || cgroups.value[0]
    if (def) addForm.value.concurrency_group_id = def.id
  }
}

async function load() {
  loading.value = true
  const r = await api('/users')
  items.value = r.data?.data || []
  stats.value = r.data?.stats || stats.value
  loading.value = false
}
onMounted(() => { load(); loadGroups() })

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  // Newest first — created_at desc, falling back to id so users without a
  // timestamp still get a stable order.
  const sorted = [...items.value].sort((a, b) => (b.created_at || 0) - (a.created_at || 0))
  return sorted.filter((u) => {
    if (roleFilter.value && u.role !== roleFilter.value) return false
    if (statusFilter.value && u.status !== statusFilter.value) return false
    if (q && !(
      (u.email || '').toLowerCase().includes(q) ||
      (u.name || '').toLowerCase().includes(q) ||
      (u.id || '').toLowerCase().includes(q)
    )) return false
    return true
  })
})

// Client-side pagination — user list is bounded.
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

let toastTimer = null
function flash(m) {
  toast.value = m
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toast.value = ''), 2000)
}

async function createUser() {
  if (!addForm.value.email.trim()) { flash('请输入邮箱'); return }
  const r = await api('/users', jsonBody('POST', addForm.value))
  if (r.ok) {
    showAdd.value = false
    const def = cgroups.value.find((g) => g.is_default) || cgroups.value[0]
    addForm.value = { email: '', name: '', password: '', role: 'user', credits: 0, notes: '', concurrency_group_id: def ? def.id : '' }
    flash('用户已创建')
    load()
  } else flash(r.data?.detail || '创建失败')
}

async function saveEdit() {
  const u = editing.value
  // Email + 用户名 are intentionally NOT in the patch — they're displayed
  // read-only in the form, and the admin shouldn't be in the habit of
  // rewriting a user's identity from this page.
  const patch = {
    status: u.status,
    credits: u.credits,
    role: u.role,
    notes: u.notes || '',
    concurrency_group_id: u.concurrency_group_id || '',
  }
  if (u._newPassword) patch.password = u._newPassword
  const r = await api(`/users/${u.id}`, jsonBody('PATCH', patch))
  if (r.ok) { editing.value = null; flash('已保存'); load() }
  else flash(r.data?.detail || '保存失败')
}

async function toggleStatus(u) {
  // Optimistic: flip instantly so the switch moves the moment it's clicked;
  // persist in the background and revert on failure (no full table reload).
  const prev = u.status
  const next = u.status === 'active' ? 'disabled' : 'active'
  u.status = next
  const r = await api(`/users/${u.id}`, jsonBody('PATCH', { status: next }))
  if (r.ok) flash(next === 'active' ? '已启用' : '已禁用')
  else { u.status = prev; flash(r.data?.detail || '操作失败') }
}

async function delUser(u) {
  if (!confirm(`删除用户 ${u.email}? 此操作不可恢复`)) return
  const r = await api(`/users/${u.id}`, { method: 'DELETE' })
  if (r.ok) { flash('已删除'); load() } else flash(r.data?.detail || '删除失败')
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
  pagedItems.value.length > 0 && pagedItems.value.every((u) => selected.value.has(u.id)))
function toggleSelectAll() {
  const s = new Set(selected.value)
  if (allSelected.value) pagedItems.value.forEach((u) => s.delete(u.id))
  else pagedItems.value.forEach((u) => s.add(u.id))
  selected.value = s
}
async function delSelected() {
  const ids = [...selected.value]
  if (!ids.length) return
  if (!confirm(`确认删除选中的 ${ids.length} 个用户?此操作不可恢复。`)) return
  const r = await api('/users/delete-bulk', jsonBody('POST', { ids }))
  if (r.ok) { flash(`已删除 ${r.data?.deleted ?? ids.length} 个`); selected.value = new Set(); load() }
  else flash(r.data?.detail || '删除失败')
}

async function quickCredits(u, delta) {
  const r = await api(`/users/${u.id}/credits`, jsonBody('POST', { delta }))
  if (r.ok) { flash(`已${delta > 0 ? '增加' : '扣除'} ${Math.abs(delta).toLocaleString('en-US')} 积分`); load() }
  else flash(r.data?.detail || '调整失败')
}
</script>

<template>
  <section class="space-y-4">
    <!-- KPI strip — same shape as LogsView / ModelsView -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-white/45">用户总数</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums">{{ stats.total }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-emerald-300/80">正常</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-emerald-300">{{ stats.active }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-fuchsia-300/80">管理员</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-fuchsia-300">{{ stats.admins }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-amber-300/80">总积分</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-amber-300">{{ points(stats.credits_total).toLocaleString('en-US') }}</div>
      </div>
    </div>

    <!-- Toolbar -->
    <div class="card p-3 flex items-center gap-3 flex-wrap">
      <div class="flex items-center gap-1">
        <button @click="setFilter(() => roleFilter = '')" class="fp" :class="roleFilter === '' && 'fp-on'">全部角色</button>
        <button @click="setFilter(() => roleFilter = 'admin')" class="fp" :class="roleFilter === 'admin' && 'fp-fuchsia'">
          <span class="w-1.5 h-1.5 rounded-full bg-fuchsia-400"></span>管理员
        </button>
        <button @click="setFilter(() => roleFilter = 'agent')" class="fp" :class="roleFilter === 'agent' && 'fp-amber'">
          <span class="w-1.5 h-1.5 rounded-full bg-amber-400"></span>代理
        </button>
        <button @click="setFilter(() => roleFilter = 'user')" class="fp" :class="roleFilter === 'user' && 'fp-on'">用户</button>
      </div>
      <div class="w-px h-5 bg-white/10"></div>
      <div class="flex items-center gap-1">
        <button @click="setFilter(() => statusFilter = '')" class="fp" :class="statusFilter === '' && 'fp-on'">所有状态</button>
        <button @click="setFilter(() => statusFilter = 'active')" class="fp" :class="statusFilter === 'active' && 'fp-emerald'">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>正常
        </button>
        <button @click="setFilter(() => statusFilter = 'disabled')" class="fp" :class="statusFilter === 'disabled' && 'fp-rose'">
          <span class="w-1.5 h-1.5 rounded-full bg-rose-400"></span>禁用
        </button>
      </div>
      <div class="flex-1 min-w-[200px]">
        <input v-model="search" class="field !py-1.5 text-xs" placeholder="搜索 邮箱 / 用户名 / ID…" />
      </div>
      <button v-if="selected.size" @click="delSelected" class="btn-soft danger" title="删除选中的用户">
        <Icon name="trash" class="w-3.5 h-3.5" /> 删除选中 ({{ selected.size }})
      </button>
      <button @click="load" class="btn-soft">
        <Icon name="refresh" class="w-3.5 h-3.5" /> 刷新
      </button>
      <button @click="showAdd = true" class="btn-primary">
        <Icon name="plus" class="w-3.5 h-3.5" /> 新建用户
      </button>
    </div>

    <!-- Table -->
    <div class="card overflow-hidden">
      <div v-if="loading && !items.length" class="text-center text-sm text-white/40 py-20">加载中…</div>
      <div v-else-if="!filtered.length" class="flex flex-col items-center gap-3 text-white/40 py-20">
        <span class="w-14 h-14 rounded-2xl bg-white/[0.04] grid place-items-center">
          <Icon name="accounts" class="w-6 h-6" />
        </span>
        <span class="text-sm">{{ items.length ? '没有匹配的用户' : '还没有用户' }}</span>
        <button v-if="!items.length" @click="showAdd = true" class="btn-soft mt-1">新建第一个</button>
      </div>

      <table v-else class="w-full text-sm table-fixed">
        <colgroup>
          <col class="w-9" />      <!-- select -->
          <col class="w-40" />     <!-- username -->
          <col />                  <!-- email (flex) -->
          <col class="w-36" />     <!-- notes -->
          <col class="w-28" />     <!-- concurrency -->
          <col class="w-20" />     <!-- role -->
          <col class="w-16" />     <!-- status switch -->
          <col class="w-24" />     <!-- credits -->
          <col class="w-24" />     <!-- recharge total -->
          <col class="w-20" />     <!-- generation count -->
          <col class="w-20" />     <!-- banned word hits -->
          <col class="w-28" />     <!-- registered -->
          <col class="w-28" />     <!-- last login -->
          <col class="w-32" />     <!-- login IP -->
          <col class="w-24" />     <!-- actions -->
        </colgroup>
        <thead>
          <tr class="text-[10px] uppercase tracking-[0.2em] text-white/40 border-b border-white/[0.06]">
            <th class="text-center px-3 py-3 font-medium">
              <input type="checkbox" :checked="allSelected" @change="toggleSelectAll"
                     class="chk" title="全选" />
            </th>
            <th class="text-left px-5 py-3 font-medium">用户名</th>
            <th class="text-left px-3 py-3 font-medium">邮箱</th>
            <th class="text-left px-3 py-3 font-medium">备注</th>
            <th class="text-left px-3 py-3 font-medium">并发</th>
            <th class="text-left px-3 py-3 font-medium">角色</th>
            <th class="text-left px-3 py-3 font-medium">状态</th>
            <th class="text-right px-3 py-3 font-medium">积分</th>
            <th class="text-right px-3 py-3 font-medium">累计充值</th>
            <th class="text-right px-3 py-3 font-medium">生图次数</th>
            <th class="text-right px-3 py-3 font-medium">违禁触发</th>
            <th class="text-left px-3 py-3 font-medium">注册时间</th>
            <th class="text-left px-3 py-3 font-medium">最近登录</th>
            <th class="text-left px-3 py-3 font-medium">登录 IP</th>
            <th class="text-right px-3 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in pagedItems" :key="u.id"
              class="border-b border-white/[0.04] hover:bg-white/[0.03] transition-colors">
            <td class="px-3 py-3.5 align-middle text-center">
              <input type="checkbox" :checked="selected.has(u.id)" @change="toggleSelect(u.id)" @click.stop
                     class="chk" />
            </td>
            <td class="px-5 py-3.5 align-middle text-sm font-medium text-white/90 truncate" :title="u.name || '—'">
              {{ u.name || '—' }}
            </td>
            <td class="px-3 py-3.5 align-middle text-xs text-white/75 truncate" :title="u.email">
              {{ u.email || '—' }}
            </td>
            <td class="px-3 py-3.5 align-middle text-xs truncate" :class="u.notes ? 'text-white/70' : 'text-white/25'" :title="u.notes || ''">
              {{ u.notes || '—' }}
            </td>
            <td class="px-3 py-3.5 align-middle text-xs truncate text-white/70" :title="cgroupLabel(u.concurrency_group_id)">
              {{ cgroupLabel(u.concurrency_group_id) }}
            </td>
            <td class="px-3 py-3.5 align-middle">
              <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1 whitespace-nowrap"
                    :class="u.role === 'admin'
                      ? 'bg-fuchsia-500/10 text-fuchsia-300 ring-fuchsia-400/30'
                      : u.role === 'agent'
                        ? 'bg-amber-500/10 text-amber-300 ring-amber-400/30'
                        : 'bg-white/[0.06] text-white/70 ring-white/15'">
                <span class="w-1.5 h-1.5 rounded-full"
                      :class="u.role === 'admin' ? 'bg-fuchsia-400' : u.role === 'agent' ? 'bg-amber-400' : 'bg-slate-400'"></span>
                {{ roleLabel(u.role) }}
              </span>
            </td>
            <td class="px-3 py-3.5 align-middle">
              <button class="sw" :class="u.status === 'active' && 'sw-on'"
                      :aria-pressed="u.status === 'active'"
                      :title="u.status === 'active' ? '点击禁用账号' : '点击启用账号'"
                      @click="toggleStatus(u)">
                <span class="sw-thumb"></span>
              </button>
            </td>
            <td class="px-3 py-3.5 align-middle text-right tabular-nums text-white/85 whitespace-nowrap">
              {{ points(u.credits).toLocaleString('en-US') }}
            </td>
            <td class="px-3 py-3.5 align-middle text-right tabular-nums whitespace-nowrap"
                :class="u.recharge_total > 0 ? 'text-emerald-300' : 'text-white/25'">
              ¥{{ (u.recharge_total || 0).toLocaleString('en-US') }}
            </td>
            <td class="px-3 py-3.5 align-middle text-right tabular-nums whitespace-nowrap"
                :class="u.generation_count > 0 ? 'text-white/85' : 'text-white/25'">
              {{ (u.generation_count || 0).toLocaleString('en-US') }}
            </td>
            <td class="px-3 py-3.5 align-middle text-right tabular-nums whitespace-nowrap"
                :class="u.banned_word_hits > 0 ? 'text-rose-300' : 'text-white/25'">
              {{ (u.banned_word_hits || 0).toLocaleString('en-US') }}
            </td>
            <td class="px-3 py-3.5 align-middle text-xs whitespace-nowrap">
              <div v-if="u.created_at" class="leading-tight" :title="fmtTs(u.created_at)">
                <div class="text-white/65 tabular-nums">{{ fmtDate(u.created_at) }}</div>
                <div class="text-white/35 tabular-nums">{{ fmtClock(u.created_at) }}</div>
              </div>
              <span v-else class="text-white/25">—</span>
            </td>
            <td class="px-3 py-3.5 align-middle text-xs whitespace-nowrap">
              <div v-if="u.last_login_at" class="leading-tight" :title="fmtTs(u.last_login_at)">
                <div class="text-white/65 tabular-nums">{{ fmtDate(u.last_login_at) }}</div>
                <div class="text-white/35 tabular-nums">{{ fmtClock(u.last_login_at) }}</div>
              </div>
              <span v-else class="text-white/25">从未登录</span>
            </td>
            <td class="px-3 py-3.5 align-middle text-xs font-mono text-white/55 truncate" :title="u.last_login_ip || ''">
              {{ u.last_login_ip || '—' }}
            </td>
            <td class="px-3 py-3.5 align-middle text-right whitespace-nowrap">
              <div class="inline-flex items-center gap-1">
                <button @click="editing = JSON.parse(JSON.stringify(u))" class="act" title="编辑">
                  <Icon name="config" class="w-3.5 h-3.5" />
                </button>
                <button @click="delUser(u)" class="act danger" title="删除">
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

    <!-- Add modal -->
    <div v-if="showAdd"
         class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-start justify-center p-4 overflow-y-auto"
         @click.self="showAdd = false">
      <div class="card !shadow-2xl my-12 w-full max-w-md">
        <div class="px-5 py-4 border-b border-white/[0.06] flex items-center justify-between">
          <h2 class="text-sm font-semibold">新建用户</h2>
          <button @click="showAdd = false" class="text-white/40 hover:text-white">
            <Icon name="close" class="w-5 h-5" />
          </button>
        </div>
        <div class="p-5 space-y-3">
          <div>
            <label class="lbl">邮箱 <span class="text-rose-300">*</span></label>
            <input v-model="addForm.email" class="field" placeholder="user@example.com" />
          </div>
          <div>
            <label class="lbl">用户名</label>
            <input v-model="addForm.name" class="field" placeholder="6-24位,仅字母数字" />
          </div>
          <div>
            <label class="lbl">初始密码</label>
            <input v-model="addForm.password" type="password" class="field" placeholder="留空表示不设密码；否则需满足8-24位且含大小写/数字/符号" />
          </div>
          <div>
            <label class="lbl">初始积分</label>
            <input v-model.number="addForm.credits" type="number" min="0" step="1" class="field" />
          </div>
          <div>
            <label class="lbl">角色</label>
            <SelectMenu v-model="addForm.role" :options="ROLE_OPTIONS" />
          </div>
          <div>
            <label class="lbl">并发分组</label>
            <SelectMenu v-model="addForm.concurrency_group_id" :options="cgroupOptions" placeholder="选择分组" />
          </div>
          <div>
            <label class="lbl">备注 <span class="text-white/35">(可选)</span></label>
            <textarea v-model="addForm.notes" rows="2" class="field resize-none" placeholder="给该用户加个备注,仅管理员可见"></textarea>
          </div>
          <div class="flex justify-end gap-2 pt-2">
            <button @click="showAdd = false" class="btn-soft">取消</button>
            <button @click="createUser" class="btn-primary">创建</button>
          </div>
        </div>
      </div>
    </div>

    <!-- Edit modal -->
    <div v-if="editing"
         class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-start justify-center p-4 overflow-y-auto"
         @click.self="editing = null">
      <div class="card !shadow-2xl my-12 w-full max-w-md">
        <div class="px-5 py-4 border-b border-white/[0.06] flex items-center justify-between">
          <h2 class="text-sm font-semibold">编辑用户</h2>
          <button @click="editing = null" class="text-white/40 hover:text-white">
            <Icon name="close" class="w-5 h-5" />
          </button>
        </div>
        <div class="p-5 space-y-3">
          <!-- Email + 用户名 are show-only — identity edits go through register
               or a future support flow, not from this maintenance screen. -->
          <div>
            <label class="lbl">邮箱</label>
            <input :value="editing.email" disabled class="field font-mono" />
          </div>
          <div>
            <label class="lbl">用户名</label>
            <input :value="editing.name" disabled class="field" />
          </div>
          <div>
            <label class="lbl">状态</label>
            <SelectMenu v-model="editing.status" :options="STATUS_OPTIONS" />
          </div>
          <div>
            <label class="lbl">角色</label>
            <!-- 管理员唯一:管理员账号角色锁定,不可改;其他人只能在 普通用户/代理 间切换 -->
            <input v-if="editing.role === 'admin'" value="管理员(唯一,不可更改)" disabled class="field" />
            <SelectMenu v-else v-model="editing.role" :options="ROLE_OPTIONS" />
          </div>
          <div>
            <label class="lbl">积分</label>
            <input v-model.number="editing.credits" type="number" min="0" step="1" class="field" />
          </div>
          <div>
            <label class="lbl">并发分组 <span class="text-white/35">(限制同时生成数)</span></label>
            <SelectMenu v-model="editing.concurrency_group_id" :options="cgroupOptions" placeholder="选择分组" />
          </div>
          <div>
            <label class="lbl">备注 <span class="text-white/35">(可选)</span></label>
            <textarea v-model="editing.notes" rows="2" class="field resize-none" placeholder="给该用户加个备注,仅管理员可见"></textarea>
          </div>
          <div>
            <label class="lbl">重置密码 <span class="text-white/35">(留空保持不变)</span></label>
            <input v-model="editing._newPassword" type="password" class="field" placeholder="新密码(8-24位,含大小写/数字/符号)" autocomplete="new-password" />
          </div>
          <div class="flex justify-end gap-2 pt-2">
            <button @click="editing = null" class="btn-soft">取消</button>
            <button @click="saveEdit" class="btn-primary">保存</button>
          </div>
        </div>
      </div>
    </div>

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
.lbl {
  display: block;
  font-size: 0.72rem;
  font-weight: 500;
  color: rgb(255 255 255 / 0.55);
  margin-bottom: 0.4rem;
}

/* --- filter pills (mirrors LogsView/ModelsView) */
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
.fp-fuchsia {
  background: rgb(217 70 239 / 0.22);
  color: rgb(245 208 254);
  box-shadow: inset 0 0 0 1px rgb(245 208 254 / 0.45);
}
.fp-amber {
  background: rgb(245 158 11 / 0.22);
  color: rgb(252 211 77);
  box-shadow: inset 0 0 0 1px rgb(252 211 77 / 0.45);
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

/* disabled inputs in the edit modal — readable, but visually 'cool' so the
   admin knows they can't change them. */
.field:disabled {
  opacity: 0.65;
  cursor: not-allowed;
  background: rgb(255 255 255 / 0.025);
}

/* iOS-style switch for the 状态 column — mirrors the one in ModelsView. */
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

.fade-enter-active, .fade-leave-active { transition: opacity 0.15s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
