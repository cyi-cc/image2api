<script setup>
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { api, jsonBody, generatedUrl } from '../api'
import { auth, refreshMe } from '../auth'
import { draft } from '../playground'
import Icon from '../components/Icon.vue'
import SelectMenu from '../components/SelectMenu.vue'
import MediaLightbox from '../components/MediaLightbox.vue'
import { pointsLabel } from '../credits'
import { sortResolutions } from '../utils/format'
import { copyText } from '../utils/clipboard'

const route = useRoute()

// ---- credits: the logged-in user's REAL server-side balance ----
const credits = computed(() => Number(auth.user?.credits || 0))

const allModels = ref([])      // managed-models list
const presets = ref([])        // video family presets

// Seed every form field from the shared draft (module-level) so navigating
// away from the page and coming back keeps the prompt + selected model +
// params. Each local ref then syncs back into the draft on change.
const mode = ref(draft.mode || 'image')
const modelId = ref(draft.modelId || '')
const prompt = ref(draft.prompt || '')
const ratio = ref(draft.ratio || '')
const resolution = ref(draft.resolution || '')
const duration = ref(draft.duration || '')

watch(mode,       (v) => { draft.mode = v })
watch(modelId,    (v) => { draft.modelId = v })
watch(prompt,     (v) => { draft.prompt = v })
watch(ratio,      (v) => { draft.ratio = v })
watch(resolution, (v) => { draft.resolution = v })
watch(duration,   (v) => { draft.duration = v })

const refImages = ref([])      // [{ name, dataUrl }]
const fileInput = ref(null)

// Concurrent generation: each 生成 click fires an INDEPENDENT /generate and adds
// a card — the UI never locks, so several can run at once. `tasks` holds the
// in-session cards (newest first); `history` fills the grid up to 10 with the
// user's recent finished results from the server.
const tasks = ref([])
const history = ref([])
// Gateway-timeout statuses our BACKEND never emits — a CDN/proxy (e.g. EdgeOne
// 524) gave up waiting while the synchronous /generate is STILL rendering. The
// task stays "running" and loadHistory() claims it once the result lands.
const GATEWAY_TIMEOUT = new Set([0, 408, 504, 520, 521, 522, 523, 524, 525])
const error = ref('')
const lightbox = ref(null)
// Videos whose first-frame thumbnail is missing (old videos) — fall back to
// the muted <video> preview for those cards.
const thumbFail = reactive({})
const toast = ref('')
let pollTimer = null

const fileKey = (u) => (u || '').split('?')[0].split('/').pop()
const taskKey = (x) => [x.model, x.kind, (x.prompt || '').trim()].join('|')
// Up to 10 cards (一行五个 × 2): in-session tasks first, then the server's recent
// rows (进行中 + 成功) so the grid stays filled; loadHistory() prunes an optimistic
// task once the server tracks it → never a duplicate. 新的顶掉老的.
const displayItems = computed(() => [...tasks.value, ...history.value].slice(0, 10))

// ---- derived ----
const models = computed(() =>
  allModels.value.filter((m) => m.enabled !== false && m.type === mode.value),
)
const modelOptions = computed(() =>
  models.value.map((m) => ({ value: m.id, label: m.alias || m.name || m.id })),
)
const model = computed(() => allModels.value.find((m) => m.id === modelId.value) || null)
const familyPreset = computed(() => {
  if (mode.value !== 'video' || !model.value) return null
  return presets.value.find((p) => p.key === model.value.id) || null
})

const ratios = computed(() => {
  const fromModel = model.value?.ratios || []
  if (fromModel.length) return fromModel
  return mode.value === 'video' ? ['16x9'] : ['1:1']
})
// Firefly Image 5 instruct-edit derives the aspect ratio from the reference
// image — hide the ratio picker (backend also omits aspectRatio) when a ref is
// attached, otherwise the request is rejected with a validation error.
const showRatio = computed(() => !(modelId.value === 'firefly-image-5' && refImages.value.length > 0))
const resolutions = computed(() => {
  const fromModel = model.value?.resolutions || []
  if (fromModel.length) return sortResolutions(fromModel)
  // Legacy record with no declared tiers: fall back to the priced tiers so we
  // never offer (or default to) a resolution the server has no price for.
  const priced = Object.keys(model.value?.prices || {})
  if (priced.length) return sortResolutions(priced)
  return mode.value === 'video' ? ['720p'] : ['1K']
})
const durations = computed(() => {
  // duration_prices arrives as a JSON object whose keys Go sorts alphabetically
  // ("10s" before "5s"). Re-sort by the numeric seconds so the shortest is first.
  const keys = Object.keys(model.value?.duration_prices || {})
    .sort((a, b) => parseFloat(a) - parseFloat(b))
  if (keys.length) return keys
  return familyPreset.value?.durations || ['5s']
})

