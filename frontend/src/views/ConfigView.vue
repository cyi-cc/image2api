<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { api, jsonBody } from '../api'
import { site, applyFavicon } from '../site'
import TagInput from '../components/TagInput.vue'
import Logo from '../components/Logo.vue'

// ---- logs (retention window) ----
const logsCfg = reactive({ retention_days: 30 })
const logsBusy = ref(false); const logsSaved = ref(false)
async function loadLogs() {
  const r = await api('/settings/logs')
  if (r.ok && r.data) logsCfg.retention_days = Number(r.data.retention_days) || 30
}
async function saveLogs() {
  logsBusy.value = true; logsSaved.value = false
  const r = await api('/settings/logs', jsonBody('PUT', { retention_days: Number(logsCfg.retention_days) || 30 }))
  logsBusy.value = false
  if (r.ok) { logsSaved.value = true; setTimeout(() => (logsSaved.value = false), 2000) }
}

// ---- media (生成图片/视频文件留存) ----
const mediaCfg = reactive({ retention_days: 30 })
const mediaBusy = ref(false); const mediaSaved = ref(false); const mediaRemoved = ref(0)
async function loadMedia() {
  const r = await api('/settings/media')
  if (r.ok && r.data) mediaCfg.retention_days = Number(r.data.retention_days) || 30
}
async function saveMedia() {
  mediaBusy.value = true; mediaSaved.value = false; mediaRemoved.value = 0
  const r = await api('/settings/media', jsonBody('PUT', { retention_days: Number(mediaCfg.retention_days) || 30 }))
  mediaBusy.value = false
  if (r.ok) {
    mediaSaved.value = true
    mediaRemoved.value = Number(r.data?.removed || 0)
    setTimeout(() => (mediaSaved.value = false), 2500)
  }
}

// ---- site (branding shown across the app) ----
// Default homepage 子标题 — shown on the home Hero when unset, and pre-filled
// into the input so the admin edits from it (same idea as 标题 defaulting to Vivid).
const DEFAULT_SUBTITLE = '把脑海里的画面写成一句话,GPT、Gemini、Firefly、Flux 等顶级模型替你变成图像与视频。'
const siteForm = reactive({ title: '', logo: '', subtitle: '', qq: '', qq_link: '', qq_group: '', qq_group_link: '', email: '', shop: '' })
const siteBusy = ref(false); const siteSaved = ref(false)
async function loadSite() {
  const r = await api('/settings/site')
  if (r.ok && r.data) {
    siteForm.title = r.data.title || ''
    siteForm.logo = r.data.logo || ''
    siteForm.subtitle = r.data.subtitle || DEFAULT_SUBTITLE
    const c = r.data.contact || {}
    siteForm.qq = c.qq || ''; siteForm.qq_link = c.qq_link || ''
    siteForm.qq_group = c.qq_group || ''
    siteForm.qq_group_link = c.qq_group_link || ''
    siteForm.email = c.email || ''; siteForm.shop = c.shop || ''
  }
}
// ---- logo (drag/click to stage; uploaded to R2 only on 保存) ----
const logoStaged = ref('')      // dataUrl of a newly picked logo, pending save
const logoRemove = ref(false)   // true → delete logo on save (back to default)
const logoDragOver = ref(false)
const logoInput = ref(null)
// What the preview shows: staged file > (removed → none) > current saved logo.
const logoPreview = computed(() => logoStaged.value || (logoRemove.value ? '' : (siteForm.logo || '')))
function pickLogo() { logoInput.value && logoInput.value.click() }
function readLogo(f) {
  if (!f || !f.type || !f.type.startsWith('image/')) return
  if (f.size > 4 * 1024 * 1024) { flashSite('logo 不能超过 4MB'); return }
  const reader = new FileReader()
  reader.onload = () => { logoStaged.value = reader.result; logoRemove.value = false }
  reader.readAsDataURL(f)
}
function onLogoInput(ev) { readLogo((ev.target.files || [])[0]); if (ev.target) ev.target.value = '' }
function onLogoDrop(ev) { ev.preventDefault(); logoDragOver.value = false; readLogo((ev.dataTransfer?.files || [])[0]) }
function clearLogo() { logoStaged.value = ''; logoRemove.value = true }  // 恢复默认
const siteErr = ref('')
function flashSite(msg) { siteErr.value = msg; setTimeout(() => (siteErr.value = ''), 2500) }

async function saveSite() {
  siteBusy.value = true; siteSaved.value = false; siteErr.value = ''
  // 1) logo first — upload the staged file (or delete) to R2, only on 保存.
  if (logoStaged.value) {
    const lr = await api('/settings/logo', jsonBody('POST', { data: logoStaged.value }))
    if (lr.ok && lr.data?.logo != null) { siteForm.logo = lr.data.logo; site.logo = lr.data.logo; applyFavicon(site.logo); logoStaged.value = '' }
    else { siteBusy.value = false; flashSite(lr.data?.detail || 'Logo 上传失败'); return }
  } else if (logoRemove.value) {
    await api('/settings/logo', { method: 'DELETE' })
    siteForm.logo = ''; site.logo = ''; applyFavicon(''); logoRemove.value = false
  }
  // 2) the rest of the site form (logo is managed above, not sent here).
  const r = await api('/settings/site', jsonBody('PUT', {
    title: siteForm.title,
    subtitle: siteForm.subtitle,
    contact: { qq: siteForm.qq, qq_link: siteForm.qq_link, qq_group: siteForm.qq_group, qq_group_link: siteForm.qq_group_link, email: siteForm.email, shop: siteForm.shop },
  }))
  siteBusy.value = false
  if (r.ok && r.data) {
    site.subtitle = r.data.data?.subtitle ?? siteForm.subtitle.trim()
    // Mirror into the shared `site` store so headers / wordmark update without a reload.
    site.title = r.data.data?.title || siteForm.title.trim()
    site.contact = r.data.data?.contact || site.contact
    siteSaved.value = true
    setTimeout(() => (siteSaved.value = false), 2000)
  } else flashSite(r.data?.detail || '保存失败')
}

