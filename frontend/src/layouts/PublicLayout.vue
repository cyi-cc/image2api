<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { auth, isAuthed, isAdmin, openLogin } from '../auth'
import { isDark, toggleTheme } from '../theme'
import { site } from '../site'
import Icon from '../components/Icon.vue'
import Logo from '../components/Logo.vue'
import { pointsLabel } from '../credits'
import { draft } from '../playground'

const route = useRoute()

// 画图/记录 only show once signed in; clicking 设置 while logged out opens login.
const nav = computed(() => {
  const items = [{ to: '/', label: '首页', icon: 'overview' }]
  if (isAuthed()) {
    items.push({ to: '/user', label: '画图', icon: 'spark' })
    items.push({ to: '/logs', label: '记录', icon: 'log' })
    items.push({ to: '/mylogs', label: '日志', icon: 'files' })
    items.push({ to: '/invite', label: '邀请', icon: 'accounts' })
  }
  // 文档 + 关于 are public — visible to guests too.
  items.push({ to: '/docs', label: '文档', icon: 'log' })
  items.push({ to: '/about', label: '关于', icon: 'accounts' })
  return items
})

function onSettings(e) {
  if (!isAuthed()) { e.preventDefault(); openLogin('/settings') }
}

// credits — the logged-in user's real server-side balance (auth.user.credits)
const credits = computed(() => Number(auth.user?.credits || 0))

const showBalance = computed(() => route.path === '/user')

const creditsLabel = computed(() => pointsLabel(credits.value))

// On the 画图 workbench the header label tracks the active mode — it flips
// with the 生图/生视频 tab, and on state restore (回显) reflects whatever a
// pending job is generating (video → 生视频). draft.mode is kept in sync by
// PlaygroundView for both cases.
const currentLabel = computed(() => {
  if (route.path === '/user') return draft.mode === 'video' ? '生视频' : '生图'
  return route.meta?.label || ''
})
</script>

<template>
  <div class="theme-x min-h-screen flex bg-[var(--app-bg)] text-[color:var(--fg-2)] selection:bg-violet-400/30">
    <!-- ===== Left rail ===== -->
    <aside class="fixed inset-y-0 left-0 z-30 w-16 md:w-20 flex flex-col items-center py-5 border-r border-[color:var(--hairline)]">
      <!-- Logo -->
      <router-link to="/" class="mb-8 group transition-transform hover:scale-105">
        <Logo :size="40" class="rounded-xl shadow-lg shadow-violet-500/20 ring-1 ring-white/10" />
      </router-link>

      <!-- Nav -->
      <nav class="flex flex-col gap-1.5 flex-1">
        <router-link
          v-for="n in nav" :key="n.to" :to="n.to"
          :exact-active-class="n.to === '/' ? 'active' : ''"
          :active-class="n.to === '/' ? '' : 'active'"
          class="rail-link group">
          <span class="w-10 h-10 rounded-xl grid place-items-center transition-all ring-1 ring-transparent group-hover:bg-[var(--hover)] group-hover:ring-[color:var(--hairline)]">
            <Icon :name="n.icon" class="w-4 h-4 transition-colors" />
          </span>
          <span class="rail-label">{{ n.label }}</span>
        </router-link>
      </nav>

      <!-- Bottom: admin shortcut (admins only) + settings -->
      <div class="flex flex-col items-center gap-3">
        <router-link v-if="isAdmin()" to="/admin/overview" title="进入管理后台"
                     class="rail-bottom">
          <Icon name="shield" class="w-4 h-4" />
        </router-link>
        <!-- Light/dark toggle — sits right above 设置. Sun when dark (click→light),
             moon when light (click→dark). -->
        <button type="button" @click="toggleTheme"
                :title="isDark ? '切换到亮色' : '切换到暗色'" class="rail-bottom">
          <svg v-if="isDark" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>
          <svg v-else class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
        </button>
        <router-link to="/settings" title="设置" @click="onSettings"
                     :class="$route.path === '/settings' ? 'rail-bottom active' : 'rail-bottom'">
          <Icon name="config" class="w-4 h-4" />
        </router-link>
      </div>
    </aside>

    <!-- ===== Main column ===== -->
    <div class="flex-1 min-w-0 ml-16 md:ml-20 relative">
      <!-- soft background mesh -->
      <div aria-hidden="true" class="pointer-events-none absolute inset-0 overflow-hidden">
        <div class="absolute -top-32 left-1/3 w-[40rem] h-[40rem] rounded-full opacity-[0.16]"
             style="background: radial-gradient(circle, #a855f7, transparent 60%); filter: blur(100px)"></div>
        <div class="absolute top-1/2 -right-40 w-[36rem] h-[36rem] rounded-full opacity-[0.14]"
             style="background: radial-gradient(circle, #06b6d4, transparent 60%); filter: blur(100px)"></div>
        <div class="absolute bottom-0 left-0 w-[32rem] h-[32rem] rounded-full opacity-[0.10]"
             style="background: radial-gradient(circle, #f43f5e, transparent 60%); filter: blur(100px)"></div>
      </div>

      <!-- Page header — shows the Vivid wordmark on home, route label
           elsewhere. Same vertical position across routes so the brand
           stamp doesn't jump around. -->
      <header class="relative z-10 px-8 md:px-14 pt-10 pb-4 flex items-center justify-between gap-4">
        <div class="flex items-baseline gap-2">
          <img v-if="site.logo" :src="site.logo" :alt="site.title" class="h-7 w-auto self-center object-contain" />
          <span v-else class="text-[22px] font-bold tracking-tight bg-gradient-to-r from-fuchsia-300 via-violet-300 to-sky-300 bg-clip-text text-transparent">
            {{ site.title }}
          </span>
          <span class="text-[10px] uppercase tracking-[0.3em] text-[color:var(--fg-faint)]">{{ route.path === '/' ? (site.subtitle || 'AI 生图 · 生视频') : currentLabel }}</span>
        </div>
        <router-link v-if="showBalance" to="/settings"
                     class="text-xs text-[color:var(--fg-2)] hover:text-[color:var(--fg)] tabular-nums transition-colors">
          余额 <span class="text-[color:var(--fg)] font-semibold">{{ creditsLabel }}</span>
          <span class="text-[color:var(--fg-faint)] ml-1">· 充值</span>
        </router-link>
      </header>

      <main :class="['relative z-10 px-8 md:px-14 pb-24 pt-2', { 'public-dark': isDark }]">
        <router-view v-slot="{ Component }">
          <transition name="fade" mode="out-in">
            <component :is="Component" />
          </transition>
        </router-view>
      </main>
    </div>
  </div>
