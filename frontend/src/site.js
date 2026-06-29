// Site-wide branding (currently just the wordmark / tab title) backed by the
// admin-editable config at /admin/api/site. One reactive object so every
// component that wants to display "<title>" stays in sync the moment the
// admin saves a change.
import { reactive } from 'vue'

const BASE = import.meta.env.VITE_API_BASE || ''

export const site = reactive({
  title: 'Vivid',
  logo: '',
  subtitle: '',
  // Defaults so the 关于 page is never blank even if /site hasn't loaded (or a
  // cache serves an older payload without `contact`). The backend value, once
  // fetched, overrides these.
  contact: {
    qq: '1114639355',
    qq_link: 'https://qm.qq.com/q/ItgCcNA7ac',
    qq_group: '1106849765',
    qq_group_link: 'https://qm.qq.com/q/976LeMFoHu',
    email: 'vividairun@gmail.com',
    shop: 'https://pay.ldxp.cn/shop/chiyi',
  },
  ready: false,
})

export async function loadSite() {
  try {
    const r = await fetch(`${BASE}/admin/api/site`)
    if (r.ok) {
      const data = await r.json()
      if (data.title) site.title = String(data.title)
      site.logo = data.logo ? String(data.logo) : ''
      site.subtitle = data.subtitle ? String(data.subtitle) : ''
      if (data.contact) site.contact = { ...site.contact, ...data.contact }
    }
  } catch { /* offline — keep the default. */ }
  site.ready = true
}
