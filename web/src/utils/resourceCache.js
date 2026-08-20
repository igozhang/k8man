/**
 * 读接口的短时缓存：先展示上次结果，避免打开/刷新时空档；
 * 在 TTL 内视为新鲜，跳过立刻打 API。写操作后需主动 invalidate。
 *
 * 内存 Map 服务 SPA 内切页；sessionStorage 撑住同标签 F5。
 * 选用集群用 TTL，非选用用 TTL_IDLE（刷新不必勤）。
 */

const STORE_PREFIX = 'k8man.rc.v1.'
const INDEX_KEY = 'k8man.rc.v1.__keys'
const PREF_PREFIX = 'k8man.pref.v1.'
const MAX_ENTRIES = 96
const MAX_ITEM_CHARS = 400_000

/** 当前选用集群：命中则不再请求。过期仍可展示，并在后台刷新。 */
export const TTL = {
  clusters: 30_000,
  namespaces: 90_000,
  pods: 25_000,
  controllers: 30_000,
  services: 45_000,
  configmaps: 45_000,
  nodes: 30_000,
  nodeUsage: 20_000,
  podMetrics: 15_000,
  overview: 45_000,
  diagnose: 90_000,
  inspections: 60_000,
  docs: 60_000,
  ui: 5 * 60_000
}

/** 非选用集群：只保证卡片/切回时有数，刷新窗口拉长。 */
export const TTL_IDLE = {
  namespaces: 10 * 60_000,
  pods: 5 * 60_000,
  controllers: 5 * 60_000,
  services: 8 * 60_000,
  configmaps: 8 * 60_000,
  nodes: 5 * 60_000,
  nodeUsage: 5 * 60_000,
  podMetrics: 5 * 60_000,
  overview: 10 * 60_000,
  diagnose: 15 * 60_000
}

export function ttlOf(kind, selected = true) {
  if (selected !== false) return TTL[kind] ?? 30_000
  return TTL_IDLE[kind] ?? (TTL[kind] ?? 30_000) * 8
}

const mem = new Map()
/** 同 key 并发读合并成一次网络请求 */
const inflight = new Map()
/** invalidate 后丢弃仍在飞的旧请求，避免把过期结果写回 */
const gens = new Map()

function genOf(key) {
  return gens.get(key) || 0
}

function bumpGen(key) {
  const g = genOf(key) + 1
  gens.set(key, g)
  inflight.delete(key)
  return g
}

export function cacheKey(kind, clusterId, extra) {
  const id = String(clusterId ?? '')
  if (extra == null || extra === '') return `${kind}::${id}`
  return `${kind}::${id}::${extra}`
}

function clone(v) {
  if (v == null) return v
  try {
    return JSON.parse(JSON.stringify(v))
  } catch {
    return v
  }
}

function readIndex() {
  try {
    const raw = sessionStorage.getItem(INDEX_KEY)
    const list = raw ? JSON.parse(raw) : []
    return Array.isArray(list) ? list : []
  } catch {
    return []
  }
}

function writeIndex(keys) {
  try {
    sessionStorage.setItem(INDEX_KEY, JSON.stringify(keys.slice(-MAX_ENTRIES)))
  } catch {
    /* quota / 隐私模式 */
  }
}

function persist(key, entry) {
  try {
    const json = JSON.stringify(entry)
    if (json.length > MAX_ITEM_CHARS) return
    sessionStorage.setItem(STORE_PREFIX + key, json)
    const idx = readIndex().filter((k) => k !== key)
    idx.push(key)
    while (idx.length > MAX_ENTRIES) {
      const drop = idx.shift()
      if (drop) {
        sessionStorage.removeItem(STORE_PREFIX + drop)
        mem.delete(drop)
      }
    }
    writeIndex(idx)
  } catch {
    evictOldest(8)
  }
}

function evictOldest(n) {
  const idx = readIndex()
  for (let i = 0; i < n && idx.length; i++) {
    const drop = idx.shift()
    if (!drop) break
    try {
      sessionStorage.removeItem(STORE_PREFIX + drop)
    } catch {
      /* ignore */
    }
    mem.delete(drop)
  }
  writeIndex(idx)
}

