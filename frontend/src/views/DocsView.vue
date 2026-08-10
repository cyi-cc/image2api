<script setup>
// API 对接文档 — OpenAI-compatible. Lists live models and shows ready-to-run
// curl / Python(openai SDK) examples for image + video, wired to this
// deployment's base URL and the caller's model ids.
import { ref, computed, onMounted } from 'vue'
import { auth } from '../auth'
import { api } from '../api'
import Icon from '../components/Icon.vue'

const base = computed(() => location.origin)            // /v1 is same-origin (dev: Vite proxy)
const keyHint = computed(() => auth.user?.api_keys?.[0]?.key_preview || 'YOUR_API_KEY')

const models = ref([])
onMounted(async () => {
  const r = await api('/managed-models')
  if (r.ok) models.value = (r.data?.data || []).filter((m) => m.enabled !== false)
})

const imageModels = computed(() => models.value.filter((m) => m.type === 'image'))
const videoModels = computed(() => models.value.filter((m) => m.type === 'video'))
function pubName(m) {
  return m?.alias || m?.id || ''
}
const sampleImage = computed(() => pubName(imageModels.value[0]) || 'firefly-image-4')
const sampleVideo = computed(() => pubName(videoModels.value[0]) || 'firefly-kling3')
const sampleSeconds = computed(() => String(videoModels.value[0]?.durations?.[0] || '8s').replace(/s$/, ''))

function modeEmpty(m) {
  if (m.type === 'image') return !(m.resolutions || []).length && !m.image_to_image && !(m.ratios || []).length
  return !(m.resolutions || []).length && !(m.durations || []).length && !m.max_reference_images && !(m.ratios || []).length
}

function priceOf(m) {
  if (m.type === 'video') {
    // Video charge = resolution price + duration price; show the combined range.
    const rv = Object.values(m.prices || {}).filter((v) => v != null).map(Number)
    const durationPrices = m.duration_prices || {}
    const dv = Object.entries(durationPrices)
      .filter(([key, value]) => key !== 'per_second' && value != null)
      .map(([, value]) => Number(value))
    if (durationPrices.per_second != null) {
      const seconds = (m.durations || []).map((d) => parseFloat(d)).filter(Number.isFinite)
      if (seconds.length) {
        dv.push(Number(durationPrices.per_second) * Math.min(...seconds))
        dv.push(Number(durationPrices.per_second) * Math.max(...seconds))
      }
    }
    if (!rv.length || !dv.length) return '—'
    const lo = Math.min(...rv) + Math.min(...dv)
    const hi = Math.max(...rv) + Math.max(...dv)
    return lo === hi ? `${points(lo)} 积分` : `${points(lo)}–${points(hi)} 积分`
  }
  const vals = Object.values(m.prices || {}).filter((v) => v != null).map(Number)
  if (!vals.length) return '—'
  const lo = Math.min(...vals), hi = Math.max(...vals)
  return lo === hi ? `${points(lo)} 积分` : `${points(lo)}–${points(hi)} 积分`
}

// ---- request parameter tables ----
const imageParams = [
  ['model', 'string', '必填', '模型名(别名优先),见上表(图像)'],
  ['prompt', 'string', '必填', '文字描述'],
  ['size', 'string', '可选', '宽x高,如 "1024x1024"。同时决定「比例」+「分辨率档」(按长边)。具体怎么填见下方对照表;留空 = 1:1 · 2K'],
]
const editParams = [
  ['image', 'file', '必填', '输入图;多张参考图重复 image[] 字段(multipart 文件上传)'],
  ['prompt', 'string', '必填', '编辑/参考描述'],
  ['model', 'string', '必填', '模型名(别名优先,需支持图生图)'],
  ['size', 'string', '可选', '同图像:决定比例 + 分辨率档(见下方对照表)'],
]
const videoParams = [
  ['model', 'string', '必填', '模型名(别名优先),见上表(视频)'],
  ['prompt', 'string', '必填', '文字描述'],
  ['seconds', 'string|int', '必填', '时长秒数,如 "5" "8"(取决于模型支持)'],
  ['size', 'string', '可选', '如 "1280x720" / "720x1280" → 决定比例与分辨率'],
  ['input_reference', 'file', '可选', '参考图/首尾帧(multipart 文件上传;多张时重复 input_reference[] 字段)。runway 图生视频必填 1 张,张数上限见上方模型表'],
  ['reference_mode', 'string', '可选', '参考图用途:"frame"=首尾帧(最多 2 张,第 1 张为首帧、第 2 张为尾帧,只传 1 张即仅首帧),"asset"=普通参考图。留空用模型默认;仅支持单一模式的模型不可传'],
]

