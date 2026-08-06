<script setup>
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { auth, isAuthed, isAdmin, openLogin, openRegister, logout } from '../auth'
import { isDark, toggleTheme } from '../theme'
import { site } from '../site'
import Icon from '../components/Icon.vue'
import Logo from '../components/Logo.vue'
import { pointsLabel } from '../credits'
import { draft } from '../playground'
import { announcement, openAnnouncement } from '../announcement'

const route = useRoute()

// Top-bar links. 画图/图片 only show once signed in; everything else a
// signed-in user needs lives in the avatar menu so the bar stays short.
const nav = computed(() => {
  const items = [{ to: '/', label: '首页' }]
  if (isAuthed()) {
    items.push({ to: '/user', label: '画图' })
    items.push({ to: '/logs', label: '图片' })
  }
  items.push({ to: '/docs', label: '文档' })
  items.push({ to: '/about', label: '关于' })
  return items
})

// Avatar menu — only the surfaces the top nav doesn't already expose
// (画图/图片 live in the bar, so they're intentionally left out here).
const userMenu = [
  { to: '/mylogs', label: '生成日志' },
  { to: '/invite', label: '邀请返利' },
  { to: '/orders', label: '订单' },
  { to: '/settings', label: '设置' },
]

const menuOpen = ref(false)     // avatar dropdown
const mobileOpen = ref(false)   // hamburger panel
watch(() => route.fullPath, () => { menuOpen.value = false; mobileOpen.value = false })

// credits — the logged-in user's real server-side balance (auth.user.credits)
const credits = computed(() => Number(auth.user?.credits || 0))
const creditsLabel = computed(() => pointsLabel(credits.value))
const displayName = computed(() => {
  const n = (auth.user?.name || '').trim()
  if (n) return n
  const e = (auth.user?.email || '').trim()
  return e ? e.split('@')[0] : '我的'
})

// On the 画图 workbench the label tracks the active mode — it flips with the
// 生图/生视频 tab, and on state restore reflects a pending job's kind.
const currentLabel = computed(() => {
  if (route.path === '/user') return draft.mode === 'video' ? '生视频' : '生图'
  return route.meta?.label || ''
})

// The home page paints its own full-bleed hero; other pages keep the padded
// shell plus a route-label header.
const isHome = computed(() => route.path === '/')

async function onLogout() {
  menuOpen.value = false
  await logout()
}
</script>