function readSession(key) {
  try {
    const raw = sessionStorage.getItem(STORE_PREFIX + key)
    if (!raw) return null
    const entry = JSON.parse(raw)
    if (!entry || typeof entry.at !== 'number') return null
    return entry
  } catch {
    return null
  }
}

function peekEntry(key) {
  if (mem.has(key)) return mem.get(key)
  const entry = readSession(key)
  if (entry) mem.set(key, entry)
  return entry || null
}

export function cacheGet(key) {
  const entry = peekEntry(key)
  return entry ? { data: clone(entry.data), at: entry.at } : null
}

export function cacheSet(key, data) {
  const entry = { data: clone(data), at: Date.now() }
  mem.set(key, entry)
  persist(key, entry)
  return entry.at
}

export function cacheAge(key) {
  const hit = peekEntry(key)
  return hit ? Date.now() - hit.at : Infinity
}

export function cacheFresh(key, ttl) {
  return cacheAge(key) < ttl
}

/**
 * 新鲜则立刻返回缓存；过期则打 API（同 key 合并 in-flight）。
 * 调用方应先 cacheGet 展示旧数据，再 await 本函数做后台更新。
 */
export function cacheLoad(key, ttl, fetcher, { force = false } = {}) {
  if (!force && cacheFresh(key, ttl)) {
    const hit = cacheGet(key)
    return Promise.resolve(hit ? hit.data : undefined)
  }
  const existing = inflight.get(key)
  if (existing && !force) return existing.then((data) => clone(data))
  // force 作废同 key 的在途请求，避免旧结果后到覆盖
  const gen = force ? bumpGen(key) : genOf(key)
  const p = Promise.resolve()
    .then(fetcher)
    .then((data) => {
      if (genOf(key) === gen) cacheSet(key, data)
      return data
    })
    .finally(() => {
      if (inflight.get(key) === p) inflight.delete(key)
    })
  inflight.set(key, p)
  return p.then((data) => clone(data))
}

export function cacheInvalidate(key) {
  bumpGen(key)
  mem.delete(key)
  try {
    sessionStorage.removeItem(STORE_PREFIX + key)
  } catch {
    /* ignore */
  }
  writeIndex(readIndex().filter((k) => k !== key))
}

export function cacheInvalidateWhere(pred) {
  const keys = new Set([...mem.keys(), ...readIndex()])
  for (const k of keys) {
    if (pred(k)) cacheInvalidate(k)
  }
}

/** 某集群某命名空间的列表，以及依赖它们的概览/诊断 */
export function invalidateNsLists(clusterId, ns) {
  const id = String(clusterId || '')
  const extra = String(ns || '')
  if (!id) return
  for (const kind of ['pods', 'controllers', 'services', 'configmaps', 'podMetrics']) {
    cacheInvalidate(cacheKey(kind, id, extra))
  }
  cacheInvalidate(cacheKey('overview', id))
  cacheInvalidate(cacheKey('diagnose', id))
}

export function invalidateClusterLists(clusterId) {
  const id = String(clusterId || '')
  if (!id) return
  cacheInvalidateWhere((k) => k.split('::')[1] === id)
  cacheInvalidate(cacheKey('inspections', 'local'))
}

export function invalidateDocs(docId) {
  if (docId) cacheInvalidate(cacheKey('doc', 'local', String(docId)))
  cacheInvalidateWhere((k) => k.startsWith('docs::'))
}

function readPrefStore(name) {
  try {
    const raw = sessionStorage.getItem(PREF_PREFIX + name)
    const obj = raw ? JSON.parse(raw) : {}
    return obj && typeof obj === 'object' ? obj : {}
  } catch {
    return {}
  }
}

export function getViewPref(name, clusterId) {
  const all = readPrefStore(name)
  const hit = all[String(clusterId || '')]
  return hit && typeof hit === 'object' ? hit : null
}

export function setViewPref(name, clusterId, value) {
  const id = String(clusterId || '')
  if (!id) return
  const all = readPrefStore(name)
  all[id] = value
  try {
    sessionStorage.setItem(PREF_PREFIX + name, JSON.stringify(all))
  } catch {
    /* ignore */
  }
}