// ---- registration ----
const reg = reactive({ open: true, email_code: false, allow_password_reset: true })
// Email domain whitelist edited as tag chips so admins can't typo a comma
// out of a domain or accidentally leave a stray space.
const domains = ref([])
const regBusy = ref(false); const regSaved = ref(false)

// ---- smtp ----
const smtp = reactive({ host: '', port: 587, username: '', password: '', from_addr: '', use_tls: true })
const smtpBusy = ref(false); const smtpSaved = ref(false)

// ---- rewards ----
const credits = reactive({ checkin_enabled: true, checkin_reward: 3, invite_enabled: true, invite_reward: 3, cdk_redeem_enabled: true })
const credBusy = ref(false); const credSaved = ref(false)

// ---- deai (去AI特征 附加价格) ----
const deaiCfg = reactive({ enabled: false, price_1k: 1, price_2k: 2, price_4k: 3 })
const deaiBusy = ref(false); const deaiSaved = ref(false)
async function loadDeai() {
  const r = await api('/settings/deai')
  if (r.ok && r.data) Object.assign(deaiCfg, r.data)
}
async function saveDeai() {
  deaiBusy.value = true; deaiSaved.value = false
  const r = await api('/settings/deai', jsonBody('PUT', {
    enabled: !!deaiCfg.enabled,
    price_1k: Number(deaiCfg.price_1k) || 0,
    price_2k: Number(deaiCfg.price_2k) || 0,
    price_4k: Number(deaiCfg.price_4k) || 0,
  }))
  deaiBusy.value = false
  if (r.ok) { deaiSaved.value = true; setTimeout(() => (deaiSaved.value = false), 2000) }
}

// ---- Adobe provider behavior ----
const adobeCfg = reactive({ allow_sub_account_seedance: false })
const adobeBusy = ref(false); const adobeSaved = ref(false); const adobeErr = ref('')
async function loadAdobe() {
  const r = await api('/settings/adobe')
  if (r.ok && r.data) Object.assign(adobeCfg, r.data)
}
async function saveAdobe() {
  adobeBusy.value = true; adobeSaved.value = false; adobeErr.value = ''
  const r = await api('/settings/adobe', jsonBody('PUT', {
    allow_sub_account_seedance: !!adobeCfg.allow_sub_account_seedance,
  }))
  adobeBusy.value = false
  if (r.ok) {
    Object.assign(adobeCfg, r.data?.data || {})
    adobeSaved.value = true
    setTimeout(() => (adobeSaved.value = false), 2000)
  } else adobeErr.value = r.data?.detail || '保存失败'
}

// ---- announcement (公告, markdown; re-pops for users who haven't seen edits) ----
const ann = reactive({ content: '' })
const annBusy = ref(false); const annSaved = ref(false)
async function loadAnnouncement() {
  const r = await api('/settings/announcement')
  if (r.ok && r.data) ann.content = r.data.content || ''
}
async function saveAnnouncement() {
  annBusy.value = true; annSaved.value = false
  const r = await api('/settings/announcement', jsonBody('PUT', { content: ann.content }))
  annBusy.value = false
  if (r.ok) { annSaved.value = true; setTimeout(() => (annSaved.value = false), 2000) }
}

// ---- payment (易支付 充值) ----
const pay = reactive({ enabled: false, pid: '', key: '', api_base: '', methods: ['wxpay', 'alipay'], min_amount: 1, points_ratio: 100 })
const payBusy = ref(false); const paySaved = ref(false); const payErr = ref('')
const PAY_METHODS = [{ v: 'wxpay', label: '微信' }, { v: 'alipay', label: '支付宝' }]
async function loadPay() {
  const r = await api('/settings/pay')
  if (r.ok && r.data) Object.assign(pay, r.data, { methods: r.data.methods || [] })
}
function togglePayMethod(m) {
  const i = pay.methods.indexOf(m)
  if (i >= 0) pay.methods.splice(i, 1); else pay.methods.push(m)
}
async function savePay() {
  payBusy.value = true; paySaved.value = false; payErr.value = ''
  const r = await api('/settings/pay', jsonBody('PUT', {
    ...pay, min_amount: Number(pay.min_amount) || 0, points_ratio: Number(pay.points_ratio) || 100,
  }))
  payBusy.value = false
  if (r.ok) { paySaved.value = true; setTimeout(() => (paySaved.value = false), 2000) }
  else payErr.value = r.data?.detail || '保存失败'
}

// ---- proxy (carried when calling upstream during generation) ----
const proxy = reactive({ proxy: '' })
const proxyBusy = ref(false); const proxySaved = ref(false)
async function loadProxy() {
  const r = await api('/settings/proxy')
  if (r.ok && r.data) proxy.proxy = r.data.proxy || ''
}
async function saveProxy() {
  proxyBusy.value = true; proxySaved.value = false
  const r = await api('/settings/proxy', jsonBody('PUT', { proxy: proxy.proxy }))
  proxyBusy.value = false
  if (r.ok) { proxySaved.value = true; setTimeout(() => (proxySaved.value = false), 2000) }
}
// Probe the currently-entered proxy (not necessarily saved) — surfaces the
// egress IP on success, or the concrete dial/DNS error on failure.
const proxyTestBusy = ref(false)
const proxyTest = reactive({ ok: null, msg: '' })  // ok: null=idle, true/false=result
async function testProxy() {
  proxyTestBusy.value = true; proxyTest.ok = null; proxyTest.msg = ''
  const r = await api('/settings/proxy/test', jsonBody('POST', { proxy: proxy.proxy }))
  proxyTestBusy.value = false
  if (r.ok && r.data?.ok) {
    proxyTest.ok = true
    const ip = r.data.data?.exit_ip || '未知'
    const ms = r.data.data?.elapsed_ms
    proxyTest.msg = `连接成功 · 出口 IP ${ip}${ms != null ? ` · ${ms}ms` : ''}`
  } else {
    proxyTest.ok = false
    proxyTest.msg = r.data?.detail || '代理测试失败'
  }
}

