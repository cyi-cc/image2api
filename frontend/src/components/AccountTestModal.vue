<script setup>
import { ref, computed, onUnmounted } from 'vue'
import { api, jsonBody } from '../api'
import Icon from './Icon.vue'
import SelectMenu from './SelectMenu.vue'

// 账号生成测试:固定使用某个 provider 账号跑一次生成(图像或视频)。
// 模型列表由 AccountsView 打开页面时预加载好传入,弹窗即开即用。
const props = defineProps({
  account: { type: Object, required: true }, // { pool, id, email, type }
  allModels: { type: Array, default: () => [] },
})
const emit = defineEmits(['close'])

// 该账号所属 provider 的全部模型(图像 + 视频)。
const models = computed(() =>
  props.allModels.filter((m) => (m.provider || '') === props.account.pool))
const selectedModel = ref('')
if (models.value.length) selectedModel.value = models.value[0].alias || models.value[0].id

const modelOptions = computed(() => models.value.map((m) => ({
  value: m.alias || m.id,
  label: `${m.alias || m.id}${m.type === 'video' ? ' · 视频' : ''}`,
})))
const currentModel = computed(() =>
  models.value.find((m) => (m.alias || m.id) === selectedModel.value) || null)
const isVideo = computed(() => currentModel.value?.type === 'video')

const prompt = ref('a cute cat sitting on a desk, studio lighting')

const busy = ref(false)
const status = ref('')
const error = ref('')
const resultUrl = ref('')
const resultKind = ref('')

// Gateway-timeout recovery — same policy as TestModal: the backend detaches the
// render from the request, so on a 524/504 we keep polling /jobs/mine?source=admin.
const GATEWAY_TIMEOUT = new Set([0, 408, 504, 520, 521, 522, 523, 524, 525])
let recoverTimer = null
let recoverJobId = ''
let recoverSubmitTs = 0
onUnmounted(() => clearTimeout(recoverTimer))

function firstDuration(m) {
  const keys = Object.keys(m?.duration_prices || {})
    .sort((a, b) => parseFloat(a) - parseFloat(b))
  return keys[0] || '5s'
}

async function run() {
  if (!selectedModel.value) { error.value = '请选择模型'; return }
  if (!prompt.value.trim()) { error.value = '请输入指令'; return }
  busy.value = true
  error.value = ''
  status.value = isVideo.value ? '正在生成视频 (约 1–3 分钟)…' : '正在生成…'
  resultUrl.value = ''
  resultKind.value = ''
  const m = currentModel.value
  const payload = {
    model: selectedModel.value,
    prompt: prompt.value,
    ratio: (m?.ratios && m.ratios[0]) || (isVideo.value ? '16x9' : '1:1'),
    resolution: (m?.resolutions && m.resolutions[0]) || '',
    account_id: props.account.id,
  }
  if (isVideo.value) payload.duration = firstDuration(m)
  recoverSubmitTs = Date.now()
  const r = await api('/test', jsonBody('POST', payload))
  if (r.ok && r.data?.url) {
    busy.value = false
    resultUrl.value = r.data.url
    resultKind.value = r.data.kind || (isVideo.value ? 'video' : 'image')
    status.value = `完成 · ${r.data.provider} · ${(r.data.elapsed_ms / 1000).toFixed(1)}s`
  } else if (GATEWAY_TIMEOUT.has(r.status)) {
    status.value = isVideo.value ? '生成视频中 (约 1–3 分钟)…' : '生成中…'
    recoverJobId = ''
    clearTimeout(recoverTimer)
    recoverTimer = setTimeout(recover, 3000)
  } else {
    busy.value = false
    status.value = ''
    error.value = r.data?.detail || `失败 (${r.status})`
  }
}

async function recover() {
  const r = await api('/jobs/mine?source=admin')
  if (!r.ok) { recoverTimer = setTimeout(recover, 3000); return }
  const { pending, latest } = r.data || {}
  if (pending) {
    recoverJobId = pending.id
    recoverTimer = setTimeout(recover, 3000)
    return
  }
  const mine = latest && (
    (recoverJobId && latest.id === recoverJobId) ||
    (!recoverJobId && latest.ts && latest.ts * 1000 >= recoverSubmitTs - 2000)
  )
  if (mine && latest.status === 'success' && latest.url) {
    busy.value = false
    resultUrl.value = latest.url
    resultKind.value = latest.kind || (isVideo.value ? 'video' : 'image')
    status.value = `完成 · ${(latest.elapsed_ms / 1000).toFixed(1)}s`
    return
  }
  if (mine && latest.status === 'failed') {
    busy.value = false
    status.value = ''
    error.value = latest.error || '生成失败'
    return
  }
  recoverTimer = setTimeout(recover, 3000)
}
</script>

<template>
  <div class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-start justify-center overflow-y-auto p-4"
       @click.self="emit('close')">
    <div class="card !shadow-2xl my-12 w-full max-w-lg">
      <div class="px-5 py-4 border-b border-white/[0.06] flex items-center justify-between">
        <div class="min-w-0">
          <h2 class="text-sm font-semibold">账号生成测试</h2>
          <div class="text-xs text-white/45 font-mono truncate">{{ account.email || account.id }} · {{ account.pool }}</div>
        </div>
        <button @click="emit('close')" class="text-white/40 hover:text-white transition-colors">
          <Icon name="close" class="w-5 h-5" />
        </button>
      </div>

      <div class="p-5 space-y-4">
        <div>
          <label class="lbl">模型</label>
          <div v-if="!models.length" class="text-xs text-amber-300">该账号所属 provider ({{ account.pool }}) 没有可用的模型</div>
          <SelectMenu v-else v-model="selectedModel" :options="modelOptions" placeholder="选择模型" />
        </div>

        <div>
          <label class="lbl">指令</label>
          <textarea v-model="prompt" rows="3" class="field resize-none" placeholder="输入测试指令…"></textarea>
        </div>

        <button @click="run" :disabled="busy || !models.length" class="btn-primary w-full">
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
</style>
