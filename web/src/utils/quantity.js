/**
 * Kubernetes resource.Quantity 可读化（全站共用）
 * CPU：n/u/m/核 → m 或「x 核」
 * Memory：Ki/Mi/Gi… → 自动进位
 */

export function parseQuantity(q, { asCPU = false } = {}) {
  if (q == null || q === '' || q === '-') return null
  if (typeof q === 'object') {
    if (typeof q.number === 'number') return q.number
    if (q.Format != null && q.number != null) return Number(q.number)
    q = String(q.string || q.String || q)
  }
  const s = String(q).trim()
  if (!s) return null

  if (asCPU) {
    const m = s.match(/^([0-9]+(?:\.[0-9]+)?)(n|u|m)?$/)
    if (!m) return null
    const n = parseFloat(m[1])
    const unit = m[2] || ''
    if (unit === 'n') return n / 1e9
    if (unit === 'u') return n / 1e6
    if (unit === 'm') return n / 1e3
    return n
  }

  const m = s.match(/^([0-9]+(?:\.[0-9]+)?)(Ki|Mi|Gi|Ti|Pi|Ei|k|K|M|G|T|P|E)?$/)
  if (!m) return null
  const n = parseFloat(m[1])
  const unit = m[2] || ''
  const binary = {
    Ki: 1024,
    Mi: 1024 ** 2,
    Gi: 1024 ** 3,
    Ti: 1024 ** 4,
    Pi: 1024 ** 5,
    Ei: 1024 ** 6
  }
  const decimal = {
    k: 1e3,
    K: 1e3,
    M: 1e6,
    G: 1e9,
    T: 1e12,
    P: 1e15,
    E: 1e18
  }
  if (binary[unit]) return n * binary[unit]
  if (decimal[unit]) return n * decimal[unit]
  return n
}

/** @returns {string} 如 990m、4 核、1.50 核 */
export function fmtCPU(q) {
  const cores = parseQuantity(q, { asCPU: true })
  if (cores == null || Number.isNaN(cores)) return '-'
  if (cores < 0.001) return `${(cores * 1e6).toFixed(0)}µ`
  if (cores < 1) return `${Math.round(cores * 1000)}m`
  if (cores < 10) return `${cores.toFixed(2)} 核`
  return `${cores.toFixed(1)} 核`
}

/** @returns {string} 如 63.2 Mi、15.60 Gi */
export function fmtMemory(q) {
  const bytes = parseQuantity(q, { asCPU: false })
  if (bytes == null || Number.isNaN(bytes)) return '-'
  const units = ['B', 'Ki', 'Mi', 'Gi', 'Ti', 'Pi']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  const digits = v >= 100 ? 0 : v >= 10 ? 1 : 2
  return `${v.toFixed(digits)} ${units[i]}`
}

export function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return String(t)
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/** metrics-server NodeMetrics / Pod container usage（用量可为 0，不能用 ||） */
export function metricCPU(m) {
  const u = m?.usage ?? m?.Usage
  if (!u) return null
  const v = u.cpu ?? u.Cpu
  return v == null || v === '' ? null : v
}
export function metricMem(m) {
  const u = m?.usage ?? m?.Usage
  if (!u) return null
  const v = u.memory ?? u.Memory
  return v == null || v === '' ? null : v
}

/** 按节点名把 metrics 用量合并进节点列表（以节点列表为准） */
export function mergeNodeUsage(nodes, metrics) {
  const byName = new Map()
  for (const m of metrics || []) {
    const name = m?.metadata?.name || m?.name
    if (!name) continue
    byName.set(name, m)
  }
  return (Array.isArray(nodes) ? nodes : []).map((n) => {
    if (!n || typeof n !== 'object') return n
    const m = byName.get(n.name)
    return {
      ...n,
      usedCPU: m ? metricCPU(m) : null,
      usedMemory: m ? metricMem(m) : null,
      metricsTime: m ? (m.timestamp ?? m.metadata?.creationTimestamp ?? null) : null
    }
  })
}
