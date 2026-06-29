// Robust clipboard copy. navigator.clipboard only exists in a SECURE context
// (https or localhost) — on http://<ip> it's undefined, so the modern path
// throws and we fall back to the legacy execCommand('copy') via a hidden
// textarea, which works without a secure context. Returns true on success.
export async function copyText(text) {
  const s = text == null ? '' : String(text)
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(s)
      return true
    }
  } catch { /* fall through to the legacy path */ }
  try {
    const ta = document.createElement('textarea')
    ta.value = s
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-9999px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
