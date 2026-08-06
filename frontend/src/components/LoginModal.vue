<script setup>
// Global login/register/forgot modal. Controlled by shared auth.loginOpen.
// Mounted once in App.vue so it overlays any page (home, user, redirect).
import { ref, reactive, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { auth, setSession, closeLogin } from '../auth'
import Logo from './Logo.vue'

const BASE = import.meta.env.VITE_API_BASE || ''
const router = useRouter()

const mode = ref('login')          // login | register | forgot
const cfg = reactive({ open: true, email_code: false, allow_password_reset: true, has_admin: true })
// `identifier` is the login field (email OR username); `email` is only used by
// register/forgot, where an actual email address is required.
const form = reactive({ identifier: '', username: '', email: '', password: '', code: '' })
const busy = ref(false)
const error = ref('')
const notice = ref('')
const showPw = ref(false)          // password visibility toggle
const codeCooldown = ref(0)        // seconds left before re-sending
const sendingCode = ref(false)     // request in flight → spinner on the button
let cooldownTimer = null

// Per-mode copy for the header so each tab reads as its own little page.
const heading = computed(() => ({
  login: { title: '欢迎回来', sub: '登录以继续你的创作' },
  register: { title: '创建账号', sub: '加入 Vivid,开始 AI 生图' },
  forgot: { title: '找回密码', sub: '通过邮箱验证重置你的密码' },
}[mode.value]))

// Register/reset need an email code only once an admin exists (the very first
// account bootstraps the admin and skips it).
const needsCode = computed(() =>
  cfg.email_code && cfg.has_admin && (mode.value === 'register' || mode.value === 'forgot'))

// Hide the tab bar when 登录 is the only available tab (注册 + 找回密码 both
// hidden) — a lone tab reads as a stray button. Conditions mirror the v-if on
// each tab below.
const showTabs = computed(() =>
  (cfg.open || !cfg.has_admin) || cfg.allow_password_reset)

async function loadConfig() {
  try {
    const r = await fetch(`${BASE}/admin/api/auth/config`)
    if (r.ok) Object.assign(cfg, await r.json())
  } catch { /* offline — keep defaults */ }
}

async function sendCode() {
  error.value = ''; notice.value = ''
  if (!form.email) { error.value = '请先输入邮箱'; return }
  if (codeCooldown.value > 0 || sendingCode.value) return
  // Spin while the request is in flight; only start the countdown once the
  // server actually accepts the send (so a failure lets the user retry at once).
  sendingCode.value = true
  try {
    const r = await post('/auth/send-code', { email: form.email, purpose: mode.value === 'forgot' ? 'reset' : 'register' })
    if (!r.ok) { error.value = r.detail || '验证码发送失败'; return }
    notice.value = '验证码已发送,请查收邮箱'
    codeCooldown.value = 60
    clearInterval(cooldownTimer)
    cooldownTimer = setInterval(() => { if (--codeCooldown.value <= 0) clearInterval(cooldownTimer) }, 1000)
  } catch {
    error.value = '验证码发送失败'
  } finally {
    sendingCode.value = false
  }
}

function switchMode(m) {
  mode.value = m
  error.value = ''; notice.value = ''; showPw.value = false
  // Clear all inputs on tab switch so a password/email typed under one tab
  // doesn't carry over (and read as an auto-filled value) into another.
  form.identifier = ''; form.username = ''; form.email = ''; form.password = ''; form.code = ''
  codeCooldown.value = 0; clearInterval(cooldownTimer)
}

// Reset + reload config every time the modal opens. Honour startMode so an
// invite link can open straight on the register tab.
watch(() => auth.loginOpen, (open) => {
  if (!open) return
  error.value = ''; notice.value = ''; showPw.value = false
  form.identifier = ''; form.username = ''; form.email = ''; form.password = ''; form.code = ''
  loadConfig()
  if (auth.startMode === 'register') switchMode('register')
  else mode.value = 'login'
})

async function post(path, body) {
  const r = await fetch(`${BASE}/admin/api${path}`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  })
  let data = {}
  try { data = await r.json() } catch { /* ignore */ }
  return { ok: r.ok, ...data }
}

