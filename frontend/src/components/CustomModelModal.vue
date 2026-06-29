<script setup>
import { ref, computed, watch } from 'vue'
import { api, jsonBody } from '../api'
import Icon from './Icon.vue'
import SelectMenu from './SelectMenu.vue'

const emit = defineEmits(['close', 'saved'])

const RATIO_OPTS = ['1:1', '16:9', '9:16', '4:3', '3:4', '21:9', '3:2', '5:4', '4:5', '2:3', '2:1']
const IMG_RES = ['1K', '2K', '4K']
const VID_RES = ['720p', '1080p', '2K', '4K']
const ALL_RES = ['1K', '2K', '4K', '720p', '1080p']
const DUR_OPTS = ['5s', '6s', '8s', '10s', '15s']

const id = ref('')
const type = ref('image')
const ratios = ref(['1:1', '16:9', '9:16'])
const maxRefs = ref(0)
const refMode = ref('none')
const weight = ref(0)
// tier -> { price, agent } — blank price means the tier is NOT supported.
const res = ref(Object.fromEntries(ALL_RES.map((r) => [r, { price: '', agent: '' }])))
const dur = ref(Object.fromEntries(DUR_OPTS.map((d) => [d, { price: '', agent: '' }])))
// Duration rows shown: the presets above + any custom seconds the admin adds.
const durList = ref([...DUR_OPTS])
const customDurInput = ref('')
const error = ref('')
const saving = ref(false)

// Add a custom duration (any positive integer seconds, e.g. 12 → "12s").
function addCustomDur() {
  const n = parseInt(customDurInput.value, 10)
  if (!(n > 0)) return
  const key = n + 's'
  if (!durList.value.includes(key)) {
    if (!dur.value[key]) dur.value[key] = { price: '', agent: '' }
    durList.value.push(key)
  }
  customDurInput.value = ''
}
// Custom (non-preset) durations can be removed; presets stay.
function removeDur(key) {
  if (DUR_OPTS.includes(key)) return
  durList.value = durList.value.filter((k) => k !== key)
  delete dur.value[key]
}

const isVideo = computed(() => type.value === 'video')
// Resolution tiers depend on type: image = 1K/2K/4K, video = 540p/720p/1080p/2K/4K.
const resOpts = computed(() => (isVideo.value ? VID_RES : IMG_RES))

// 首尾帧(frame) only has first+last slots → cap reference images at 2.
const refsCap = computed(() => (refMode.value === 'frame' ? 2 : 99))
watch([refMode, maxRefs], () => {
  if (refMode.value === 'frame' && Number(maxRefs.value) > 2) maxRefs.value = 2
})

function toggleRatio(r) {
  const i = ratios.value.indexOf(r)
  if (i >= 0) ratios.value.splice(i, 1)
  else ratios.value.push(r)
}

// collect checked tiers into { tier: price } and { tier: agentPrice }
// blank price = tier not supported (skipped), matching the edit form.
function collect(tiers, allowed) {
  const prices = {}, agent = {}, keys = []
  for (const [k, v] of Object.entries(tiers)) {
    if (allowed && !allowed.includes(k)) continue
    const raw = String(v.price ?? '').trim()
    if (raw === '') continue
    const n = Number(raw)
    if (Number.isNaN(n) || n < 0) continue
    prices[k] = n; keys.push(k)
    const ar = String(v.agent ?? '').trim()
    if (ar !== '') { const a = Number(ar); if (!Number.isNaN(a) && a >= 0) agent[k] = a }
  }
  return { prices, agent, keys }
}

async function save() {
  const mid = id.value.trim()
  if (!mid) { error.value = '请填写模型 id'; return }
  if (refMode.value === 'frame' && Number(maxRefs.value) > 2) maxRefs.value = 2
  const r = collect(res.value, resOpts.value)
  if (!r.keys.length) { error.value = '请至少勾选一个分辨率并填价格'; return }
  const body = {
    id: mid,
    name: mid,
    type: type.value,
    provider: 'custom',
    prices: r.prices,
    prices_agent: r.agent,
    ratios: ratios.value.slice(),
    max_reference_images: Number(maxRefs.value) || 0,
    reference_mode: refMode.value,
    weight: Number(weight.value) || 0,
    image_to_image: (Number(maxRefs.value) || 0) > 0,
  }
  if (isVideo.value) {
    body.resolutions = r.keys
    const d = collect(dur.value)
    if (!d.keys.length) { error.value = '视频请至少勾选一个时长并填价格'; return }
    body.duration_prices = d.prices
    body.duration_prices_agent = d.agent
    body.durations = d.keys
  }
  saving.value = true; error.value = ''
  try {
    const resp = await api('/managed-models', jsonBody('POST', body))
    if (resp.ok || resp.data?.ok || resp.status === 200) emit('saved')
    else error.value = resp.data?.detail || '创建失败'
  } catch (e) { error.value = String(e) }
  finally { saving.value = false }
}
</script>

