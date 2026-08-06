<script setup>
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import Icon from '../components/Icon.vue'
import ModelFormModal from '../components/ModelFormModal.vue'
import CustomModelModal from '../components/CustomModelModal.vue'
import TestModal from '../components/TestModal.vue'
import { points } from '../credits'

const models = ref([])
const loading = ref(false)
const showForm = ref(false)
const showCustom = ref(false)
const editing = ref(null) // null = add, object = edit
const testing = ref(null) // model being tested, or null

const kindFilter = ref('')      // '' | 'image' | 'video'
const statusFilter = ref('')    // '' | 'enabled' | 'disabled'
const search = ref('')

const TYPE_LABEL = { image: '生图', video: '生视频' }
const REF_MODE_LABEL = { none: '无', frame: '首帧/首尾帧', asset: '参考图模式' }

function capEmpty(m) {
  if (m.type === 'image') return !(m.ratios || []).length && !m.image_to_image && (m.reference_mode === 'none' || !m.reference_mode)
  return !(m.durations || []).length && !(m.resolutions || []).length && !m.max_reference_images && !m.id?.startsWith('seedance') && !(m.ratios || []).length
}

async function loadModels() {
  loading.value = true
  const r = await api('/managed-models')
  models.value = r.data?.data || []
  loading.value = false
}

function openAdd() { editing.value = null; showForm.value = true }
function openEdit(m) { editing.value = { ...m }; showForm.value = true }
function onSaved() { showForm.value = false; loadModels() }

async function toggleEnabled(m) {
  // Optimistic: flip the switch instantly, persist in the background, revert on
  // failure. Avoids the lag of awaiting the PATCH + a full table reload before
  // the toggle visibly moves.
  const cur = m.enabled !== false
  const next = !cur
  m.enabled = next
  const r = await api(`/managed-models/${encodeURIComponent(m.id)}`, {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled: next }),
  })
  if (!r.ok) m.enabled = cur
}

async function remove(m) {
  if (!confirm(`确认删除模型 ${m.id}?`)) return
  await api(`/managed-models/${encodeURIComponent(m.id)}`, { method: 'DELETE' })
  loadModels()
}

const stats = computed(() => {
  const total = models.value.length
  const image = models.value.filter((m) => m.type === 'image').length
  const video = models.value.filter((m) => m.type === 'video').length
  const enabled = models.value.filter((m) => m.enabled !== false).length
  return { total, image, video, enabled, disabled: total - enabled }
})

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  return models.value.filter((m) => {
    if (kindFilter.value && m.type !== kindFilter.value) return false
    if (statusFilter.value === 'enabled' && m.enabled === false) return false
    if (statusFilter.value === 'disabled' && m.enabled !== false) return false
    if (q && !(m.id.toLowerCase().includes(q) || (m.alias || '').toLowerCase().includes(q) || (m.provider || '').toLowerCase().includes(q))) return false
    return true
  })
})