// ---- size → 比例 × 分辨率档 对照表(用 size 该传的值)----
// size 的长边映射档位:<1800→1K · 1800–3499→2K · ≥3500→4K;宽高比映射比例。
const sizeTable = [
  { ratio: '1:1 · 方',   k1: '1024x1024', k2: '2048x2048', k4: '4096x4096' },
  { ratio: '5:4 · 横',   k1: '1280x1024', k2: '2560x2048', k4: '3840x3072' },
  { ratio: '4:3 · 横',   k1: '1024x768',  k2: '2048x1536', k4: '4096x3072' },
  { ratio: '3:2 · 横',   k1: '1200x800',  k2: '2400x1600', k4: '3600x2400' },
  { ratio: '16:9 · 横',  k1: '1280x720',  k2: '2048x1152', k4: '4096x2304' },
  { ratio: '2:1 · 横',   k1: '1440x720',  k2: '2880x1440', k4: '4096x2048' },
  { ratio: '21:9 · 超宽', k1: '1680x720',  k2: '2520x1080', k4: '5040x2160' },
  { ratio: '3:1 · 超宽',  k1: '1536x512',  k2: '2304x768',  k4: '3840x1280' },
  { ratio: '4:1 · 超宽',  k1: '1728x432',  k2: '2880x720',  k4: '4096x1024' },
  { ratio: '8:1 · 超宽',  k1: '1728x216',  k2: '2880x360',  k4: '4096x512' },
  { ratio: '4:5 · 竖',   k1: '1024x1280', k2: '2048x2560', k4: '3072x3840' },
  { ratio: '3:4 · 竖',   k1: '768x1024',  k2: '1536x2048', k4: '3072x4096' },
  { ratio: '2:3 · 竖',   k1: '800x1200',  k2: '1600x2400', k4: '2400x3600' },
  { ratio: '9:16 · 竖',  k1: '720x1280',  k2: '1152x2048', k4: '2304x4096' },
  { ratio: '1:3 · 竖',   k1: '512x1536',  k2: '768x2304',  k4: '1280x3840' },
  { ratio: '1:4 · 竖',   k1: '432x1728',  k2: '720x2880',  k4: '1024x4096' },
  { ratio: '1:8 · 竖',   k1: '216x1728',  k2: '360x2880',  k4: '512x4096' },
]

// ---- 视频 size → 比例 × 分辨率(720p / 1080p)----
// 视频按「短边」判档:短边 <1080 → 720p,≥1080 → 1080p;宽高比映射比例。
const videoSizeTable = [
  { ratio: '16:9 · 横', p720: '1280x720', p1080: '1920x1080' },
  { ratio: '9:16 · 竖', p720: '720x1280', p1080: '1080x1920' },
  { ratio: '1:1 · 方',  p720: '720x720',  p1080: '1080x1080' },
  { ratio: '4:3 · 横',  p720: '960x720',  p1080: '1440x1080' },
  { ratio: '3:4 · 竖',  p720: '720x960',  p1080: '1080x1440' },
  { ratio: '3:2 · 横',  p720: '1080x720', p1080: '1620x1080' },
  { ratio: '2:3 · 竖',  p720: '720x1080', p1080: '1080x1620' },
]