<template>
  <div class="fixed inset-0 z-50 bg-slate-900/40 backdrop-blur-sm flex items-start justify-center overflow-y-auto p-4"
       @click.self="emit('close')">
    <div class="card !shadow-xl mt-10 mb-10 w-full max-w-xl">
      <div class="px-5 py-4 border-b border-slate-100 flex items-center justify-between">
        <h2 class="text-sm font-semibold">添加自定义模型(上游 / provider=custom)</h2>
        <button @click="emit('close')" class="text-slate-400 hover:text-slate-700"><Icon name="close" class="w-5 h-5" /></button>
      </div>
      <div class="p-5 space-y-4">
        <p class="text-xs text-slate-500 leading-relaxed">
          id 要与上游模型名<strong class="text-slate-700">一致</strong> —— 生成时按 id 自动路由到「支持该 id 的上游账号」。价格按本地价计费。
        </p>

        <div class="flex gap-3">
          <div class="flex-1">
            <label class="text-xs text-slate-500 block mb-1">模型 id <span class="text-rose-500">*</span></label>
            <input v-model="id" class="field font-mono text-xs h-10" placeholder="gpt-image-2" />
          </div>
          <div class="w-28">
            <label class="text-xs text-slate-500 block mb-1">类型</label>
            <SelectMenu v-model="type" :options="[{value:'image',label:'图像'},{value:'video',label:'视频'}]" />
          </div>
          <div class="w-24">
            <label class="text-xs text-slate-500 block mb-1">权重</label>
            <input v-model.number="weight" type="number" class="field h-10" placeholder="0" />
          </div>
        </div>

        <div>
          <label class="text-xs text-slate-500 block mb-1.5">比例(多选)</label>
          <div class="flex flex-wrap gap-1.5">
            <button v-for="r in RATIO_OPTS" :key="r" type="button" @click="toggleRatio(r)"
                    class="px-2 py-1 rounded text-xs ring-1 transition-colors"
                    :class="ratios.includes(r) ? 'bg-indigo-500/15 text-indigo-600 ring-indigo-300' : 'bg-slate-50 text-slate-500 ring-slate-200 hover:ring-slate-300'">{{ r }}</button>
          </div>
        </div>

        <div>
          <label class="text-xs text-slate-500 block mb-1.5">分辨率 · 价格(填普通价 = 支持该档,<strong class="text-slate-600">留空 = 不支持</strong>)</label>
          <div class="space-y-1.5">
            <div v-for="r in resOpts" :key="r" class="flex items-center gap-2">
              <span class="w-16 text-xs font-mono text-slate-500">{{ r }}</span>
              <input v-model="res[r].price" type="number" class="field !py-1 flex-1" placeholder="普通价(留空=不支持)" />
              <input v-model="res[r].agent" type="number" class="field !py-1 flex-1" placeholder="代理价(留空跟随)" />
            </div>
          </div>
        </div>

        <div v-if="isVideo">
          <label class="text-xs text-slate-500 block mb-1.5">时长 · 价格(总价 = 分辨率价 + 时长价;<strong class="text-slate-600">留空 = 不支持</strong>)</label>
          <div class="space-y-1.5">
            <div v-for="d in durList" :key="d" class="flex items-center gap-2">
              <span class="w-16 text-xs font-mono text-slate-500">{{ d }}</span>
              <input v-model="dur[d].price" type="number" class="field !py-1 flex-1" placeholder="普通价(留空=不支持)" />
              <input v-model="dur[d].agent" type="number" class="field !py-1 flex-1" placeholder="代理价(留空跟随)" />
              <button v-if="!DUR_OPTS.includes(d)" type="button" @click="removeDur(d)"
                      class="shrink-0 w-7 h-7 grid place-items-center rounded text-slate-400 hover:text-rose-500 hover:bg-rose-50" title="删除该时长">
                <Icon name="close" class="w-3.5 h-3.5" />
              </button>
            </div>
          </div>
          <!-- 自定义时长:输入任意秒数,Sora 类上游支持的任意时长都能加 -->
          <div class="flex items-center gap-2 mt-2">
            <input v-model="customDurInput" type="number" min="1" @keydown.enter.prevent="addCustomDur"
                   class="field !py-1 w-32" placeholder="自定义秒数" />
            <button type="button" @click="addCustomDur" class="btn-soft text-xs whitespace-nowrap">+ 添加时长</button>
          </div>
        </div>

        <div class="flex gap-3">
          <div class="flex-1">
            <label class="text-xs text-slate-500 block mb-1">参考图张数<span v-if="refMode==='frame'" class="text-white/40">(首尾帧最多 2)</span></label>
            <input v-model.number="maxRefs" type="number" min="0" :max="refsCap" class="field h-10" />
          </div>
          <div class="flex-1">
            <label class="text-xs text-slate-500 block mb-1">参考模式</label>
            <SelectMenu v-model="refMode" :options="[{value:'none',label:'无'},{value:'asset',label:'参考图'},{value:'frame',label:'首尾帧(视频)'}]" />
          </div>
        </div>

        <button @click="save" :disabled="saving" class="btn-primary w-full">{{ saving ? '创建中…' : '创建模型' }}</button>
        <p v-if="error" class="text-xs text-rose-600">{{ error }}</p>
      </div>
    </div>
  </div>
</template>