// ---- Cloudflare R2 object storage (persisted in PostgreSQL, hot-reloaded) ----
const r2 = reactive({
  endpoint: '', region: 'auto', bucket_name: '', public_base_url: '',
  access_key_id: '', secret_access_key: '', configured: false,
})
const r2Busy = ref(false); const r2Saved = ref(false); const r2Err = ref('')
const r2TestBusy = ref(false); const r2Test = reactive({ ok: null, msg: '' })
const r2Ready = computed(() =>
  (r2.endpoint || '').trim() &&
  (r2.public_base_url || '').trim() &&
  (r2.access_key_id || '').trim() &&
  ((r2.secret_access_key || '').trim() || r2.configured)
)
async function loadR2() {
  const res = await api('/settings/r2')
  if (res.ok && res.data) Object.assign(r2, res.data)
}
async function saveR2() {
  r2Busy.value = true; r2Saved.value = false; r2Err.value = ''; r2Test.ok = null; r2Test.msg = ''
  const payload = {
    endpoint: r2.endpoint,
    region: r2.region || 'auto',
    bucket_name: r2.bucket_name,
    public_base_url: r2.public_base_url,
    access_key_id: r2.access_key_id,
  }
  if (r2.secret_access_key && r2.secret_access_key !== '***') payload.secret_access_key = r2.secret_access_key
  const res = await api('/settings/r2', jsonBody('PUT', payload))
  r2Busy.value = false
  if (res.ok) {
    Object.assign(r2, res.data?.data || {})
    site.mediaBaseUrl = (r2.public_base_url || '').replace(/\/+$/, '')
    r2Saved.value = true
    setTimeout(() => (r2Saved.value = false), 2000)
  } else r2Err.value = res.data?.detail || 'R2 配置保存失败'
}
async function testR2() {
  r2TestBusy.value = true; r2Test.ok = null; r2Test.msg = ''
  const res = await api('/settings/r2/test', { method: 'POST' })
  r2TestBusy.value = false
  r2Test.ok = res.ok
  r2Test.msg = res.ok ? (res.data?.detail || 'R2 连接成功') : (res.data?.detail || 'R2 连接测试失败')
}

// Email-code requires SMTP to be configured (host saved) first.
const smtpConfigured = computed(() => !!(smtp.host || '').trim())

// SMTP save requires the four required fields to be non-empty. Password is
// optional on update — leaving it blank means "keep the current one".
const smtpReady = computed(() =>
  (smtp.host || '').trim() &&
  Number(smtp.port) > 0 &&
  (smtp.username || '').trim() &&
  (smtp.from_addr || '').trim()
)

async function loadReg() {
  const r = await api('/settings/registration')
  if (r.ok && r.data) {
    Object.assign(reg, { open: r.data.open, email_code: r.data.email_code,
      allow_password_reset: r.data.allow_password_reset })
    // Normalise to lowercase + strip a leading @ on each so the chips render
    // exactly what the server will compare against.
    domains.value = (r.data.allowed_email_domains || [])
      .map((d) => String(d).trim().toLowerCase().replace(/^@/, ''))
      .filter(Boolean)
  }
}
async function loadSmtp() {
  const r = await api('/settings/smtp')
  if (r.ok && r.data) Object.assign(smtp, r.data)
}
async function loadCredits() {
  const r = await api('/settings/credits')
  if (r.ok && r.data) Object.assign(credits, r.data)
}

async function saveReg() {
  regBusy.value = true; regSaved.value = false
  const r = await api('/settings/registration', jsonBody('PUT', {
    open: reg.open, email_code: reg.email_code,
    allow_password_reset: reg.allow_password_reset,
    allowed_email_domains: domains.value,
  }))
  regBusy.value = false
  if (r.ok) { regSaved.value = true; setTimeout(() => (regSaved.value = false), 2000); loadReg() }
}
async function saveSmtp() {
  smtpBusy.value = true; smtpSaved.value = false
  const payload = { host: smtp.host, port: Number(smtp.port) || 587, username: smtp.username,
    from_addr: smtp.from_addr, use_tls: smtp.use_tls }
  if (smtp.password && smtp.password !== '***') payload.password = smtp.password
  const r = await api('/settings/smtp', jsonBody('PUT', payload))
  smtpBusy.value = false
  if (r.ok) { smtpSaved.value = true; setTimeout(() => (smtpSaved.value = false), 2000); loadSmtp() }
}

// ---- SMTP test send ----
const testEmail = ref('')
const testBusy = ref(false)
const testMsg = ref('')   // result message (success or error)
const testOk = ref(false)
async function sendTest() {
  const to = (testEmail.value || '').trim()
  if (!to || !to.includes('@')) { testMsg.value = '请填写有效的收件邮箱'; testOk.value = false; return }
  testBusy.value = true; testMsg.value = ''
  // Tests the SAVED config, so save first if you just edited the fields.
  const r = await api('/settings/smtp/test', jsonBody('POST', { email: to }))
  testBusy.value = false
  testOk.value = r.ok
  testMsg.value = r.ok ? (r.data?.detail || `测试邮件已发送至 ${to}`) : (r.data?.detail || `发送失败 (${r.status})`)
}
async function saveCredits() {
  credBusy.value = true; credSaved.value = false
  const r = await api('/settings/credits', jsonBody('PUT', {
    checkin_enabled: credits.checkin_enabled,
    checkin_reward: Number(credits.checkin_reward) || 0,
    invite_enabled: credits.invite_enabled,
    invite_reward: Number(credits.invite_reward) || 0,
    cdk_redeem_enabled: credits.cdk_redeem_enabled,
  }))
  credBusy.value = false
  if (r.ok) { credSaved.value = true; setTimeout(() => (credSaved.value = false), 2000) }
}