// ---- examples (built in script so refs resolve correctly) ----
const examples = computed(() => [
  {
    title: '文生图 · curl',
    code:
`curl ${base.value}/v1/images/generations \\
  -H "Authorization: Bearer ${keyHint.value}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${sampleImage.value}",
    "prompt": "a corgi running in a golden wheat field, cinematic",
    "size": "2048x2048"
  }'`,
  },
  {
    title: '文生图 · Python (openai SDK)',
    code:
`import urllib.request
from openai import OpenAI

client = OpenAI(api_key="${keyHint.value}", base_url="${base.value}/v1")

resp = client.images.generate(
    model="${sampleImage.value}",
    prompt="a corgi running in a golden wheat field, cinematic",
    size="2048x2048",   # 2K · 1:1,见下方对照表
)
# 结果是图片 URL(上游原始直链,会过期 → 尽快下载/转存)
urllib.request.urlretrieve(resp.data[0].url, "out.png")`,
  },
  {
    title: '图生图 / 参考图 · curl (multipart)',
    code:
`curl ${base.value}/v1/images/edits \\
  -H "Authorization: Bearer ${keyHint.value}" \\
  -F model="${sampleImage.value}" \\
  -F prompt="把这张图改成赛博朋克风格" \\
  -F size="2048x2048" \\
  -F image=@input.png
# 多张参考图:重复 -F image=@a.png -F image=@b.png`,
  },
  {
    title: '图生图 · Python (openai SDK)',
    code:
`import urllib.request
from openai import OpenAI

client = OpenAI(api_key="${keyHint.value}", base_url="${base.value}/v1")

resp = client.images.edit(
    model="${sampleImage.value}",
    image=open("input.png", "rb"),     # 多张:image=[open("a.png","rb"), open("b.png","rb")]
    prompt="把这张图改成赛博朋克风格",
)
# 结果是图片 URL(上游原始直链,会过期 → 尽快下载/转存)
urllib.request.urlretrieve(resp.data[0].url, "out.png")`,
  },
  {
    title: '视频 · curl(创建 → 轮询 → 下载)',
    code:
`# 1) 创建任务 → 立即返回 {"id": "...", "status": "queued"}
curl ${base.value}/v1/videos \\
  -H "Authorization: Bearer ${keyHint.value}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${sampleVideo.value}",
    "prompt": "a paper boat sailing down a rainy street, cinematic",
    "seconds": "${sampleSeconds.value}",
    "size": "1280x720"
  }'

# 2) 轮询状态,直到 status=completed
curl ${base.value}/v1/videos/<VIDEO_ID> \\
  -H "Authorization: Bearer ${keyHint.value}"

# 3) 下载 mp4(完成后)
curl ${base.value}/v1/videos/<VIDEO_ID>/content \\
  -H "Authorization: Bearer ${keyHint.value}" -o out.mp4`,
  },
  {
    title: '视频 · Python (requests, 轮询)',
    code:
`import time, requests

base = "${base.value}/v1"
h = {"Authorization": "Bearer ${keyHint.value}"}

# 1) 创建
job = requests.post(f"{base}/videos", headers=h, json={
    "model": "${sampleVideo.value}",
    "prompt": "a paper boat sailing down a rainy street",
    "seconds": "${sampleSeconds.value}",
    "size": "1280x720",
}).json()
vid = job["id"]

# 2) 轮询
while True:
    s = requests.get(f"{base}/videos/{vid}", headers=h).json()
    if s["status"] in ("completed", "failed"):
        break
    time.sleep(5)

# 3) 下载
if s["status"] == "completed":
    mp4 = requests.get(f"{base}/videos/{vid}/content", headers=h).content
    open("out.mp4", "wb").write(mp4)`,
  },
  {
    title: '列出模型 · curl',
    code:
`curl ${base.value}/v1/models \\
  -H "Authorization: Bearer ${keyHint.value}"`,
  },
])

// ---- copy + toast ----
const toastMsg = ref('')
let t = null
function toast(m) { toastMsg.value = m; clearTimeout(t); t = setTimeout(() => (toastMsg.value = ''), 1800) }
async function copy(text) {
  try { await navigator.clipboard.writeText(text); toast('已复制') } catch { toast('复制失败') }
}
</script>

