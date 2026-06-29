<script setup>
// Admin → 首页内容
// CRUD for the cards rendered on the public home page:
//   - hero  : top-3-by-weight stacked deck in the hero
//   - bento : "从一个起点开始" grid
//   - work  : "我们的作品" marquee — admin-curated featured outputs
// All three kinds use a real image as the background; admins pick one from
// the already-generated files or paste an external URL.
import { ref, reactive, computed, onMounted } from 'vue'
import { api, jsonBody, generatedUrl } from '../api'
import Icon from '../components/Icon.vue'

const items = ref([])
const filter = ref('all')         // 'all' | 'hero' | 'bento' | 'work'
const loading = ref(false)
const editing = ref(null)         // truthy when the form modal is open
const picking = ref(false)        // truthy when the image-picker modal is open
const recentFiles = ref([])       // populated from /stats.recent for the picker
const page = ref(1)
const pageSize = ref(12)
const form = reactive({
  id: '', kind: 'hero', title: '', subtitle: '', prompt: '',
  image: '', weight: 100, span: '',
})
const saving = ref(false)
const error = ref('')

async function refresh() {
  loading.value = true
  const r = await api('/showcase')
  const grouped = r.data?.data || {}
  // Guard every group — a payload missing hero/bento would throw on spread of
  // undefined and freeze the page on "加载中…".
  items.value = [...(grouped.hero || []), ...(grouped.bento || []), ...(grouped.work || [])]
  loading.value = false
}

const filtered = computed(() => {
  if (filter.value === 'all') return items.value
  return items.value.filter((x) => x.kind === filter.value)
})

// Client-side pagination over the filtered set. The showcase store is small
// (admin curates manually) so paging client-side is fine — no extra API calls
// when the admin flips pages.
const totalPages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize.value)))
const pagedItems = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return filtered.value.slice(start, start + pageSize.value)
})
function setFilter(v) { filter.value = v; page.value = 1 }
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

const KIND_DEFAULT_WEIGHT = { hero: 200, bento: 300, work: 100 }

function openNew(kind) {
  editing.value = { kind }
  Object.assign(form, {
    id: '', kind, title: '', subtitle: '', prompt: '',
    image: '', weight: KIND_DEFAULT_WEIGHT[kind], span: '',
  })
  error.value = ''
}

function openEdit(rec) {
  editing.value = rec
  Object.assign(form, {
    id: rec.id, kind: rec.kind, title: rec.title || '', subtitle: rec.subtitle || '',
    prompt: rec.prompt || '', image: rec.image || '',
    weight: rec.weight ?? 100, span: rec.span || '',
  })
  error.value = ''
}

function closeForm() { editing.value = null }

async function save() {
  if (!form.image.trim()) { error.value = '请选择底图'; return }
  if (form.kind !== 'work') {
    if (!form.title.trim()) { error.value = '请输入标题'; return }
    if (!form.prompt.trim()) { error.value = '请输入提示词'; return }
  }
  saving.value = true; error.value = ''
  const payload = {
    kind: form.kind,
    title: form.title.trim(),
    subtitle: form.subtitle.trim(),
    prompt: form.prompt.trim(),
    image: form.image.trim(),
    weight: Number(form.weight) || 0,
    span: form.span.trim(),
  }
  const r = form.id
    ? await api(`/showcase/${form.id}`, jsonBody('PATCH', payload))
    : await api('/showcase', jsonBody('POST', payload))
  saving.value = false
  if (r.ok) { closeForm(); refresh() }
  else error.value = r.data?.detail || `保存失败 (${r.status})`
}

async function remove(rec) {
  if (!confirm(`删除「${rec.title || rec.image}」?`)) return
  const r = await api(`/showcase/${rec.id}`, { method: 'DELETE' })
  if (r.ok) refresh()
}

// Image picker — show the admin's OWN recently generated images (scoped to their
// owner directory, not everyone's). The admin clicks one to fill `form.image`,
// or pastes a URL into the text field.
async function openPicker() {
  picking.value = true
  if (!recentFiles.value.length) {
    const r = await api('/my-images')
    const files = r.data?.data || []
    recentFiles.value = files.filter((f) =>
      /\.(png|jpe?g|webp|gif)$/i.test(f.name)
    )
  }
}
function closePicker() { picking.value = false }
function pickImage(file) {
  form.image = file.name
  picking.value = false
}

function bgFor(image) {
  if (!image) return {}
  const src = /^https?:\/\//i.test(image) ? image : generatedUrl(image)
  return {
    backgroundImage: `url("${src}")`,
    backgroundSize: 'cover',
    backgroundPosition: 'center',
  }
}