onMounted(() => { loadSite(); loadReg(); loadSmtp(); loadCredits(); loadAnnouncement(); loadPay(); loadProxy(); loadR2(); loadLogs(); loadMedia(); loadDeai(); loadAdobe() })
</script>

<template>
  <section class="space-y-5">
    <!-- Cloudflare R2 -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <div>
          <h2 class="text-sm font-semibold">Cloudflare R2 存储</h2>
          <p class="text-xs text-slate-400 mt-1">图片、视频、Logo 和首页素材统一存入 R2。配置保存在 PostgreSQL，保存后立即生效，无需修改 Compose 或重启容器。</p>
        </div>
        <span v-if="r2Saved" class="text-xs text-emerald-300">已保存 ✓</span>
        <span v-else class="text-xs" :class="r2.configured ? 'text-emerald-300' : 'text-amber-300'">{{ r2.configured ? '已配置' : '尚未配置' }}</span>
      </div>
      <div class="grid sm:grid-cols-2 gap-3">
        <div class="sm:col-span-2">
          <label class="flbl">S3 API</label>
          <input v-model="r2.endpoint" placeholder="https://账户ID.r2.cloudflarestorage.com/muses-r2bucket" class="field font-mono" />
          <p class="hint mt-1">可以直接粘贴 Cloudflare 页面复制的完整 S3 API；若地址末尾包含 Bucket，保存时会自动拆分。</p>
        </div>
        <div><label class="flbl">Bucket 名称</label><input v-model="r2.bucket_name" placeholder="muses-r2bucket（S3 API 已包含时可留空）" class="field" /></div>
        <div><label class="flbl">Region</label><input v-model="r2.region" placeholder="auto" class="field" /></div>
        <div class="sm:col-span-2">
          <label class="flbl">公开访问地址 / 自定义域</label>
          <input v-model="r2.public_base_url" placeholder="https://muses-r2bucket.ordoeden.com" class="field font-mono" />
          <p class="hint mt-1">Bucket 必须允许公开读取；这里填写自定义域或已启用的 r2.dev 地址，不要填写 S3 API。</p>
        </div>
        <div><label class="flbl">Access Key ID</label><input v-model="r2.access_key_id" autocomplete="off" placeholder="R2 API Token Access Key ID" class="field font-mono" /></div>
        <div><label class="flbl">Secret Access Key</label><input v-model="r2.secret_access_key" type="password" autocomplete="new-password" placeholder="留空表示不修改" class="field font-mono" /></div>
      </div>
      <p v-if="r2Err" class="text-xs text-rose-300 mt-3">{{ r2Err }}</p>
      <div class="mt-4 flex items-center gap-2 flex-wrap">
        <button @click="saveR2" :disabled="r2Busy || !r2Ready" class="btn-primary">{{ r2Busy ? '保存中…' : '保存 R2 配置' }}</button>
        <button @click="testR2" :disabled="r2TestBusy || !r2.configured" class="btn-ghost">{{ r2TestBusy ? '测试中…' : '测试写入与删除' }}</button>
        <span v-if="!r2Ready" class="text-xs text-slate-400">请填写 S3 API、公开地址和访问密钥</span>
        <span v-else-if="r2Test.msg" class="text-xs" :class="r2Test.ok ? 'text-emerald-300' : 'text-rose-300'">{{ r2Test.msg }}</span>
      </div>
    </div>

    <!-- site -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">网站</h2>
        <span v-if="siteSaved" class="text-xs text-emerald-300">已保存 ✓</span>
      </div>
      <div class="space-y-3">
        <label class="row">
          <span><span class="lbl">网页主标题</span><span class="hint">显示在浏览器标签、首页 Logo、侧栏和登录卡上。未设置时默认显示 "Vivid"。</span></span>
          <input v-model="siteForm.title" placeholder="Vivid" class="txt" />
        </label>
        <div class="row">
          <span><span class="lbl">Logo</span><span class="hint">侧栏 / 公开页头部 / 浏览器标签的 Logo。点击或拖拽图片到下图替换,点「保存设置」后才上传到存储(替换会自动删旧图)。下图当前显示的就是默认 Logo。</span></span>
          <div class="flex items-center gap-3">
            <div @click="pickLogo" @drop="onLogoDrop" @dragover.prevent="logoDragOver = true" @dragleave="logoDragOver = false"
                 title="点击或拖拽图片替换"
                 class="w-16 h-16 rounded-xl grid place-items-center overflow-hidden shrink-0 cursor-pointer transition-all"
                 :class="logoDragOver ? 'ring-2 ring-indigo-400 bg-indigo-50/40' : ''">
              <img v-if="logoPreview" :src="logoPreview" class="w-full h-full object-cover" />
              <Logo v-else :size="64" class="w-full h-full" />
            </div>
            <input ref="logoInput" type="file" accept="image/*" class="hidden" @change="onLogoInput" />
          </div>
        </div>
        <p v-if="siteErr" class="text-xs text-rose-500 -mt-1">{{ siteErr }}</p>
        <label class="row">
          <span><span class="lbl">子标题</span><span class="hint">首页 Hero 大标题下方那句话。留空则显示默认:「把脑海里的画面写成一句话,GPT、Gemini、Firefly、Flux 等顶级模型替你变成图像与视频。」</span></span>
          <input v-model="siteForm.subtitle" placeholder="留空 = 默认那句宣传语" class="txt" />
        </label>
        <label class="row">
          <span><span class="lbl">联系 QQ</span><span class="hint">QQ 号(显示用)。留空则不显示该项。</span></span>
          <input v-model="siteForm.qq" placeholder="1114639355" class="txt" />
        </label>
        <label class="row">
          <span><span class="lbl">QQ 链接</span><span class="hint">加好友链接(qm.qq.com/...)。填了则「关于」里的 QQ 可点击、新标签打开。</span></span>
          <input v-model="siteForm.qq_link" placeholder="https://qm.qq.com/q/ItgCcNA7ac" class="txt" />
        </label>
        <label class="row">
          <span><span class="lbl">QQ 群</span><span class="hint">交流群号(显示用)。留空则不显示。</span></span>
          <input v-model="siteForm.qq_group" placeholder="1106849765" class="txt" />
        </label>
        <label class="row">
          <span><span class="lbl">QQ 群链接</span><span class="hint">加群链接(qm.qq.com/...)。填了则「关于」里的 QQ 群可点击、新标签打开。</span></span>
          <input v-model="siteForm.qq_group_link" placeholder="https://qm.qq.com/q/976LeMFoHu" class="txt" />
        </label>
        <label class="row">
          <span><span class="lbl">联系邮箱</span><span class="hint">首页「联系我们」里可点击发邮件。留空则不显示。</span></span>
          <input v-model="siteForm.email" placeholder="vividairun@gmail.com" class="txt" />
        </label>
        <label class="row">
          <span><span class="lbl">商店地址</span><span class="hint">充值/购买页链接,首页「联系我们」里展示为"前往充值商店"。留空则不显示。</span></span>
          <input v-model="siteForm.shop" placeholder="https://pay.ldxp.cn/shop/chiyi" class="txt" />
        </label>
      </div>
      <div class="mt-4 flex items-center gap-3">
        <button @click="saveSite" :disabled="siteBusy || !siteForm.title.trim()" class="btn-primary">{{ siteBusy ? '保存中…' : '保存设置' }}</button>
        <span v-if="!siteForm.title.trim()" class="text-xs text-slate-400">请输入主标题</span>
      </div>
    </div>

    <!-- registration -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">注册与登录</h2>
        <span v-if="regSaved" class="text-xs text-emerald-300">已保存 ✓</span>
      </div>
      <div class="space-y-3">
        <label class="row">
          <span><span class="lbl">开放注册</span><span class="hint">关闭后只能由管理员手动创建账号(首个账号不受限)。</span></span>
          <input type="checkbox" v-model="reg.open" class="sw" />
        </label>
        <label class="row" :class="!smtpConfigured && 'opacity-50'">
          <span><span class="lbl">注册/找回需要邮箱验证码</span><span class="hint">启用后注册、找回密码需输入邮件发送的验证码。<b v-if="!smtpConfigured">需先在下方配置并保存 SMTP 才能开启。</b></span></span>
          <input type="checkbox" v-model="reg.email_code" :disabled="!smtpConfigured" class="sw" />
        </label>
        <label class="row" :class="!reg.email_code && 'opacity-50'">
          <span><span class="lbl">支持找回密码</span><span class="hint">凭邮箱+邮件验证码重置。<b v-if="!reg.email_code">需先开启「邮箱验证码」才能启用。</b></span></span>
          <!-- When email_code is off, password reset is effectively disabled
               regardless of the stored flag (the auth endpoint gates it the
               same way). Reflect that by showing the switch off — but keep
               the stored value untouched so re-enabling email_code restores
               the user's prior preference. -->
          <input type="checkbox"
                 :checked="reg.email_code && reg.allow_password_reset"
                 :disabled="!reg.email_code"
                 @change="reg.allow_password_reset = $event.target.checked"
                 class="sw" />
        </label>
        <!-- Stack the tag input under the label so the chips have room -->
        <div class="row !block !border-b-0">
          <div class="mb-2">
            <span class="lbl">允许的邮箱后缀</span><br />
            <span class="hint">输入后缀按回车添加(如 gmail.com),点 × 删除。<b>留空 = 不限制</b>,允许任意域名注册。</span>
          </div>
          <TagInput v-model="domains" placeholder="留空 = 不限制,或输入后缀回车添加" />
        </div>
      </div>
      <div class="mt-4 flex items-center gap-3">
        <button @click="saveReg" :disabled="regBusy" class="btn-primary">{{ regBusy ? '保存中…' : '保存设置' }}</button>
        <span v-if="!domains.length" class="text-xs text-slate-400">未设置后缀 = 不限制邮箱域名</span>
      </div>
    </div>

    <!-- SMTP -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">邮件服务 (SMTP)</h2>
        <span v-if="smtpSaved" class="text-xs text-emerald-300">已保存 ✓</span>
      </div>
      <p class="text-xs text-slate-400 mb-4">用于发送注册 / 找回密码的验证码邮件。465 端口自动用 SSL,其余端口可选 STARTTLS。</p>
      <div class="grid sm:grid-cols-2 gap-3">
        <div><label class="flbl">SMTP 主机</label><input v-model="smtp.host" placeholder="smtp.gmail.com" class="field" /></div>
        <div><label class="flbl">端口</label><input type="number" v-model.number="smtp.port" placeholder="587" class="field" /></div>
        <div><label class="flbl">用户名</label><input v-model="smtp.username" placeholder="you@gmail.com" class="field" /></div>
        <div><label class="flbl">密码 / 授权码</label><input v-model="smtp.password" type="password" placeholder="留空表示不修改" class="field" /></div>
        <div><label class="flbl">发件地址 (From)</label><input v-model="smtp.from_addr" placeholder="no-reply@yourdomain.com" class="field" /></div>
      </div>
      <!-- STARTTLS sits below the grid as its own labelled row, matching the
           layout of the toggle-style settings in 注册与登录 / 积分奖励 -->
      <label class="row mt-2">
        <span><span class="lbl">使用 STARTTLS</span><span class="hint">端口 587 等明文端口加密会话;465 端口已自动用 SSL,无需开启。</span></span>
        <input type="checkbox" v-model="smtp.use_tls" class="sw" />
      </label>
      <div class="mt-4 flex items-center gap-3">
        <button @click="saveSmtp" :disabled="smtpBusy || !smtpReady" class="btn-primary">{{ smtpBusy ? '保存中…' : '保存设置' }}</button>
        <span v-if="!smtpReady" class="text-xs text-slate-400">请填写 主机 / 端口 / 用户名 / 发件地址</span>
      </div>

      <!-- Test send: verifies the SAVED config actually delivers mail -->
      <div class="mt-4 pt-4 border-t border-white/[0.06]">
        <label class="flbl">测试发送 <span class="text-slate-400 font-normal">(用已保存的配置发一封测试邮件验证)</span></label>
        <div class="flex items-center gap-3 mt-1">
          <input v-model="testEmail" type="email" placeholder="收件邮箱,如 you@example.com" class="field flex-1" @keyup.enter="sendTest" />
          <button @click="sendTest" :disabled="testBusy || !smtpConfigured" class="btn-soft whitespace-nowrap">
            {{ testBusy ? '发送中…' : '发送测试' }}
          </button>
        </div>
        <p v-if="!smtpConfigured" class="text-xs text-slate-400 mt-1.5">请先保存 SMTP 配置后再测试。</p>
        <p v-else-if="testMsg" class="text-xs mt-1.5" :class="testOk ? 'text-emerald-300' : 'text-rose-300'">{{ testMsg }}</p>
      </div>
    </div>

    <!-- proxy -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">代理 (生图请求)</h2>
        <span v-if="proxySaved" class="text-xs text-emerald-300">已保存 ✓</span>
      </div>
      <p class="text-xs text-slate-400 mb-4">调用上游生成图片/视频时统一使用的 HTTP 代理,留空 = 直连。格式如 <code class="px-1 bg-slate-100 rounded">http://127.0.0.1:7890</code>。修改即时生效,无需重启。</p>
      <input v-model="proxy.proxy" placeholder="留空 = 直连,如 http://127.0.0.1:7890" class="field" />
      <div class="mt-4 flex items-center gap-2">
        <button @click="saveProxy" :disabled="proxyBusy" class="btn-primary">{{ proxyBusy ? '保存中…' : '保存设置' }}</button>
        <button @click="testProxy" :disabled="proxyTestBusy || !proxy.proxy.trim()" class="btn-ghost">{{ proxyTestBusy ? '测试中…' : '代理测试' }}</button>
      </div>
      <p v-if="proxyTest.msg" class="text-xs mt-2" :class="proxyTest.ok ? 'text-emerald-300' : 'text-rose-300'">{{ proxyTest.msg }}</p>
    </div>

    <!-- Adobe provider behavior -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">Adobe 视频</h2>
        <span v-if="adobeSaved" class="text-xs text-emerald-300">已保存 ✓</span>
      </div>
      <div class="space-y-3">
        <label class="row">
          <span>
            <span class="lbl">允许子号生成 Seedance 视频</span>
            <span class="hint">关闭时，系统会过滤 Adobe 子号，不让其调用 seedance-fast 和 seedance-2.0。默认关闭。</span>
          </span>
          <input type="checkbox" v-model="adobeCfg.allow_sub_account_seedance" class="sw" />
        </label>
      </div>
      <div v-if="adobeCfg.allow_sub_account_seedance" class="mt-3 rounded-lg border border-amber-400/30 bg-amber-400/10 px-3 py-2 text-xs leading-relaxed text-amber-300">
        ⚠ 风险提示：Adobe 子号通常不具备稳定的 Seedance 权限。开启后会直接请求 Adobe，上游容易返回 HTTP 408，生成成功率可能较低；发生 408 时不会自动证明账号失效。
      </div>
      <p v-if="adobeErr" class="text-xs text-rose-300 mt-3">{{ adobeErr }}</p>
      <div class="mt-4">
        <button @click="saveAdobe" :disabled="adobeBusy" class="btn-primary">{{ adobeBusy ? '保存中…' : '保存设置' }}</button>
      </div>
    </div>

    <!-- rewards -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">积分奖励</h2>
        <span v-if="credSaved" class="text-xs text-emerald-300">已保存 ✓</span>
      </div>
      <div class="space-y-3">
        <label class="row">
          <span><span class="lbl">开启每日签到</span><span class="hint">关闭后用户无法签到领取积分。</span></span>
          <input type="checkbox" v-model="credits.checkin_enabled" class="sw" />
        </label>
        <label class="row" :class="!credits.checkin_enabled && 'opacity-50'">
          <span><span class="lbl">每日签到奖励</span><span class="hint">用户每天签到获得的积分。</span></span>
          <input type="number" min="0" v-model.number="credits.checkin_reward" :disabled="!credits.checkin_enabled" class="num" />
        </label>
        <label class="row">
          <span><span class="lbl">开启邀请奖励</span><span class="hint">关闭后邀请好友不再发放积分奖励。</span></span>
          <input type="checkbox" v-model="credits.invite_enabled" class="sw" />
        </label>
        <label class="row" :class="!credits.invite_enabled && 'opacity-50'">
          <span><span class="lbl">邀请奖励</span><span class="hint">被邀请好友首次生图后,邀请人获得的积分。</span></span>
          <input type="number" min="0" v-model.number="credits.invite_reward" :disabled="!credits.invite_enabled" class="num" />
        </label>
        <label class="row">
          <span><span class="lbl">开启兑换码</span><span class="hint">关闭后用户无法兑换兑换码,前台也不再显示兑换入口。</span></span>
          <input type="checkbox" v-model="credits.cdk_redeem_enabled" class="sw" />
        </label>
      </div>
      <div class="mt-4"><button @click="saveCredits" :disabled="credBusy" class="btn-primary">{{ credBusy ? '保存中…' : '保存设置' }}</button></div>
    </div>

    <!-- deai (去AI特征) -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">去AI特征</h2>
        <span v-if="deaiSaved" class="text-xs text-emerald-300">已保存 ✓</span>
      </div>
      <p class="text-xs text-slate-400 mb-4">画图台开启「去AI特征」后,按画质档位在模型价格之上额外扣除的积分。仅对图片生成生效。</p>
      <div class="space-y-3">
        <label class="row">
          <span><span class="lbl">开启去AI特征</span><span class="hint">关闭后画图台不显示该选项,也不会加价。默认关闭。</span></span>
          <input type="checkbox" v-model="deaiCfg.enabled" class="sw" />
        </label>
        <label class="row" :class="!deaiCfg.enabled && 'opacity-50'">
          <span><span class="lbl">1K 附加价格</span><span class="hint">默认 1 积分。</span></span>
          <input type="number" min="0" v-model.number="deaiCfg.price_1k" class="num" />
        </label>
        <label class="row" :class="!deaiCfg.enabled && 'opacity-50'">
          <span><span class="lbl">2K 附加价格</span><span class="hint">默认 2 积分。</span></span>
          <input type="number" min="0" v-model.number="deaiCfg.price_2k" class="num" />
        </label>
        <label class="row" :class="!deaiCfg.enabled && 'opacity-50'">
          <span><span class="lbl">4K 附加价格</span><span class="hint">默认 3 积分。</span></span>
          <input type="number" min="0" v-model.number="deaiCfg.price_4k" class="num" />
        </label>
      </div>
      <div class="mt-4"><button @click="saveDeai" :disabled="deaiBusy" class="btn-primary">{{ deaiBusy ? '保存中…' : '保存设置' }}</button></div>
    </div>

    <!-- announcement (公告) -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-1">
        <h2 class="text-sm font-semibold">公告</h2>
        <span v-if="annSaved" class="text-xs text-emerald-500">已保存</span>
      </div>
      <p class="text-xs text-slate-400 mb-3">支持 Markdown。登录用户会在首次访问时弹出;<strong class="text-slate-500">更新内容后</strong>,所有没看过新版本的用户会重新弹出。留空则不显示。</p>
      <textarea v-model="ann.content" rows="8" placeholder="# 标题&#10;&#10;支持 **加粗**、列表、[链接](https://...)、`代码` 等 Markdown 语法。"
                class="field font-mono text-xs leading-relaxed" style="resize:vertical"></textarea>
      <div class="mt-4"><button @click="saveAnnouncement" :disabled="annBusy" class="btn-primary">{{ annBusy ? '保存中…' : '保存设置' }}</button></div>
    </div>

    <!-- payment (易支付充值) -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-1">
        <h2 class="text-sm font-semibold">充值 (易支付)</h2>
        <span v-if="paySaved" class="text-xs text-emerald-500">已保存</span>
      </div>
      <p class="text-xs text-slate-400 mb-3">对接易支付。关闭后用户看不到充值入口。商户ID、密钥、支付地址不能为空。</p>
      <div class="space-y-3">
        <label class="row">
          <span><span class="lbl">开启充值</span><span class="hint">关闭后前台不显示充值入口,且无法下单。</span></span>
          <input type="checkbox" v-model="pay.enabled" class="sw" />
        </label>
        <label class="row">
          <span><span class="lbl">支付地址</span><span class="hint">易支付站点根地址,自动拼 /mapi.php。</span></span>
          <input v-model="pay.api_base" placeholder="https://www.ezfpy.cn" class="field !w-64" />
        </label>
        <label class="row">
          <span><span class="lbl">商户ID (PID)</span></span>
          <input v-model="pay.pid" class="field !w-64" />
        </label>
        <label class="row">
          <span><span class="lbl">商户密钥</span></span>
          <input v-model="pay.key" type="password" class="field !w-64" />
        </label>
        <div class="row">
          <span><span class="lbl">支付方式</span><span class="hint">勾选哪些,前台就只显示哪些。</span></span>
          <div class="flex gap-3">
            <label v-for="m in PAY_METHODS" :key="m.v" class="inline-flex items-center gap-1.5 text-sm cursor-pointer">
              <input type="checkbox" :checked="pay.methods.includes(m.v)" @change="togglePayMethod(m.v)" />
              {{ m.label }}
            </label>
          </div>
        </div>
        <label class="row">
          <span><span class="lbl">最低充值金额 (元)</span></span>
          <input type="number" min="0" step="0.01" v-model.number="pay.min_amount" class="num" />
        </label>
        <label class="row">
          <span><span class="lbl">积分充值比例</span><span class="hint">1 元 = 多少积分。例如 100 → 充 10 元到账 1000 积分。</span></span>
          <input type="number" min="1" v-model.number="pay.points_ratio" class="num" />
        </label>
      </div>
      <p v-if="payErr" class="text-xs text-rose-500 mt-3">{{ payErr }}</p>
      <div class="mt-4"><button @click="savePay" :disabled="payBusy" class="btn-primary">{{ payBusy ? '保存中…' : '保存设置' }}</button></div>
    </div>

    <!-- logs retention -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">日志</h2>
        <span v-if="logsSaved" class="text-xs text-emerald-300">已保存 ✓</span>
      </div>
      <div class="space-y-3">
        <label class="row">
          <span>
            <span class="lbl">最大留存时间</span>
            <span class="hint">超过这个天数的日志会被自动清除,内存里同时还有 500 条的硬上限。范围 1–365 天,默认 30。</span>
          </span>
          <div class="flex items-center gap-2">
            <input type="number" min="1" max="365" v-model.number="logsCfg.retention_days" class="num" />
            <span class="text-xs text-white/45">天</span>
          </div>
        </label>
      </div>
      <div class="mt-4"><button @click="saveLogs" :disabled="logsBusy || !logsCfg.retention_days" class="btn-primary">{{ logsBusy ? '保存中…' : '保存设置' }}</button></div>
    </div>

    <!-- media (生成文件) -->
    <div class="card p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-semibold">生成文件 (图片 / 视频)</h2>
        <span v-if="mediaSaved" class="text-xs text-emerald-300">
          已保存 ✓<span v-if="mediaRemoved" class="text-white/45"> · 立即清理 {{ mediaRemoved }} 个文件</span>
        </span>
      </div>
      <div class="space-y-3">
        <label class="row">
          <span>
            <span class="lbl">最大留存时间</span>
            <span class="hint">超过该天数的生成文件(包括用户生图和后台测试图)会被自动删除。范围 1–365 天,默认 30。每 5 分钟最多扫一次,保存设置时会立刻清理一次。</span>
          </span>
          <div class="flex items-center gap-2">
            <input type="number" min="1" max="365" v-model.number="mediaCfg.retention_days" class="num" />
            <span class="text-xs text-white/45">天</span>
          </div>
        </label>
      </div>
      <div class="mt-4"><button @click="saveMedia" :disabled="mediaBusy || !mediaCfg.retention_days" class="btn-primary">{{ mediaBusy ? '保存中…' : '保存设置' }}</button></div>
    </div>
  </section>