const maxRefs = computed(() => {
  if (mode.value === 'video') {
    const a = Number(familyPreset.value?.max_reference_images || 0)
    const b = Number(model.value?.max_reference_images || 0)
    return Math.max(a, b)
  }
  // Image-to-image: honor the model's configured max (gpt-image-2=3,
  // seedream-4.5=6, flux-klein-2=4 …). Fall back to 1 when image_to_image is on
  // but no count was set.
  const m = Number(model.value?.max_reference_images || 0)
  if (m > 0) return m
  return model.value?.image_to_image ? 1 : 0
})
const refMode = computed(() => familyPreset.value?.reference_mode || model.value?.reference_mode || 'none')
// Most video models (veo31, luma) support pure text2video, so refs are optional.
// A model can opt into strict image-to-video by declaring `requires_reference`
// in its preset (e.g. runway-gen4-turbo) — then a first-frame image is mandatory.
const refsRequired = computed(() =>
  mode.value === 'video' && !!familyPreset.value?.requires_reference)

// ---- price (per generation, derived from selected model + params) ----
// 代理用户走代理价:某档设了代理价就用它,否则回退普通价(支持的档位始终由普通价决定)。
const isAgent = computed(() => auth.user?.role === 'agent')
function tierPrice(normalMap, agentMap, key) {
  const n = (normalMap || {})[key]
  if (n == null) return null            // 不支持该档(由普通价决定)
  if (isAgent.value) {
    const a = (agentMap || {})[key]
    if (a != null) return Number(a)
  }
  return Number(n)
}
const price = computed(() => {
  if (!model.value) return null
  const m = model.value
  if (mode.value === 'video') {
    const rp = tierPrice(m.prices, m.prices_agent, resolution.value)
    const dp = tierPrice(m.duration_prices, m.duration_prices_agent, duration.value)
    if (rp == null || dp == null) return null
    return rp + dp
  }
  return tierPrice(m.prices, m.prices_agent, resolution.value)
})
const priceLabel = computed(() => price.value == null ? '—' : pointsLabel(price.value))
const canAfford = computed(() => price.value == null || credits.value >= price.value)

// ---- helpers ----
function firstOf(arr) {
  return (arr && arr.length) ? arr[0] : ''
}
// Selecting a model (or switching image/video) resets each picker to that
// model's FIRST tier — the default should always be the first option, not
// whatever was carried over from the previously-selected model.
function applyModelDefaults() {
  ratio.value = firstOf(ratios.value)
  resolution.value = firstOf(resolutions.value)
  duration.value = firstOf(durations.value)
  // 切换模型保留已上传的参考图,只按新模型的上限裁剪(上限为 0 则清空)。
  if (refImages.value.length > maxRefs.value) {
    refImages.value = refImages.value.slice(0, maxRefs.value)
  }
}
function selectModel(id) {
  modelId.value = id
  applyModelDefaults()
}

function setMode(m) {
  if (mode.value === m) return
  mode.value = m
  // pick a default model of the new kind, if any
  const first = allModels.value.find((x) => x.enabled !== false && x.type === m)
  modelId.value = first?.id || ''
  applyModelDefaults()
}

function openPicker() { fileInput.value && fileInput.value.click() }
// Backend rejects reference images over 8MB (maxReferenceImageBytes). Enforce it
// here at pick time so an oversized image fails fast with a clear message instead
// of charging + failing upstream after the upload.
const MAX_REF_BYTES = 8 * 1024 * 1024
function onFiles(ev) {
  addFiles(Array.from(ev.target.files || []))
  if (ev.target) ev.target.value = ''
}
// Shared by the file picker AND drag-and-drop. Filters to images, honors the
// per-model max + 8MB cap, reads each to a data URL.
function addFiles(files) {
  files = files.filter((f) => f && f.type && f.type.startsWith('image/'))
  const room = Math.max(0, maxRefs.value - refImages.value.length)
  const tooBig = []
  let added = 0
  for (const f of files) {
    if (added >= room) break
    if (f.size > MAX_REF_BYTES) { tooBig.push(f.name); continue }
    const reader = new FileReader()
    reader.onload = () => refImages.value.push({ name: f.name, dataUrl: reader.result })
    reader.readAsDataURL(f)
    added++
  }
  error.value = tooBig.length
    ? `图片超过 8MB 已跳过：${tooBig.join('、')}（请压缩后再传）`
    : ''
}
// Drag-and-drop onto the reference area.
const dragOver = ref(false)
function onDrop(ev) {
  ev.preventDefault()
  dragOver.value = false
  if (maxRefs.value <= 0) return
  addFiles(Array.from(ev.dataTransfer?.files || []))
}
function onDragOver(ev) {
  ev.preventDefault()
  if (maxRefs.value > 0) dragOver.value = true
}
function onDragLeave(ev) {
  // ignore leave events bubbling from children
  if (ev.currentTarget.contains(ev.relatedTarget)) return
  dragOver.value = false
}
function removeRef(i) { refImages.value.splice(i, 1) }

