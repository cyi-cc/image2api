<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { api, jsonBody } from '../api'
import { site } from '../site'
import TagInput from '../components/TagInput.vue'

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
const siteForm = reactive({ title: '', logo: '', subtitle: '', qq: '', qq_link: '', qq_group: '', qq_group_link: '', email: '', shop: '' })
const siteBusy = ref(false); const siteSaved = ref(false)
async function loadSite() {
  const r = await api('/settings/site')
  if (r.ok && r.data) {
    siteForm.title = r.data.title || ''
    siteForm.logo = r.data.logo || ''
    siteForm.subtitle = r.data.subtitle || ''
    const c = r.data.contact || {}
    siteForm.qq = c.qq || ''; siteForm.qq_link = c.qq_link || ''
    siteForm.qq_group = c.qq_group || ''
    siteForm.qq_group_link = c.qq_group_link || ''
    siteForm.email = c.email || ''; siteForm.shop = c.shop || ''
  }
}
async function saveSite() {
  siteBusy.value = true; siteSaved.value = false
  const r = await api('/settings/site', jsonBody('PUT', {
    title: siteForm.title,
    logo: siteForm.logo,
    subtitle: siteForm.subtitle,
    contact: { qq: siteForm.qq, qq_link: siteForm.qq_link, qq_group: siteForm.qq_group, qq_group_link: siteForm.qq_group_link, email: siteForm.email, shop: siteForm.shop },
  }))
  siteBusy.value = false
  if (r.ok && r.data) {
    site.logo = r.data.data?.logo ?? siteForm.logo.trim()
    site.subtitle = r.data.data?.subtitle ?? siteForm.subtitle.trim()
    // Mirror the change into the shared `site` store so every header /
    // wordmark / tab title updates without a reload. The PUT response is
    // nested ({ ok, data: { title } }) unlike the flat GET, so read the
    // saved value from there — falling back to the input we just submitted.
    site.title = r.data.data?.title || siteForm.title.trim()
    site.contact = r.data.data?.contact || site.contact
    siteSaved.value = true
    setTimeout(() => (siteSaved.value = false), 2000)
  }
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
const credits = reactive({ checkin_enabled: true, checkin_reward: 3, invite_enabled: true, invite_reward: 3 })
const credBusy = ref(false); const credSaved = ref(false)

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
  }))
  credBusy.value = false
  if (r.ok) { credSaved.value = true; setTimeout(() => (credSaved.value = false), 2000) }
}

onMounted(() => { loadSite(); loadReg(); loadSmtp(); loadCredits(); loadProxy(); loadLogs(); loadMedia() })
</script>

<template>
  <section class="space-y-5">
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
        <label class="row">
          <span><span class="lbl">Logo 图片地址</span><span class="hint">侧栏 / 公开页头部显示的 Logo 图片 URL。留空则用文字主标题。</span></span>
          <input v-model="siteForm.logo" placeholder="https://.../logo.png" class="txt" />
        </label>
        <label class="row">
          <span><span class="lbl">子标题</span><span class="hint">主标题下方的副标题 / slogan,公开页展示。留空则不显示。</span></span>
          <input v-model="siteForm.subtitle" placeholder="如:聚合顶级 AI 模型的生图生视频平台" class="txt" />
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
      </div>
      <div class="mt-4"><button @click="saveCredits" :disabled="credBusy" class="btn-primary">{{ credBusy ? '保存中…' : '保存设置' }}</button></div>
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
.num { width: 6rem; padding: 0.4rem 0.55rem; font-size: 0.8rem; text-align: right; }
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