</template>

<style scoped>
/* Colors here pair with the dark admin shell (`.public-dark` wraps <main>).
   We use white-alpha instead of slate-* so they hold up against the glass
   card background; the older slate values were authored for the white
   light-mode admin and washed out almost completely. */
.row { display: flex; align-items: center; justify-content: space-between; gap: 1.5rem; padding: 0.75rem 0; border-bottom: 1px solid var(--hairline); }
.row:last-child { border-bottom: none; }
.row > span:first-child { display: flex; flex-direction: column; gap: 0.2rem; }
.lbl { font-weight: 500; color: var(--fg); font-size: 0.875rem; }
.hint { font-size: 0.72rem; color: var(--fg-3); line-height: 1.5; }
.hint b { color: rgb(225 29 72); font-weight: 500; }
html.dark .hint b { color: rgb(253 164 175); }   /* rose — visible warning, not pure red */
.flbl { display: block; font-size: 0.72rem; color: var(--fg-3); margin-bottom: 0.35rem; }
/* Pill toggle switch — applied to <input type="checkbox" class="sw">. Keeps
   the markup as-is so the existing v-model bindings keep working, but the
   control now reads as an on/off slider instead of a tick box. The "locked
   but currently on" state (e.g. allow_password_reset when email_code is off)
   now reads as a disabled-but-on switch, which matches user intuition. */