// Re-hydrate reference thumbnails from server URLs (after a reload). Fetches
// each /images URL (same-origin, cookie-authed) and converts to a data URL so
// the thumbnail renders AND the ref can be re-submitted unchanged. Shared by
// image and video — both persist their refs the same way server-side.
// Re-display refs by URL only — the thumbnail renders straight from /images/<ref>.
// We DON'T fetch+convert here; conversion to base64 happens lazily at submit time
// (refToBase64), so a ref that's only viewed never needs a network round-trip.
function restoreRefs(urls) {
  if (!Array.isArray(urls) || !urls.length) return
  if (refImages.value.length) return   // don't clobber refs the user already added
  refImages.value = urls.map((u) => ({ name: 'ref', url: u }))
}

// refToBase64 yields the raw base64 the backend expects, from either a freshly
// uploaded ref (dataUrl) or a restored one (url → fetch). Returns '' on failure.
async function refToBase64(r) {
  try {
    if (r.dataUrl) return r.dataUrl.replace(/^data:[^,]*,/, '')
    if (r.url) {
      const blob = await (await fetch(r.url)).blob()
      const dataUrl = await new Promise((res, rej) => {
        const fr = new FileReader()
        fr.onload = () => res(fr.result)
        fr.onerror = rej
        fr.readAsDataURL(blob)
      })
      return dataUrl.replace(/^data:[^,]*,/, '')
    }
  } catch { /* fall through */ }
  return ''
}

let toastTimer = null
function flash(msg) {
  toast.value = msg
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toast.value = ''), 1800)
}

async function copyPrompt(item) {
  const text = (item && item.prompt) || ''
  if (!text.trim()) return
  flash(await copyText(text) ? '指令已复制' : '复制失败')
}

async function copyImage(url) {
  try {
    const blob = await (await fetch(url)).blob()
    const pngBlob = blob.type === 'image/png'
      ? blob
      : await new Promise((resolve, reject) => {
          createImageBitmap(blob).then((bitmap) => {
            const canvas = document.createElement('canvas')
            canvas.width = bitmap.width
            canvas.height = bitmap.height
            const ctx = canvas.getContext('2d')
            if (!ctx) { reject(new Error('no canvas ctx')); return }
            ctx.drawImage(bitmap, 0, 0)
            canvas.toBlob((out) => {
              if (out) resolve(out)
              else reject(new Error('png convert failed'))
            }, 'image/png')
          }).catch(reject)
        })
    await navigator.clipboard.write([new ClipboardItem({ 'image/png': pngBlob })])
    flash('图片已复制')
  } catch {
    flash('复制失败')
  }
}

// ---- generate (concurrent — no lock) ----
// 生图 can request 1–4 images at once: each is an independent task/charge.
const count = ref(1)
const batchCount = computed(() => (mode.value === 'image' ? Math.max(1, Math.min(4, count.value)) : 1))

async function run() {
  if (!modelId.value) { error.value = '请选择模型'; return }
  if (!prompt.value.trim()) { error.value = '请输入提示词'; return }
  if (refsRequired.value && refImages.value.length < 1) {
    error.value = '该视频模型需要至少 1 张参考图 (首帧)'
    return
  }
  if (price.value == null) {
    error.value = '该参数组合未定价 (留空 = 不支持)'
    return
  }
  const n = batchCount.value
  if (price.value != null && credits.value < price.value * n) {
    error.value = `积分不足 — 需要 ${pointsLabel(price.value * n)},余额 ${pointsLabel(credits.value)}`
    return
  }
  error.value = ''
  // A new generation clears any lingering real-time error cards.
  tasks.value = tasks.value.filter((t) => t.status !== 'failed')
  // Fire N independent tasks (no await between them → all run concurrently).
  for (let i = 0; i < n; i++) fireOne()
}