</template>

<style scoped>
.rail-link {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.5rem;
  padding: 0.25rem 0;
  color: var(--fg-3);
  transition: color 0.15s ease;
}
.rail-link:hover { color: var(--fg); }
.rail-link.active { color: var(--fg); }
.rail-link.active::before {
  content: '';
  position: absolute;
  left: -1rem;
  /* Anchor to the icon box, not the whole link (which includes the label
     underneath). rail-link has padding-top 0.25rem and the icon span is 2.5rem
     tall — so the icon center sits at 0.25rem + 1.25rem = 1.5rem from top. */
  top: 1.5rem;
  transform: translateY(-50%);
  width: 3px;
  height: 24px;
  border-radius: 4px;
  background: linear-gradient(180deg, #f0abfc, #a78bfa);
}
.rail-link.active > span:first-child {
  background: var(--hover) !important;
  --tw-ring-color: var(--hairline);
}
.rail-label {
  font-size: 10px;
  letter-spacing: 0.04em;
  font-weight: 500;
}

.rail-bottom {
  width: 2.5rem;
  height: 2.5rem;
  border-radius: 0.75rem;
  display: grid;
  place-items: center;
  color: var(--fg-3);
  --tw-ring-color: transparent;
  transition: color 0.15s ease, background 0.15s ease, box-shadow 0.15s ease;
}
.rail-bottom:hover {
  color: var(--fg);
  background: var(--hover);
  box-shadow: inset 0 0 0 1px var(--hairline);
}
.rail-bottom.active {
  color: var(--fg);
  background: var(--hover);
  box-shadow: inset 0 0 0 1px var(--hairline);
}

/* Admin shortcut — only rendered for admins. Tinted so it doesn't get lost
   next to the plain settings cog right below it. */
.admin-shortcut {
  color: rgb(196 181 253);   /* violet-300 */
  background: linear-gradient(135deg, rgb(167 139 250 / 0.12), rgb(236 72 153 / 0.10));
  box-shadow: inset 0 0 0 1px rgb(167 139 250 / 0.25);
}
.admin-shortcut:hover {
  color: white;
  background: linear-gradient(135deg, rgb(167 139 250 / 0.22), rgb(236 72 153 / 0.18));
  box-shadow: inset 0 0 0 1px rgb(167 139 250 / 0.45);
}


.fade-enter-active, .fade-leave-active { transition: opacity 0.2s ease, transform 0.2s ease; }
.fade-enter-from { opacity: 0; transform: translateY(8px); }
.fade-leave-to { opacity: 0; }
</style>