.sw {
  -webkit-appearance: none;
  appearance: none;
  position: relative;
  flex-shrink: 0;
  width: 2.25rem;
  height: 1.3rem;
  border-radius: 9999px;
  background: rgb(203 213 225);   /* slate-300 */
  cursor: pointer;
  transition: background 0.18s ease;
  outline: none;
}
.sw::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: calc(1.3rem - 4px);
  height: calc(1.3rem - 4px);
  border-radius: 9999px;
  background: white;
  box-shadow: 0 1px 2px rgb(15 23 42 / 0.2);
  transition: transform 0.18s ease;
}
.sw:checked { background: #4f46e5; }   /* indigo-600 — matches btn-primary */
.sw:checked::after { transform: translateX(calc(2.25rem - 1.3rem)); }
.sw:focus-visible { box-shadow: 0 0 0 3px rgb(99 102 241 / 0.25); }
.sw:disabled { cursor: not-allowed; opacity: 0.55; }
/* All three input variants now share the same dark-glass surface as the rest
   of the admin shell. Background + border use white-alpha so they read
   against the card; placeholders are tuned for legibility, not noise. */
.num, .txt, .field {
  background: rgb(15 23 42 / 0.03);
  border: 1px solid var(--hairline);
  color: var(--fg);
  border-radius: 0.55rem;
  outline: none;
  transition: border-color 0.18s, background 0.18s, box-shadow 0.18s;
}
html.dark .num, html.dark .txt, html.dark .field {
  background: rgb(255 255 255 / 0.04);
  border-color: rgb(255 255 255 / 0.1);
  color: white;
}
.num::placeholder, .txt::placeholder, .field::placeholder { color: var(--fg-faint); }
.num:focus, .txt:focus, .field:focus {
  border-color: rgb(167 139 250 / 0.65);
  background: rgb(255 255 255 / 0.06);
  box-shadow: 0 0 0 3px rgb(167 139 250 / 0.15);
}
.num:disabled, .txt:disabled, .field:disabled { opacity: 0.45; cursor: not-allowed; }
.num { width: 6rem; padding: 0.4rem 0.55rem; font-size: 0.8rem; text-align: right; -moz-appearance: textfield; appearance: textfield; }
.num::-webkit-outer-spin-button, .num::-webkit-inner-spin-button { -webkit-appearance: none; margin: 0; }
.txt { width: 16rem; max-width: 60%; padding: 0.4rem 0.65rem; font-size: 0.8rem; }
.field { width: 100%; padding: 0.55rem 0.75rem; font-size: 0.85rem; }

/* Section save buttons — solid violet to match brand. The global .btn-primary
   under .public-dark goes to white; we override here so the save action stays
   visually distinct as the primary action on a form. */
.btn-primary {
  padding: 0.55rem 1.15rem; border-radius: 0.6rem;
  font-size: 0.8rem; font-weight: 600; color: white;
  background: linear-gradient(135deg, #a855f7 0%, #7c3aed 50%, #ec4899 100%);
  box-shadow: 0 8px 20px -8px rgb(168 85 247 / 0.55);
  transition: filter 0.15s, transform 0.12s, box-shadow 0.18s, opacity 0.15s;
}
/* Re-assert the gradient on hover for ALL states: the global
   `.public-dark .btn-primary:hover` in style.css repaints the background white
   with no `:not(:disabled)` guard, so even disabled save buttons (注册与登录 /
   邮件服务 default to disabled) flashed white on hover. Keep this rule
   unconditional; only the brightness/lift below is gated on :not(:disabled). */
.btn-primary:hover { background: linear-gradient(135deg, #a855f7 0%, #7c3aed 50%, #ec4899 100%); }
.btn-primary:hover:not(:disabled) { filter: brightness(1.08); box-shadow: 0 10px 24px -8px rgb(168 85 247 / 0.7); }
.btn-primary:active:not(:disabled) { transform: translateY(1px); }
.btn-primary:disabled { opacity: 0.45; cursor: not-allowed; box-shadow: none; }
</style>