const SPAN_PRESETS = ['', 'md:col-span-2', 'md:row-span-2', 'md:col-span-2 md:row-span-2']

onMounted(refresh)
</script>

<template>
  <section class="space-y-4">
    <!-- header / filter / new -->
    <div class="card p-4 flex items-center justify-between gap-3 flex-wrap">
      <div class="flex items-center gap-1.5">
        <button @click="setFilter('all')" class="filter-pill" :class="filter === 'all' && 'on'">全部</button>
        <button @click="setFilter('hero')" class="filter-pill" :class="filter === 'hero' && 'on'">Hero 卡片</button>
        <button @click="setFilter('bento')" class="filter-pill" :class="filter === 'bento' && 'on'">Bento 灵感</button>
        <button @click="setFilter('work')" class="filter-pill" :class="filter === 'work' && 'on'">我们的作品</button>
      </div>
      <div class="flex items-center gap-2">
        <button @click="openNew('hero')" class="btn-soft">+ Hero</button>
        <button @click="openNew('bento')" class="btn-soft">+ Bento</button>
        <button @click="openNew('work')" class="btn-soft">+ Work</button>
      </div>
    </div>

    <!-- grid -->
    <div v-if="loading" class="text-center text-xs text-[color:var(--fg-faint)] py-12">加载中…</div>
    <div v-else-if="!filtered.length" class="text-center text-xs text-[color:var(--fg-faint)] py-12">没有条目</div>
    <div v-else class="grid sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-3">
      <div v-for="rec in pagedItems" :key="rec.id"
           class="media-card relative rounded-2xl overflow-hidden ring-1 ring-white/10 aspect-[4/3] group bg-white/[0.04]"
           :style="bgFor(rec.image)">
        <!-- Fallback gradient for legacy entries that haven't been migrated yet. -->
        <div v-if="!rec.image" class="absolute inset-0" :style="{ background: rec.gradient }"></div>
        <div class="absolute inset-0 bg-gradient-to-t from-black/85 via-black/30 to-transparent"></div>
        <div class="absolute top-3 left-3 flex items-center gap-1.5">
          <span class="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded ring-1"
                :class="rec.kind === 'hero' ? 'bg-fuchsia-500/20 text-fuchsia-200 ring-fuchsia-400/30'
                       : rec.kind === 'bento' ? 'bg-violet-500/20 text-violet-200 ring-violet-400/30'
                       : 'bg-sky-500/20 text-sky-200 ring-sky-400/30'">
            {{ rec.kind }}
          </span>
          <span class="text-[10px] text-white/65 tabular-nums px-1.5 py-0.5 rounded bg-black/40 ring-1 ring-white/10">w={{ rec.weight }}</span>
        </div>
        <div class="absolute top-3 right-3 flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
          <button @click="openEdit(rec)" class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center" title="编辑">
            <Icon name="config" class="w-3.5 h-3.5" />
          </button>
          <button @click="remove(rec)" class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-rose-500/80 text-white grid place-items-center" title="删除">
            <Icon name="trash" class="w-3.5 h-3.5" />
          </button>
        </div>
        <div class="absolute inset-x-0 bottom-0 p-4">
          <div v-if="rec.subtitle" class="text-[10px] uppercase tracking-[0.3em] text-white/55">{{ rec.subtitle }}</div>
          <div v-if="rec.title" class="text-base font-bold text-white mt-1">{{ rec.title }}</div>
          <div v-if="rec.prompt" class="text-[11px] text-white/65 mt-1 line-clamp-2">{{ rec.prompt }}</div>
        </div>
      </div>
    </div>

    <!-- pagination — shown when there's more than one page worth of entries -->
    <div v-if="!loading && totalPages > 1"
         class="card !p-3 flex items-center justify-between gap-3">
      <div class="text-xs text-[color:var(--fg-3)] tabular-nums px-2">
        <span class="text-[color:var(--fg)]">{{ (page - 1) * pageSize + 1 }}–{{ Math.min(filtered.length, page * pageSize) }}</span>
        / {{ filtered.length }} 条
      </div>
      <div class="flex items-center gap-1">
        <template v-for="(n, i) in pageNumbers" :key="i">
          <span v-if="n === null" class="px-1 text-[color:var(--fg-faint)]">…</span>
          <button v-else @click="goPage(n)" class="pg" :class="page === n && 'pg-on'">{{ n }}</button>
        </template>
      </div>
    </div>

    <!-- ======= form modal ======= -->
    <transition name="fade">
      <div v-if="editing"
           class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-start justify-center overflow-y-auto p-4"
           @click.self="closeForm">
        <div class="card w-full max-w-2xl !shadow-2xl my-auto">
          <div class="px-5 py-3 border-b border-[color:var(--hairline)] flex items-center justify-between">
            <h2 class="text-sm font-semibold">
              {{ form.id ? '编辑' : '新增' }} ·
              {{ form.kind === 'hero' ? 'Hero 卡片' : form.kind === 'bento' ? 'Bento 灵感' : '作品' }}
            </h2>
            <button @click="closeForm" class="text-[color:var(--fg-faint)] hover:text-[color:var(--fg)]">
              <Icon name="close" class="w-4 h-4" />
            </button>
          </div>

          <div class="p-5 space-y-4">
            <!-- live preview -->
            <div class="relative rounded-2xl overflow-hidden ring-1 ring-white/10 aspect-[5/2] bg-white/[0.04]"
                 :style="bgFor(form.image)">
              <div class="absolute inset-0 bg-gradient-to-t from-black/85 via-black/30 to-transparent"></div>
              <div v-if="!form.image" class="absolute inset-0 grid place-items-center text-xs text-white/40">
                未选择底图
              </div>
              <div class="absolute inset-x-0 bottom-0 p-5">
                <div v-if="form.subtitle" class="text-[10px] uppercase tracking-[0.3em] text-white/55">{{ form.subtitle }}</div>
                <div v-if="form.title" class="text-xl font-bold text-white mt-1">{{ form.title }}</div>
                <div v-if="form.prompt" class="text-xs text-white/65 mt-1 line-clamp-2">{{ form.prompt }}</div>
              </div>
            </div>

            <div class="grid sm:grid-cols-2 gap-3">
              <div>
                <label class="block text-xs text-[color:var(--fg-3)] mb-1.5">类型</label>
                <div class="flex gap-1.5">
                  <button type="button" @click="form.kind = 'hero'" class="kind-btn" :class="form.kind === 'hero' && 'on'">Hero</button>
                  <button type="button" @click="form.kind = 'bento'" class="kind-btn" :class="form.kind === 'bento' && 'on'">Bento</button>
                  <button type="button" @click="form.kind = 'work'" class="kind-btn" :class="form.kind === 'work' && 'on'">Work</button>
                </div>
              </div>
              <div>
                <label class="block text-xs text-[color:var(--fg-3)] mb-1.5">权重 <span class="text-[color:var(--fg-faint)]">(越大越靠前)</span></label>
                <input v-model.number="form.weight" type="number" class="field" />
              </div>
            </div>

            <!-- image picker (the central change — admins pick a real image) -->
            <div>
              <label class="block text-xs text-[color:var(--fg-3)] mb-1.5">底图</label>
              <div class="flex gap-2">
                <input v-model="form.image" class="field font-mono text-[11px]"
                       placeholder="user/abc.png 或 https://…" />
                <button type="button" @click="openPicker" class="btn-soft shrink-0">选择已生成</button>
              </div>
              <p class="text-[11px] text-[color:var(--fg-faint)] mt-1">填写 /generated 下的相对路径,或粘贴一个外链 URL。</p>
            </div>

            <template v-if="form.kind !== 'work'">
              <div class="grid sm:grid-cols-2 gap-3">
                <div>
                  <label class="block text-xs text-[color:var(--fg-3)] mb-1.5">标题</label>
                  <input v-model="form.title" class="field" placeholder="电影感人物" />
                </div>
                <div>
                  <label class="block text-xs text-[color:var(--fg-3)] mb-1.5">副标题</label>
                  <input v-model="form.subtitle" class="field" placeholder="CINEMATIC PORTRAIT" />
                </div>
              </div>
              <div>
                <label class="block text-xs text-[color:var(--fg-3)] mb-1.5">提示词 <span class="text-[color:var(--fg-faint)]">(点 Bento 后会预填到画图)</span></label>
                <textarea v-model="form.prompt" rows="3" class="field resize-none"
                          placeholder="一位身穿米色风衣的女子站在雨夜的霓虹街道,胶片质感,浅景深,电影感"></textarea>
              </div>
            </template>
            <template v-else>
              <div>
                <label class="block text-xs text-[color:var(--fg-3)] mb-1.5">作品标题 <span class="text-[color:var(--fg-faint)]">(可选)</span></label>
                <input v-model="form.title" class="field" placeholder="留空则只展示图片" />
              </div>
            </template>

            <div v-if="form.kind === 'bento'">
              <label class="block text-xs text-[color:var(--fg-3)] mb-1.5">网格跨度 <span class="text-[color:var(--fg-faint)]">(Tailwind class)</span></label>
              <div class="flex gap-1.5 flex-wrap mb-2">
                <button v-for="s in SPAN_PRESETS" :key="s" type="button" @click="form.span = s"
                        class="px-2.5 py-1 text-[11px] rounded-lg ring-1 ring-[color:var(--hairline)] hover:bg-[color:var(--hover)]"
                        :class="form.span === s ? 'bg-[color:var(--btn-solid-bg)] text-[color:var(--btn-solid-fg)]' : 'bg-[color:var(--surface-2)] text-[color:var(--fg-2)]'">
                  {{ s || '默认 1×1' }}
                </button>
              </div>
              <input v-model="form.span" class="field font-mono text-[11px]" placeholder="md:col-span-2" />
            </div>

            <p v-if="error" class="text-xs text-rose-500">{{ error }}</p>
          </div>

          <div class="px-5 py-3 border-t border-[color:var(--hairline)] flex items-center justify-end gap-2">
            <button @click="closeForm" class="btn-ghost">取消</button>
            <button @click="save" :disabled="saving" class="btn-primary">
              {{ saving ? '保存中…' : '保存' }}
            </button>
          </div>
        </div>
      </div>
    </transition>

    <!-- ======= image picker modal ======= -->
    <transition name="fade">
      <div v-if="picking"
           class="fixed inset-0 z-[60] bg-black/80 backdrop-blur-sm grid place-items-center p-4"
           @click.self="closePicker">
        <div class="card w-full max-w-4xl !shadow-2xl">
          <div class="px-5 py-3 border-b border-[color:var(--hairline)] flex items-center justify-between">
            <h2 class="text-sm font-semibold">选择底图 · 最近生成</h2>
            <button @click="closePicker" class="text-[color:var(--fg-faint)] hover:text-[color:var(--fg)]">
              <Icon name="close" class="w-4 h-4" />
            </button>
          </div>
          <div class="p-4 max-h-[70vh] overflow-y-auto">
            <div v-if="!recentFiles.length" class="text-center text-xs text-[color:var(--fg-faint)] py-10">尚未有生成过的图片</div>
            <div v-else class="grid grid-cols-3 sm:grid-cols-4 md:grid-cols-6 gap-2">
              <button v-for="f in recentFiles" :key="f.name" type="button" @click="pickImage(f)"
                      class="relative aspect-square rounded-lg overflow-hidden ring-1 ring-white/10 hover:ring-fuchsia-400/60 transition-all">
                <img :src="generatedUrl(f.name)" loading="lazy" class="w-full h-full object-cover" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </transition>
  </section>