onMounted(loadModels)
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
        <div class="text-[11px] uppercase tracking-wider text-indigo-300/80">图像</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-indigo-300">{{ stats.image }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-fuchsia-300/80">视频</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-fuchsia-300">{{ stats.video }}</div>
      </div>
      <div class="card p-4">
        <div class="text-[11px] uppercase tracking-wider text-emerald-300/80">启用</div>
        <div class="text-2xl font-semibold mt-1 tabular-nums text-emerald-300">{{ stats.enabled }}<span class="text-white/35 text-lg ml-1">/ {{ stats.total }}</span></div>
      </div>
    </div>

    <!-- Toolbar -->
    <div class="card p-3 flex items-center gap-3 flex-wrap">
      <div class="flex items-center gap-1">
        <button @click="kindFilter = ''" class="fp" :class="kindFilter === '' && 'fp-on'">全部</button>
        <button @click="kindFilter = 'image'" class="fp" :class="kindFilter === 'image' && 'fp-on'">图像</button>
        <button @click="kindFilter = 'video'" class="fp" :class="kindFilter === 'video' && 'fp-on'">视频</button>
      </div>
      <div class="w-px h-5 bg-white/10"></div>
      <div class="flex items-center gap-1">
        <button @click="statusFilter = ''" class="fp" :class="statusFilter === '' && 'fp-on'">所有状态</button>
        <button @click="statusFilter = 'enabled'" class="fp" :class="statusFilter === 'enabled' && 'fp-emerald'">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>启用
        </button>
        <button @click="statusFilter = 'disabled'" class="fp" :class="statusFilter === 'disabled' && 'fp-white'">
          <span class="w-1.5 h-1.5 rounded-full bg-white/40"></span>停用
        </button>
      </div>
      <div class="flex-1 min-w-[200px]">
        <input v-model="search" class="field !py-1.5 text-xs" placeholder="搜索 模型 ID / 别名 / Provider…" />
      </div>
      <button @click="loadModels" class="btn-soft">
        <Icon name="refresh" class="w-3.5 h-3.5" /> 刷新
      </button>
      <button @click="openAdd" class="btn-primary">
        <Icon name="plus" class="w-3.5 h-3.5" /> 新增模型
      </button>
      <button @click="showCustom = true" class="btn-soft">
        <Icon name="plus" class="w-3.5 h-3.5" /> 自定义模型
      </button>
    </div>

    <!-- Table -->
    <div class="card overflow-hidden">
      <div v-if="loading && !models.length" class="text-center text-sm text-white/40 py-20">加载中…</div>
      <div v-else-if="!filtered.length" class="flex flex-col items-center gap-3 text-white/40 py-20">
        <span class="w-14 h-14 rounded-2xl bg-white/[0.04] grid place-items-center"><Icon name="models" class="w-6 h-6" /></span>
        <span class="text-sm">{{ models.length ? '没有匹配的模型' : '还没有模型,点右上角「新增模型」' }}</span>
      </div>

      <table v-else class="w-full text-sm table-fixed">
        <colgroup>
          <col />                  <!-- model id + provider -->
          <col class="w-20" />     <!-- type -->
          <col />                  <!-- pricing -->
          <col />                  <!-- capability -->
          <col class="w-20" />     <!-- weight -->
          <col class="w-24" />     <!-- generation count -->
          <col class="w-20" />     <!-- status switch -->
          <col class="w-36" />     <!-- actions -->
        </colgroup>
        <thead>
          <tr class="text-[10px] uppercase tracking-[0.2em] text-white/40 border-b border-white/[0.06]">
            <th class="text-left px-5 py-3 font-medium">模型</th>
            <th class="text-left px-3 py-3 font-medium">类型</th>
            <th class="text-left px-3 py-3 font-medium">定价</th>
            <th class="text-left px-3 py-3 font-medium">能力</th>
            <th class="text-right px-3 py-3 font-medium">权重</th>
            <th class="text-right px-3 py-3 font-medium">生图次数</th>
            <th class="text-left px-3 py-3 font-medium">状态</th>
            <th class="text-right px-5 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in filtered" :key="m.id"
              class="border-b border-white/[0.04] hover:bg-white/[0.03] transition-colors">
            <!-- Model id + provider underneath -->
            <td class="px-5 py-3.5 align-middle min-w-0">
              <div class="flex items-start gap-2 min-w-0">
                <div class="font-mono text-xs text-white/90 truncate" :title="m.id">{{ m.id }}</div>
                <span v-if="m.alias" class="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] bg-sky-500/10 text-sky-300 ring-1 ring-sky-400/20 shrink-0">{{ m.alias }}</span>
              </div>
              <div class="mt-1 text-[10px] text-white/45 capitalize truncate">{{ m.provider || '—' }}</div>
            </td>

            <!-- Type chip with the same shape as Logs/Provider 健康 -->
            <td class="px-3 py-3.5 align-middle">
              <span class="inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1"
                    :class="m.type === 'video'
                      ? 'bg-fuchsia-500/10 text-fuchsia-300 ring-fuchsia-400/30'
                      : 'bg-indigo-500/10 text-indigo-300 ring-indigo-400/30'">
                {{ TYPE_LABEL[m.type] || m.type }}
              </span>
            </td>

            <!-- Pricing block — image shows 1K/2K/4K + price, video shows
                 duration + price; each as a small price-chip. -->
            <td class="px-3 py-3.5 align-middle">
              <!-- Each tier shows a pair: 普通价 (emerald) + 代理价 (amber).
                   代理价未设置时回退普通价数值。 -->
              <div v-if="m.type === 'image'" class="flex flex-wrap gap-1">
                <span v-for="r in (m.resolutions || [])" :key="r" class="price-chip">
                  <span class="text-white/85">{{ r }}</span>
                  <span class="text-white/30 mx-1">普通</span>
                  <span class="text-emerald-300 tabular-nums">{{ points(m.prices?.[r]) }}</span>
                  <span class="text-amber-300/40 ml-1.5">代理</span>
                  <span class="text-amber-300 tabular-nums">{{ points(m.prices_agent?.[r] ?? m.prices?.[r]) }}</span>
                </span>
                <span v-if="!(m.resolutions || []).length" class="text-white/30 text-xs">—</span>
              </div>
              <div v-else class="flex flex-wrap gap-1">
                <span v-for="r in (m.resolutions || [])" :key="'r'+r" class="price-chip">
                  <span class="text-white/85">{{ r }}</span>
                  <span class="text-white/30 mx-1">普通</span>
                  <span class="text-emerald-300 tabular-nums">{{ points(m.prices?.[r]) }}</span>
                  <span class="text-amber-300/40 ml-1.5">代理</span>
                  <span class="text-amber-300 tabular-nums">{{ points(m.prices_agent?.[r] ?? m.prices?.[r]) }}</span>
                </span>
                <span v-if="m.duration_prices?.per_second != null" class="price-chip">
                  <span class="text-white/85">/s</span>
                  <span class="text-white/30 mx-1">普通</span>
                  <span class="text-sky-300 tabular-nums">{{ points(m.duration_prices.per_second) }}</span>
                  <span class="text-amber-300/40 ml-1.5">代理</span>
                  <span class="text-amber-300 tabular-nums">{{ points(m.duration_prices_agent?.per_second ?? m.duration_prices.per_second) }}</span>
                </span>
                <span v-else v-for="d in (m.durations || [])" :key="'d'+d" class="price-chip">
                  <span class="text-white/85">{{ d }}</span>
                  <span class="text-white/30 mx-1">普通</span>
                  <span class="text-sky-300 tabular-nums">+{{ points(m.duration_prices?.[d]) }}</span>
                  <span class="text-amber-300/40 ml-1.5">代理</span>
                  <span class="text-amber-300 tabular-nums">+{{ points(m.duration_prices_agent?.[d] ?? m.duration_prices?.[d]) }}</span>
                </span>
                <span v-if="!(m.resolutions || []).length && !(m.durations || []).length && m.duration_prices?.per_second == null" class="text-white/30 text-xs">—</span>
              </div>
            </td>

            <!-- Capability — duration range, frame/ref modes, media support -->
            <td class="px-3 py-3.5 align-middle">
              <div class="flex flex-wrap items-center gap-1 text-[11px]">
                <!-- Duration range (4s–15s) -->
                <span v-if="m.type === 'video' && (m.durations || []).length >= 2" class="cap-chip cap-dur">{{ m.durations[0] }}–{{ m.durations[m.durations.length - 1] }}</span>
                <span v-else-if="m.type === 'video' && (m.durations || []).length === 1" class="cap-chip cap-dur">{{ m.durations[0] }}</span>
                <!-- Resolutions for image -->
                <span v-if="m.type === 'image'" v-for="r in (m.resolutions || [])" :key="'ir'+r" class="cap-chip cap-slate">{{ r }}</span>
                <!-- Resolutions for video -->
                <span v-if="m.type === 'video'" v-for="r in (m.resolutions || [])" :key="'vr'+r" class="cap-chip cap-slate">{{ r }}</span>
                <!-- Frame mode (首尾帧 2) -->
                <span v-if="m.reference_mode === 'frame' && m.max_reference_images > 0" class="cap-chip cap-frame">首尾帧 {{ Math.min(m.max_reference_images, 2) }}</span>
                <!-- Reference image mode (参考图 9) -->
                <span v-if="m.reference_mode === 'frame' && m.max_reference_images > 2" class="cap-chip cap-ref">参考图 {{ m.max_reference_images }}</span>
                <span v-else-if="m.reference_mode && m.reference_mode !== 'none' && m.reference_mode !== 'frame' && m.max_reference_images > 0" class="cap-chip cap-ref">参考图 {{ m.max_reference_images }}</span>
                <span v-else-if="m.type === 'image' && m.image_to_image" class="cap-chip cap-ref">参考图</span>
                <!-- Video/Audio for seedance -->
                <span v-if="m.id?.startsWith('seedance')" class="cap-chip cap-media">视频 3</span>
                <span v-if="m.id?.startsWith('seedance')" class="cap-chip cap-media">音频 3</span>
                <!-- Ratios -->
                <span v-for="r in (m.ratios || [])" :key="'rt'+r" class="cap-chip cap-mono">{{ r.replace(':', '×') }}</span>
                <span v-if="capEmpty(m)" class="text-white/30 text-xs">—</span>
              </div>
            </td>

            <!-- Display weight — higher floats to the top of the dropdown/list -->
            <td class="px-3 py-3.5 align-middle text-right tabular-nums whitespace-nowrap"
                :class="(m.weight || 0) !== 0 ? 'text-white/85' : 'text-white/30'"
                title="展示权重(越大越靠前)">
              {{ m.weight || 0 }}
            </td>

            <!-- Successful generations to date, from event_log via /managed-models -->
            <td class="px-3 py-3.5 align-middle text-right tabular-nums whitespace-nowrap"
                :class="m.generation_count > 0 ? 'text-white/85' : 'text-white/25'">
              {{ (m.generation_count || 0).toLocaleString('en-US') }}
            </td>

            <!-- Status as a real toggle so admins read it as on/off, not a
                 button to delete or whatever. -->
            <td class="px-3 py-3.5 align-middle">
              <button class="sw" :class="m.enabled !== false && 'sw-on'"
                      :aria-pressed="m.enabled !== false" @click="toggleEnabled(m)">
                <span class="sw-thumb"></span>
              </button>
            </td>

            <!-- Actions — small soft buttons, danger variant for delete -->
            <td class="px-3 py-3.5 align-middle text-right whitespace-nowrap">
              <div class="inline-flex items-center gap-1">
                <button @click="testing = m" class="act" title="测试生成">
                  <Icon name="test" class="w-3.5 h-3.5" />
                </button>
                <button @click="openEdit(m)" class="act" title="编辑">
                  <Icon name="config" class="w-3.5 h-3.5" />
                </button>
                <button @click="remove(m)" class="act danger" title="删除">
                  <Icon name="trash" class="w-3.5 h-3.5" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <ModelFormModal v-if="showForm" :model="editing" @close="showForm = false" @saved="onSaved" />
    <CustomModelModal v-if="showCustom" @close="showCustom = false" @saved="() => { showCustom = false; loadModels() }" />
    <TestModal v-if="testing" :model="testing" @close="testing = null" />
  </section>