<template>
  <div class="theme-x min-h-screen bg-[var(--app-bg)] text-[color:var(--fg-2)] selection:bg-violet-400/30">
    <!-- ===== Top bar ===== -->
    <header class="fixed top-0 inset-x-0 z-40 border-b border-transparent backdrop-blur-xl top-bar">
      <div class="mx-auto max-w-[1400px] px-4 md:px-8 h-[58px] flex items-center gap-3">
        <!-- brand -->
        <router-link to="/" class="flex items-center gap-2.5 shrink-0 transition-transform hover:scale-[1.03]">
          <img v-if="site.logo" :src="site.logo" :alt="site.title" class="w-8 h-8 object-contain" />
          <Logo v-else :size="30" />
          <span class="text-[17px] font-bold tracking-tight text-[color:var(--fg)]">{{ site.title }}</span>
          <span class="brand-spark" aria-hidden="true">✦</span>
        </router-link>

        <!-- links (desktop) -->
        <nav class="hidden md:flex items-center gap-0.5 ml-4">
          <router-link v-for="n in nav" :key="n.to" :to="n.to"
                       :exact-active-class="n.to === '/' ? 'on' : ''"
                       :active-class="n.to === '/' ? '' : 'on'"
                       class="top-link">{{ n.label }}</router-link>
        </nav>

        <div class="flex-1"></div>

        <!-- balance (signed in) -->
        <router-link v-if="isAuthed()" to="/settings"
                     class="hidden sm:block text-xs text-[color:var(--fg-2)] hover:text-[color:var(--fg)] tabular-nums transition-colors">
          余额 <span class="text-[color:var(--fg)] font-semibold">{{ creditsLabel }}</span>
        </router-link>

        <!-- theme -->
        <button type="button" @click="toggleTheme" class="round-btn"
                :title="isDark ? '切换到亮色' : '切换到暗色'">
          <svg v-if="isDark" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>
          <svg v-else class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
        </button>

        <!-- 公告 -->
        <button v-if="isAuthed() && announcement.content.trim()" type="button"
                @click="openAnnouncement" class="round-btn" title="公告">
          <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 11l18-7-4 14-6-3-3 4-1-6z"/></svg>
        </button>

        <!-- admin shortcut -->
        <router-link v-if="isAdmin()" to="/admin/overview" class="round-btn" title="进入管理后台">
          <Icon name="shield" class="w-4 h-4" />
        </router-link>

        <!-- guest actions -->
        <template v-if="!isAuthed()">
          <button type="button" class="hidden sm:inline-flex top-link" @click="openLogin('/user')">登录</button>
          <button type="button" class="solid-btn" @click="openRegister('', '/user')">
            免费开始 <span aria-hidden="true">→</span>
          </button>
        </template>

        <!-- signed-in: avatar menu -->
        <div v-else class="relative">
          <button type="button" class="name-btn" @click.stop="menuOpen = !menuOpen" title="我的">
            <span class="name-txt">{{ displayName }}</span>
            <svg class="w-3.5 h-3.5 shrink-0 transition-transform" :class="menuOpen && 'rotate-180'"
                 viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
          </button>
          <div v-if="menuOpen" class="menu" @click.stop>
            <router-link v-for="m in userMenu" :key="m.to" :to="m.to" class="menu-item">{{ m.label }}</router-link>
            <div class="menu-sep"></div>
            <button type="button" class="menu-item w-full text-left" @click="onLogout">退出登录</button>
          </div>
        </div>

        <!-- hamburger -->
        <button type="button" class="round-btn hamburger" @click.stop="mobileOpen = !mobileOpen" aria-label="菜单">
          <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 7h16M4 12h16M4 17h16"/></svg>
        </button>
      </div>

      <!-- mobile panel -->
      <div v-if="mobileOpen" class="md:hidden border-t border-[color:var(--hairline)] px-3 py-3 bg-[var(--menu-bg)]">
        <router-link v-for="n in nav" :key="n.to" :to="n.to"
                     :exact-active-class="n.to === '/' ? 'm-on' : ''"
                     :active-class="n.to === '/' ? '' : 'm-on'"
                     class="m-link">{{ n.label }}</router-link>
        <template v-if="isAuthed()">
          <div class="menu-sep"></div>
          <router-link v-for="m in userMenu" :key="m.to" :to="m.to" active-class="m-on" class="m-link">{{ m.label }}</router-link>
        </template>
        <template v-else>
          <div class="menu-sep"></div>
          <button type="button" class="m-link w-full text-left" @click="openLogin('/user')">登录</button>
        </template>
      </div>
    </header>

    <!-- click-away for the dropdowns -->
    <div v-if="menuOpen || mobileOpen" class="fixed inset-0 z-30"
         @click="menuOpen = false; mobileOpen = false"></div>

    <!-- ===== Main column ===== -->
    <div class="relative">
      <!-- soft background mesh -->
      <div aria-hidden="true" class="pointer-events-none absolute inset-0 overflow-hidden">
        <div class="absolute -top-32 left-1/3 w-[40rem] h-[40rem] rounded-full opacity-[0.16]"
             style="background: radial-gradient(circle, #a855f7, transparent 60%); filter: blur(100px)"></div>
        <div class="absolute top-1/2 -right-40 w-[36rem] h-[36rem] rounded-full opacity-[0.14]"
             style="background: radial-gradient(circle, #06b6d4, transparent 60%); filter: blur(100px)"></div>
        <div class="absolute bottom-0 left-0 w-[32rem] h-[32rem] rounded-full opacity-[0.10]"
             style="background: radial-gradient(circle, #f43f5e, transparent 60%); filter: blur(100px)"></div>
        <div class="anime-stars"></div>
      </div>

      <!-- Route label header — the home page brings its own hero instead. -->
      <header v-if="!isHome" class="relative z-10 mx-auto max-w-[1400px] px-4 md:px-8 pt-[86px] pb-4">
        <span class="text-[22px] font-bold tracking-tight text-[color:var(--fg)]">{{ currentLabel }}</span>
      </header>

      <main :class="['relative z-10 mx-auto max-w-[1400px] pb-24',
                     isHome ? 'pt-[58px] px-0' : 'px-4 md:px-8 pt-2',
                     { 'public-dark': isDark }]">
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
.top-bar {
  background: color-mix(in srgb, var(--app-bg) 72%, transparent);
  transition: border-color 0.2s ease;
}
.top-bar:hover { border-bottom-color: var(--hairline); }

