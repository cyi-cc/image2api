<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { api, jsonBody } from '../api'
import { sortResolutions } from '../utils/format'
import Icon from './Icon.vue'

const props = defineProps({
  model: { type: Object, required: true },
})
const emit = defineEmits(['close'])
const publishName = computed(() => props.model.alias || props.model.id)

const isVideo = props.model.type === 'video'

// Video capabilities — the authoritative source is the family preset
// (core/video_models.FAMILIES), not the user-edited managed-model record
// which may have stale max_reference_images / reference_mode values.
const familyPreset = ref(null)
onMounted(async () => {
  if (!isVideo) return
  const r = await api('/video-presets')
  const list = r.data?.data || []
  familyPreset.value = list.find((p) => p.key === props.model.id) || null
})

const ratios = (props.model.ratios && props.model.ratios.length)
  ? props.model.ratios
  : (isVideo ? ['16x9'] : ['1:1'])

const resolutions = (props.model.resolutions && props.model.resolutions.length)
  ? sortResolutions(props.model.resolutions)
  : (isVideo ? ['720p'] : ['2K'])

const durations = computed(() => {
  const declared = props.model?.durations || []
  if (props.model?.duration_prices?.per_second != null && declared.length) return declared
  // duration_prices keys come back alphabetically from Go ("10s" before "5s");
  // sort by numeric seconds so the shortest is first.
  const keys = Object.keys(props.model?.duration_prices || {})
    .filter((key) => key !== 'per_second')
    .sort((a, b) => parseFloat(a) - parseFloat(b))
  if (keys.length) return keys
  if (familyPreset.value?.durations?.length) return familyPreset.value.durations
  return ['5s', '10s']
})

// Reference image support — for video models, max_reference_images > 0 means
// frames can/must be uploaded. Kling 3 i2v actually requires >= 1.
const maxRefs = computed(() => {
  const fromPreset = Number(familyPreset.value?.max_reference_images || 0)
  const fromModel = Number(props.model?.max_reference_images || 0)
  const m = Math.max(fromPreset, fromModel)
  if (m > 0) return m
  // Image models advertise image-to-image via a boolean (not a count) — allow one.
  if (!isVideo && props.model?.image_to_image) return 1
  return 0
})
const refMode = computed(() => familyPreset.value?.reference_mode || props.model?.reference_mode || 'none')
// Reference images are ALWAYS optional — every video model supports pure
// text2video; frame refs (首帧/末帧) only enhance the result when supplied.
const refsRequired = computed(() => false)
const refsLabel = computed(() => {
  if (!isVideo) return maxRefs.value > 0 ? '参考图 (可选)' : ''
  if (refMode.value === 'asset') return `参考图 (最多 ${maxRefs.value} 张)`
  if (refMode.value === 'frame') {
    if (maxRefs.value >= 2) return `首帧 / 末帧 (1=首帧, 2=首尾帧, 最多 ${maxRefs.value} 张)`
    return `首帧 (可选, ${maxRefs.value} 张)`
  }
  return ''
})

const prompt = ref(isVideo
  ? 'A cinematic shot of a golden retriever running through a wheat field at sunset.'
  : 'a cute cat sitting on a desk, studio lighting')
const ratio = ref(ratios[0])
const resolution = ref(resolutions[0])
const duration = ref(durations.value[0])

const refImages = ref([]) // [{ name, dataUrl }]
const fileInput = ref(null)
// Image 5 instruct-edit derives aspect from the reference image — hide the ratio
// picker when a ref is attached (backend omits aspectRatio to avoid a 400).
const showRatio = computed(() => !(props.model.id === 'firefly-image-5' && refImages.value.length > 0))

function openPicker() { fileInput.value && fileInput.value.click() }

function onFiles(ev) {
  const files = Array.from(ev.target.files || [])
  const room = Math.max(0, maxRefs.value - refImages.value.length)
  const toAdd = files.slice(0, room)
  for (const f of toAdd) {
    const reader = new FileReader()
    reader.onload = () => {
      refImages.value.push({ name: f.name, dataUrl: reader.result })
    }
    reader.readAsDataURL(f)
  }
  if (ev.target) ev.target.value = ''
}

function removeRef(i) { refImages.value.splice(i, 1) }

const busy = ref(false)
const status = ref('')
const error = ref('')
const resultUrl = ref('')
const resultKind = ref('')