async function submit() {
  error.value = ''; notice.value = ''
  if (mode.value === 'login') {
    if (!form.identifier || !form.password) { error.value = '请输入账号和密码'; return }
  } else {
    if (!form.email || !form.password) { error.value = '请输入邮箱和密码'; return }
    if (mode.value === 'register' && !form.username.trim()) { error.value = '请输入用户名'; return }
    if (mode.value === 'register') {
      const u = form.username.trim()
      if (!/^[A-Za-z0-9]{6,24}$/.test(u)) {
        error.value = '用户名需为 6-24 位字母或数字'
        return
      }
    }
    if (!/^\d{6}$/.test((form.code || '').trim()) && needsCode.value) {
      error.value = '邮箱验证码必须是 6 位纯数字'
      return
    }
    const pw = form.password || ''
    if (pw.length < 8 || pw.length > 24) {
      error.value = '密码长度需为 8-24 位'
      return
    }
  }
  busy.value = true
  try {
    if (needsCode.value && !form.code.trim()) { error.value = '请输入邮箱验证码'; busy.value = false; return }
    if (mode.value === 'forgot') {
      const r = await post('/auth/reset-password', {
        email: form.email, password: form.password, email_code: form.code.trim(),
      })
      if (!r.ok) throw new Error(r.detail || '重置失败')
      notice.value = '密码已重置,请用新密码登录'
      switchMode('login')
      return
    }
    const path = mode.value === 'register' ? '/auth/register' : '/auth/login'
    const payload = mode.value === 'login'
      ? { identifier: form.identifier.trim(), password: form.password }
      : { email: form.email, password: form.password }
    if (mode.value === 'register') {
      payload.username = form.username.trim()
      payload.email_code = form.code.trim()
      if (auth.pendingInvite) payload.invite_code = auth.pendingInvite
    }
    const r = await post(path, payload)
    if (!r.ok) throw new Error(r.detail || '操作失败')
    auth.pendingInvite = ''   // consumed
    setSession(r.token, r.user)
    const intent = auth.loginIntent
    closeLogin()
    const dest = intent || (r.user.role === 'admin' ? '/admin/overview' : '/user')
    router.push(dest)
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <transition name="modal">
    <div v-if="auth.loginOpen" class="fixed inset-0 z-50 grid place-items-center px-4">
      <div class="absolute inset-0 bg-black/70 backdrop-blur-md" @click="closeLogin"></div>

      <div class="card relative z-10 w-full max-w-[26rem]">
        <!-- ambient brand glow -->
        <div class="pointer-events-none absolute -top-24 -right-16 h-56 w-56 rounded-full bg-fuchsia-500/20 blur-3xl"></div>
        <div class="pointer-events-none absolute -bottom-24 -left-16 h-56 w-56 rounded-full bg-violet-500/20 blur-3xl"></div>

        <div class="relative p-7">
          <button class="absolute top-4 right-4 grid h-7 w-7 place-items-center rounded-lg text-[color:var(--fg-3)] hover:text-[color:var(--fg)] hover:bg-[var(--hover)] transition-colors"
                  @click="closeLogin" aria-label="关闭">
            <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
          </button>

          <!-- header -->
          <div class="flex flex-col items-center text-center mb-6">
            <div class="logo-halo mb-3"><Logo :size="44" /></div>
            <transition name="swap" mode="out-in">
              <div :key="mode">
                <h2 class="text-lg font-semibold tracking-tight text-[color:var(--fg)]">{{ heading.title }}</h2>
                <p class="mt-1 text-[13px] text-[color:var(--fg-3)]">{{ heading.sub }}</p>
              </div>
            </transition>
          </div>

          <!-- segmented tabs — hidden when 登录 is the only tab -->
          <div v-if="showTabs" class="tabs mb-5">
            <button class="tab" :class="mode === 'login' && 'tab-on'" @click="switchMode('login')">登录</button>
            <button v-if="cfg.open || !cfg.has_admin" class="tab" :class="mode === 'register' && 'tab-on'" @click="switchMode('register')">注册</button>
            <button v-if="cfg.allow_password_reset" class="tab" :class="mode === 'forgot' && 'tab-on'" @click="switchMode('forgot')">找回密码</button>
          </div>

          <transition name="swap" mode="out-in">
            <p v-if="mode === 'register' && auth.pendingInvite" class="invite-banner">
              <svg viewBox="0 0 24 24" class="h-4 w-4 shrink-0" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12v10H4V12"/><path d="M2 7h20v5H2z"/><path d="M12 22V7"/><path d="M12 7H7.5a2.5 2.5 0 0 1 0-5C11 2 12 7 12 7z"/><path d="M12 7h4.5a2.5 2.5 0 0 0 0-5C13 2 12 7 12 7z"/></svg>
              <span>已应用邀请码 <span class="font-mono font-semibold text-emerald-200">{{ auth.pendingInvite }}</span>,完成首次生图后邀请人得 3 积分</span>
            </p>
          </transition>

          <form @submit.prevent="submit" class="space-y-3">
            <div v-if="mode === 'register'" class="lm-field">
              <svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>
              <input v-model="form.username" type="text" autocomplete="username" placeholder="用户名(6-24位,仅字母数字)" class="fld" />
            </div>

            <div v-if="mode === 'login'" class="lm-field">
              <svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>
              <input v-model="form.identifier" type="text" autocomplete="username" placeholder="邮箱或用户名" class="fld" />
            </div>

            <div v-else class="lm-field">
              <svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7"/></svg>
              <input v-model="form.email" type="email" autocomplete="email" placeholder="邮箱" class="fld" />
            </div>

            <div class="lm-field">
              <svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
              <input v-model="form.password" :type="showPw ? 'text' : 'password'" autocomplete="current-password"
                     :placeholder="mode === 'forgot' ? '新密码(8-24位,含大小写/数字/符号)' : '密码'" class="fld pr-10" />
              <button type="button" class="eye" @click="showPw = !showPw" :aria-label="showPw ? '隐藏密码' : '显示密码'">
                <svg v-if="showPw" viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
                <svg v-else viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9.88 9.88a3 3 0 1 0 4.24 4.24"/><path d="M10.73 5.08A10.43 10.43 0 0 1 12 5c7 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68"/><path d="M6.61 6.61A13.526 13.526 0 0 0 2 12s3 7 10 7a9.74 9.74 0 0 0 5.39-1.61"/><path d="m2 2 20 20"/></svg>
              </button>
            </div>

            <div v-if="needsCode" class="flex items-center gap-2">
              <div class="lm-field flex-1">
                <svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 11 3 3L22 4"/><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/></svg>
                <input v-model="form.code" type="text" maxlength="6" inputmode="numeric" placeholder="邮箱验证码(6位数字)" class="fld tracking-[0.3em]" />
              </div>
              <button type="button" @click="sendCode" :disabled="codeCooldown > 0 || sendingCode" class="code-btn">
                <svg v-if="sendingCode" class="code-spin" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
                  <path d="M21 12a9 9 0 1 1-6.219-8.56" />
                </svg>
                <template v-else>{{ codeCooldown > 0 ? `${codeCooldown}s` : '获取验证码' }}</template>
              </button>
            </div>

            <p v-if="mode !== 'login'" class="text-[11px] leading-5 text-[color:var(--fg-3)]">
              用户名：6-24 位，仅字母数字。密码：8-24 位，必须包含大写字母、小写字母、数字和符号。
            </p>

            <button type="submit" :disabled="busy" class="btn-primary w-full">
              <span v-if="busy" class="spinner"></span>
              {{ busy ? '处理中…' : mode === 'login' ? '登 录' : mode === 'register' ? '注 册' : '重置密码' }}
            </button>
          </form>

          <transition name="swap" mode="out-in">
            <p v-if="error" class="msg msg-err"><svg viewBox="0 0 24 24" class="h-3.5 w-3.5 shrink-0" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="10"/><path d="M12 8v4M12 16h.01"/></svg>{{ error }}</p>
            <p v-else-if="notice" class="msg msg-ok"><svg viewBox="0 0 24 24" class="h-3.5 w-3.5 shrink-0" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>{{ notice }}</p>
          </transition>

          <p v-if="!cfg.open && cfg.has_admin" class="text-xs text-[color:var(--fg-3)] mt-4 text-center">当前未开放注册,请联系管理员开通账号。</p>
        </div>
      </div>
    </div>
  </transition>
</template>

<style scoped>
.card {
  border-radius: 1.25rem;
  background: linear-gradient(165deg, #ffffff 0%, #f6f6fb 100%);
  color: var(--fg);
  border: 1px solid var(--hairline);
  box-shadow: 0 30px 70px -22px rgb(15 23 42 / 0.25);
  overflow: hidden;
}
html.dark .card {
  background: linear-gradient(165deg, #14161f 0%, #0d0f15 100%);
  color: rgb(255 255 255 / 0.9);
  border: none;
  box-shadow: 0 30px 70px -20px rgb(0 0 0 / 0.7);
}

.logo-halo {
  position: relative;
  filter: drop-shadow(0 8px 18px rgb(168 85 247 / 0.45));
}

/* segmented tab control */
.tabs {
  display: flex; gap: 0.25rem; padding: 0.25rem;
  border-radius: 0.75rem;
  background: var(--surface-2);
  border: 1px solid var(--hairline);
}
.tab {
  flex: 1; padding: 0.45rem 0; border-radius: 0.55rem;
  font-size: 0.8125rem; color: var(--fg-3);
  transition: background 0.18s, color 0.18s, box-shadow 0.18s;
}
.tab:hover { color: var(--fg-2); }
.tab-on {
  color: rgb(124 58 237);
  background: linear-gradient(135deg, rgb(167 139 250 / 0.22), rgb(236 72 153 / 0.18));
  box-shadow: 0 1px 0 rgb(255 255 255 / 0.08) inset, 0 4px 12px -4px rgb(168 85 247 / 0.5);
}
html.dark .tab-on { color: white; }

/* icon-prefixed input */
.lm-field { position: relative; }
.lm-field .ic {
  position: absolute; left: 0.8rem; top: 50%; transform: translateY(-50%);
  width: 1.05rem; height: 1.05rem; color: var(--fg-3);
  pointer-events: none; transition: color 0.18s;
}
.lm-field:focus-within .ic { color: rgb(124 58 237 / 0.95); }
html.dark .lm-field:focus-within .ic { color: rgb(196 181 253 / 0.95); }
.fld {
  width: 100%; padding: 0.7rem 0.85rem 0.7rem 2.4rem; border-radius: 0.7rem;
  background: rgb(15 23 42 / 0.03); border: 1px solid var(--hairline);
  color: var(--fg); font-size: 0.875rem; outline: none;
  transition: border-color 0.18s, box-shadow 0.18s, background 0.18s;
}
html.dark .fld { background: rgb(255 255 255 / 0.04); }
.fld:focus {
  border-color: rgb(167 139 250 / 0.65);
  background: rgb(124 58 237 / 0.04);
  box-shadow: 0 0 0 3px rgb(167 139 250 / 0.18);
}
html.dark .fld:focus { background: rgb(255 255 255 / 0.06); }
.fld::placeholder { color: var(--fg-faint); }
/* Keep autofill on-theme in dark (otherwise it paints white/yellow). */
html.dark .fld:-webkit-autofill,
html.dark .fld:-webkit-autofill:hover,
html.dark .fld:-webkit-autofill:focus {
  -webkit-text-fill-color: white;
  caret-color: white;
  -webkit-box-shadow: 0 0 0 1000px #1a1c26 inset;
  box-shadow: 0 0 0 1000px #1a1c26 inset;
  transition: background-color 9999s ease-in-out 0s;
}
.eye {
  position: absolute; right: 0.55rem; top: 50%; transform: translateY(-50%);
  display: grid; place-items: center; width: 1.9rem; height: 1.9rem;
  border-radius: 0.45rem; color: var(--fg-3);
  transition: color 0.15s, background 0.15s;
}
.eye:hover { color: var(--fg); background: var(--hover); }

.code-btn {
  flex-shrink: 0; height: 2.7rem; padding: 0 0.85rem; border-radius: 0.7rem;
  min-width: 5.5rem; /* keep width stable between text / spinner */
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 0.75rem; white-space: nowrap;
  color: rgb(124 58 237); /* violet-600 — readable on the light card */
  background: rgb(167 139 250 / 0.12); border: 1px solid rgb(167 139 250 / 0.4);
  transition: background 0.15s, opacity 0.15s;
}
html.dark .code-btn { color: rgb(196 181 253 / 0.95); background: rgb(167 139 250 / 0.1); border-color: rgb(167 139 250 / 0.3); }
.code-btn:hover:not(:disabled) { background: rgb(167 139 250 / 0.2); }
/* Disabled (sending / countdown): just dim — keep the theme text color so it
   never washes out to invisible white on the light card. */
.code-btn:disabled { opacity: 0.55; cursor: not-allowed; }
.code-spin { width: 1.05rem; height: 1.05rem; animation: code-spin 0.7s linear infinite; }
@keyframes code-spin { to { transform: rotate(360deg); } }

.btn-primary {
  display: flex; align-items: center; justify-content: center; gap: 0.5rem;
  padding: 0.72rem 0; border-radius: 0.7rem; font-size: 0.9rem; font-weight: 600;
  letter-spacing: 0.02em; color: white; margin-top: 0.35rem;
  background: linear-gradient(135deg, #a855f7 0%, #7c3aed 50%, #ec4899 100%);
  box-shadow: 0 10px 24px -8px rgb(168 85 247 / 0.6);
  transition: transform 0.12s, box-shadow 0.18s, filter 0.18s;
}
.btn-primary:hover:not(:disabled) { filter: brightness(1.08); box-shadow: 0 12px 28px -8px rgb(168 85 247 / 0.75); }
.btn-primary:active:not(:disabled) { transform: translateY(1px); }
.btn-primary:disabled { opacity: 0.6; cursor: not-allowed; }

.spinner {
  width: 0.95rem; height: 0.95rem; border-radius: 9999px;
  border: 2px solid rgb(255 255 255 / 0.35); border-top-color: white;
  animation: spin 0.7s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }

.invite-banner {
  display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.85rem;
  padding: 0.6rem 0.75rem; border-radius: 0.7rem; font-size: 0.75rem; line-height: 1.4;
  color: rgb(167 243 208);
  background: rgb(16 185 129 / 0.1); border: 1px solid rgb(16 185 129 / 0.25);
}
.invite-banner svg { color: rgb(110 231 183); }

.msg {
  display: flex; align-items: center; gap: 0.4rem; margin-top: 0.85rem;
  font-size: 0.78rem; line-height: 1.4;
}
.msg-err { color: rgb(253 164 175); }
.msg-ok { color: rgb(110 231 183); }

/* modal in/out */
.modal-enter-active { transition: opacity 0.2s ease; }
.modal-leave-active { transition: opacity 0.16s ease; }
.modal-enter-from, .modal-leave-to { opacity: 0; }
.modal-enter-active .card { transition: transform 0.26s cubic-bezier(0.22, 1, 0.36, 1), opacity 0.26s; }
.modal-enter-from .card { transform: translateY(12px) scale(0.97); opacity: 0; }

/* per-mode content swap */
.swap-enter-active, .swap-leave-active { transition: opacity 0.16s ease, transform 0.16s ease; }
.swap-enter-from { opacity: 0; transform: translateY(4px); }
.swap-leave-to { opacity: 0; transform: translateY(-4px); }
</style>
