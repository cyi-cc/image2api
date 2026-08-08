// Credit display helpers. Credits and model prices are stored server-side as
// 积分 (points) — the single unit across the whole app. Prices can be fractional
// (0.11 积分/s), so keep two decimals instead of rounding them away. These
// helpers only format for display; they do NOT convert units.
/** Round to a 积分 value with at most 2 decimals. */
export function points(value) {
  return Math.round(Number(value || 0) * 100) / 100
}

/** "<n> 积分" label with thousands separators. */
export function pointsLabel(value) {
  return points(value).toLocaleString('en-US', { maximumFractionDigits: 2 }) + ' 积分'
}