</template>

<style scoped>
/* --- filter pills (mirrors LogsView so the admin shell stays consistent) */
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
.fp-white {
  background: rgb(255 255 255 / 0.18);
  color: white;
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.35);
}

/* --- price chips: tight pill showing a key (resolution or duration) plus
       its emerald-tinted price. */
.price-chip {
  display: inline-flex;
  align-items: center;
  padding: 0.18rem 0.55rem;
  font-size: 0.7rem;
  border-radius: 9999px;
  background: rgb(255 255 255 / 0.04);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
  white-space: nowrap;
}

/* --- capability chips: smaller, monochrome variants */
.cap-chip {
  display: inline-flex; align-items: center;
  padding: 0.15rem 0.5rem;
  font-size: 0.68rem; font-weight: 500;
  border-radius: 9999px;
  white-space: nowrap;
}
.cap-dur {
  background: rgb(99 102 241 / 0.12);
  color: rgb(165 180 252);
  box-shadow: inset 0 0 0 1px rgb(129 140 248 / 0.3);
}
.cap-frame {
  background: rgb(236 72 153 / 0.12);
  color: rgb(244 114 182);
  box-shadow: inset 0 0 0 1px rgb(244 114 182 / 0.35);
}
.cap-ref {
  background: rgb(16 185 129 / 0.12);
  color: rgb(110 231 183);
  box-shadow: inset 0 0 0 1px rgb(52 211 153 / 0.3);
}
.cap-media {
  background: rgb(245 158 11 / 0.12);
  color: rgb(252 211 77);
  box-shadow: inset 0 0 0 1px rgb(252 211 77 / 0.35);
}
.cap-slate {
  background: rgb(255 255 255 / 0.05);
  color: rgb(255 255 255 / 0.7);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
}
.cap-mono {
  background: transparent;
  color: rgb(255 255 255 / 0.5);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
}

