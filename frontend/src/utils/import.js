// Smart parsing of pasted credentials (Adobe cookies / ChatGPT JWTs),
// ported verbatim from admin.html so import behaviour is unchanged.

export function looksLikeJwt(s) {
  s = (s || '').replace(/^Bearer\s+/i, '').trim()
  const parts = s.split('.')
  if (parts.length !== 3) return false
  return parts.every((p) => /^[A-Za-z0-9_-]+$/.test(p) && p.length > 4)
}

function decodeJwtPayload(s) {
  try {
    let p = (s || '').replace(/^Bearer\s+/i, '').trim().split('.')[1]
    if (!p) return null
    p = p.replace(/-/g, '+').replace(/_/g, '/')
    p += '='.repeat((4 - (p.length % 4)) % 4)
    return JSON.parse(atob(p))
  } catch (_) { return null }
}

// Runway JWTs carry a top-level numeric `id` plus an `sso` claim and, crucially,
// no OpenAI (https://api.openai.com/*) claims — that's what distinguishes them
// from a ChatGPT JWT, which is otherwise also an opaque three-part token.
export function looksLikeRunwayJwt(s) {
  const claims = decodeJwtPayload(s)
  if (!claims || typeof claims !== 'object') return false
  if (Object.keys(claims).some((k) => k.startsWith('https://api.openai.com/'))) return false
  return 'sso' in claims && claims.id != null
}

// Grok website "sso" JWTs carry ONLY a session_id claim (no openai claims, no
// runway id/sso) — that's what tells them apart from a ChatGPT/Runway JWT.
export function looksLikeGrokJwt(s) {
  const claims = decodeJwtPayload(s)
  if (!claims || typeof claims !== 'object') return false
  if (Object.keys(claims).some((k) => k.startsWith('https://api.openai.com/'))) return false
  if ('sso' in claims || claims.id != null) return false
  return 'session_id' in claims
}

// Leonardo cookies carry the better-auth session cookie — that's what tells them
// apart from an Adobe cookie (both are otherwise opaque cookie strings).
export function looksLikeLeonardoCookie(s) {
  return /better-auth\.session_token/.test(s || '') || /better-auth\.session_data/.test(s || '')
}

// Krea cookies carry the Supabase auth cookie.
export function looksLikeKreaCookie(s) {
  return /sb-superb-auth-token/.test(s || '')
}

// Creative Fabrica session cookies carry the cfauth_* family (cfauth_uid /
// cfauth_sig / cfauth_utp) plus the WordPress SSO cookie — those markers tell
// them apart from an Adobe/Krea/Leonardo cookie (all otherwise opaque strings).
export function looksLikeCreativeFabricaCookie(s) {
  return /cfauth_/.test(s || '') || /cfAmpAnonymousId/.test(s || '') || /wordpress_logged_in_/.test(s || '')
}

// An Imagine.art credential is a JSON object { token, refreshToken } (both JWTs).
function isImagineObj(o) {
  return !!o && typeof o === 'object' &&
    typeof o.token === 'string' && looksLikeJwt(o.token) &&
    typeof o.refreshToken === 'string' && looksLikeJwt(o.refreshToken)
}

// String form (a pasted JSON object on a line).
export function looksLikeImagineToken(s) {
  try { return isImagineObj(JSON.parse(s)) } catch (_) { return false }
}

// Classify an opaque credential string by its distinctive shape. Imagine is
// JSON-shaped, so it must be checked before the cookie heuristics.
function cookieType(v) {
  if (looksLikeImagineToken(v)) return 'imagine'
  if (looksLikeKreaCookie(v)) return 'krea'
  if (looksLikeLeonardoCookie(v)) return 'leonardo'
  if (looksLikeCreativeFabricaCookie(v)) return 'creativefabrica'
  return 'adobe'
}

function cookieFromAny(item) {
  if (typeof item === 'string') return item.trim()
  if (item && typeof item === 'object') {
    if (typeof item.cookie === 'string') return item.cookie.trim()
    if (typeof item.value === 'string' && !('name' in item)) return item.value.trim()
    if (Array.isArray(item.cookies)) {
      return item.cookies.filter((c) => c && c.name).map((c) => `${c.name}=${c.value}`).join('; ')
    }
  }
  return ''
}

/** Returns a list of { type: 'adobe' | 'openai' | 'runway' | 'leonardo', value }. */
export function parseImportInput(text) {
  text = (text || '').trim()
  if (!text) return []
  // Try JSON first.
  try {
    const j = JSON.parse(text)
    if (Array.isArray(j) && j.length > 0) {
      // Chrome cookie export: array of {name,value} → one cookie account.
      if (j.every((it) => it && typeof it === 'object' && 'name' in it && 'value' in it)) {
        const joined = j.filter((c) => c && c.name).map((c) => `${c.name}=${c.value}`).join('; ')
        return joined ? [{ type: cookieType(joined), value: joined }] : []
      }
      // Otherwise treat as multiple accounts. An Imagine credential is itself a
      // JSON object {token,refreshToken} — keep it as its JSON string value.
      return j.map((it) => {
        if (isImagineObj(it)) return { type: 'imagine', value: JSON.stringify(it) }
        const v = cookieFromAny(it)
        return { type: cookieType(v), value: v }
      }).filter((x) => x.value)
    }
    if (j && typeof j === 'object') {
      if (isImagineObj(j)) return [{ type: 'imagine', value: JSON.stringify(j) }]
      const v = cookieFromAny(j)
      return v ? [{ type: cookieType(v), value: v }] : []
    }
  } catch (_) { /* not JSON */ }
  // Not JSON → split per line, identify each. A JWT is either a Runway token
  // (top-level id+sso, no openai claims) or a ChatGPT token; anything else is
  // treated as an Adobe cookie string.
  const lines = text.split(/\r?\n/).map((s) => s.trim()).filter(Boolean)
  return lines.map((line) => {
    // Accept a bare JWT or one with a leading `sso=` (grok cookie value form).
    const stripped = line.replace(/^Bearer\s+/i, '').replace(/^sso=/, '')
    if (looksLikeJwt(stripped)) {
      const value = stripped
      return looksLikeRunwayJwt(value)
        ? { type: 'runway', value }
        : looksLikeGrokJwt(value)
          ? { type: 'grok', value }
          : { type: 'openai', value }
    }
    return { type: cookieType(line), value: line }
  })
}
