<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import {
  TTL,
  cacheFresh,
  cacheGet,
  cacheInvalidate,
  cacheKey,
  cacheLoad,
  cacheSet,
  notifyInspectionsChanged,
  getViewPref,
  setViewPref,
  ttlOf
} from '../utils/resourceCache'
import { permTypeLabel, clusterStatusLabel, clusterStatusClass, frontProxyLabel, frontProxyClass, severityLabel, issueReasonDisplay } from '../utils/labels'
import { formatIssueMessage, issueMessageSuffix, sanitizeNFSExportRoot } from '../utils/inspect'
import { canWrite } from '../stores/opsAuth'

/** 选用集群：巡检缓存超过此时长则自动实时刷一次 Pods */
const PODS_INSPECT_STALE_MS = 10 * 60 * 1000
const INSPECT_KEY = cacheKey('inspections', 'local')

const router = useRouter()
const { clusters, currentClusterId, setCurrentClusterId, refreshClusters } = useClusterStore()

/** 非生产环境标志：test / uat / dev / pre（独立片段，避免误伤 prod） */
const NON_PROD_ENV_RE = /(^|[^a-z0-9])(test|uat|dev|pre)([^a-z0-9]|$)/i

function isNonProdName(name) {
  return NON_PROD_ENV_RE.test(String(name || ''))
}

const sortedClusters = computed(() => {
  const selected = currentClusterId.value
  return [...clusters.value].sort((a, b) => {
    const aSel = a.id === selected ? 0 : 1
    const bSel = b.id === selected ? 0 : 1
    if (aSel !== bSel) return aSel - bSel
    const aNon = isNonProdName(a.name) ? 1 : 0
    const bNon = isNonProdName(b.name) ? 1 : 0
    if (aNon !== bNon) return aNon - bNon
    return String(a.name || '').localeCompare(String(b.name || ''), 'zh')
  })
})

const emptyStats = () => ({
  namespaces: 0,
  nodes: 0,
  pods: 0,
  badPods: 0,
  largePods: null,
  largePodSkipped: '',
  nfsUsagePercent: null,
  nfsStorageClass: '',
  nfsServer: '',
  nfsPath: '',
  nfsSource: '',
  nfsSkipped: '',
  podsSource: '', // inspect | sample
  inspectAt: null,
  truncated: false
})
const emptyCard = () => ({
  stats: emptyStats(),
  loading: false,
  refreshing: false,
  podsRefreshing: false,
  podsRefreshError: '',
  error: ''
})

const cards = ref({})
/** clusterId -> 最新巡检摘要（Pods / 异常 Pod 以巡检为准） */
const inspectById = ref({})
const seqById = new Map()
const podsRefreshSeqById = new Map()
/** 同一份过期快照只自动刷一次，避免反复巡检 */
const autoPodsRefreshKeys = new Set()
let loadAllSeq = 0
let detailSeq = 0

function podsRefreshSeqFor(id) {
  let s = podsRefreshSeqById.get(id)
  if (!s) {
    s = createSeq()
    podsRefreshSeqById.set(id, s)
  }
  return s
}

const emptyDetail = () => ({
  open: false,
  kind: '',
  clusterId: '',
  clusterName: '',
  title: '',
  loading: false,
  error: '',
  rows: [],
  sampled: false,
  sampleLimit: 0,
  fromInspect: false,
  inspectAt: null,
  truncated: false,
  scanPods: 0
})

const detail = ref(emptyDetail())

/** 双击卡片标题打开的概要抽屉 */
const summary = ref({
  open: false,
  clusterId: '',
  clusterName: '',
  inspecting: false,
  error: ''
})

