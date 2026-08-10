import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import './style.css'
import { auth, refreshMe, openLogin } from './auth'
import { site, loadSite } from './site'

import PublicLayout from './layouts/PublicLayout.vue'

// Route-level code splitting: a guest landing on the home page must not
// download the whole admin console. Only the public shell + HomeView are in
// the entry chunk; every other view (and the admin shell) is fetched on demand.
const AdminLayout = () => import('./layouts/AdminLayout.vue')

import HomeView from './views/HomeView.vue'
const PlaygroundView = () => import('./views/PlaygroundView.vue')
const UserLogsView = () => import('./views/UserLogsView.vue')
const UserLogsTableView = () => import('./views/UserLogsTableView.vue')
const SettingsView = () => import('./views/SettingsView.vue')
const OrdersView = () => import('./views/OrdersView.vue')
const AdminOrdersView = () => import('./views/AdminOrdersView.vue')
const InviteView = () => import('./views/InviteView.vue')
const DocsView = () => import('./views/DocsView.vue')
const AboutView = () => import('./views/AboutView.vue')
const OverviewView = () => import('./views/OverviewView.vue')
const ModelsView = () => import('./views/ModelsView.vue')
const AccountsView = () => import('./views/AccountsView.vue')
const UsersView = () => import('./views/UsersView.vue')
const ConcurrencyView = () => import('./views/ConcurrencyView.vue')
const BannedWordsView = () => import('./views/BannedWordsView.vue')
const BannedWordHitsView = () => import('./views/BannedWordHitsView.vue')
const CdksView = () => import('./views/CdksView.vue')
const InvitesAdminView = () => import('./views/InvitesAdminView.vue')
const ImagesView = () => import('./views/ImagesView.vue')
const LogsView = () => import('./views/LogsView.vue')
const ConfigView = () => import('./views/ConfigView.vue')
const ShowcaseView = () => import('./views/ShowcaseView.vue')

const routes = [
  {
    path: '/',
    component: PublicLayout,
    children: [
      { path: '', component: HomeView, meta: { label: '首页' } },
      { path: 'user', component: PlaygroundView, meta: { label: '画图' } },
      { path: 'logs', component: UserLogsView, meta: { label: '图片' } },
      { path: 'mylogs', component: UserLogsTableView, meta: { label: '日志' } },
      { path: 'invite', component: InviteView, meta: { label: '邀请' } },
      { path: 'docs', component: DocsView, meta: { label: '文档' } },
      { path: 'about', component: AboutView, meta: { label: '关于' } },
      { path: 'orders', component: OrdersView, meta: { label: '订单' } },
      { path: 'settings', component: SettingsView, meta: { label: '设置' } },
    ],
  },
  {
    path: '/admin',
    component: AdminLayout,
    children: [
      { path: '', redirect: '/admin/overview' },
      { path: 'overview', component: OverviewView, meta: { label: '概览' } },
      { path: 'models',   component: ModelsView,   meta: { label: '模型管理' } },
      { path: 'accounts', component: AccountsView, meta: { label: '账号管理' } },
      { path: 'users',    component: UsersView,    meta: { label: '用户管理' } },
      { path: 'concurrency', component: ConcurrencyView, meta: { label: '并发分组' } },
      { path: 'banned-words', component: BannedWordsView, meta: { label: '违禁词列表' } },
      { path: 'banned-word-hits', component: BannedWordHitsView, meta: { label: '违禁词触发列表' } },
      { path: 'orders', component: AdminOrdersView, meta: { label: '订单管理' } },
      { path: 'cdks',     component: CdksView,     meta: { label: '兑换码管理' } },
      { path: 'invites',  component: InvitesAdminView, meta: { label: '邀请日志' } },
      { path: 'images',   component: ImagesView,   meta: { label: '作品管理' } },
      { path: 'showcase', component: ShowcaseView, meta: { label: '首页内容' } },
      { path: 'logs',     component: LogsView,     meta: { label: '日志管理' } },
      { path: 'config',   component: ConfigView,   meta: { label: '系统配置' } },
    ],
  },
  // legacy redirects
  { path: '/playground',   redirect: '/user' },
  { path: '/home',         redirect: '/' },
  { path: '/overview',     redirect: '/admin/overview' },
  { path: '/models',       redirect: '/admin/models' },
  { path: '/video-models', redirect: '/admin/models' },
  { path: '/accounts',     redirect: '/admin/accounts' },
  // NOTE: /images is the generated-artifact path (served by the backend), so the
  // old "/images → /admin/images" shortcut is gone. Use /files for that shortcut.
  { path: '/files',        redirect: '/admin/images' },
  { path: '/config',       redirect: '/admin/config' },
  { path: '/refresh',      redirect: '/admin/overview' },
  { path: '/test',         redirect: '/user' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// Pages that require a login. The home page (/) stays public; everything a
// signed-in user touches (画图/记录/设置) and the whole admin area is gated.
const PROTECTED = ['/user', '/logs', '/invite', '/settings', '/orders']
function isProtected(path) {
  return path.startsWith('/admin') || PROTECTED.includes(path)
}

// Guard: validate the stored token against /me once (auth.ready), then trust
// state. Unauthed visits to a protected page stay on home and pop the login
// modal (no separate login page); the modal navigates to `intent` on success.
router.beforeEach(async (to) => {
  if (!isProtected(to.path)) return true
  if (!auth.ready) await refreshMe()
  if (!auth.token || !auth.user) {
    openLogin(to.fullPath)
    return to.path === '/' ? false : '/'
  }
  if (to.path.startsWith('/admin') && auth.user.role !== 'admin') {
    return '/user'   // logged in but not an admin -> user side
  }
  return true
})

// Keep the browser tab title in sync with the current route's label and the
// admin-editable site title. Admin routes get an extra prefix so the two
// sides are distinguishable at a glance.
function applyTitle(route) {
  const label = route.meta?.label || ''
  const scope = route.path.startsWith('/admin') ? 'Admin · ' : ''
  const brand = site.title || 'Vivid'
  document.title = label ? `${brand} • ${scope}${label}` : brand
}
router.afterEach(applyTitle)
// Re-apply once the admin-set title resolves (loadSite is async — the first
// navigation uses the default, then this catches up).
loadSite().then(() => applyTitle(router.currentRoute.value))

createApp(App).use(router).mount('#app')

// Globally strip the browser's video extras (画中画/PiP、下载、投屏、播放速率/增强菜单)
// from EVERY <video> — applied to existing nodes + anything Vue renders later.
// (Edge's image "视觉搜索" is handled by rendering thumbnails as CSS background
//  images instead of <img>, since there's no attribute to disable it.)
function hardenVideo(v) {
  try {
    v.disablePictureInPicture = true
    v.disableRemotePlayback = true
    v.setAttribute('controlslist', 'nodownload noremoteplayback noplaybackrate')
  } catch { /* ignore */ }
}
new MutationObserver((muts) => {
  for (const m of muts) {
    for (const n of m.addedNodes) {
      if (n.nodeType !== 1) continue
      if (n.tagName === 'VIDEO') hardenVideo(n)
      else n.querySelectorAll && n.querySelectorAll('video').forEach(hardenVideo)
    }
  }
}).observe(document.documentElement, { childList: true, subtree: true })
document.querySelectorAll('video').forEach(hardenVideo)