.top-link {
  padding: 0.45rem 0.8rem;
  border-radius: 999px;
  font-size: 13.5px;
  font-weight: 500;
  color: var(--fg-2);
  background: transparent;
  border: none;
  cursor: pointer;
  transition: color 0.15s ease, background 0.15s ease;
}
.top-link:hover { color: var(--fg); background: var(--hover); }
.top-link.on {
  color: #a21caf;
  font-weight: 700;
  background: linear-gradient(135deg, rgb(244 114 182 / 0.16), rgb(129 140 248 / 0.16));
  box-shadow: inset 0 0 0 1px rgb(217 70 239 / 0.3);
}
html.dark .top-link.on {
  color: #f5d0fe;
  background: linear-gradient(135deg, rgb(244 114 182 / 0.2), rgb(129 140 248 / 0.2));
  box-shadow: inset 0 0 0 1px rgb(217 70 239 / 0.35), 0 0 16px -6px rgb(217 70 239 / 0.55);
}

.brand-spark {
  font-size: 12px;
  line-height: 1;
  background: linear-gradient(135deg, #f472b6, #38bdf8);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  animation: brandSpark 3.5s ease-in-out infinite;
}
@keyframes brandSpark {
  0%, 100% { opacity: 0.55; transform: scale(0.9) rotate(0deg); }
  50% { opacity: 1; transform: scale(1.15) rotate(20deg); }
}
@media (prefers-reduced-motion: reduce) { .brand-spark { animation: none; } }

.m-link {
  display: block;
  padding: 0.6rem 0.75rem;
  border-radius: 0.85rem;
  font-size: 14px;
  color: var(--fg-2);
  background: transparent;
  border: none;
  cursor: pointer;
  transition: color 0.15s ease, background 0.15s ease;
}
.m-link:hover { color: var(--fg); background: var(--hover); }
.m-link.m-on {
  color: #a21caf;
  font-weight: 700;
  background: linear-gradient(135deg, rgb(244 114 182 / 0.16), rgb(129 140 248 / 0.16));
}
html.dark .m-link.m-on { color: #f5d0fe; background: linear-gradient(135deg, rgb(244 114 182 / 0.2), rgb(129 140 248 / 0.2)); }

.round-btn {
  width: 2.125rem;
  height: 2.125rem;
  border-radius: 999px;
  display: grid;
  place-items: center;
  color: var(--fg-3);
  background: var(--surface);
  box-shadow: inset 0 0 0 1px var(--hairline);
  transition: color 0.15s ease, background 0.15s ease;
}
.round-btn:hover { color: var(--fg); background: var(--hover); }

/* mobile-only: .round-btn's display:grid outranks Tailwind's md:hidden, so
   the breakpoint lives here instead */
@media (min-width: 768px) { .hamburger { display: none; } }

.solid-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.5rem 1.1rem;
  border-radius: 999px;
  font-size: 13.5px;
  font-weight: 700;
  color: #fff;
  background: linear-gradient(135deg, #f472b6, #a855f7 55%, #38bdf8);
  border: none;
  cursor: pointer;
  box-shadow: 0 12px 26px -14px rgb(168 85 247 / 0.75);
  transition: transform 0.15s ease, filter 0.15s ease, box-shadow 0.15s ease;
}
.solid-btn:hover { filter: brightness(1.06); transform: translateY(-1px); box-shadow: 0 16px 30px -14px rgb(168 85 247 / 0.85); }

.name-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  max-width: 10rem;
  height: 2.125rem;
  padding: 0 0.7rem 0 0.85rem;
  border-radius: 999px;
  border: none;
  cursor: pointer;
  font-size: 13px;
  font-weight: 600;
  color: var(--fg);
  background: var(--surface);
  box-shadow: inset 0 0 0 1px var(--hairline);
  transition: background 0.15s ease, box-shadow 0.15s ease;
}
.name-btn:hover {
  background: linear-gradient(135deg, rgb(244 114 182 / 0.14), rgb(129 140 248 / 0.14));
  box-shadow: inset 0 0 0 1px rgb(217 70 239 / 0.3);
}
.name-txt {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.menu {
  position: absolute;
  right: 0;
  top: 2.75rem;
  width: 11.5rem;
  padding: 0.4rem;
  border-radius: 0.9rem;
  background: var(--menu-bg);
  box-shadow: inset 0 0 0 1px var(--hairline), 0 20px 44px rgb(0 0 0 / 0.24);
  z-index: 50;
}
.menu-item {
  display: block;
  padding: 0.5rem 0.65rem;
  border-radius: 0.55rem;
  font-size: 13px;
  color: var(--fg-2);
  background: transparent;
  border: none;
  cursor: pointer;
}
.menu-item:hover { background: var(--hover); color: var(--fg); }
.menu-sep { height: 1px; background: var(--hairline); margin: 0.35rem 0.25rem; }

.fade-enter-active, .fade-leave-active { transition: opacity 0.2s ease, transform 0.2s ease; }
.fade-enter-from { opacity: 0; transform: translateY(8px); }
.fade-leave-to { opacity: 0; }
</style>