async function fireOne() {
  // Snapshot the form NOW — concurrent tasks keep their own params even if the
  // user edits the form (or fires another batch) while this one runs.
  const task = {
    id: Math.random().toString(36).slice(2, 10),
    model: model.value?.alias || modelId.value,
    kind: mode.value,
    prompt: prompt.value,
    ratio: ratio.value,
    resolution: resolution.value,
    duration: mode.value === 'video' ? duration.value : '',
    status: 'pending',
    url: '',
    error: '',
    elapsed_ms: 0,
    charged: price.value,
    ts: Date.now(),
  }
  const refsSnapshot = refImages.value.slice()
  const chargedPrice = price.value
  tasks.value.unshift(task)
  if (tasks.value.length > 10) tasks.value = tasks.value.slice(0, 10)

  // Optimistically deduct the price (server debits before generating; a failure
  // refunds + refreshMe reconciles).
  if (auth.user && chargedPrice != null) {
    auth.user.credits = Math.max(0, Number(auth.user.credits || 0) - chargedPrice)
  }

  const payload = {
    model: task.model, prompt: task.prompt, ratio: task.ratio, resolution: task.resolution,
  }
  if (task.kind === 'video') payload.duration = task.duration
  if (refsSnapshot.length) {
    const refs = await Promise.all(refsSnapshot.map(refToBase64))
    payload.reference_images = refs.filter(Boolean)
  }

  try {
    const r = await api('/generate', jsonBody('POST', payload))
    if (r.ok && r.data?.url) {
      task.status = 'done'
      task.url = r.data.url
      task.elapsed_ms = r.data.elapsed_ms
      task.charged = r.data.charged ?? chargedPrice
      if (auth.user && r.data.credits != null) auth.user.credits = r.data.credits
    } else if (GATEWAY_TIMEOUT.has(r.status)) {
      // CDN/代理回源超时(如 EdgeOne 524)—— 后端仍在生成。保持 running,
      // loadHistory() 在结果落库后认领它。
      task.status = 'running'
    } else {
      await refreshMe()
      task.status = 'failed'
      task.error = r.data?.detail || `失败 (${r.status})`
    }
  } catch (e) {
    await refreshMe()
    task.status = 'failed'
    task.error = String(e)
  }
  loadHistory()
}

// Fill the grid up to 10 with the user's recent rows (进行中 + 成功). Past
// FAILURES are never shown from history — an error is only relevant for the live
// generation the user just ran. Prune optimistic tasks the server now tracks.
let prevPending = 0
async function loadHistory() {
  // Server-side filter: status IN (pending, success), newest 12 — exactly the
  // rows the grid shows, in one query (no client over-fetch).
  const r = await api('/logs?limit=10&statuses=pending,success&source=user&exclude_showcase=1&media=1')
  if (!r.ok) return
  history.value = (r.data?.data || [])
    .filter((e) => e.status === 'pending' || e.file)
    .map((e) => ({
      id: 'srv-' + e.id,
      prompt: e.prompt, model: e.model, kind: e.kind,
      ratio: e.ratio, resolution: e.resolution, duration: e.duration,
      status: e.status === 'success' ? 'done' : 'running',
      url: e.file ? generatedUrl(e.file) : '',
      error: '',
      elapsed_ms: e.elapsed_ms,
    }))
  // Hand each optimistic task over to the server once it's tracked there: drop a
  // pending task when a matching server pending row exists, a done task once its
  // file is in the server's rows. A FAILED task is a live error — keep it.
  const serverPending = new Set(history.value.filter((h) => h.status === 'running').map(taskKey))
  const serverFiles = new Set(history.value.filter((h) => h.url).map((h) => fileKey(h.url)))
  tasks.value = tasks.value.filter((t) => {
    if (t.status === 'failed') return true
    if (t.status === 'done') return !serverFiles.has(fileKey(t.url))
    return !serverPending.has(taskKey(t))
  })
  if (serverPending.size < prevPending) refreshMe()
  prevPending = serverPending.size
}

// Delete one of my works: remove the stored file (+thumb) server-side, then
// drop the card locally so it disappears before the next history poll.
async function deleteItem(item) {
  if (!item || !item.url) return
  if (!confirm('确定删除这个作品？删除后不可恢复')) return
  const rel = (item.url || '').split('?')[0].split('/images/').pop()
  const r = await api('/my-files?file=' + encodeURIComponent(rel), { method: 'DELETE' })
  if (r.ok) {
    tasks.value = tasks.value.filter((t) => t.id !== item.id)
    history.value = history.value.filter((h) => h.id !== item.id)
    flash('已删除')
  } else {
    flash(r.data?.detail || '删除失败')
  }
}