// Gateway-timeout recovery: the backend detaches the render from the request
// (context.WithoutCancel), so an EdgeOne 524 / proxy timeout does NOT kill it —
// it finishes and is logged as a source="admin" event. On such a timeout we keep
// the modal "生成中" and poll /jobs/mine?source=admin to recover the result.
const GATEWAY_TIMEOUT = new Set([0, 408, 504, 520, 521, 522, 523, 524, 525])
let recoverTimer = null
let recoverJobId = ''
let recoverSubmitTs = 0
onUnmounted(() => clearTimeout(recoverTimer))

async function run() {
  if (!prompt.value.trim()) { error.value = '请输入提示词'; return }
  if (refsRequired.value && refImages.value.length < 1) {
    error.value = '该视频模型需要至少 1 张参考图 (首帧)'
    return
  }
  busy.value = true
  error.value = ''
  status.value = isVideo ? '正在生成视频 (约 1–3 分钟)…' : '正在生成…'
  resultUrl.value = ''
  resultKind.value = ''
  const payload = {
    model: publishName.value,
    prompt: prompt.value,
    ratio: ratio.value,
    resolution: resolution.value,
  }
  if (isVideo) {
    payload.duration = duration.value
  }
  if (refImages.value.length) {
    // Backend accepts raw base64 only — strip the "data:...;base64," prefix.
    payload.reference_images = refImages.value.map((r) => r.dataUrl.replace(/^data:[^,]*,/, ''))
  }
  recoverSubmitTs = Date.now()
  const r = await api('/test', jsonBody('POST', payload))
  if (r.ok && r.data?.url) {
    busy.value = false
    resultUrl.value = r.data.url
    resultKind.value = r.data.kind || (isVideo ? 'video' : 'image')
    status.value = `完成 · ${r.data.provider} · ${(r.data.elapsed_ms / 1000).toFixed(1)}s`
  } else if (GATEWAY_TIMEOUT.has(r.status)) {
    // CDN/代理回源超时(如 EdgeOne 524)—— 后端仍在生成。保持锁住,轮询恢复结果。
    status.value = isVideo ? '生成视频中 (约 1–3 分钟)…' : '生成中…'
    recoverJobId = ''
    clearTimeout(recoverTimer)
    recoverTimer = setTimeout(recover, 3000)
  } else {
    busy.value = false
    status.value = ''
    error.value = r.data?.detail || `失败 (${r.status})`
  }
}

// Poll the admin's own in-flight/just-finished test job after a gateway timeout.
async function recover() {
  const r = await api('/jobs/mine?source=admin')
  if (!r.ok) { recoverTimer = setTimeout(recover, 3000); return }
  const { pending, latest } = r.data || {}
  if (pending) {
    // Still rendering server-side — remember its id and keep waiting.
    recoverJobId = pending.id
    recoverTimer = setTimeout(recover, 3000)
    return
  }
  // No pending: did OUR job finish? Match by the id we saw, or (if it completed
  // before our first poll) by a latest that started at/after our submit.
  const mine = latest && (
    (recoverJobId && latest.id === recoverJobId) ||
    (!recoverJobId && latest.ts && latest.ts * 1000 >= recoverSubmitTs - 2000)
  )
  if (mine && latest.status === 'success' && latest.url) {
    busy.value = false
    resultUrl.value = latest.url
    resultKind.value = latest.kind || (isVideo ? 'video' : 'image')
    status.value = `完成 · ${(latest.elapsed_ms / 1000).toFixed(1)}s`
    return
  }
  if (mine && latest.status === 'failed') {
    busy.value = false
    status.value = ''
    error.value = latest.error || '生成失败'
    return
  }
  // Not resolved yet (event still committing) — keep polling.
  recoverTimer = setTimeout(recover, 3000)
}
</script>

