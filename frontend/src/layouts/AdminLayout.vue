<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import Icon from '../components/Icon.vue'
import Logo from '../components/Logo.vue'
import { site } from '../site'
import { isDark, toggleTheme } from '../theme'

const route = useRoute()

const tabs = [
  { label: '概览',    to: '/admin/overview', icon: 'overview' },
  { label: '模型管理', to: '/admin/models',   icon: 'models' },
  { label: '账号管理', to: '/admin/accounts', icon: 'plug' },
  { label: '用户管理', to: '/admin/users',    icon: 'accounts' },
  { label: '兑换码',   to: '/admin/cdks',     icon: 'spark' },
  { label: '邀请日志', to: '/admin/invites',  icon: 'accounts' },
  { label: '图片管理', to: '/admin/images',   icon: 'files' },
  { label: '首页内容', to: '/admin/showcase', icon: 'spark' },
  { label: '日志',    to: '/admin/logs',     icon: 'log' },
  { label: '配置',    to: '/admin/config',   icon: 'config' },
]

const currentLabel = computed(() => route.meta?.label || '')
</script>

<template>
  <div class="theme-x h-screen flex bg-[var(--app-bg)] text-[color:var(--fg-2)] selection:bg-violet-400/30 overflow-hidden">
    <!-- ===== Sidebar ===== -->
    <aside class="w-60 shrink-0 border-r border-[color:var(--hairline)] bg-[var(--surface)] backdrop-blur-md flex flex-col">
      <router-link to="/" class="h-16 flex items-center gap-2.5 px-5 border-b border-[color:var(--hairline)] group">
        <img v-if="site.logo" :src="site.logo" :alt="site.title" class="w-8 h-8 rounded-[10px] object-contain shadow-lg shadow-violet-500/20 ring-1 ring-white/10" />
        <Logo v-else :size="32" class="rounded-[10px] shadow-lg shadow-violet-500/20 ring-1 ring-white/10" />
        <div class="leading-tight min-w-0">
          <div class="text-sm font-semibold truncate tracking-tight text-[color:var(--fg)]">{{ site.title }}</div>
          <div class="text-[11px] text-[color:var(--fg-3)] truncate">Admin</div>
        </div>
      </router-link>

      <nav class="flex-1 px-3 py-4 space-y-1">
        <router-link
          v-for="t in tabs" :key="t.to" :to="t.to"
          class="admin-link group"
          active-class="active">
          <span class="absolute left-0 top-1/2 -translate-y-1/2 w-[3px] h-5 rounded-r-full opacity-0 transition-opacity"
                style="background: linear-gradient(180deg, #f0abfc, #a78bfa)"></span>
          <Icon :name="t.icon" class="w-4 h-4 shrink-0 opacity-70 group-hover:opacity-100 transition-opacity" />
          <span class="text-sm">{{ t.label }}</span>
        </router-link>
      </nav>

      <div class="p-3 border-t border-[color:var(--hairline)] space-y-1">
        <button type="button" @click="toggleTheme"
                class="w-full flex items-center gap-2.5 rounded-lg px-3 py-2 text-xs text-[color:var(--fg-2)] hover:bg-[var(--hover)] hover:text-[color:var(--fg)] transition-colors">
          <svg v-if="isDark" class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>
          <svg v-else class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
          {{ isDark ? '亮色模式' : '暗色模式' }}
        </button>
        <router-link to="/user"
                     class="flex items-center gap-2.5 rounded-lg px-3 py-2 text-xs text-[color:var(--fg-2)] hover:bg-[var(--hover)] hover:text-[color:var(--fg)] transition-colors">
          <Icon name="spark" class="w-3.5 h-3.5" /> 用户端
          <Icon name="open" class="w-3 h-3 ml-auto" />
        </router-link>
      </div>
    </aside>

    <!-- ===== Main ===== -->
    <div class="flex-1 min-w-0 flex flex-col relative">
      <!-- soft background mesh, mirrors the public shell -->
      <div aria-hidden="true" class="pointer-events-none absolute inset-0 overflow-hidden">
        <div class="absolute -top-32 left-1/3 w-[40rem] h-[40rem] rounded-full opacity-[0.12]"
             style="background: radial-gradient(circle, #a855f7, transparent 60%); filter: blur(110px)"></div>
        <div class="absolute top-1/2 -right-40 w-[36rem] h-[36rem] rounded-full opacity-[0.10]"
             style="background: radial-gradient(circle, #06b6d4, transparent 60%); filter: blur(110px)"></div>
        <div class="absolute bottom-0 left-0 w-[32rem] h-[32rem] rounded-full opacity-[0.08]"
             style="background: radial-gradient(circle, #f43f5e, transparent 60%); filter: blur(110px)"></div>
      </div>

      <header class="relative z-10 h-14 shrink-0 border-b border-[color:var(--hairline)] bg-[var(--app-bg)]/70 backdrop-blur-md flex items-center px-8">
        <div class="text-[10px] uppercase tracking-[0.25em] text-[color:var(--fg-3)] font-medium mr-3">Admin</div>
        <div class="text-[color:var(--fg-faint)] mr-3">/</div>
        <h1 class="text-sm font-semibold tracking-tight text-[color:var(--fg)]">{{ currentLabel }}</h1>
      </header>

      <main :class="['theme-text flex-1 overflow-y-auto overscroll-y-none relative z-10', { 'public-dark': isDark }]">
        <div class="px-8 py-7">
          <router-view v-slot="{ Component }">
            <transition name="fade" mode="out-in">
              <component :is="Component" />
            </transition>
          </router-view>
        </div>
      </main>
    </div>
  </div>
</template>

<style scoped>
.admin-link {
  position: relative;
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.55rem 0.875rem;
  border-radius: 0.625rem;
  color: var(--fg-2);
  font-weight: 500;
  transition: background 0.15s ease, color 0.15s ease;
}
.admin-link:hover { background: var(--hover); color: var(--fg); }
.admin-link.active { color: var(--fg); background: var(--hover); }
.admin-link.active > span:first-child { opacity: 1; }

.fade-enter-active, .fade-leave-active { transition: opacity 0.15s ease, transform 0.15s ease; }
.fade-enter-from { opacity: 0; transform: translateY(4px); }
.fade-leave-to { opacity: 0; }
</style>