<template>
  <div class="theme-text space-y-10">
    <header>
      <span class="sticker">✦ 开发者</span>
      <h1 class="mt-3 text-4xl md:text-5xl font-bold tracking-tight">接口文档</h1>
      <p class="text-white/45 mt-2">完全兼容 OpenAI 接口规范 — 改个 <code class="text-white/70">base_url</code> 和 <code class="text-white/70">api_key</code> 即可直接调用。图像 / 视频 / 图生图全支持。</p>
    </header>

    <!-- quickstart -->
    <section class="grid md:grid-cols-2 gap-4">
      <div class="card p-6">
        <h2 class="text-sm font-semibold text-white/80">基础信息</h2>
        <dl class="mt-4 space-y-3 text-sm">
          <div class="flex items-center justify-between gap-3">
            <dt class="text-white/45">Base URL</dt><dd class="font-mono text-white/90">{{ base }}/v1</dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-white/45">鉴权</dt><dd class="font-mono text-white/90">Authorization: Bearer &lt;key&gt;</dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-white/45">你的 Key</dt><dd class="font-mono text-white/70">{{ keyHint }}</dd>
          </div>
        </dl>
        <p class="text-[11px] text-white/40 mt-4">还没有 Key?去 <router-link to="/settings" class="text-violet-300 underline">设置 → API Key</router-link> 生成。</p>
      </div>

      <div class="card p-6">
        <h2 class="text-sm font-semibold text-white/80">端点</h2>
        <ul class="mt-4 space-y-2.5 text-sm font-mono">
          <li class="flex items-center gap-2"><span class="badge-get">GET</span><span class="text-white/80">/v1/models</span></li>
          <li class="flex items-center gap-2"><span class="badge-post">POST</span><span class="text-white/80">/v1/images/generations</span><span class="text-white/35 font-sans text-xs">文生图</span></li>
          <li class="flex items-center gap-2"><span class="badge-post">POST</span><span class="text-white/80">/v1/images/edits</span><span class="text-white/35 font-sans text-xs">图生图(multipart)</span></li>
          <li class="flex items-center gap-2"><span class="badge-post">POST</span><span class="text-white/80">/v1/videos</span><span class="text-white/35 font-sans text-xs">建视频任务</span></li>
          <li class="flex items-center gap-2"><span class="badge-get">GET</span><span class="text-white/80">/v1/videos/{id}</span><span class="text-white/35 font-sans text-xs">查状态</span></li>
          <li class="flex items-center gap-2"><span class="badge-get">GET</span><span class="text-white/80">/v1/videos/{id}/content</span><span class="text-white/35 font-sans text-xs">下载 mp4</span></li>
        </ul>
      </div>
    </section>

    <!-- models -->
    <section>
      <h2 class="text-lg font-semibold mb-3">可用模型</h2>
      <div class="card overflow-hidden">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-left text-[11px] uppercase tracking-wider text-white/40 border-b border-white/[0.08]">
              <th class="px-4 py-3 font-medium">model</th>
              <th class="px-4 py-3 font-medium">类型</th>
              <th class="px-4 py-3 font-medium">能力</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in models" :key="m.id" class="border-b border-white/[0.04] last:border-0">
              <td class="px-4 py-3 font-mono text-white/90">{{ pubName(m) }}</td>
              <td class="px-4 py-3 text-white/60">{{ m.type === 'video' ? '视频' : '图像' }}</td>
              <td class="px-4 py-3">
                <div class="flex flex-wrap items-center gap-1 text-[11px]">
                  <template v-if="m.type === 'image'">
                    <span v-for="r in (m.resolutions || [])" :key="r" class="cap cap-k">{{ r }}</span>
                  </template>
                  <template v-if="m.type === 'video'">
                    <span v-for="r in (m.resolutions || [])" :key="r" class="cap cap-k">{{ r }}</span>
                    <span v-if="(m.durations || []).length >= 2" class="cap cap-dur">{{ m.durations[0] }}–{{ m.durations[m.durations.length - 1] }}</span>
                    <span v-else-if="(m.durations || []).length === 1" class="cap cap-dur">{{ m.durations[0] }}</span>
                  </template>
                  <span v-if="m.reference_mode === 'frame' && m.max_reference_images > 0" class="cap cap-frame">首尾帧 {{ Math.min(m.max_reference_images, 2) }}</span>
                  <span v-if="m.reference_mode === 'frame' && m.max_reference_images > 2" class="cap cap-ref">参考图 {{ m.max_reference_images }}</span>
                  <span v-else-if="m.reference_mode && m.reference_mode !== 'none' && m.reference_mode !== 'frame' && m.max_reference_images > 0" class="cap cap-ref">参考图 {{ m.max_reference_images }}</span>
                  <span v-if="m.type === 'image' && m.image_to_image && (!m.reference_mode || m.reference_mode === 'none')" class="cap cap-ref">参考图 1</span>
                  <span v-for="r in (m.ratios || [])" :key="'rt'+r" class="cap cap-ratio">{{ r.replace(':', '×') }}</span>
                  <span v-if="modeEmpty(m)" class="text-white/30">—</span>
                </div>
              </td>
            </tr>
            <tr v-if="!models.length"><td colspan="3" class="px-4 py-10 text-center text-white/35">暂无可用模型</td></tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- parameters -->
    <section class="grid lg:grid-cols-2 gap-6">
      <div>
        <h2 class="text-lg font-semibold mb-3">文生图参数 <span class="text-xs font-normal text-white/40">/v1/images/generations</span></h2>
        <div class="card overflow-hidden">
          <table class="w-full text-sm">
            <thead><tr class="text-left text-[11px] uppercase tracking-wider text-white/40 border-b border-white/[0.08]">
              <th class="px-4 py-2.5 font-medium">参数</th><th class="px-4 py-2.5 font-medium">类型</th><th class="px-4 py-2.5 font-medium">必填</th><th class="px-4 py-2.5 font-medium">说明</th>
            </tr></thead>
            <tbody>
              <tr v-for="p in imageParams" :key="p[0]" class="border-b border-white/[0.04] last:border-0">
                <td class="px-4 py-2.5 font-mono text-white/85">{{ p[0] }}</td>
                <td class="px-4 py-2.5 text-white/50 font-mono text-xs">{{ p[1] }}</td>
                <td class="px-4 py-2.5 text-white/55">{{ p[2] }}</td>
                <td class="px-4 py-2.5 text-white/60 text-xs">{{ p[3] }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div>
        <h2 class="text-lg font-semibold mb-3">图生图参数 <span class="text-xs font-normal text-white/40">/v1/images/edits · multipart</span></h2>
        <div class="card overflow-hidden">
          <table class="w-full text-sm">
            <thead><tr class="text-left text-[11px] uppercase tracking-wider text-white/40 border-b border-white/[0.08]">
              <th class="px-4 py-2.5 font-medium">参数</th><th class="px-4 py-2.5 font-medium">类型</th><th class="px-4 py-2.5 font-medium">必填</th><th class="px-4 py-2.5 font-medium">说明</th>
            </tr></thead>
            <tbody>
              <tr v-for="p in editParams" :key="p[0]" class="border-b border-white/[0.04] last:border-0">
                <td class="px-4 py-2.5 font-mono text-white/85">{{ p[0] }}</td>
                <td class="px-4 py-2.5 text-white/50 font-mono text-xs">{{ p[1] }}</td>
                <td class="px-4 py-2.5 text-white/55">{{ p[2] }}</td>
                <td class="px-4 py-2.5 text-white/60 text-xs">{{ p[3] }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="lg:col-span-2">
        <h2 class="text-lg font-semibold mb-3">视频参数 <span class="text-xs font-normal text-white/40">/v1/videos · 异步</span></h2>
        <div class="card overflow-hidden">
          <table class="w-full text-sm">
            <thead><tr class="text-left text-[11px] uppercase tracking-wider text-white/40 border-b border-white/[0.08]">
              <th class="px-4 py-2.5 font-medium">参数</th><th class="px-4 py-2.5 font-medium">类型</th><th class="px-4 py-2.5 font-medium">必填</th><th class="px-4 py-2.5 font-medium">说明</th>
            </tr></thead>
            <tbody>
              <tr v-for="p in videoParams" :key="p[0]" class="border-b border-white/[0.04] last:border-0">
                <td class="px-4 py-2.5 font-mono text-white/85">{{ p[0] }}</td>
                <td class="px-4 py-2.5 text-white/50 font-mono text-xs">{{ p[1] }}</td>
                <td class="px-4 py-2.5 text-white/55">{{ p[2] }}</td>
                <td class="px-4 py-2.5 text-white/60 text-xs">{{ p[3] }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <!-- size 对照表(课时表)—— 解决"传错分辨率" -->
    <section>
      <h2 class="text-lg font-semibold mb-1">图像分辨率对照表 · <code class="text-white/70 text-sm">size</code> 该传什么</h2>
      <p class="text-xs text-white/45 mb-3">
        左边选比例,上面选分辨率档,交叉格里就是 <code class="text-white/70">size</code> 要传的值(直接复制)。
        没有 <code class="text-white/70">quality</code> 参数,图像分辨率只看 <code class="text-white/70">size</code> 的<strong class="text-white/70">长边</strong>。
        档位必须是该模型支持的(见上方「可用模型」的分辨率列),不支持会自动回退到该模型最低档。
      </p>
      <div class="card overflow-hidden">
        <table class="w-full text-sm">
          <thead><tr class="text-left text-[11px] uppercase tracking-wider text-white/40 border-b border-white/[0.08]">
            <th class="px-4 py-2.5 font-medium">比例</th>
            <th class="px-4 py-2.5 font-medium">1K</th>
            <th class="px-4 py-2.5 font-medium">2K</th>
            <th class="px-4 py-2.5 font-medium">4K</th>
          </tr></thead>
          <tbody>
            <tr v-for="row in sizeTable" :key="row.ratio" class="border-b border-white/[0.04] last:border-0">
              <td class="px-4 py-2.5 text-white/75">{{ row.ratio }}</td>
              <td class="px-4 py-2.5 font-mono text-white/85">{{ row.k1 }}</td>
              <td class="px-4 py-2.5 font-mono text-white/85">{{ row.k2 }}</td>
              <td class="px-4 py-2.5 font-mono text-white/85">{{ row.k4 }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="text-xs text-white/40 mt-2">
        例:想要 <strong class="text-white/70">2K 的 16:9 横图</strong> → <code class="text-white/70">"size": "2048x1152"</code>。
        留空 size = 默认 <strong class="text-white/70">1:1 · 2K</strong>。
      </p>

      <!-- 视频分辨率(720p / 1080p,按短边判) -->
      <h2 class="text-lg font-semibold mb-1 mt-8">视频分辨率对照表 · <code class="text-white/70 text-sm">size</code> 该传什么</h2>
      <p class="text-xs text-white/45 mb-3">
        视频用 <code class="text-white/70">720p</code> / <code class="text-white/70">1080p</code> 两档,只看 <code class="text-white/70">size</code> 的<strong class="text-white/70">短边</strong>(短边 ≥1080 = 1080p,否则 720p)。
        档位必须是该视频模型支持的(如 grok-video 仅 720p),不支持会被拒。
      </p>
      <div class="card overflow-hidden">
        <table class="w-full text-sm">
          <thead><tr class="text-left text-[11px] uppercase tracking-wider text-white/40 border-b border-white/[0.08]">
            <th class="px-4 py-2.5 font-medium">比例</th>
            <th class="px-4 py-2.5 font-medium">720p</th>
            <th class="px-4 py-2.5 font-medium">1080p</th>
          </tr></thead>
          <tbody>
            <tr v-for="row in videoSizeTable" :key="row.ratio" class="border-b border-white/[0.04] last:border-0">
              <td class="px-4 py-2.5 text-white/75">{{ row.ratio }}</td>
              <td class="px-4 py-2.5 font-mono text-white/85">{{ row.p720 }}</td>
              <td class="px-4 py-2.5 font-mono text-white/85">{{ row.p1080 }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="text-xs text-white/40 mt-2">
        例:想要 <strong class="text-white/70">720p 的 16:9 横版视频</strong> → <code class="text-white/70">"size": "1280x720"</code>;
        竖版 9:16 → <code class="text-white/70">"720x1280"</code>。
      </p>
    </section>

    <!-- examples -->
    <section class="space-y-4">
      <h2 class="text-lg font-semibold">调用示例</h2>
      <div v-for="ex in examples" :key="ex.title" class="code-card">
        <div class="code-card-bar">
          <span class="code-dots" aria-hidden="true"><i class="dot dot-r"></i><i class="dot dot-y"></i><i class="dot dot-g"></i></span>
          <span class="code-card-title">{{ ex.title }}</span>
          <button @click="copy(ex.code)" class="code-copy">
            <Icon name="copy" class="w-3.5 h-3.5" /> 复制
          </button>
        </div>
        <pre class="code-card-body"><code>{{ ex.code }}</code></pre>
      </div>
    </section>

    <!-- responses -->
    <section>
      <h2 class="text-lg font-semibold mb-3">响应 & 计费</h2>
      <div class="card p-6 space-y-3 text-sm text-white/70">
        <p><strong class="text-white/90">图像</strong>(generations / edits)返回 OpenAI 图片格式:<code class="text-white/85 font-mono">{{ '{ "created": ..., "data": [{ "url": "..." }] }' }}</code> —— <code class="text-white/85 font-mono">data[0].url</code> 是产物 URL,服务端不留存(<strong class="text-white/90">不返回 base64</strong>)。多数模型返回上游<strong class="text-white/90">原始直链</strong>;少数上游需鉴权的(如 gpt-image)会返回一个本站转发链 <code class="text-white/85 font-mono">/v1/images/{id}/content</code>,由服务端带账号凭据取回。<strong class="text-white/90">两种链接都会过期</strong>,请<strong class="text-white/90">尽快下载或转存到你自己的存储</strong>。</p>
        <p><strong class="text-white/90">视频</strong>(异步,Sora 风格三步):</p>
        <ol class="list-decimal list-inside space-y-1 text-white/65 pl-1">
          <li><code class="text-white/85 font-mono">POST /v1/videos</code> 立即返回任务对象 <code class="text-white/85 font-mono">{{ '{ "id": "...", "object": "video", "status": "queued", ... }' }}</code></li>
          <li>轮询 <code class="text-white/85 font-mono">GET /v1/videos/{id}</code>,<code class="text-white/70">status</code> 从 <code class="text-white/70">queued → in_progress → completed</code>(或 <code class="text-white/70">failed</code>)</li>
          <li>完成后 <code class="text-white/85 font-mono">GET /v1/videos/{id}/content</code> 返回 <strong class="text-white/90">mp4 原始二进制</strong>(非 base64、非 URL)</li>
        </ol>
        <p><strong class="text-white/90">计费(预扣)</strong>:生成<strong class="text-white/90">前</strong>按上表价格从你的 Key 账号预扣积分;图像或视频上游失败会自动退回 —— 失败不扣费。</p>
        <p><strong class="text-white/90">参数映射</strong>:<code class="text-white/70">size</code>(宽x高)同时决定<strong class="text-white/90">比例 + 分辨率档</strong>(长边:&lt;1800→1K · 1800–3499→2K · ≥3500→4K),<code class="text-white/70">seconds</code>→视频时长。<strong class="text-white/90">没有 quality 参数</strong>,分辨率只看 size。档位须是该模型支持的(不支持会回退到该模型最低档);参数须落在定价表内否则 400,余额不足 402。</p>
        <div class="pt-2 grid sm:grid-cols-2 gap-2 text-xs">
          <div class="flex items-center gap-2"><span class="badge-err">401</span> Key 无效 / 上游需重新授权</div>
          <div class="flex items-center gap-2"><span class="badge-err">404</span> 未知 model / 视频任务不存在</div>
          <div class="flex items-center gap-2"><span class="badge-err">400</span> 参数缺失 / 不支持或未定价</div>
          <div class="flex items-center gap-2"><span class="badge-err">402</span> 积分不足</div>
          <div class="flex items-center gap-2"><span class="badge-err">409</span> 视频尚未完成(content 未就绪)</div>
          <div class="flex items-center gap-2"><span class="badge-err">429</span> 账号并发已满,请重试</div>
          <div class="flex items-center gap-2"><span class="badge-err">503</span> 上游繁忙,请重试</div>
        </div>
      </div>
    </section>

    <transition name="fade">
      <div v-if="toastMsg" class="fixed bottom-8 left-1/2 -translate-x-1/2 z-50 bg-white text-black text-sm font-medium px-5 py-2.5 rounded-full shadow-2xl">{{ toastMsg }}</div>
    </transition>
  </div>
</template>

<style scoped>
.badge-get, .badge-post, .badge-err {
  border-radius: 4px; padding: 2px 6px; font-size: 10px; line-height: 1;
}
.badge-get { background: rgb(16 185 129 / 0.14); color: rgb(4 120 87); box-shadow: inset 0 0 0 1px rgb(16 185 129 / 0.35); }
.badge-post { background: rgb(14 165 233 / 0.14); color: rgb(3 105 161); box-shadow: inset 0 0 0 1px rgb(14 165 233 / 0.35); }
.badge-err { background: rgb(244 63 94 / 0.12); color: rgb(190 18 60); box-shadow: inset 0 0 0 1px rgb(244 63 94 / 0.3); font-family: ui-monospace, monospace; }
html.dark .badge-get { background: rgb(16 185 129 / 0.15); color: rgb(110 231 183); box-shadow: inset 0 0 0 1px rgb(52 211 153 / 0.3); }
html.dark .badge-post { background: rgb(14 165 233 / 0.15); color: rgb(125 211 252); box-shadow: inset 0 0 0 1px rgb(56 189 248 / 0.3); }
html.dark .badge-err { background: rgb(244 63 94 / 0.15); color: rgb(253 164 175); box-shadow: inset 0 0 0 1px rgb(251 113 133 / 0.3); }

.cap { display: inline-flex; align-items: center; padding: 0.18rem 0.55rem; font-size: 0.7rem; border-radius: 9999px; white-space: nowrap; }
.cap-k { background: rgb(16 185 129 / 0.12); color: rgb(4 120 87); box-shadow: inset 0 0 0 1px rgb(16 185 129 / 0.3); }
.cap-dur { background: rgb(99 102 241 / 0.12); color: rgb(55 48 163); box-shadow: inset 0 0 0 1px rgb(99 102 241 / 0.3); }
.cap-frame { background: rgb(236 72 153 / 0.12); color: rgb(159 18 57); box-shadow: inset 0 0 0 1px rgb(236 72 153 / 0.3); }
.cap-ref { background: rgb(16 185 129 / 0.12); color: rgb(4 120 87); box-shadow: inset 0 0 0 1px rgb(16 185 129 / 0.3); }
.cap-media { background: rgb(245 158 11 / 0.12); color: rgb(146 64 14); box-shadow: inset 0 0 0 1px rgb(245 158 11 / 0.3); }
.cap-ratio { background: rgb(100 116 139 / 0.1); color: rgb(51 65 85); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; box-shadow: inset 0 0 0 1px rgb(100 116 139 / 0.25); }
html.dark .cap-k { background: rgb(16 185 129 / 0.18); color: rgb(110 231 183); box-shadow: inset 0 0 0 1px rgb(52 211 153 / 0.4); }
html.dark .cap-dur { background: rgb(99 102 241 / 0.18); color: rgb(165 180 252); box-shadow: inset 0 0 0 1px rgb(129 140 248 / 0.4); }
html.dark .cap-frame { background: rgb(236 72 153 / 0.18); color: rgb(244 114 182); box-shadow: inset 0 0 0 1px rgb(244 114 182 / 0.45); }
html.dark .cap-ref { background: rgb(16 185 129 / 0.18); color: rgb(110 231 183); box-shadow: inset 0 0 0 1px rgb(52 211 153 / 0.4); }
html.dark .cap-media { background: rgb(245 158 11 / 0.18); color: rgb(252 211 77); box-shadow: inset 0 0 0 1px rgb(252 211 77 / 0.45); }
html.dark .cap-ratio { background: rgb(255 255 255 / 0.06); color: rgb(255 255 255 / 0.65); box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.12); }

/* 苹果风代码卡片：自带深色配色，不随页面主题切换，深/浅背景下都可读 */
.code-card {
  border-radius: 14px; overflow: hidden;
  background: #1d1d1f;
  box-shadow: 0 1px 2px rgb(0 0 0 / 0.25), 0 8px 24px rgb(0 0 0 / 0.18), inset 0 0 0 1px rgb(255 255 255 / 0.08);
}
.code-card-bar {
  display: flex; align-items: center; gap: 10px;
  padding: 9px 12px;
  background: linear-gradient(180deg, #333336, #2a2a2d);
  border-bottom: 1px solid rgb(0 0 0 / 0.4);
}
.code-dots { display: inline-flex; gap: 6px; }
.code-dots .dot { width: 11px; height: 11px; border-radius: 9999px; display: inline-block; }
.dot-r { background: #ff5f57; box-shadow: inset 0 0 0 0.5px rgb(0 0 0 / 0.15); }
.dot-y { background: #febc2e; box-shadow: inset 0 0 0 0.5px rgb(0 0 0 / 0.15); }
.dot-g { background: #28c840; box-shadow: inset 0 0 0 0.5px rgb(0 0 0 / 0.15); }
.code-card-title {
  flex: 1; min-width: 0; text-align: center;
  font-size: 12px; color: rgb(255 255 255 / 0.55);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.code-copy {
  display: inline-flex; align-items: center; gap: 5px;
  font-size: 12px; line-height: 1;
  color: rgb(255 255 255 / 0.6);
  background: rgb(255 255 255 / 0.08);
  border-radius: 7px; padding: 5px 9px;
  transition: background 0.15s, color 0.15s;
}
.code-copy:hover { color: #fff; background: rgb(255 255 255 / 0.16); }
.code-card-body {
  margin: 0; padding: 14px 16px;
  font-size: 12px; line-height: 1.7;
  color: #e8e8ed;
  overflow: auto;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