// Click a generated IMAGE → use it as a reference. Single-ref model: replace the
// existing ref. Multi-ref: append if there's room, else replace the last one.
function useAsRef(item) {
  if (!item || !item.url || item.status !== 'done') return
  if (item.kind === 'video') return
  const cap = maxRefs.value
  if (cap <= 0) { flash('当前模型不支持参考图'); return }
  const ref = { name: 'ref', url: item.url }
  if (cap === 1) {
    refImages.value = [ref]
  } else if (refImages.value.length >= cap) {
    refImages.value.splice(cap - 1, 1, ref)
  } else {
    refImages.value.push(ref)
  }
  flash('已加入参考图')
}

// Grab the LAST frame of a video as a PNG data URL (same-origin → canvas isn't
// tainted). Used to continue a video from where it ended (首尾帧 models).
function lastFrameDataUrl(url) {
  return new Promise((resolve) => {
    const v = document.createElement('video')
    v.crossOrigin = 'anonymous'
    v.muted = true
    v.preload = 'auto'
    v.src = url
    const grab = () => {
      try {
        const c = document.createElement('canvas')
        c.width = v.videoWidth; c.height = v.videoHeight
        c.getContext('2d').drawImage(v, 0, 0)
        resolve(c.toDataURL('image/png'))
      } catch { resolve('') }
    }
    v.addEventListener('loadeddata', () => {
      const t = Math.max(0, (v.duration || 0) - 0.05)
      if (isFinite(t) && t > 0) v.currentTime = t
      else grab()
    })
    v.addEventListener('seeked', grab)
    v.addEventListener('error', () => resolve(''))
  })
}

// Use a generated VIDEO's LAST frame as the 首帧 (first reference) — 首尾帧
// (frame) models only. Triggered by the small button; clicking the video zooms.
async function useVideoFrame(item) {
  if (!item || !item.url) return
  // Prefer the server-stored FULL-RES last-frame still; old videos without one
  // fall back to grabbing the frame from the video via canvas.
  const dataUrl = (await storedLastFrameDataUrl(item.url)) || (await lastFrameDataUrl(item.url))
  if (!dataUrl) { flash('截取末帧失败'); return }
  const ref = { name: 'frame', dataUrl }
  if (refImages.value.length === 0) refImages.value = [ref]
  else refImages.value.splice(0, 1, ref)   // replace the 首帧 slot
  flash('已把视频末帧设为首帧')
}

// Fetch the server-stored last-frame still (url + '.last.jpg'). The server
// falls back to the ORIGINAL VIDEO when the still doesn't exist (old videos),
// so verify the response really is an image before using it.
async function storedLastFrameDataUrl(url) {
  try {
    const r = await fetch(url + '.last.jpg')
    if (!r.ok || !(r.headers.get('content-type') || '').startsWith('image/')) return ''
    const blob = await r.blob()
    return await new Promise((res) => {
      const fr = new FileReader()
      fr.onload = () => res(fr.result)
      fr.onerror = () => res('')
      fr.readAsDataURL(blob)
    })
  } catch { return '' }
}

function onKey(e) { if (e.key === 'Escape') lightbox.value = null }

onMounted(async () => {
  refreshMe()   // pull the latest real balance
  const [mm, pp] = await Promise.all([api('/managed-models'), api('/video-presets')])
  allModels.value = mm.data?.data || []
  presets.value = pp.data?.data || []
  // Pre-fill from query string (?prompt=...&model=...) — used by the home
  // page's example cards to seed the form in one click.
  const qPrompt = String(route.query.prompt || '')
  const qModel = String(route.query.model || '')
  if (qPrompt) prompt.value = qPrompt
  let selected = null
  if (qModel) {
    selected = allModels.value.find((m) => m.id === qModel && m.enabled !== false)
  }
  // If the draft already points at a still-available model AND no fresher
  // intent came in from the URL, keep the draft as-is. Otherwise fall back to
  // the first usable model.
  const draftModel = !qModel && modelId.value
    ? allModels.value.find((m) => m.id === modelId.value && m.enabled !== false)
    : null
  if (!selected && !draftModel) {
    selected = allModels.value.find((m) => m.enabled !== false && m.type === 'image')
      || allModels.value.find((m) => m.enabled !== false)
  }
  // Always re-apply defaults — even when restoring the persisted draft model —
  // so a stale ratio/resolution that the model no longer supports (e.g. a saved
  // "2K" for a model that's now 1K-only) is normalized to a valid, priced tier
  // instead of being sent as-is and rejected with "unsupported or unpriced".
  const chosen = selected || draftModel
  if (chosen) {
    mode.value = chosen.type
    modelId.value = chosen.id
    applyModelDefaults()
  }
  window.addEventListener('keydown', onKey)
  // Fill the grid with the user's recent results, then refresh every 3s so
  // finished tasks (incl. gateway-timed-out ones) land without a reload.
  loadHistory()
  pollTimer = setInterval(loadHistory, 3000)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
  clearInterval(pollTimer)
})
</script>