function fmtInspectTime(t) {
  if (!t) return ''
  const d = t instanceof Date ? t : new Date(t)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function podsLabel(id) {
  return cardOf(id).stats.podsSource === 'inspect' ? 'Pods' : 'Pods（抽样）'
}

function badPodsLabel(id) {
  return cardOf(id).stats.podsSource === 'inspect' ? '异常 Pod' : '异常 Pod（抽样）'
}

function podsStatTitle(id) {
  const s = cardOf(id).stats
  if (s.podsSource === 'inspect') {
    const t = fmtInspectTime(s.inspectAt)
    const trunc = s.truncated ? ' · 可能触达扫描上限' : ''
    return t ? `来自巡检缓存 · ${t}${trunc}` : `来自巡检缓存${trunc}`
  }
  return '仅抽样部分命名空间，数字可能偏低'
}

function inspectAgeMs(insp) {
  if (!insp?.checkedAt) return Number.POSITIVE_INFINITY
  const t = new Date(insp.checkedAt).getTime()
  if (Number.isNaN(t)) return Number.POSITIVE_INFINITY
  return Date.now() - t
}

function podsInspectStale(insp) {
  return !insp?.hasScan || inspectAgeMs(insp) > PODS_INSPECT_STALE_MS
}

function autoPodsRefreshKey(id, insp) {
  return `${id}:${insp?.checkedAt || 'none'}`
}

function inspFromDetail(data) {
  if (!data) return null
  const rawPct = data.nfsUsagePercent ?? data.diagnose?.scan?.nfsUsagePercent
  const pct = rawPct == null || rawPct === '' ? null : Number(rawPct)
  return {
    hasScan: !!data.hasScan || !!data.diagnose,
    scanPods: Number(data.scanPods ?? data.diagnose?.scan?.pods) || 0,
    podIssues: Number(data.podIssues ?? data.diagnose?.counts?.pods) || 0,
    truncated: !!(data.truncated ?? data.diagnose?.truncated),
    checkedAt: data.checkedAt || null,
    largePods: Number(data.largePods ?? data.diagnose?.scan?.largePods) || 0,
    largePodSkipped: data.largePodSkipped || data.diagnose?.scan?.largePodSkipped || '',
    nfsUsagePercent: pct == null || Number.isNaN(pct) ? null : pct,
    nfsStorageClass: data.nfsStorageClass || data.diagnose?.scan?.nfsStorageClass || '',
    nfsServer: data.nfsServer || data.diagnose?.scan?.nfsServer || '',
    nfsPath: data.nfsPath || data.diagnose?.scan?.nfsPath || '',
    nfsSource: data.nfsSource || data.diagnose?.scan?.nfsSource || '',
    nfsSkipped: data.nfsSkipped || data.diagnose?.scan?.nfsSkipped || ''
  }
}

function issueBrief(row) {
  const reason = issueReasonDisplay(row?.reason)
  const msg = formatIssueMessage(row)
  const since = issueMessageSuffix(row)
  let line = reason || String(row?.reason || '')
  if (msg) line = line ? `${line} ${msg}` : msg
  if (since) line += since
  return line || '—'
}

function seqFor(id) {
  let s = seqById.get(id)
  if (!s) {
    s = createSeq()
    seqById.set(id, s)
  }
  return s
}

function cardOf(id) {
  return cards.value[id] || emptyCard()
}

function patchCard(id, patch) {
  cards.value[id] = { ...cardOf(id), ...patch }
}

function hydrateCard(id) {
  const hit = cacheGet(cacheKey('overview', id))
  const has = !!(hit?.data && typeof hit.data === 'object')
  const insp = inspectById.value[id]
  const stats = applyInspectToStats(has ? { ...emptyStats(), ...hit.data } : emptyStats(), insp)
  patchCard(id, {
    stats,
    // overview 齐全则整卡可展示；仅有巡检时 Pods/NFS 可先出，ns/nodes 仍标 loading
    loading: !has,
    refreshing: false,
    error: ''
  })
}

/** 从本地缓存同步灌入巡检摘要，打开页立刻有 Pods / NFS 等 */
function hydrateInspectIndex() {
  const hit = cacheGet(INSPECT_KEY)
  const map = buildInspectMap(hit?.data?.items)
  if (Object.keys(map).length) inspectById.value = map
  return map
}

function hydrateAllCards(list) {
  const keep = {}
  for (const c of list) {
    if (!c?.id) continue
    if (!cards.value[c.id]) hydrateCard(c.id)
    else {
      const hit = cacheGet(cacheKey('overview', c.id))
      if (hit?.data && typeof hit.data === 'object') {
        patchCard(c.id, {
          stats: applyInspectToStats({ ...emptyStats(), ...hit.data }, inspectById.value[c.id]),
          loading: false
        })
      } else {
        mergeInspectIntoCard(c.id, inspectById.value[c.id])
      }
    }
    keep[c.id] = cards.value[c.id] || emptyCard()
  }
  cards.value = keep
}

async function mapPool(items, concurrency, fn) {
  const list = [...items]
  const workers = Array.from({ length: Math.min(concurrency, list.length) || 0 }, async () => {
    while (list.length) {
      const item = list.shift()
      if (item === undefined) return
      await fn(item)
    }
  })
  await Promise.all(workers)
}

function dnsHostHint(cluster, err) {
  const msg = err || ''
  const m = msg.match(/lookup\s+([^\s:]+)\s+on\s+/i)
  if (m?.[1]) return m[1]
  try {
    if (cluster?.server) return new URL(cluster.server).hostname
  } catch {
    /* ignore */
  }
  return ''
}

function isDNSError(err) {
  return /no such host|lookup /i.test(err || '')
}

async function listNamespacesCached(id, { force = false, selected = true } = {}) {
  const key = cacheKey('namespaces', id)
  const ttl = ttlOf('namespaces', selected)
  const hit = cacheGet(key)
  if (!force && hit && cacheFresh(key, ttl)) return asArray(hit.data)
  return asArray(await cacheLoad(key, ttl, () => readApi.listNamespaces(id), { force }))
}

async function listNodesCached(id, { force = false, selected = true } = {}) {
  const usageKey = cacheKey('nodeUsage', id)
  const nodesKey = cacheKey('nodes', id)
  const ttl = ttlOf('nodes', selected)
  if (!force) {
    if (cacheFresh(nodesKey, ttl)) return asArray(cacheGet(nodesKey)?.data)
    if (cacheFresh(usageKey, ttlOf('nodeUsage', selected))) return asArray(cacheGet(usageKey)?.data)
  }
  return asArray(await cacheLoad(nodesKey, ttl, () => readApi.listNodes(id), { force }))
}

async function listPodsCached(id, nsName, { force = false, selected = true } = {}) {
  const key = cacheKey('pods', id, nsName)
  const ttl = ttlOf('pods', selected)
  const hit = cacheGet(key)
  if (!force && hit && cacheFresh(key, ttl)) return asArray(hit.data)
  return asArray(await cacheLoad(key, ttl, () => readApi.listPods(id, nsName), { force }))
}

function inspectItemToMapEntry(it) {
  if (!it?.clusterId) return null
  return {
    hasScan: !!it.hasScan,
    scanPods: Number(it.scanPods) || 0,
    podIssues: Number(it.podIssues) || 0,
    truncated: !!it.truncated,
    checkedAt: it.checkedAt || null,
    largePods: Number(it.largePods) || 0,
    largePodSkipped: it.largePodSkipped || '',
    nfsUsagePercent: (() => {
      if (it.nfsUsagePercent == null || it.nfsUsagePercent === '') return null
      const n = Number(it.nfsUsagePercent)
      return Number.isNaN(n) ? null : n
    })(),
    nfsStorageClass: it.nfsStorageClass || '',
    nfsServer: it.nfsServer || '',
    nfsPath: it.nfsPath || '',
    nfsSource: it.nfsSource || '',
    nfsSkipped: it.nfsSkipped || ''
  }
}

function buildInspectMap(items) {
  const map = {}
  for (const it of asArray(items)) {
    const entry = inspectItemToMapEntry(it)
    if (entry) map[it.clusterId] = entry
  }
  return map
}

function inspectCheckedAtMs(insp) {
  if (!insp?.checkedAt) return 0
  const t = new Date(insp.checkedAt).getTime()
  return Number.isNaN(t) ? 0 : t
}

/** 网络摘要与内存中刚写入的实时巡检合并，避免慢请求回写冲掉更新结果 */
function mergeInspectMaps(prev, next) {
  const out = { ...next }
  for (const [id, p] of Object.entries(prev || {})) {
    const n = out[id]
    if (!n) continue
    if (inspectCheckedAtMs(p) > inspectCheckedAtMs(n)) out[id] = p
  }
  return out
}

async function loadInspectIndex({ force = false } = {}) {
  const key = cacheKey('inspections', 'local')
  try {
    const data = await cacheLoad(key, TTL.inspections, () => readApi.listInspections(), { force })
    const map = mergeInspectMaps(inspectById.value, buildInspectMap(data?.items))
    inspectById.value = map
    notifyInspectionsChanged()
    return map
  } catch {
    // 保留已 hydrate 的本地摘要，避免失败时清空导致后续 loadOne 误走抽样并覆盖巡检数字
    return { ...inspectById.value }
  }
}

function applyInspectToStats(stats, insp) {
  if (!insp?.hasScan) return { ...stats, podsSource: stats.podsSource || '' }
  const pct =
    insp.nfsSkipped || insp.nfsUsagePercent == null || Number.isNaN(Number(insp.nfsUsagePercent))
      ? null
      : Number(insp.nfsUsagePercent)
  return {
    ...stats,
    pods: insp.scanPods,
    badPods: insp.podIssues,
    largePods: insp.largePodSkipped ? null : Number(insp.largePods) || 0,
    largePodSkipped: insp.largePodSkipped || '',
    nfsUsagePercent: pct,
    nfsStorageClass: insp.nfsStorageClass || '',
    nfsServer: insp.nfsServer || '',
    nfsPath: insp.nfsPath || '',
    nfsSource: insp.nfsSource || '',
    nfsSkipped: insp.nfsSkipped || '',
    podsSource: 'inspect',
    inspectAt: insp.checkedAt || null,
    truncated: !!insp.truncated
  }
}

function mergeInspectIntoCard(id, insp) {
  if (!insp?.hasScan) return
  const next = applyInspectToStats(cardOf(id).stats, insp)
  patchCard(id, { stats: next, podsRefreshError: '' })
  const key = cacheKey('overview', id)
  const hit = cacheGet(key)
  // 只写回已有 overview，避免用「仅巡检字段」冒充完整卡片并被 TTL 当成新鲜而跳过 ns/nodes 拉取
  if (hit?.data && typeof hit.data === 'object') {
    cacheSet(key, { ...hit.data, ...next })
  }
}

function syncSummaryInspecting(clusterId, inspecting, error) {
  if (summary.value.clusterId !== clusterId || !summary.value.open) return
  const patch = { ...summary.value, inspecting: !!inspecting }
  if (error !== undefined) patch.error = error || ''
  summary.value = patch
}

/** 实时巡检并写回 Pods / 异常 Pod；先保留缓存数字 */
async function refreshPodsInspect(clusterId, { autoKey = '' } = {}) {
  if (!canWrite.value || !clusterId) return
  if (cardOf(clusterId).podsRefreshing) return
  const seq = podsRefreshSeqFor(clusterId).next()
  patchCard(clusterId, { podsRefreshing: true, podsRefreshError: '' })
  syncSummaryInspecting(clusterId, true)
  try {
    const data = await writeApi.inspectCluster(clusterId)
    if (!podsRefreshSeqFor(clusterId).is(seq)) return
    const insp = inspFromDetail(data)
    if (insp) {
      inspectById.value = { ...inspectById.value, [clusterId]: insp }
      mergeInspectIntoCard(clusterId, insp)
    }
    cacheInvalidate(INSPECT_KEY)
    // 后台刷新摘要列表，供巡检页 / 顶栏共用；失败不影响卡片
    loadInspectIndex({ force: true }).catch(() => {})
  } catch (e) {
    if (autoKey) autoPodsRefreshKeys.delete(autoKey)
    if (!podsRefreshSeqFor(clusterId).is(seq)) return
    const msg = errMessage(e)
    patchCard(clusterId, { podsRefreshError: msg })
    syncSummaryInspecting(clusterId, true, msg)
  } finally {
    if (podsRefreshSeqFor(clusterId).is(seq)) {
      patchCard(clusterId, { podsRefreshing: false })
      syncSummaryInspecting(clusterId, false)
    }
  }
}

function maybeAutoRefreshPods(clusterId, insp) {
  if (!canWrite.value) return
  if (!clusterId || !podsInspectStale(insp)) return
  const key = autoPodsRefreshKey(clusterId, insp)
  if (autoPodsRefreshKeys.has(key)) return
  autoPodsRefreshKeys.add(key)
  void refreshPodsInspect(clusterId, { autoKey: key })
}

function manualRefreshPods(cluster) {
  if (!canWrite.value || !cluster?.id) return
  if (cluster.id !== currentClusterId.value) setCurrentClusterId(cluster.id)
  void refreshPodsInspect(cluster.id)
}

async function loadOne(cluster, { force = false, selected = true, inspectMap } = {}) {
  const id = cluster?.id
  if (!id) return
  const seq = seqFor(id).next()
  const key = cacheKey('overview', id)
  const ttl = ttlOf('overview', selected)
  const hit = cacheGet(key)
  const has = !!(hit?.data && typeof hit.data === 'object')
  const insp = (inspectMap || inspectById.value)[id]
  if (has) {
    patchCard(id, { stats: applyInspectToStats({ ...emptyStats(), ...hit.data }, insp), error: '' })
  } else {
    patchCard(id, { stats: applyInspectToStats(emptyStats(), insp), error: '' })
  }
  if (!selected && typeof document !== 'undefined' && document.hidden && has) {
    patchCard(id, { loading: false, refreshing: false })
    return
  }
  if (!force && cacheFresh(key, ttl)) {
    mergeInspectIntoCard(id, insp)
    patchCard(id, { loading: false, refreshing: false })
    return
  }
  patchCard(id, { loading: !has, refreshing: has, error: '' })
  const next = emptyStats()
  try {
    const ns = await listNamespacesCached(id, { selected, force })
    if (!seqFor(id).is(seq)) return
    next.namespaces = ns.length

    let nodes = []
    if (cluster.permType !== 'project') {
      try {
        nodes = await listNodesCached(id, { selected, force })
      } catch {
        nodes = []
      }
    }
    if (!seqFor(id).is(seq)) return
    next.nodes = nodes.length

    // 以最新 inspectById 为准，避免并行拉巡检时用过期 insp 走抽样、回写覆盖巡检数字
    const latestInsp = inspectById.value[id] || insp
    if (latestInsp?.hasScan) {
      Object.assign(next, applyInspectToStats(next, latestInsp))
    } else {
      let pods = 0
      let bad = 0
      const sample = ns.slice(0, selected ? 8 : 3)
      const parts = await Promise.all(
        sample.map(async (n) => {
          if (!n?.name) return { pods: 0, bad: 0 }
          try {
            const list = await listPodsCached(id, n.name, { selected, force })
            return {
              pods: list.length,
              bad: list.filter((p) => p?.status !== 'Running' && p?.status !== 'Succeeded').length
            }
          } catch {
            return { pods: 0, bad: 0 }
          }
        })
      )
      if (!seqFor(id).is(seq)) return
      const afterSample = inspectById.value[id]
      if (afterSample?.hasScan) {
        Object.assign(next, applyInspectToStats(next, afterSample))
      } else {
        for (const p of parts) {
          pods += p.pods
          bad += p.bad
        }
        next.pods = pods
        next.badPods = bad
        next.podsSource = 'sample'
      }
    }
    patchCard(id, { stats: next, error: '' })
    cacheSet(key, next)
  } catch (e) {
    if (!seqFor(id).is(seq)) return
    patchCard(id, {
      error: errMessage(e),
      stats: has ? cardOf(id).stats : emptyStats()
    })
  } finally {
    if (seqFor(id).is(seq)) {
      patchCard(id, { loading: false, refreshing: false })
    }
  }
}

async function loadAll({ force = false } = {}) {
  const seq = ++loadAllSeq
  // 同步灌缓存：打开瞬间先出数，再后台刷新
  hydrateInspectIndex()
  if (clusters.value.length) hydrateAllCards(clusters.value)

  try {
    await refreshClusters(() => readApi.listClusters())
  } catch {
    /* store 会保留上次列表 */
  }
  if (seq !== loadAllSeq) return
  const list = clusters.value
  hydrateAllCards(list)

  // 先拿巡检摘要再 loadOne，避免并行时抽样结果覆盖巡检数字；卡片已由 hydrate 先展示
  const inspectMap = await loadInspectIndex({ force })
  if (seq !== loadAllSeq) return
  for (const c of list) mergeInspectIntoCard(c.id, inspectMap[c.id])

  const selectedId = currentClusterId.value
  const current = list.find((c) => c.id === selectedId)
  const idle = list.filter((c) => c.id !== selectedId)
  if (current) {
    await loadOne(current, { force, selected: true, inspectMap })
    if (seq !== loadAllSeq) return
    maybeAutoRefreshPods(current.id, inspectById.value[current.id] || inspectMap[current.id])
  }

  await mapPool(idle, 4, async (c) => {
    if (seq !== loadAllSeq) return
    await loadOne(c, { force: false, selected: false, inspectMap })
  })
}

function openSummary(cluster) {
  if (!cluster?.id) return
  detail.value = emptyDetail()
  const same = summary.value.clusterId === cluster.id && summary.value.open
  summary.value = {
    open: true,
    clusterId: cluster.id,
    clusterName: cluster.name || '',
    // 同一集群巡检中再次打开时保留状态，避免按钮可重入并 bump 掉进行中的序号
    inspecting: same ? summary.value.inspecting : !!cardOf(cluster.id).podsRefreshing,
    error: same ? summary.value.error : ''
  }
}

function closeSummary() {
  summary.value = {
    open: false,
    clusterId: '',
    clusterName: '',
    inspecting: false,
    error: ''
  }
}

function summaryCluster() {
  return clusters.value.find((c) => c.id === summary.value.clusterId) || null
}

function summaryStat(key) {
  const id = summary.value.clusterId
  if (!id) return '—'
  return statNumDisplay(id, key)
}

async function runManualInspect() {
  if (!canWrite.value) return
  const cluster = summaryCluster()
  if (!cluster?.id) return
  // 与「刷新 Pods」共用 podsRefreshing，避免双入口并发巡检互相 bump seq
  if (summary.value.inspecting || cardOf(cluster.id).podsRefreshing) return
  // 先标 busy，再改选用，避免 watch→loadAll 与本次巡检抢同一张卡的 podsRefreshing
  syncSummaryInspecting(cluster.id, true, '')
  const seq = podsRefreshSeqFor(cluster.id).next()
  patchCard(cluster.id, { podsRefreshing: true, podsRefreshError: '' })
  if (cluster.id !== currentClusterId.value) setCurrentClusterId(cluster.id)
  try {
    const data = await writeApi.inspectCluster(cluster.id)
    if (!podsRefreshSeqFor(cluster.id).is(seq)) return
    const insp = inspFromDetail(data)
    if (insp) {
      inspectById.value = { ...inspectById.value, [cluster.id]: insp }
      mergeInspectIntoCard(cluster.id, insp)
    }
    cacheInvalidate(INSPECT_KEY)
    cacheInvalidate(cacheKey('namespaces', cluster.id))
    cacheInvalidate(cacheKey('nodes', cluster.id))
    cacheInvalidate(cacheKey('nodeUsage', cluster.id))
    // 巡检写回后强制重拉 ns/nodes；Pods 等已用巡检结果写回卡片
    await loadOne(cluster, { force: true, selected: true, inspectMap: inspectById.value })
    if (!podsRefreshSeqFor(cluster.id).is(seq)) return
    loadInspectIndex({ force: true }).catch(() => {})
    refreshClusters(() => readApi.listClusters(), { force: true }).catch(() => {})
  } catch (e) {
    if (!podsRefreshSeqFor(cluster.id).is(seq)) return
    const msg = errMessage(e)
    patchCard(cluster.id, { podsRefreshError: msg })
    syncSummaryInspecting(cluster.id, true, msg)
  } finally {
    if (podsRefreshSeqFor(cluster.id).is(seq)) {
      patchCard(cluster.id, { podsRefreshing: false })
      syncSummaryInspecting(cluster.id, false)
    }
  }
}

function onVis() {
  if (!document.hidden) loadAll()
}

function sampleLimitOf(selected) {
  return selected ? 8 : 3
}

function isBadPod(p) {
  return p?.status !== 'Running' && p?.status !== 'Succeeded'
}

async function collectSamplePods(cluster, { force = false, selected = true, badOnly = false } = {}) {
  const id = cluster?.id
  if (!id) return []
  const ns = await listNamespacesCached(id, { selected, force })
  const sample = ns.slice(0, sampleLimitOf(selected))
  const parts = await Promise.all(
    sample.map(async (n) => {
      if (!n?.name) return []
      try {
        const list = await listPodsCached(id, n.name, { selected, force })
        return list.map((p) => ({ ...p, namespace: p?.namespace || n.name }))
      } catch {
        return []
      }
    })
  )
  const rows = parts.flat()
  return badOnly ? rows.filter(isBadPod) : rows
}

async function collectInspectPodIssues(clusterId) {
  const data = await readApi.getInspection(clusterId, { timeout: 12000 })
  return asArray(data?.issues)
    .filter((it) => it?.category === 'pod')
    .map((it) => ({
      ...it,
      status: issueBrief(it),
      severity: it.severity
    }))
}

async function collectInspectStorageIssues(clusterId, reason) {
  const local = inspectById.value[clusterId] || {}
  const stats = cardOf(clusterId).stats
  const skipped = stats.largePodSkipped || local.largePodSkipped || ''
  if (skipped) {
    return [
      {
        name: '—',
        namespace: '',
        status: `未探测：${skipped}`,
        severity: ''
      }
    ]
  }
  const data = await readApi.getInspection(clusterId, { timeout: 12000 })
  return asArray(data?.issues)
    .filter((it) => it?.category === 'storage' && (!reason || it.reason === reason))
    .map((it) => ({
      ...it,
      status: issueBrief(it),
      severity: it.severity
    }))
}

async function collectInspectNFSRows(clusterId) {
  // Prefer card / inspect-index cache — avoid pulling the full diagnose blob for a single cell.
  const local = inspectById.value[clusterId] || {}
  const stats = cardOf(clusterId).stats
  const pct = stats.nfsUsagePercent ?? local.nfsUsagePercent
  const sc = stats.nfsStorageClass || local.nfsStorageClass || ''
  const server = stats.nfsServer || local.nfsServer || ''
  const nfsPath = stats.nfsPath || local.nfsPath || ''
  const skipped = stats.nfsSkipped || local.nfsSkipped || ''
  const source = stats.nfsSource || local.nfsSource || ''

  let issueRow = null
  if (pct != null && Number(pct) >= 80) {
    try {
      const data = await readApi.getInspection(clusterId, { timeout: 10000 })
      const hit = asArray(data?.issues).find((it) => it?.reason === 'NFSHighUsage')
      if (hit) {
        issueRow = {
          ...hit,
          status: issueBrief(hit),
          severity: hit.severity
        }
      }
      const scan = data?.diagnose?.scan || {}
      return [
        issueRow || {
          name: data?.nfsStorageClass || scan.nfsStorageClass || sc || 'NFS',
          namespace: '',
          status: formatNFSStatusLine({
            pct: data?.nfsUsagePercent ?? scan.nfsUsagePercent ?? pct,
            skipped: data?.nfsSkipped || scan.nfsSkipped || skipped,
            source: data?.nfsSource || scan.nfsSource || source,
            probe: scan.nfsProbe || '',
            used: scan.nfsUsedBytes,
            cap: scan.nfsCapacityBytes,
            server: data?.nfsServer || scan.nfsServer || server,
            path: data?.nfsPath || scan.nfsPath || nfsPath
          }),
          severity: 'critical'
        }
      ]
    } catch {
      // fall through to local summary
    }
  }

  return [
    {
      name: sc || 'NFS',
      namespace: '',
      status: formatNFSStatusLine({
        pct,
        skipped,
        source,
        probe: '',
        used: null,
        cap: null,
        server,
        path: nfsPath
      }),
      severity: pct != null && Number(pct) >= 80 ? 'critical' : ''
    }
  ]
}

function formatNFSEndpoint(server, path) {
  const s = String(server || '').trim()
  const p = sanitizeNFSExportRoot(path)
  if (!s) return ''
  if (!p) return s
  return p.startsWith('/') ? `${s}:${p}` : `${s}:/${p}`
}

function formatNFSStatusLine({ pct, skipped, source, probe, used, cap, server, path }) {
  if (skipped) return `未探测：${skipped}`
  if (pct == null) return '尚无用量数据'
  let line = `使用率 ${Number(pct).toFixed(0)}%`
  if (used != null && cap != null) line += `（已用 ${fmtBytesUI(used)} / ${fmtBytesUI(cap)}）`
  const ep = formatNFSEndpoint(server, path)
  if (ep) line += ` · ${ep}`
  if (source) line += ` · ${source === 'unique' ? '唯一 SC' : '默认 SC'}`
  if (probe) line += ` · ${probe}`
  return line
}

function fmtBytesUI(n) {
  const v = Number(n) || 0
  const gi = 1024 * 1024 * 1024
  const mi = 1024 * 1024
  if (v >= gi) return `${(v / gi).toFixed(2)}Gi`
  return `${Math.round(v / mi)}Mi`
}

function hasCachedStats(id) {
  return !cardOf(id).loading
}

function hasInspectStats(id) {
  return cardOf(id).stats.podsSource === 'inspect'
}

function statNumDisplay(id, key) {
  const card = cardOf(id)
  if (card.loading && !hasInspectStats(id)) return '…'
  if (card.loading && (key === 'namespaces' || key === 'nodes')) return '…'
  const v = card.stats?.[key]
  return v == null || v === '' ? '—' : v
}

function largePodsDisplay(id) {
  const s = cardOf(id).stats
  if (cardOf(id).loading && !hasInspectStats(id)) return '…'
  if (s.podsSource !== 'inspect' || s.largePods == null) return '—'
  return s.largePods
}

function nfsUsageDisplay(id) {
  const s = cardOf(id).stats
  if (cardOf(id).loading && !hasInspectStats(id)) return '…'
  if (s.podsSource !== 'inspect' || s.nfsUsagePercent == null) return '—'
  return `${Math.round(Number(s.nfsUsagePercent))}%`
}

function nfsCellStyle(id) {
  const pct = cardOf(id).stats.nfsUsagePercent
  if (pct == null || Number.isNaN(Number(pct))) return {}
  const p = Math.max(0, Math.min(100, Number(pct))) / 100
  const hue = Math.round(120 * (1 - p))
  return {
    background: `linear-gradient(180deg, hsla(${hue}, 70%, 46%, 0.12), hsla(${hue}, 72%, 36%, 0.26))`
  }
}

function nfsStatTitle(id) {
  const s = cardOf(id).stats
  if (s.podsSource !== 'inspect') return '需巡检后显示'
  if (s.nfsSkipped) return `未探测：${s.nfsSkipped}`
  if (s.nfsUsagePercent == null) return '尚无 NFS 用量'
  const ep = formatNFSEndpoint(s.nfsServer, s.nfsPath)
  const sc = s.nfsStorageClass ? `SC=${s.nfsStorageClass}` : ''
  const src = s.nfsSource === 'unique' ? '唯一 SC' : s.nfsSource === 'default' ? '默认 SC' : ''
  const t = fmtInspectTime(s.inspectAt)
  return [ep, sc, src, t ? `巡检 ${t}` : ''].filter(Boolean).join(' · ') || 'NFS 使用率'
}

function largePodsStatTitle(id) {
  const s = cardOf(id).stats
  if (s.podsSource !== 'inspect') return '需巡检后显示'
  if (s.largePodSkipped) return `未探测：${s.largePodSkipped}`
  const t = fmtInspectTime(s.inspectAt)
  return t ? `非持久化 >5Gi · 巡检 ${t}` : '非持久化存储占用超过 5Gi 的 Pod'
}

async function loadStatRows(cluster, kind, selected, fromInspect) {
  const id = cluster.id
  if (kind === 'namespaces') return listNamespacesCached(id, { selected })
  if (kind === 'nodes') {
    if (cluster.permType === 'project') return []
    return listNodesCached(id, { selected })
  }
  if (kind === 'pods') {
    if (fromInspect) return []
    return collectSamplePods(cluster, { selected, badOnly: false })
  }
  if (kind === 'badPods') {
    if (fromInspect) return collectInspectPodIssues(id)
    return collectSamplePods(cluster, { selected, badOnly: true })
  }
  if (kind === 'largePods') {
    return collectInspectStorageIssues(id, 'LargeEphemeral')
  }
  if (kind === 'nfsUsage') {
    return collectInspectNFSRows(id)
  }
  return []
}

function closeDetail() {
  detailSeq += 1
  detail.value = emptyDetail()
}

function statTitle(kind, fromInspect) {
  if (kind === 'namespaces') return 'Namespaces'
  if (kind === 'nodes') return 'Nodes'
  if (kind === 'pods') return fromInspect ? 'Pods' : 'Pods（抽样）'
  if (kind === 'badPods') return fromInspect ? '异常 Pod' : '异常 Pod（抽样）'
  if (kind === 'largePods') return '大 POD'
  if (kind === 'nfsUsage') return 'NFS 使用率'
  return kind
}

async function openStat(cluster, kind) {
  if (!cluster?.id) return
  if (kind === 'nodes' && cluster.permType === 'project') return
  if (cardOf(cluster.id).loading && !hasInspectStats(cluster.id)) return
  if (cardOf(cluster.id).loading && (kind === 'namespaces' || kind === 'nodes')) return
  closeSummary()
  const selected = cluster.id === currentClusterId.value
  const stats = cardOf(cluster.id).stats
  const fromInspect =
    stats.podsSource === 'inspect' &&
    (kind === 'pods' || kind === 'badPods' || kind === 'largePods' || kind === 'nfsUsage')
  setCurrentClusterId(cluster.id)
  const seq = ++detailSeq
  detail.value = {
    open: true,
    kind,
    clusterId: cluster.id,
    clusterName: cluster.name,
    title: statTitle(kind, fromInspect),
    loading: true,
    error: '',
    rows: [],
    sampled: (kind === 'pods' || kind === 'badPods') && !fromInspect,
    sampleLimit: sampleLimitOf(selected),
    fromInspect,
    inspectAt: stats.inspectAt || null,
    truncated: !!stats.truncated,
    scanPods: stats.pods || 0
  }
  try {
    const rows = await loadStatRows(cluster, kind, selected, fromInspect)
    if (seq !== detailSeq) return
    detail.value = { ...detail.value, rows, loading: false }
  } catch (e) {
    if (seq !== detailSeq) return
    detail.value = { ...detail.value, error: errMessage(e), loading: false, rows: [] }
  }
}

function moreLabel(kind) {
  if (kind === 'nodes') return '去工作负载'
  if (kind === 'badPods' || kind === 'largePods' || kind === 'nfsUsage') return '去诊断'
  return '去工作负载'
}

function goMore() {
  const d = detail.value
  const id = d.clusterId
  if (!id) return
  if (d.kind === 'nodes') {
    // 只切到节点 Tab，保留已有命名空间偏好，避免被默认 ns 写回冲掉
    const pref = getViewPref('workloads', id) || {}
    setViewPref('workloads', id, { tab: 'nodes', ns: pref.ns || '' })
    router.push('/workloads')
    return
  }
  if (d.kind === 'badPods') {
    router.push({ path: '/diagnose', query: { module: 'diagnose', category: 'pod' } })
    return
  }
  if (d.kind === 'largePods' || d.kind === 'nfsUsage') {
    router.push({ path: '/diagnose', query: { module: 'diagnose', category: 'storage' } })
    return
  }
  {
    const pref = getViewPref('workloads', id) || {}
    setViewPref('workloads', id, { tab: 'pods', ns: pref.ns || '' })
  }
  router.push('/workloads')
}

function openWorkloadNs(nsName) {
  const id = detail.value.clusterId
  if (!id || !nsName) return
  setViewPref('workloads', id, { ns: nsName, tab: 'pods' })
  router.push('/workloads')
}

watch(
  () => [clusters.value.map((c) => c.id).join(','), currentClusterId.value],
  () => {
    loadAll()
  },
  { immediate: true }
)

onMounted(() => {
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  loadAllSeq += 1
  detailSeq += 1
  for (const s of podsRefreshSeqById.values()) s.next()
  document.removeEventListener('visibilitychange', onVis)
  closeSummary()
})

</script>

<template>
  <div>
    <h1>概览</h1>
    <p class="page-desc">查看已导入的全部 kubeconfig。点「选用」会设为顶部当前集群，供工作负载、巡检、告警使用。Pods / 异常 Pod 优先展示巡检缓存<template v-if="canWrite">；当前选用集群缓存超过 10 分钟会自动实时巡检一次，也可点「刷新 Pods」立即更新</template>。尚未巡检时才退回命名空间抽样。全部配置的定时巡检摘要在「巡检」页。节点与用量在「工作负载」。</p>
    <div v-if="!clusters.length" class="empty">{{ canWrite ? '请先导入 kubeconfig（页脚双击 kubeconfig）。' : '暂无集群配置。' }}</div>
    <div v-else class="cluster-list">
      <div
        v-for="c in sortedClusters"
        :key="c.id"
        class="panel cluster-card"
        :class="{ 'is-current': c.id === currentClusterId }"
      >
        <div class="cluster-card-head">
          <div class="cluster-card-meta">
            <div class="cluster-card-title">
              <strong @dblclick.stop="openSummary(c)">{{ c.name }}</strong>
              <span v-if="c.id === currentClusterId" class="tag current">当前</span>
              <span class="tag" :class="c.permType">{{ permTypeLabel(c.permType) }}</span>
              <span class="muted cluster-server">{{ c.server }}</span>
            </div>
            <div class="status-line">
              <span class="tag" :class="clusterStatusClass(c.status)">{{ clusterStatusLabel(c.status) }}</span>
              <span
                v-if="frontProxyLabel(c.frontProxy)"
                class="tag"
                :class="frontProxyClass(c.frontProxy)"
                :title="'入口探测：' + frontProxyLabel(c.frontProxy)"
              >{{ frontProxyLabel(c.frontProxy) }}</span>
              <span v-if="c.kubeVersion" class="tag" :title="'apiserver ' + c.kubeVersion">{{ c.kubeVersion }}</span>
              <span v-if="cardOf(c.id).refreshing" class="muted">更新中…</span>
              <span v-if="cardOf(c.id).podsRefreshing" class="muted">Pods 实时刷新中…</span>
            </div>
            <p
              v-if="c.lastError"
              class="error error-clip"
              :title="c.lastError"
            >{{ c.lastError }}</p>
            <p
              v-if="cardOf(c.id).podsRefreshError"
              class="error error-clip"
              :title="cardOf(c.id).podsRefreshError"
            >Pods 刷新失败：{{ cardOf(c.id).podsRefreshError }}</p>
          </div>
          <div class="actions">
            <button
              v-if="canWrite"
              type="button"
              class="btn btn-sm"
              :disabled="cardOf(c.id).podsRefreshing"
              title="实时巡检并更新 Pods / 异常 Pod"
              @click="manualRefreshPods(c)"
            >{{ cardOf(c.id).podsRefreshing ? '刷新中…' : '刷新 Pods' }}</button>
            <RouterLink
              v-if="canWrite"
              class="btn btn-sm"
              :to="{ path: '/clusters', query: { edit: c.id } }"
            >编辑</RouterLink>
            <button
              type="button"
              class="btn btn-sm"
              :class="{ 'is-current': c.id === currentClusterId }"
              :disabled="c.id === currentClusterId"
              @click="setCurrentClusterId(c.id)"
            >{{ c.id === currentClusterId ? '已选用' : '选用' }}</button>
          </div>
        </div>

        <div v-if="cardOf(c.id).error" class="error-block">
          <p class="error" style="white-space: pre-wrap; margin: 0">{{ cardOf(c.id).error }}</p>
          <p v-if="isDNSError(cardOf(c.id).error)" class="hint">
            域名无法解析（走的是集群 DNS）。请到
            <RouterLink v-if="canWrite" to="/clusters">Hosts</RouterLink>
            <template v-else>Hosts（需可操作权限，页脚双击 kubeconfig）</template>
            添加映射，例如：
            <code>10.80.238.34 {{ dnsHostHint(c, cardOf(c.id).error) || 'api-host' }}</code>
            ，保存后再刷新本页。
          </p>
        </div>

        <div class="stats-strip">
          <button
            type="button"
            class="stat"
            title="点击查看"
            :disabled="!hasCachedStats(c.id)"
            @click="openStat(c, 'namespaces')"
          >
            <div class="label">Namespaces</div>
            <div class="value">{{ statNumDisplay(c.id, 'namespaces') }}</div>
          </button>
          <button
            v-if="c.permType !== 'project'"
            type="button"
            class="stat"
            title="点击查看"
            :disabled="!hasCachedStats(c.id)"
            @click="openStat(c, 'nodes')"
          >
            <div class="label">Nodes</div>
            <div class="value">{{ statNumDisplay(c.id, 'nodes') }}</div>
          </button>
          <div v-else class="stat">
            <div class="label">Nodes</div>
            <div class="value">—</div>
          </div>
          <button
            type="button"
            class="stat"
            :title="podsStatTitle(c.id)"
            :disabled="!hasCachedStats(c.id) && !hasInspectStats(c.id)"
            @click="openStat(c, 'pods')"
          >
            <div class="label">{{ podsLabel(c.id) }}</div>
            <div class="value">{{ statNumDisplay(c.id, 'pods') }}</div>
          </button>
          <button
            type="button"
            class="stat"
            :title="podsStatTitle(c.id)"
            :disabled="!hasCachedStats(c.id) && !hasInspectStats(c.id)"
            @click="openStat(c, 'badPods')"
          >
            <div class="label">{{ badPodsLabel(c.id) }}</div>
            <div
              class="value"
              :class="{ 'is-critical': Number(statNumDisplay(c.id, 'badPods')) > 0 }"
            >{{ statNumDisplay(c.id, 'badPods') }}</div>
          </button>
          <button
            type="button"
            class="stat"
            :title="largePodsStatTitle(c.id)"
            :disabled="!hasCachedStats(c.id) && !hasInspectStats(c.id)"
            @click="openStat(c, 'largePods')"
          >
            <div class="label">大 POD</div>
            <div
              class="value"
              :class="{ 'is-warning': Number(largePodsDisplay(c.id)) > 0 }"
            >{{ largePodsDisplay(c.id) }}</div>
          </button>
          <button
            type="button"
            class="stat nfs-usage"
            :class="{ 'is-critical': Number(cardOf(c.id).stats.nfsUsagePercent) >= 80 }"
            :title="nfsStatTitle(c.id)"
            :style="nfsCellStyle(c.id)"
            :disabled="!hasCachedStats(c.id) && !hasInspectStats(c.id)"
            @click="openStat(c, 'nfsUsage')"
          >
            <div class="label">NFS 使用率</div>
            <div class="value">{{ nfsUsageDisplay(c.id) }}</div>
          </button>
        </div>
      </div>
    </div>

    <div v-if="summary.open" class="drawer-backdrop" @click="closeSummary"></div>
    <div v-if="summary.open" class="drawer">
      <div class="toolbar">
        <strong>{{ summary.clusterName }}</strong>
        <div class="actions">
          <button
            v-if="canWrite"
            type="button"
            class="btn btn-sm"
            :disabled="summary.inspecting"
            @click="runManualInspect"
          >{{ summary.inspecting ? '巡检中…' : '手动巡检' }}</button>
          <button type="button" class="btn btn-sm" @click="closeSummary">关闭</button>
        </div>
      </div>
      <p v-if="summary.error" class="error">{{ summary.error }}</p>
      <table class="table summary-table">
        <tbody>
          <tr>
            <th>状态</th>
            <td>
              <span
                v-if="summaryCluster()"
                class="tag"
                :class="clusterStatusClass(summaryCluster().status)"
              >{{ clusterStatusLabel(summaryCluster().status) }}</span>
              <span v-else>—</span>
            </td>
          </tr>
          <tr>
            <th>权限</th>
            <td>{{ summaryCluster() ? permTypeLabel(summaryCluster().permType) : '—' }}</td>
          </tr>
          <tr>
            <th>版本</th>
            <td>{{ summaryCluster()?.kubeVersion || '—' }}</td>
          </tr>
          <tr>
            <th>入口</th>
            <td>{{ summaryCluster() ? (frontProxyLabel(summaryCluster().frontProxy) || '—') : '—' }}</td>
          </tr>
          <tr>
            <th>API</th>
            <td class="summary-break">{{ summaryCluster()?.server || '—' }}</td>
          </tr>
          <tr>
            <th>Namespaces</th>
            <td>{{ summaryStat('namespaces') }}</td>
          </tr>
          <tr>
            <th>Nodes</th>
            <td>{{ summaryCluster()?.permType === 'project' ? '—' : summaryStat('nodes') }}</td>
          </tr>
          <tr>
            <th>Pods</th>
            <td>{{ summaryStat('pods') }}</td>
          </tr>
          <tr>
            <th>异常 Pod</th>
            <td
              :class="{ 'is-critical': Number(summaryStat('badPods')) > 0 }"
            >{{ summaryStat('badPods') }}</td>
          </tr>
          <tr>
            <th>大 POD</th>
            <td>{{ largePodsDisplay(summary.clusterId) }}</td>
          </tr>
          <tr>
            <th>NFS 使用率</th>
            <td>{{ nfsUsageDisplay(summary.clusterId) }}</td>
          </tr>
          <tr>
            <th>巡检时间</th>
            <td>{{ fmtInspectTime(cardOf(summary.clusterId).stats.inspectAt) || '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="detail.open" class="drawer-backdrop" @click="closeDetail"></div>
    <div v-if="detail.open" class="drawer">
      <div class="toolbar">
        <strong>{{ detail.title }} · {{ detail.clusterName }}</strong>
        <div class="actions">
          <button type="button" class="btn btn-sm" @click="goMore">{{ moreLabel(detail.kind) }}</button>
          <button type="button" class="btn btn-sm" @click="closeDetail">关闭</button>
        </div>
      </div>
      <p v-if="detail.sampled" class="muted sample-hint">仅抽样前 {{ detail.sampleLimit }} 个命名空间，与卡片数字一致。尚未巡检或探测失败时退回抽样。</p>
      <p v-else-if="detail.fromInspect && detail.kind === 'pods'" class="muted sample-hint">
        共 {{ detail.scanPods }} 个 Pod，来自定时巡检扫描<template v-if="fmtInspectTime(detail.inspectAt)">（{{ fmtInspectTime(detail.inspectAt) }}）</template>。
        <template v-if="detail.truncated">扫描可能触达上限，数字接近上限时请到工作负载核对。</template>
        完整列表请点「去工作负载」。
      </p>
      <p v-else-if="detail.fromInspect && detail.kind === 'badPods'" class="muted sample-hint">
        来自定时巡检的 Pod 问题（含 CrashLoop / 未就绪等，不限于 Phase）。
        <template v-if="fmtInspectTime(detail.inspectAt)">巡检时间 {{ fmtInspectTime(detail.inspectAt) }}。</template>
      </p>
      <p v-else-if="detail.kind === 'largePods'" class="muted sample-hint">
        非持久化存储（ephemeral）占用超过 5Gi 的 Pod，级别为警告。
        <template v-if="fmtInspectTime(detail.inspectAt)">巡检时间 {{ fmtInspectTime(detail.inspectAt) }}。</template>
      </p>
      <p v-else-if="detail.kind === 'nfsUsage'" class="muted sample-hint">
        默认 NFS SC（或唯一 NFS SC）的共享盘使用率；≥80% 为严重。
        <template v-if="fmtInspectTime(detail.inspectAt)">巡检时间 {{ fmtInspectTime(detail.inspectAt) }}。</template>
      </p>
      <p v-if="detail.error" class="error">{{ detail.error }}</p>
      <p v-else-if="detail.loading" class="muted">加载中…</p>
      <template v-else>
        <table v-if="detail.rows.length" class="table">
          <thead>
            <tr v-if="detail.kind === 'namespaces'">
              <th>名称</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
            <tr v-else-if="detail.kind === 'nodes'">
              <th>名称</th>
              <th>状态</th>
              <th>IP</th>
            </tr>
            <tr v-else-if="detail.fromInspect && detail.kind === 'badPods'">
              <th>严重度</th>
              <th>命名空间</th>
              <th>名称</th>
              <th>问题</th>
              <th>操作</th>
            </tr>
            <tr v-else-if="detail.kind === 'largePods'">
              <th>严重度</th>
              <th>命名空间</th>
              <th>名称</th>
              <th>问题</th>
              <th>操作</th>
            </tr>
            <tr v-else-if="detail.kind === 'nfsUsage'">
              <th>严重度</th>
              <th>名称</th>
              <th>说明</th>
            </tr>
            <tr v-else>
              <th>命名空间</th>
              <th>名称</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, idx) in detail.rows" :key="(row.namespace || '') + '/' + (row.name || idx) + '/' + (row.reason || '')">
              <template v-if="detail.kind === 'namespaces'">
                <td>{{ row.name }}</td>
                <td>{{ row.status || '—' }}</td>
                <td class="actions">
                  <button type="button" class="btn btn-sm" @click="openWorkloadNs(row.name)">查看</button>
                </td>
              </template>
              <template v-else-if="detail.kind === 'nodes'">
                <td>{{ row.name }}</td>
                <td>{{ row.status || '—' }}</td>
                <td>{{ row.internalIP || '—' }}</td>
              </template>
              <template v-else-if="detail.fromInspect && (detail.kind === 'badPods' || detail.kind === 'largePods')">
                <td>{{ severityLabel(row.severity) }}</td>
                <td>{{ row.namespace || '—' }}</td>
                <td>{{ row.name }}</td>
                <td>{{ row.status || '—' }}</td>
                <td class="actions">
                  <button
                    v-if="row.namespace"
                    type="button"
                    class="btn btn-sm"
                    @click="openWorkloadNs(row.namespace)"
                  >查看</button>
                  <span v-else class="muted">—</span>
                </td>
              </template>
              <template v-else-if="detail.kind === 'nfsUsage'">
                <td>{{ row.severity ? severityLabel(row.severity) : '—' }}</td>
                <td>{{ row.name || '—' }}</td>
                <td>{{ row.status || row.message || '—' }}</td>
              </template>
              <template v-else>
                <td>{{ row.namespace || '—' }}</td>
                <td>{{ row.name }}</td>
                <td>{{ row.status || '—' }}</td>
                <td class="actions">
                  <button type="button" class="btn btn-sm" @click="openWorkloadNs(row.namespace)">查看</button>
                </td>
              </template>
            </tr>
          </tbody>
        </table>
        <div v-else-if="detail.fromInspect && detail.kind === 'pods'" class="empty">完整 Pod 列表请到「工作负载」查看。</div>
        <div v-else-if="detail.kind === 'largePods'" class="empty">
          {{ cardOf(detail.clusterId).stats.largePodSkipped
            ? `未探测：${cardOf(detail.clusterId).stats.largePodSkipped}`
            : '当前没有超过 5Gi 非持久化占用的 Pod。' }}
        </div>
        <div v-else class="empty">暂无数据。</div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.cluster-list {
  display: flex;
  flex-direction: column;
}
.cluster-card {
  padding: 0;
  overflow: hidden;
}
.cluster-card.is-current {
  border-color: var(--accent);
}
.cluster-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px 16px;
  padding: 16px;
}
.cluster-card-meta {
  min-width: 0;
  flex: 1 1 auto;
}
.cluster-card-title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.35rem 0.5rem;
}
.cluster-server {
  min-width: 0;
  word-break: break-all;
}
.status-line {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.35rem 0.5rem;
  margin-top: 6px;
}
.status-line + .error-clip {
  margin: 4px 0 0;
}
.cluster-card-head .actions {
  flex-shrink: 0;
  margin-top: 0;
}
.cluster-card-head .btn {
  flex-shrink: 0;
  margin-top: 0;
}
.cluster-card .stats-strip {
  margin: 0;
  border-top: 1px solid var(--line);
  border-radius: 0;
}
.error-block {
  margin: 0;
  padding: 12px 16px;
  border-top: 1px solid var(--line);
}
.hint {
  margin: 8px 0 0;
  font-size: 13px;
  color: var(--text);
  line-height: 1.5;
}
.hint code {
  font-family: var(--mono);
  font-size: 12px;
}
.sample-hint {
  margin: 0 0 12px;
}
.stat .value.is-critical {
  color: var(--danger);
}
.summary-table {
  margin-top: 4px;
}
.summary-table th {
  width: 7.5em;
  font-weight: 500;
  color: var(--muted, #6b7a6b);
  vertical-align: middle;
}
.summary-table td {
  vertical-align: middle;
}
.summary-table td.is-critical {
  color: var(--danger);
}
.summary-break {
  word-break: break-all;
}
.toolbar .actions {
  margin-top: 0;
}
</style>