<template>
  <div class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-start justify-center overflow-y-auto p-4"
       @click.self="emit('close')">
    <div class="card !shadow-2xl my-12 w-full max-w-lg">
      <div class="px-5 py-4 border-b border-white/[0.06] flex items-center justify-between">
        <div class="min-w-0">
          <h2 class="text-sm font-semibold">测试模型</h2>
          <div class="text-xs text-white/45 font-mono truncate">{{ publishName }}</div>
        </div>
        <button @click="emit('close')" class="text-white/40 hover:text-white transition-colors">
          <Icon name="close" class="w-5 h-5" />
        </button>
      </div>

      <div class="p-5 space-y-4">
        <div>
          <label class="lbl">提示词</label>
          <textarea v-model="prompt" rows="3" class="field resize-none" placeholder="输入测试提示词…"></textarea>
        </div>

        <!-- Param controls — same pill-button row as the public 画图 page.
             Show the row whenever there's at least one option (even a single
             one) so the chosen value is visible, not silently hidden. -->
        <div v-if="ratios.length > 0 && showRatio">
          <label class="lbl">比例</label>
          <div class="flex flex-wrap gap-1.5">
            <button v-for="r in ratios" :key="r" type="button" @click="ratio = r"
                    class="opt" :class="ratio === r && 'opt-on'">{{ r }}</button>
          </div>
        </div>

        <div v-if="resolutions.length > 0">
          <label class="lbl">{{ isVideo ? '分辨率' : '画质' }}</label>
          <div class="flex flex-wrap gap-1.5">
            <button v-for="r in resolutions" :key="r" type="button" @click="resolution = r"
                    class="opt" :class="resolution === r && 'opt-on'">{{ r }}</button>
          </div>
        </div>

        <div v-if="isVideo && durations.length > 0">
          <label class="lbl">时长</label>
          <div class="flex flex-wrap gap-1.5">
            <button v-for="d in durations" :key="d" type="button" @click="duration = d"
                    class="opt" :class="duration === d && 'opt-on'">{{ d }}</button>
          </div>
        </div>

        <!-- reference images -->
        <div v-if="maxRefs > 0">
          <label class="lbl">
            {{ refsLabel }}
            <span v-if="refsRequired" class="text-rose-300">*</span>
          </label>
          <div class="flex gap-2 flex-wrap items-start">
            <div v-for="(img, i) in refImages" :key="i"
                 class="relative w-20 h-20 rounded-lg overflow-hidden ring-1 ring-white/10 bg-white/[0.04]">
              <img :src="img.dataUrl" class="w-full h-full object-cover" />
              <button type="button" @click="removeRef(i)"
                      class="absolute top-1 right-1 w-5 h-5 rounded-full bg-black/60 text-white hover:bg-rose-500 transition-colors grid place-items-center">
                <Icon name="close" class="w-3 h-3" />
              </button>
              <div v-if="refMode === 'frame' && maxRefs >= 2"
                   class="absolute bottom-0 inset-x-0 text-[10px] text-white bg-black/60 text-center py-0.5">
                {{ i === 0 ? '首帧' : (i === 1 ? '末帧' : '') }}
              </div>
            </div>
            <button v-if="refImages.length < maxRefs" type="button" @click="openPicker"
                    class="w-20 h-20 rounded-lg border-2 border-dashed border-white/15 text-white/40 hover:bg-white/[0.04] hover:border-white/30 transition-colors grid place-items-center">
              <Icon name="plus" class="w-5 h-5" />
            </button>
          </div>
          <input ref="fileInput" type="file" accept="image/*" multiple class="hidden" @change="onFiles" />
        </div>

        <button @click="run" :disabled="busy" class="btn-primary w-full">
          <Icon name="spark" class="w-4 h-4" /> {{ busy ? (isVideo ? '生成中…(请耐心等待)' : '生成中…') : '生成' }}
        </button>

        <p v-if="status" class="text-xs text-white/55">{{ status }}</p>
        <p v-if="error" class="text-xs text-rose-300 break-all">{{ error }}</p>

        <div v-if="resultUrl" class="rounded-xl ring-1 ring-white/10 bg-white/[0.03] overflow-hidden grid place-items-center min-h-[220px]">
          <video v-if="resultKind === 'video'" :src="resultUrl" controls autoplay
                 class="max-w-full max-h-[420px] object-contain" />
          <img v-else :src="resultUrl" class="max-w-full max-h-[360px] object-contain" />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.lbl {
  display: block;
  font-size: 0.72rem;
  font-weight: 500;
  color: rgb(255 255 255 / 0.55);
  margin-bottom: 0.4rem;
}

/* Pill option button — exactly the same shape as the public 画图 page's
   ratio/resolution/duration row. Replaces the native <select> drop-down. */
.opt {
  display: inline-flex;
  align-items: center;
  padding: 0.4rem 0.75rem;
  font-size: 0.75rem;
  font-weight: 500;
  border-radius: 0.5rem;
  color: rgb(255 255 255 / 0.65);
  background: rgb(255 255 255 / 0.04);
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.08);
  transition: background 0.15s ease, color 0.15s ease, box-shadow 0.15s ease;
}
.opt:hover { background: rgb(255 255 255 / 0.08); color: white; }
.opt-on {
  background: rgb(255 255 255 / 0.92);
  color: rgb(15 23 42);
  box-shadow: none;
}
</style>
