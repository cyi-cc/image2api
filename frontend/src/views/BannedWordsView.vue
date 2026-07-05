<script setup>
import { ref, computed, onMounted } from 'vue'
import { api, jsonBody } from '../api'
import Icon from '../components/Icon.vue'

const items = ref([])
const loading = ref(false)
const newWord = ref('')
const toast = ref('')
let toastTimer = null
function flash(msg) { toast.value = msg; clearTimeout(toastTimer); toastTimer = setTimeout(() => (toast.value = ''), 1800) }

async function load() {
  loading.value = true
  const r = await api('/banned-words')
  items.value = r.data?.data || []
  loading.value = false
}

async function add() {
  const word = newWord.value.trim()
  if (!word) { flash('违禁词不能为空'); return }
  const r = await api('/banned-words', jsonBody('POST', { word }))
  if (r.ok) { newWord.value = ''; flash('已添加'); load() }
  else flash(r.data?.detail || '添加失败')
}

// bulk import — paste words separated by newlines / commas / 、 / ;
const importOpen = ref(false)
const importText = ref('')
const importing = ref(false)
async function doImport() {
  const text = importText.value.trim()
  if (!text) { flash('请先粘贴要导入的违禁词'); return }
  importing.value = true
  const r = await api('/banned-words/import', jsonBody('POST', { text }))
  importing.value = false
  if (r.ok) {
    importOpen.value = false
    importText.value = ''
    flash(`导入完成：新增 ${r.data?.added ?? 0} 个，跳过 ${r.data?.skipped ?? 0} 个`) 
    load()
  } else flash(r.data?.detail || '导入失败')
}

async function del(w) {
  if (!confirm(`删除违禁词「${w.word}」?`)) return
  const r = await api(`/banned-words/${w.id}`, { method: 'DELETE' })
  if (r.ok) { flash('已删除'); selected.value.delete(w.id); load() }
  else flash(r.data?.detail || '删除失败')
}

// multi-select — header checkbox selects/deselects the CURRENT PAGE only.
const selected = ref(new Set())
function toggleSelect(id) {
  const s = new Set(selected.value)
  s.has(id) ? s.delete(id) : s.add(id)
  selected.value = s
}
const allSelected = computed(() =>
  pagedItems.value.length > 0 && pagedItems.value.every((w) => selected.value.has(w.id)))
function toggleSelectAll() {
  const s = new Set(selected.value)
  if (allSelected.value) pagedItems.value.forEach((w) => s.delete(w.id))
  else pagedItems.value.forEach((w) => s.add(w.id))
  selected.value = s
}
async function delSelected() {
  const ids = [...selected.value]
  if (!ids.length) return
  if (!confirm(`确认删除选中的 ${ids.length} 个违禁词?`)) return
  let ok = 0
  for (const id of ids) {
    const r = await api(`/banned-words/${id}`, { method: 'DELETE' })
    if (r.ok) ok++
  }
  selected.value = new Set()
  flash(`已删除 ${ok} 个`)
  load()
}

// pagination (client-side; the full list arrives in one payload)
const page = ref(1)
const pageSize = 20
const totalPages = computed(() => Math.max(1, Math.ceil(items.value.length / pageSize)))
const pagedItems = computed(() => {
  const start = (Math.min(page.value, totalPages.value) - 1) * pageSize
  return items.value.slice(start, start + pageSize)
})
function goPage(n) {
  const t = Math.max(1, Math.min(totalPages.value, n))
  if (t !== page.value) page.value = t
}
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

onMounted(load)
</script>