/* --- iOS-style toggle for 启用/停用 */
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
.sw-on { background: rgb(16 185 129 / 0.7); box-shadow: inset 0 0 0 1px rgb(16 185 129 / 0.5); }
.sw-on .sw-thumb { transform: translateX(calc(2.25rem - 1.3rem)); }

/* --- danger variant for 删除 (kept for any other .btn-soft callers) */
.btn-soft.danger {
  color: rgb(253 164 175);
  background: rgb(244 63 94 / 0.12);
  box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3);
}
.btn-soft.danger:hover {
  color: white;
  background: rgb(244 63 94 / 0.25);
}

/* --- compact square icon button for the actions column. Predictable
       width (1.9rem × 3 + gaps) keeps the row inside its 9rem slot, so
       the last button never gets clipped. */
.act {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.9rem;
  height: 1.9rem;
  border-radius: 0.5rem;
  color: rgb(255 255 255 / 0.7);
  background: rgb(255 255 255 / 0.04);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
  transition: background 0.15s, color 0.15s, box-shadow 0.15s;
}
.act:hover { background: rgb(255 255 255 / 0.1); color: white; }
.act.danger {
  color: rgb(253 164 175);
  background: rgb(244 63 94 / 0.12);
  box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3);
}
.act.danger:hover {
  color: white;
  background: rgb(244 63 94 / 0.25);
}
</style>