</template>

<style scoped>
/* All colors come from the theme vars (:root light / html.dark dark) so the view
   adapts to BOTH themes. Selected states use --btn-solid-* which inverts per
   theme (light: dark bg/white text · dark: white bg/dark text). */
.filter-pill {
  padding: 0.375rem 0.75rem;
  font-size: 0.75rem;
  border-radius: 0.5rem;
  background: var(--surface-2);
  color: var(--fg-2);
  transition: background 0.15s, color 0.15s;
}
.filter-pill:hover { background: var(--hover); color: var(--fg); }
.filter-pill.on { background: var(--btn-solid-bg); color: var(--btn-solid-fg); }

.kind-btn {
  flex: 1;
  padding: 0.5rem 0;
  border-radius: 0.5rem;
  font-size: 0.75rem;
  background: var(--surface-2);
  color: var(--fg-2);
  transition: background 0.15s, color 0.15s;
}
.kind-btn:hover { background: var(--hover); }
.kind-btn.on { background: var(--btn-solid-bg); color: var(--btn-solid-fg); }

/* No scoped .field — use the GLOBAL .field (bg-white + border-slate-200 in light,
   .public-dark .field in dark) so inputs match every other modal and the border
   is clearly visible. A scoped override here only re-broke the border. */
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

/* numbered pagination — matches LogsView / ImagesView */
.pg {
  min-width: 1.75rem;
  padding: 0.3rem 0.55rem;
  font-size: 0.72rem;
  font-weight: 500;
  text-align: center;
  border-radius: 0.45rem;
  color: var(--fg-2);
  background: var(--surface-2);
  box-shadow: inset 0 0 0 1px var(--hairline);
  transition: background 0.15s, color 0.15s;
}
.pg:hover:not(.pg-on) { background: var(--hover); color: var(--fg); }
.pg-on {
  background: var(--btn-solid-bg);
  color: var(--btn-solid-fg);
  box-shadow: none;
}
</style>