<template>
  <section class="theme-text grid lg:grid-cols-[420px_1fr] gap-6">
    <!-- LEFT: controls — never locked. 生成 fires an independent task each click,
         so several generations can run at once (concurrent). -->
    <div class="card p-5 space-y-5 lg:sticky lg:top-24 self-start">
      <!-- mode switch -->
      <div class="grid grid-cols-2 gap-2 p-1 bg-slate-100 rounded-xl">
        <button @click="setMode('image')" type="button"
                class="rounded-lg py-2 text-sm font-medium transition-colors disabled:cursor-not-allowed"
                :class="mode === 'image' ? 'bg-white text-slate-900 shadow-sm' : 'text-slate-500 hover:text-slate-700'">
          <Icon name="files" class="w-4 h-4 inline -mt-0.5" /> 生图
        </button>
        <button @click="setMode('video')" type="button"
                class="rounded-lg py-2 text-sm font-medium transition-colors disabled:cursor-not-allowed"
                :class="mode === 'video' ? 'bg-white text-slate-900 shadow-sm' : 'text-slate-500 hover:text-slate-700'">
          <Icon name="video" class="w-4 h-4 inline -mt-0.5" /> 生视频
        </button>
      </div>

      <!-- model -->
      <div>
        <label class="block text-xs font-medium text-slate-500 mb-1.5">模型</label>
        <SelectMenu v-if="models.length" :model-value="modelId" @update:model-value="selectModel"
                    :options="modelOptions" placeholder="选择模型" mono />
        <div v-else class="rounded-lg border border-dashed border-slate-200 px-3 py-4 text-xs text-slate-400 text-center">
          还没有可用的{{ mode === 'video' ? '视频' : '图像' }}模型 ·
          <router-link to="/admin/models" class="text-slate-700 underline">去添加</router-link>
        </div>
      </div>

      <!-- prompt -->
      <div>
        <div class="flex items-center justify-between mb-1.5">
          <label class="block text-xs font-medium text-slate-500">提示词</label>
          <span class="text-[11px] tabular-nums text-slate-400">{{ prompt.length }}</span>
        </div>
        <textarea v-model="prompt" rows="4" class="field resize-none disabled:opacity-60 disabled:cursor-not-allowed"
                  placeholder="描述想要的画面…如：黄昏时分,金色麦田里奔跑的金毛猎犬,电影感"></textarea>
      </div>

      <!-- ratio + res + duration. Single-option controls are hidden — the
           value is still set from the model's defaults and sent to the API,
           so the user doesn't have to acknowledge a choice they don't have. -->
      <div v-if="ratios.length > 0 && showRatio">
        <label class="block text-xs font-medium text-slate-500 mb-1.5">比例</label>
        <div class="flex flex-wrap gap-1.5">
          <button v-for="r in ratios" :key="r" type="button" @click="ratio = r"
                  class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                  :class="ratio === r ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">
            {{ r }}
          </button>
        </div>
      </div>

      <div v-if="resolutions.length > 0">
        <label class="block text-xs font-medium text-slate-500 mb-1.5">{{ mode === 'video' ? '分辨率' : '画质' }}</label>
        <div class="flex flex-wrap gap-1.5">
          <button v-for="r in resolutions" :key="r" type="button" @click="resolution = r"
                  class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                  :class="resolution === r ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">
            {{ r }}
          </button>
        </div>
      </div>

      <div v-if="mode === 'video' && durations.length > 0">
        <label class="block text-xs font-medium text-slate-500 mb-1.5">时长</label>
        <div class="flex flex-wrap gap-1.5">
          <button v-for="d in durations" :key="d" type="button" @click="duration = d"
                  class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                  :class="duration === d ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">
            {{ d }}
          </button>
        </div>
      </div>

      <!-- reference images -->
      <div v-if="maxRefs > 0">
        <label class="block text-xs font-medium text-slate-500 mb-1.5">
          参考图
          <span class="text-slate-400 font-normal">
            (最多 {{ maxRefs }} 张{{ refMode === 'frame' && mode === 'video' ? (maxRefs >= 2 ? ' · 首帧/末帧' : ' · 首帧') : '' }} · 单张 ≤8MB)
          </span>
          <span v-if="refsRequired" class="text-rose-500">*</span>
        </label>
        <div class="flex gap-2 flex-wrap items-start rounded-lg transition-colors"
             :class="dragOver ? 'ring-2 ring-indigo-400 ring-offset-2 bg-indigo-50/40' : ''"
             @drop="onDrop" @dragover="onDragOver" @dragleave="onDragLeave">
          <div v-for="(img, i) in refImages" :key="i"
               class="relative w-20 h-20 rounded-lg overflow-hidden border border-slate-200 bg-slate-50 transition-all">
            <img :src="img.dataUrl || img.url" class="w-full h-full object-cover" />
            <button type="button" @click="removeRef(i)"
                    class="absolute top-1 right-1 w-5 h-5 rounded-full bg-slate-900/70 text-white hover:bg-rose-500 grid place-items-center disabled:opacity-40 disabled:cursor-not-allowed">
              <Icon name="close" class="w-3 h-3" />
            </button>
            <div v-if="refMode === 'frame' && mode === 'video' && maxRefs >= 2"
                 class="absolute bottom-0 inset-x-0 text-[10px] text-white bg-slate-900/60 text-center py-0.5">
              {{ i === 0 ? '首帧' : (i === 1 ? '末帧' : '') }}
            </div>
          </div>
          <button v-if="refImages.length < maxRefs" type="button" @click="openPicker"
                  class="w-20 h-20 rounded-lg border-2 border-dashed border-slate-200 text-slate-400 hover:bg-slate-50 hover:border-slate-300 grid place-items-center disabled:opacity-40 disabled:cursor-not-allowed"
                  :title="dragOver ? '松开以添加' : '点击或拖拽图片到此'">
            <Icon :name="dragOver ? 'download' : 'plus'" class="w-5 h-5" />
          </button>
        </div>
        <input ref="fileInput" type="file" accept="image/*" multiple class="hidden" @change="onFiles" />
      </div>

      <!-- 生图张数 1–4 (image only) — each is a separate concurrent generation. -->
      <div v-if="mode === 'image'">
        <label class="block text-xs font-medium text-slate-500 mb-1.5">张数</label>
        <div class="flex gap-1.5">
          <button v-for="n in [1, 2, 3, 4]" :key="n" type="button" @click="count = n"
                  class="flex-1 rounded-lg py-1.5 text-xs font-medium transition-colors"
                  :class="count === n ? 'bg-slate-900 text-white' : 'bg-slate-100 text-slate-600 hover:bg-slate-200'">
            {{ n }}
          </button>
        </div>
      </div>

      <button @click="run" :disabled="!models.length || price == null || !canAfford"
              class="btn-primary w-full !py-3 flex items-center justify-center gap-2 leading-none">
        <Icon name="spark" class="w-4 h-4 shrink-0" />
        <span class="leading-none">生成<span v-if="batchCount > 1"> {{ batchCount }} 张</span></span>
        <span v-if="price != null" class="text-xs opacity-70 tabular-nums leading-none">· {{ batchCount > 1 ? pointsLabel(price * batchCount) : priceLabel }}</span>
        <span v-if="price != null && !canAfford" class="text-xs text-rose-200 leading-none">积分不足</span>
      </button>

      <!-- Validation / upload errors (model/prompt/ref/price/credits/oversized
           image). The `error` ref had no render target before, so these messages
           were silently swallowed. -->
      <p v-if="error" class="text-xs text-rose-500 break-all">{{ error }}</p>

    </div>

    <!-- RIGHT: concurrent gallery — one card per task, newest first; filled up to
         10 with the user's recent results. No lock: 生成 can be clicked anytime.
         min-w-0 keeps a long prompt from blowing the 1fr track wider than the page. -->
    <div class="min-w-0">
      <div v-if="displayItems.length" class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-3">
        <div v-for="item in displayItems" :key="item.id"
             class="group relative rounded-xl overflow-hidden ring-1 ring-slate-200 bg-slate-100 aspect-[4/5]">
          <!-- done: media + caption -->
          <template v-if="item.status === 'done' && item.url">
            <!-- first-frame still as background-image (same as image cards); the
                 hidden probe img flips to the <video> fallback for old videos. -->
            <div v-if="item.kind === 'video' && !thumbFail[item.id]" @click="lightbox = item" title="点击播放"
                 :style="{ backgroundImage: `url(${item.url}.thumb.jpg)` }"
                 class="absolute inset-0 w-full h-full bg-cover bg-center cursor-zoom-in transition-transform duration-300 group-hover:scale-105">
              <img :src="item.url + '.thumb.jpg'" class="hidden" @error="thumbFail[item.id] = true" />
            </div>
            <video v-else-if="item.kind === 'video'" :src="item.url" muted loop preload="metadata"
                   @click="lightbox = item" title="点击放大"
                   class="absolute inset-0 w-full h-full object-cover cursor-zoom-in"
                   @mouseenter="$event.target.play && $event.target.play()"
                   @mouseleave="$event.target.pause && $event.target.pause()" />
            <!-- background-image (not <img>) so Edge shows no 视觉搜索 overlay icon. -->
            <div v-else @click="lightbox = item" title="点击放大"
                 :style="{ backgroundImage: `url(${item.url}.thumb.jpg)` }"
                 class="absolute inset-0 w-full h-full bg-cover bg-center cursor-zoom-in transition-transform duration-300 group-hover:scale-105"></div>
            <div class="absolute inset-x-0 bottom-0 h-1/2 bg-gradient-to-t from-black/85 via-black/30 to-transparent pointer-events-none"></div>
            <!-- hover action: 上参考图. Image → use as reference; video → 末帧设为首帧
                 (only shown when the model supports 首尾帧). Clicking the media zooms. -->
            <div class="absolute top-2 right-2 flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
              <button v-if="item.kind !== 'video'" @click.stop.prevent="copyImage(item.url)" title="复制图片"
                      class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center">
                <Icon name="copy" class="w-3.5 h-3.5" />
              </button>
              <a :href="item.url" :download="(item.url || '').split('/').pop()" @click.stop title="下载"
                 class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center">
                <Icon name="download" class="w-3.5 h-3.5" />
              </a>
              <button v-if="item.kind === 'video' ? (refMode === 'frame' && maxRefs > 0) : (maxRefs > 0)"
                      @click.stop="item.kind === 'video' ? useVideoFrame(item) : useAsRef(item)"
                      :title="item.kind === 'video' ? '把末帧设为首帧' : '作为参考图'"
                      class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-black/70 text-white grid place-items-center">
                <Icon name="plus" class="w-3.5 h-3.5" />
              </button>
              <button @click.stop.prevent="deleteItem(item)" title="删除"
                      class="w-7 h-7 rounded-lg bg-black/50 ring-1 ring-white/10 hover:bg-rose-600/80 text-white grid place-items-center">
                <Icon name="trash" class="w-3.5 h-3.5" />
              </button>
            </div>
            <div class="absolute inset-x-0 bottom-0 p-2.5 pointer-events-none">
              <div class="pg-cap text-[11px] leading-tight font-medium line-clamp-2 transition-colors"
                   :class="item.prompt ? 'pointer-events-auto cursor-pointer' : ''"
                   :title="item.prompt ? '点击复制提示词' : ''" @click.stop="copyPrompt(item)">{{ item.prompt }}</div>
              <div class="pg-cap-sub text-[9px] mt-0.5 font-mono truncate">{{ item.model }}<span v-if="item.elapsed_ms"> · {{ (item.elapsed_ms / 1000).toFixed(1) }}s</span></div>
            </div>
          </template>
          <!-- pending / running -->
          <div v-else-if="item.status === 'pending' || item.status === 'running'"
               class="absolute inset-0 grid place-items-center text-slate-400 text-xs px-3 text-center">
            <div class="flex flex-col items-center gap-2">
              <span class="w-10 h-10 rounded-xl bg-white grid place-items-center animate-pulse"><Icon name="spark" class="w-4 h-4" /></span>
              {{ item.kind === 'video' ? '生成视频中…' : '生成中…' }}
              <span class="text-[10px] text-slate-400/80 line-clamp-1 max-w-full">{{ item.prompt }}</span>
            </div>
          </div>
          <!-- failed -->
          <div v-else class="absolute inset-0 grid place-items-center text-rose-500 text-xs px-3 text-center">
            <div>
              <Icon name="close" class="w-6 h-6 mx-auto mb-1 opacity-60" />
              <div>生成失败</div>
              <div v-if="item.error" class="text-[10px] text-rose-400 line-clamp-2 mt-1">{{ item.error }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Lightbox — shared component, consistent with 图片管理 / 日志 -->
    <MediaLightbox
      v-if="lightbox"
      :src="lightbox.url"
      :kind="lightbox.kind"
      :prompt="lightbox.prompt"
      :meta="[lightbox.model, lightbox.ratio, lightbox.resolution, (lightbox.kind === 'video' ? lightbox.duration : '')].filter(Boolean).join(' · ')"
      :download-name="(lightbox.url || '').split('/').pop()"
      @close="lightbox = null" />

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
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

/* Card captions sit on a dark gradient — keep them white even in light theme.
   The global `.theme-text` remap would otherwise darken them (it turns
   over-image whites dark for the marketing pages), making them unreadable here. */
.pg-cap { color: #fff !important; }
.pg-cap.cursor-pointer:hover { color: rgb(255 255 255 / 0.75) !important; }
.pg-cap-sub { color: rgb(255 255 255 / 0.62) !important; }
</style>