<template>
  <section class="theme-text space-y-4">
    <div class="card p-4 flex items-center justify-between gap-3 flex-wrap">
      <div>
        <h2 class="text-sm font-semibold">违禁词管理</h2>
        <p class="text-xs text-white/45 mt-0.5">提示词包含违禁词的生成请求(画图台 + API)会被<strong class="text-white/70">直接拦截</strong>,并累计触发次数(见用户管理)。匹配不区分大小写。</p>
      </div>
      <div class="flex items-center gap-2">
        <button v-if="selected.size" @click="delSelected" class="btn-soft danger shrink-0" title="删除选中的违禁词">
          <Icon name="trash" class="w-3.5 h-3.5" /> 删除选中 ({{ selected.size }})
        </button>
        <input v-model="newWord" @keyup.enter="add" class="field !py-1.5 text-xs w-52" placeholder="输入违禁词后回车" />
        <button @click="add" class="btn-primary shrink-0">+ 添加</button>
        <button @click="importOpen = true" class="btn-soft shrink-0">批量导入</button>
      </div>
    </div>

    <div class="card overflow-hidden">
      <table class="w-full text-sm">
        <thead>
          <tr class="text-[10px] uppercase tracking-[0.2em] text-white/40 border-b border-white/[0.06]">
            <th class="text-center px-3 py-3 font-medium w-9">
              <input type="checkbox" :checked="allSelected" @change="toggleSelectAll" class="chk" title="全选本页" />
            </th>
            <th class="text-left px-5 py-3 font-medium">违禁词</th>
            <th class="text-right px-3 py-3 font-medium">触发次数</th>
            <th class="text-left px-3 py-3 font-medium">添加时间</th>
            <th class="text-right px-3 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading && !items.length"><td colspan="5" class="text-center text-xs text-white/40 py-10">加载中…</td></tr>
          <tr v-else-if="!items.length"><td colspan="5" class="text-center text-xs text-white/40 py-10">还没有违禁词</td></tr>
          <tr v-for="w in pagedItems" :key="w.id" class="border-b border-white/[0.04] hover:bg-white/[0.03] transition-colors">
            <td class="px-3 py-3.5 align-middle text-center">
              <input type="checkbox" :checked="selected.has(w.id)" @change="toggleSelect(w.id)" @click.stop class="chk" />
            </td>
            <td class="px-5 py-3.5 align-middle text-sm font-medium text-white/90">{{ w.word }}</td>
            <td class="px-3 py-3.5 align-middle text-right tabular-nums" :class="w.hits > 0 ? 'text-rose-300' : 'text-white/50'">{{ w.hits }}</td>
            <td class="px-3 py-3.5 align-middle text-xs text-white/50">{{ new Date(w.created_at).toLocaleString() }}</td>
            <td class="px-3 py-3.5 align-middle text-right">
              <button @click="del(w)" class="act danger" title="删除"><Icon name="trash" class="w-3.5 h-3.5" /></button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="totalPages > 1" class="flex items-center justify-between px-5 py-3 border-t border-white/[0.06] text-xs text-white/45">
        <div><span class="tabular-nums text-white/75">{{ items.length ? (Math.min(page, totalPages) - 1) * pageSize + 1 : 0 }}–{{ Math.min(items.length, Math.min(page, totalPages) * pageSize) }}</span><span class="ml-1">/ {{ items.length }} 条</span></div>
        <div class="flex items-center gap-1">
          <template v-for="(n, i) in pageNumbers" :key="i">
            <span v-if="n === null" class="px-1 text-white/30">…</span>
            <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
          </template>
        </div>
      </div>
    </div>

    <div v-if="importOpen" class="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" @click.self="importOpen = false">
      <div class="card w-full max-w-lg p-5 space-y-3">
        <h3 class="text-sm font-semibold">批量导入违禁词</h3>
        <p class="text-xs text-white/45">每行一个，或用逗号、顿号、分号分隔；已存在的词会自动跳过。</p>
        <textarea v-model="importText" rows="8" class="field w-full text-xs font-mono resize-y" placeholder="违禁词1&#10;违禁词2&#10;违禁词3"></textarea>
        <div class="flex justify-end gap-2">
          <button @click="importOpen = false" class="btn-soft">取消</button>
          <button @click="doImport" :disabled="importing" class="btn-primary">{{ importing ? '导入中…' : '导入' }}</button>
        </div>
      </div>
    </div>

    <transition name="fade">
      <div v-if="toast" class="fixed bottom-6 left-1/2 -translate-x-1/2 z-[60] bg-slate-900 text-white text-xs px-4 py-2 rounded-lg shadow-lg">{{ toast }}</div>
    </transition>
  </section>
</template>

<style scoped>
.act {
  display: inline-flex; align-items: center; justify-content: center;
  width: 1.9rem; height: 1.9rem; border-radius: 0.5rem;
  color: rgb(255 255 255 / 0.7); background: rgb(255 255 255 / 0.04);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
  transition: background 0.15s, color 0.15s;
}
.act:hover { background: rgb(255 255 255 / 0.1); color: white; }
.act.danger { color: rgb(253 164 175); background: rgb(244 63 94 / 0.12); box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3); }
.act.danger:hover { color: white; background: rgb(244 63 94 / 0.25); }
.btn-soft.danger {
  color: rgb(253 164 175);
  background: rgb(244 63 94 / 0.12);
  box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3);
}
.btn-soft.danger:hover {
  color: white;
  background: rgb(244 63 94 / 0.25);
}
.chk { accent-color: rgb(217 70 239); width: 0.9rem; height: 0.9rem; cursor: pointer; }
.pg { min-width: 1.75rem; padding: 0.3rem 0.55rem; font-size: 0.72rem; font-weight: 500; text-align: center; border-radius: 0.45rem; color: rgb(255 255 255 / 0.7); background: rgb(255 255 255 / 0.04); box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08); transition: background 0.15s, color 0.15s; }
.pg:hover:not(.pg-on) { background: rgb(255 255 255 / 0.1); color: white; }
.pg-on { background: rgb(255 255 255 / 0.92); color: rgb(15 23 42); box-shadow: none; }
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
