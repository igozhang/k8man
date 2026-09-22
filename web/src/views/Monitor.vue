<script setup>
import { ref, watch, onMounted, computed } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory, fmtTime, mergeNodeUsage, parseQuantity } from '../utils/quantity'
import { metricsErrorTip } from '../utils/metrics'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { severityLabel, severityClass } from '../utils/labels'
import { issueInspectBrief } from '../utils/inspect'
import {
  TTL,
  cacheFresh,
  cacheGet,
  cacheKey,
  cacheLoad,
  cacheSet,
  cacheInvalidate,
  notifyInspectionsChanged,
  getViewPref,
  setViewPref,
  invalidateDocs
} from '../utils/resourceCache'
import InspectionDrawer from '../components/InspectionDrawer.vue'
import InspectReportDrawer from '../components/InspectReportDrawer.vue'
import InspectRules from '../components/InspectRules.vue'
import FilterSelect from '../components/FilterSelect.vue'
import FilterMultiSelect from '../components/FilterMultiSelect.vue'
import PodNameCell from '../components/PodNameCell.vue'
import { canWrite } from '../stores/opsAuth'

const INSPECT_KEY = cacheKey('inspections', 'local')
const BLOCK_LINES = 3
const SORT_KEYS = new Set(['cpu', 'memory', 'storage'])

const { currentClusterId, currentCluster, refreshClusters } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
/** '' | nodes | pods；兼容旧 pref cpu/memory/storage → pods */
const tab = ref('')
const selectedControllers = ref([])
const controllerOptions = ref([])
const ctrlOptSeq = createSeq()
const podOwnerMap = ref(new Map())
/** 当前排序列；点击同列切换升降序 */
const sortKey = ref('cpu')
const sortDir = ref('desc')
const nodeRows = ref([])
const podMetrics = ref([])
const storageRows = ref([])
const error = ref('')
const nodeTip = ref('')
const podTip = ref('')
const storageTip = ref('')
const nodeLoading = ref(false)
const nodeRefreshing = ref(false)
const podLoading = ref(false)
const podRefreshing = ref(false)
const storageLoading = ref(false)
const storageRefreshing = ref(false)
const nsLoading = ref(false)

const inspectItems = ref(asArray(cacheGet(INSPECT_KEY)?.data?.items))
const inspectListFetchedAt = ref(cacheGet(INSPECT_KEY)?.at || null)
const inspectLoading = ref(false)
const inspectError = ref('')
const inspectingId = ref('')
const inspectTargetId = ref('')
const inspectDrawer = ref(null)
const inspectReportDrawer = ref(null)
const inspectExportPeriod = ref('week')
const inspectExportScope = ref('all')
const inspectExportBusy = ref(false)
const inspectPreviewBusy = ref(false)

const inspecting = computed(() => !!inspectingId.value)

const nodesSeq = createSeq()
const podsSeq = createSeq()
const storageSeq = createSeq()
const inspectSeq = createSeq()
const inspectRunSeq = createSeq()
const inspectReportSeq = createSeq()

const isProject = computed(() => currentCluster.value?.permType === 'project')
const showNodesTab = computed(() => !isProject.value)
const isPodsTab = computed(() => tab.value === 'pods')
const podsBusy = computed(() => podLoading.value || storageLoading.value)
const podsRefreshing = computed(
  () =>
    (podRefreshing.value || storageRefreshing.value) &&
    !podLoading.value &&
    !storageLoading.value
)

function controllerKey(kind, name) {
  return `${kind}/${name}`
}

function shortKind(kind) {
  const k = String(kind || '')
  if (k === 'Deployment') return 'Deploy'
  if (k === 'StatefulSet') return 'STS'
  if (k === 'DaemonSet') return 'Daemon'
  return k
}

function controllerOptionFromRow(r) {
  return {
    value: controllerKey(r.kind, r.name),
    label: `${shortKind(r.kind)} / ${r.name}`
  }
}

const activeControllerFilter = computed(() => {
  const selected = (selectedControllers.value || []).map(String)
  if (!selected.length) return null
  const opts = controllerOptions.value || []
  if (opts.length) {
    const all = new Set(opts.map((o) => o.value))
    if (selected.length === all.size && selected.every((v) => all.has(v))) return null
  }
  return new Set(selected)
})

const filterActive = computed(() => !!activeControllerFilter.value)

function refreshPodOwnerMap(pods) {
  const map = new Map()
  for (const p of asArray(pods)) {
    if (p?.name && p.ownerKind && p.ownerName) {
      map.set(p.name, controllerKey(p.ownerKind, p.ownerName))
    }
  }
  podOwnerMap.value = map
}

function podsMissingOwnerFields(pods) {
  const list = asArray(pods)
  if (!list.length) return false
  return list.every((p) => !p.ownerKind || !p.ownerName)
}

function mapDeployment(d) {
  const name = d.metadata?.name || d.name || ''
  const ready = d.status?.readyReplicas ?? 0
  const desired = d.spec?.replicas ?? d.status?.replicas ?? 0
  return { kind: 'Deployment', name, ready: `${ready}/${desired}` }
}

function mapStatefulSet(ss) {
  const name = ss.metadata?.name || ss.name || ''
  const ready = ss.status?.readyReplicas ?? 0
  const desired = ss.spec?.replicas ?? ss.status?.replicas ?? 0
  return { kind: 'StatefulSet', name, ready: `${ready}/${desired}` }
}

function mapDaemonSet(ds) {
  const name = ds.metadata?.name || ds.name || ''
  const ready = ds.status?.numberReady ?? 0
  const desired = ds.status?.desiredNumberScheduled ?? 0
  return { kind: 'DaemonSet', name, ready: `${ready}/${desired}` }
}

async function loadControllers(clusterId, namespace) {
  const settled = await Promise.allSettled([
    readApi.listDeployments(clusterId, namespace),
    readApi.listStatefulSets(clusterId, namespace),
    readApi.listDaemonSets(clusterId, namespace)
  ])
  const deps = settled[0].status === 'fulfilled' ? asArray(settled[0].value) : []
  const sts = settled[1].status === 'fulfilled' ? asArray(settled[1].value) : []
  const dss = settled[2].status === 'fulfilled' ? asArray(settled[2].value) : []
  const failed = settled.filter((s) => s.status === 'rejected')
  if (failed.length === settled.length) {
    throw failed[0].reason || new Error('控制器列表加载失败')
  }
  const list = [
    ...deps.map(mapDeployment),
    ...sts.map(mapStatefulSet),
    ...dss.map(mapDaemonSet)
  ]
	list.sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name))
  return list.filter((r) => r.name)
}

async function loadControllerOptions({ force = false } = {}) {
  const seq = ctrlOptSeq.next()
  const clusterId = currentClusterId.value
  const namespace = ns.value
  if (!clusterId || !namespace) {
    controllerOptions.value = []
    selectedControllers.value = []
    return
  }
  const key = cacheKey('controllers', clusterId, namespace)
  try {
    const list = asArray(
      await cacheLoad(key, TTL.controllers, () => loadControllers(clusterId, namespace), { force })
    )
    if (!ctrlOptSeq.is(seq)) return
    controllerOptions.value = list.map(controllerOptionFromRow)
    const valid = new Set(controllerOptions.value.map((o) => o.value))
    selectedControllers.value = (selectedControllers.value || []).filter((v) => valid.has(v))
  } catch {
    if (!ctrlOptSeq.is(seq)) return
  }
}

async function ensurePodsForOwnerFilter() {
  if (!filterActive.value) return
  const clusterId = currentClusterId.value
  const namespace = ns.value
  if (!clusterId || !namespace) return
  const key = cacheKey('pods', clusterId, namespace)
  const hit = cacheGet(key)
  const cached = hit ? asArray(hit.data) : []
  const force = podsMissingOwnerFields(cached)
  if (!force && cached.length) {
    refreshPodOwnerMap(cached)
    return
  }
  try {
    const list = asArray(
      await cacheLoad(key, TTL.pods, () => readApi.listPods(clusterId, namespace), { force })
    )
    // 切集群 / NS 后丢弃过期归属表，避免筛错
    if (currentClusterId.value !== clusterId || ns.value !== namespace) return
    refreshPodOwnerMap(list)
  } catch {
    /* 过滤启用时 owner 空 → 表为空，不回退全部 */
  }
}

/** 将 metrics-server 容器行聚合成 Pod 级 CPU/内存 */
function aggregatePodMetricRows(items) {
  const out = []
  for (const m of asArray(items)) {
    const name = m?.metadata?.name || m?.name || ''
    if (!name) continue
    let cpu = 0
    let mem = 0
    let hasCpu = false
    let hasMem = false
    for (const c of asArray(m.containers)) {
      const cCpu = parseQuantity(c?.usage?.cpu, { asCPU: true })
      const cMem = parseQuantity(c?.usage?.memory)
      if (cCpu != null && !Number.isNaN(cCpu)) {
        cpu += cCpu
        hasCpu = true
      }
      if (cMem != null && !Number.isNaN(cMem)) {
        mem += cMem
        hasMem = true
      }
    }
    out.push({
      name,
      cpuCores: hasCpu ? cpu : null,
      memoryBytes: hasMem ? mem : null
    })
  }
  return out
}

function passControllerFilter(podName) {
  const set = activeControllerFilter.value
  if (!set) return true
  const owners = podOwnerMap.value
  if (!owners.size) return false
  const key = owners.get(podName)
  return !!(key && set.has(key))
}

function cmpNullableNum(a, b, dir) {
  const aNull = a == null || Number.isNaN(a)
  const bNull = b == null || Number.isNaN(b)
  if (aNull && bNull) return 0
  if (aNull) return 1
  if (bNull) return -1
  const d = a - b
  if (d === 0) return 0
  return dir === 'asc' ? d : -d
}

const displayPodRows = computed(() => {
  const byName = new Map()
  for (const r of aggregatePodMetricRows(podMetrics.value)) {
    byName.set(r.name, {
      name: r.name,
      cpuCores: r.cpuCores,
      memoryBytes: r.memoryBytes,
      usedBytes: null,
      node: ''
    })
  }
  for (const s of asArray(storageRows.value)) {
    if (!s?.name) continue
    const n = Number(s.usedBytes)
    const usedBytes = s.usedBytes == null || Number.isNaN(n) ? null : n
    const cur = byName.get(s.name)
    if (cur) {
      cur.usedBytes = usedBytes
      cur.node = s.node || cur.node || ''
    } else {
      byName.set(s.name, {
        name: s.name,
        cpuCores: null,
        memoryBytes: null,
        usedBytes,
        node: s.node || ''
      })
    }
  }
  const rows = [...byName.values()].filter((r) => passControllerFilter(r.name))
  const key = SORT_KEYS.has(sortKey.value) ? sortKey.value : 'cpu'
  const dir = sortDir.value === 'asc' ? 'asc' : 'desc'
  const valOf = (r) => {
    if (key === 'memory') return r.memoryBytes
    if (key === 'storage') return r.usedBytes
    return r.cpuCores
  }
  rows.sort((a, b) => {
    const d = cmpNullableNum(valOf(a), valOf(b), dir)
    if (d) return d
    return String(a.name).localeCompare(String(b.name), 'zh')
  })
  return rows
})

function toggleSort(kind) {
  if (!SORT_KEYS.has(kind)) return
  if (sortKey.value === kind) {
    sortDir.value = sortDir.value === 'desc' ? 'asc' : 'desc'
  } else {
    sortKey.value = kind
    sortDir.value = 'desc'
  }
}

function sortArrow(kind) {
  if (sortKey.value !== kind) return ''
  return sortDir.value === 'asc' ? '↑' : '↓'
}

function fmtBytesNum(bytes) {
  if (bytes == null || Number.isNaN(Number(bytes))) return '-'
  return fmtMemory(Number(bytes))
}

function issueRank(severity) {
  if (severity === 'critical') return 0
  if (severity === 'error') return 1
  if (severity === 'warning') return 2
  if (severity === 'info') return 3
  return 2
}

function sortedPreviewIssues(preview) {
  const rows = asArray(preview)
  return [...rows].sort((a, b) => {
    const d = issueRank(a.severity) - issueRank(b.severity)
    if (d) return d
    return String(a.name || '').localeCompare(String(b.name || ''), 'zh')
  })
}

const inspectIssueBlocks = computed(() => {
  const blocks = []
  for (const it of inspectItems.value) {
    const critical = Number(it.critical) || 0
    const errorN = Number(it.error) || 0
    const warning = Number(it.warning) || 0
    if (!critical && !errorN && !warning) continue
    // 提示不当故障刷：卡片与预览只看严重/错误/警告（服务端 preview 也会跳过 info）
    const sorted = sortedPreviewIssues(it.preview).filter((iss) => iss?.severity !== 'info')
    const lines = sorted.slice(0, BLOCK_LINES).map((iss) => ({
      ...iss,
      clusterId: it.clusterId,
      clusterName: it.clusterName
    }))
    const total = critical + errorN + warning
    blocks.push({
      clusterId: it.clusterId,
      clusterName: it.clusterName || it.clusterId,
      checkedAt: it.checkedAt,
      critical,
      error: errorN,
      warning,
      summary: it.summary,
      largePods: it.largePodSkipped ? null : Number(it.largePods) || 0,
      nfsUsagePercent: (() => {
        if (it.nfsSkipped || it.nfsUsagePercent == null || it.nfsUsagePercent === '') return null
        const n = Number(it.nfsUsagePercent)
        return Number.isNaN(n) ? null : n
      })(),
      nfsStorageClass: it.nfsStorageClass || '',
      nfsServer: it.nfsServer || '',
      nfsPath: it.nfsPath || '',
      lines,
      more: Math.max(0, total - lines.length),
      inspecting: inspectingId.value === it.clusterId
    })
  }
  blocks.sort((a, b) => {
    const rank = (x) => (x.critical > 0 ? 0 : x.error > 0 ? 1 : x.warning > 0 ? 2 : 3)
    const ra = rank(a)
    const rb = rank(b)
    if (ra !== rb) return ra - rb
    return String(a.clusterName).localeCompare(String(b.clusterName), 'zh')
  })
  return blocks
})

const inspectListCacheTag = computed(() => {
  if (!inspectItems.value.length) return ''
  if (inspectListRefreshing.value) return '摘要刷新中…'
  const at = inspectListFetchedAt.value
  if (!at) return ''
  return `摘要缓存 · ${fmtInspectTime(new Date(at))}`
})
const inspectCounts = computed(() => {
  let critical = 0
  let errorN = 0
  let warning = 0
  let info = 0
  for (const it of inspectItems.value) {
    critical += Number(it.critical) || 0
    errorN += Number(it.error) || 0
    warning += Number(it.warning) || 0
    info += Number(it.info) || 0
  }
  return { critical, error: errorN, warning, info }
})
const inspectHasRun = computed(() => inspectItems.value.some((it) => !!it.checkedAt))
const inspectListRefreshing = computed(
  () => inspectLoading.value && inspectItems.value.length > 0
)
const inspectMeta = computed(() => {
  const items = inspectItems.value
  const n = items.length
  if (!n) return '暂无 kubeconfig'
  let last = null
  let next = null
  for (const it of items) {
    if (it.checkedAt) {
      const t = new Date(it.checkedAt)
      if (!Number.isNaN(t.getTime()) && (!last || t > last)) last = t
    }
    if (it.nextDue) {
      const t = new Date(it.nextDue)
      if (!Number.isNaN(t.getTime()) && (!next || t < next)) next = t
    }
  }
  const parts = [`${n} 个配置`]
  if (last) parts.push(`上次 ${fmtInspectTime(last)}`)
  if (next) parts.push(`下次 ${fmtInspectTime(next)}`)
  const { critical, error: errorN, warning, info } = inspectCounts.value
  if (critical || errorN || warning) {
    const bits = []
    if (critical) bits.push(`${critical} 严重`)
    if (errorN) bits.push(`${errorN} 错误`)
    if (warning) bits.push(`${warning} 警告`)
    parts.push(bits.join(' / '))
  } else if (info) {
    parts.push(`${info} 提示`)
  } else if (last) {
    parts.push('全部正常')
  } else {
    parts.push('尚未巡检')
  }
  if (inspectListRefreshing.value) parts.push('刷新中')
  return parts.join(' · ')
})

function fmtInspectTime(t) {
  const d = t instanceof Date ? t : new Date(t)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function issueLineBrief(row) {
  return issueInspectBrief(row)
}

function nfsTagTitle(block) {
  const parts = []
  const server = String(block?.nfsServer || '').trim()
  const path = String(block?.nfsPath || '').trim()
  if (server) {
    parts.push(path ? (path.startsWith('/') ? `${server}:${path}` : `${server}:/${path}`) : server)
  }
  if (block?.nfsStorageClass) parts.push(`SC=${block.nfsStorageClass}`)
  return parts.join(' · ')
}

function blockCacheTag(block) {
  if (block.inspecting) return '实时巡检中…'
  if (block.checkedAt) return `缓存 · ${fmtInspectTime(block.checkedAt)}`
  return '尚未巡检'
}

function applyInspectList(data, fetchedAt) {
  inspectItems.value = asArray(data?.items)
  if (fetchedAt) inspectListFetchedAt.value = fetchedAt
}

async function loadInspections({ force = false } = {}) {
  const seq = inspectSeq.next()
  inspectError.value = ''
  const hit = cacheGet(INSPECT_KEY)
  if (hit?.data) applyInspectList(hit.data, hit.at)
  if (!force && cacheFresh(INSPECT_KEY, TTL.inspections)) {
    inspectLoading.value = false
    return
  }
  inspectLoading.value = !inspectItems.value.length
  try {
    const data = await cacheLoad(INSPECT_KEY, TTL.inspections, () => readApi.listInspections(), { force })
    if (!inspectSeq.is(seq)) return
    const fresh = cacheGet(INSPECT_KEY)
    applyInspectList(data, fresh?.at || Date.now())
    notifyInspectionsChanged()
  } catch (e) {
    if (!inspectSeq.is(seq)) return
    inspectError.value = errMessage(e)
    if (!hit) inspectItems.value = []
  } finally {
    if (inspectSeq.is(seq)) inspectLoading.value = false
  }
}

function detailFromListItem(item, clusterId, clusterName) {
  return {
    clusterId,
    clusterName: item?.clusterName || clusterName || '',
    checkedAt: item?.checkedAt,
    summary: item?.summary || '',
    probeStatus: item?.probeStatus,
    probeError: item?.probeError,
    issues: asArray(item?.preview),
    elapsedMs: item?.elapsedMs
  }
}

function clusterLabel(clusterId) {
  const item = inspectItems.value.find((it) => it.clusterId === clusterId)
  return item?.clusterName || currentCluster.value?.name || ''
}

async function runLiveInspect(clusterId, { openDrawer = true } = {}) {
  if (!canWrite.value || !clusterId) return
  if (inspectingId.value) {
    inspectError.value = '已有巡检进行中，请稍候'
    return
  }
  const seq = inspectRunSeq.next()
  inspectingId.value = clusterId
  inspectError.value = ''

  const item = inspectItems.value.find((it) => it.clusterId === clusterId)
  const hasCache = !!item?.checkedAt
  let liveDone = false

  if (openDrawer) {
    inspectDrawer.value?.open(detailFromListItem(item, clusterId, clusterLabel(clusterId)), {
      source: hasCache ? 'cache' : 'none',
      running: true
    })
  }

  readApi.getInspection(clusterId).then((data) => {
    if (!inspectRunSeq.is(seq) || liveDone) return
    if (openDrawer) {
      inspectDrawer.value?.open(
        {
          ...data,
          issues: asArray(data?.issues).length ? data.issues : asArray(data?.preview)
        },
        { source: 'cache', running: true }
      )
    }
  }).catch(() => {})

  try {
    const data = await writeApi.inspectCluster(clusterId)
    if (!inspectRunSeq.is(seq)) return
    liveDone = true
    cacheInvalidate(INSPECT_KEY)
    if (openDrawer) {
      inspectDrawer.value?.updateLive({
        ...data,
        issues: asArray(data?.issues).length ? data.issues : asArray(data?.preview)
      })
    }
    await Promise.all([
      loadInspections({ force: true }),
      refreshClusters(() => readApi.listClusters(), { force: true }).catch(() => {})
    ])
  } catch (e) {
    if (!inspectRunSeq.is(seq)) return
    liveDone = true
    const msg = errMessage(e)
    inspectError.value = msg
    if (openDrawer) {
      inspectDrawer.value?.setRunning(false, msg)
    }
  } finally {
    if (inspectRunSeq.is(seq)) inspectingId.value = ''
  }
}

async function openInspectDetail(clusterId) {
  if (!clusterId) return
  if (canWrite.value) {
    await runLiveInspect(clusterId, { openDrawer: true })
    return
  }
  const item = inspectItems.value.find((it) => it.clusterId === clusterId)
  inspectDrawer.value?.open(detailFromListItem(item, clusterId, clusterLabel(clusterId)), {
    source: item?.checkedAt ? 'cache' : 'none',
    running: false
  })
  try {
    const data = await readApi.getInspection(clusterId)
    inspectDrawer.value?.open(
      {
        ...data,
        issues: asArray(data?.issues).length ? data.issues : asArray(data?.preview)
      },
      { source: 'cache', running: false }
    )
  } catch {
    /* 无缓存时抽屉已展示空快照 */
  }
}

async function runInspect() {
  const targetId = inspectTargetId.value
  if (!targetId) return
  await runLiveInspect(targetId, { openDrawer: true })
}

function inspectExportParams() {
  const period = inspectExportPeriod.value || 'week'
  const scope = inspectExportScope.value || 'all'
  const clusterId = scope === 'current' ? inspectTargetId.value : ''
  return { period, scope, clusterId }
}

function downloadMarkdownFile(filename, text) {
  const raw = String(text || '')
  if (!raw.trim()) {
    throw new Error('报告内容为空，未下载')
  }
  let name = String(filename || 'k8man-inspect.md').replace(/[<>:"/\\|?*\x00-\x1f]/g, '_')
  if (!name.toLowerCase().endsWith('.md')) name += '.md'
  const blob = new Blob([raw], { type: 'text/markdown;charset=utf-8' })
  const obj = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = obj
    a.download = name
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(obj)
  }
}

async function previewInspectReport() {
  if (inspectPreviewBusy.value || inspectExportBusy.value) return
  const { period, scope, clusterId } = inspectExportParams()
  if (scope === 'current' && !clusterId) {
    inspectError.value = '请先选择 kubeconfig（当前基地）'
    return
  }
  inspectError.value = ''
  const seq = inspectReportSeq.next()
  inspectPreviewBusy.value = true
  inspectReportDrawer.value?.open({
    title: '巡检报告预览',
    loading: true,
    markdown: '',
    summary: '',
    docId: '',
    error: ''
  })
  try {
    const data = await readApi.previewInspectionReport({ period, scope, clusterId })
    if (!inspectReportSeq.is(seq)) return
    inspectReportDrawer.value?.update({
      title: data?.title || '巡检报告预览',
      markdown: data?.markdown || '',
      summary: data?.summary || '',
      loading: false,
      error: ''
    })
  } catch (e) {
    if (!inspectReportSeq.is(seq)) return
    const msg = errMessage(e)
    inspectError.value = msg
    inspectReportDrawer.value?.update({ loading: false, error: msg })
  } finally {
    if (inspectReportSeq.is(seq)) inspectPreviewBusy.value = false
  }
}

async function exportInspectReport() {
  if (!canWrite.value) {
    inspectError.value = '导出需运维写权限（将同步到运维文档「巡检报告」）'
    return
  }
  if (inspectExportBusy.value || inspectPreviewBusy.value) return
  const { period, scope, clusterId } = inspectExportParams()
  if (scope === 'current' && !clusterId) {
    inspectError.value = '请先选择 kubeconfig（当前基地）'
    return
  }
  inspectError.value = ''
  const seq = inspectReportSeq.next()
  inspectExportBusy.value = true
  inspectReportDrawer.value?.open({
    title: '正在导出巡检报告…',
    loading: true,
    exporting: true,
    summary: '生成 Markdown 并同步到运维文档…',
    error: '',
    docId: ''
  })
  try {
    const data = await writeApi.exportInspectionReport({ period, scope, clusterId })
    if (!inspectReportSeq.is(seq)) return
    try {
      downloadMarkdownFile(data?.filename, data?.markdown)
    } catch (dlErr) {
      // 文档已写入，下载失败仍展示结果
      inspectError.value = errMessage(dlErr)
    }
    if (data?.doc?.id) invalidateDocs(data.doc.id)
    inspectReportDrawer.value?.open({
      title: data?.title || '巡检报告',
      markdown: data?.markdown || '',
      summary: (data?.summary || '') + (data?.doc?.id ? ' · 已同步到运维文档' : ''),
      docId: data?.doc?.id || '',
      loading: false,
      exporting: false,
      error: inspectError.value || ''
    })
  } catch (e) {
    if (!inspectReportSeq.is(seq)) return
    const msg = errMessage(e)
    inspectError.value = msg
    inspectReportDrawer.value?.update({ loading: false, exporting: false, error: msg })
  } finally {
    if (inspectReportSeq.is(seq)) {
      inspectExportBusy.value = false
      inspectReportDrawer.value?.update({ exporting: false, loading: false })
    }
  }
}

function resetView() {
  nodesSeq.bump()
  podsSeq.bump()
  storageSeq.bump()
  ctrlOptSeq.bump()
  nodeRows.value = []
  podMetrics.value = []
  storageRows.value = []
  nodeTip.value = ''
  podTip.value = ''
  storageTip.value = ''
  nodeLoading.value = false
  nodeRefreshing.value = false
  podLoading.value = false
  podRefreshing.value = false
  storageLoading.value = false
  storageRefreshing.value = false
  nsLoading.value = false
  namespaces.value = []
  ns.value = ''
  tab.value = ''
  selectedControllers.value = []
  controllerOptions.value = []
  podOwnerMap.value = new Map()
  error.value = ''
}

function normalizeMonitorTab(raw) {
  if (raw === 'cpu' || raw === 'memory' || raw === 'storage' || raw === 'pods') return 'pods'
  if (raw === 'nodes') return 'nodes'
  return ''
}

function applyMonitorPref() {
  const pref = getViewPref('monitor', currentClusterId.value)
  ns.value = pref?.ns || ''
  const next = normalizeMonitorTab(pref?.tab)
  if (next === 'nodes' && showNodesTab.value) tab.value = 'nodes'
  else if (next === 'pods') tab.value = 'pods'
  else tab.value = showNodesTab.value ? 'nodes' : 'pods'
}

function persistMonitorPref() {
  if (!currentClusterId.value) return
  setViewPref('monitor', currentClusterId.value, { tab: tab.value, ns: ns.value })
}

async function ensureNamespaces({ force = false } = {}) {
  if (!currentClusterId.value) return
  const clusterId = currentClusterId.value
  const key = cacheKey('namespaces', clusterId)
  const hit = cacheGet(key)
  if (hit) {
    namespaces.value = asArray(hit.data)
    if (ns.value && !namespaces.value.some((n) => n.name === ns.value)) ns.value = ''
  }
  if (!force && cacheFresh(key, TTL.namespaces)) return
  nsLoading.value = !namespaces.value.length
  try {
    const list = asArray(
      await cacheLoad(key, TTL.namespaces, () => readApi.listNamespaces(clusterId), { force })
    )
    if (currentClusterId.value !== clusterId) return
    namespaces.value = list
    if (ns.value && !list.some((n) => n.name === ns.value)) ns.value = ''
  } catch (e) {
    if (currentClusterId.value !== clusterId) return
    error.value = errMessage(e)
    if (!hit) namespaces.value = []
  } finally {
    nsLoading.value = false
  }
}

async function loadNodes({ force = false } = {}) {
  const seq = nodesSeq.next()
  nodeTip.value = ''
  if (!currentClusterId.value || isProject.value) {
    nodeRows.value = []
    nodeLoading.value = false
    nodeRefreshing.value = false
    return
  }
  const key = cacheKey('nodeUsage', currentClusterId.value)
  const hit = cacheGet(key)
  if (hit) nodeRows.value = asArray(hit.data)
  else nodeRows.value = []
  if (!force && cacheFresh(key, TTL.nodeUsage)) {
    nodeLoading.value = false
    nodeRefreshing.value = false
    return
  }
  nodeLoading.value = !nodeRows.value.length
  nodeRefreshing.value = nodeRows.value.length > 0
  try {
    const id = currentClusterId.value
    const [nodes, metrics] = await Promise.all([
      cacheLoad(cacheKey('nodes', id), TTL.nodes, () => readApi.listNodes(id), { force }),
      readApi.nodeMetrics(id).catch((e) => {
        if (nodesSeq.is(seq)) nodeTip.value = metricsErrorTip(e)
        return []
      })
    ])
    if (!nodesSeq.is(seq)) return
    const list = asArray(nodes)
    const merged = mergeNodeUsage(list, asArray(metrics))
    cacheSet(key, merged)
    nodeRows.value = merged
  } catch (e) {
    if (!nodesSeq.is(seq)) return
    nodeTip.value = errMessage(e)
    if (!hit) nodeRows.value = []
  } finally {
    if (nodesSeq.is(seq)) {
      nodeLoading.value = false
      nodeRefreshing.value = false
    }
  }
}

async function loadPodMetrics({ force = false } = {}) {
  const seq = podsSeq.next()
  podTip.value = ''
  if (!currentClusterId.value || !ns.value) {
    podMetrics.value = []
    podLoading.value = false
    podRefreshing.value = false
    return
  }
  const id = currentClusterId.value
  const namespace = ns.value
  const key = cacheKey('podMetrics', id, namespace)
  const hit = cacheGet(key)
  if (hit) podMetrics.value = asArray(hit.data)
  else podMetrics.value = []
  if (!force && cacheFresh(key, TTL.podMetrics)) {
    podLoading.value = false
    podRefreshing.value = false
    return
  }
  podLoading.value = !podMetrics.value.length
  podRefreshing.value = podMetrics.value.length > 0
  try {
    const items = asArray(
      await cacheLoad(key, TTL.podMetrics, () => readApi.podMetrics(id, namespace), { force })
    )
    if (!podsSeq.is(seq)) return
    podMetrics.value = items
  } catch (e) {
    if (!podsSeq.is(seq)) return
    podTip.value = metricsErrorTip(e)
    if (!hit) podMetrics.value = []
  } finally {
    if (podsSeq.is(seq)) {
      podLoading.value = false
      podRefreshing.value = false
    }
  }
}

function storageSkipTip(skipped) {
  const s = String(skipped || '').trim()
  if (!s) return ''
  if (s === 'project credential') return 'Project 凭证无法读取节点 kubelet summary，存储用量不可用。'
  if (s === 'timeout budget exhausted') return '读取存储用量超时，请稍后重试。'
  if (s === 'no kubelet summary') return '无法获取 kubelet summary（权限或节点代理不可用）。'
  if (s === 'clients not ready') return '集群客户端未就绪。'
  if (s === 'namespace required') return '请选择命名空间。'
  return s
}

/** 超时等瞬时失败不要占满 TTL，否则短时间重试仍看到旧 skip */
function isTransientStorageSkip(skipped) {
  const s = String(skipped || '')
  return s === 'timeout budget exhausted' || /timeout/i.test(s)
}

async function loadStorageMetrics({ force = false } = {}) {
  const seq = storageSeq.next()
  storageTip.value = ''
  if (!currentClusterId.value || !ns.value) {
    storageRows.value = []
    storageLoading.value = false
    storageRefreshing.value = false
    return
  }
  if (isProject.value) {
    storageRows.value = []
    storageTip.value = storageSkipTip('project credential')
    storageLoading.value = false
    storageRefreshing.value = false
    return
  }
  const id = currentClusterId.value
  const namespace = ns.value
  const key = cacheKey('podStorageMetrics', id, namespace)
  const hit = cacheGet(key)
  if (hit) {
    const data = hit.data || {}
    storageRows.value = asArray(data.items)
    if (data.skipped) storageTip.value = storageSkipTip(data.skipped)
    else if (data.truncated) storageTip.value = '部分节点 summary 未取到，列表可能不完整。'
  } else {
    storageRows.value = []
  }
  // 瞬时 skip 的缓存不当作新鲜命中，允许立刻重试
  if (!force && cacheFresh(key, TTL.podStorageMetrics) && !isTransientStorageSkip(hit?.data?.skipped)) {
    storageLoading.value = false
    storageRefreshing.value = false
    return
  }
  storageLoading.value = !storageRows.value.length
  storageRefreshing.value = storageRows.value.length > 0
  try {
    const data = await cacheLoad(
      key,
      TTL.podStorageMetrics,
      async () => {
        const raw = await readApi.podStorageMetrics(id, namespace)
        return {
          items: asArray(raw?.items),
          skipped: raw?.skipped || '',
          truncated: !!raw?.truncated
        }
      },
      { force: force || isTransientStorageSkip(hit?.data?.skipped) }
    )
    if (!storageSeq.is(seq)) return
    storageRows.value = asArray(data?.items)
    if (data?.skipped) {
      storageTip.value = storageSkipTip(data.skipped)
    } else if (data?.truncated) {
      storageTip.value = '部分节点 summary 未取到，列表可能不完整。'
    } else {
      storageTip.value = ''
    }
    if (isTransientStorageSkip(data?.skipped)) {
      cacheInvalidate(key)
    }
  } catch (e) {
    if (!storageSeq.is(seq)) return
    storageTip.value = errMessage(e)
    if (!hit) storageRows.value = []
    cacheInvalidate(key)
  } finally {
    if (storageSeq.is(seq)) {
      storageLoading.value = false
      storageRefreshing.value = false
    }
  }
}

function stopPodLoads() {
  podsSeq.bump()
  storageSeq.bump()
  podLoading.value = false
  podRefreshing.value = false
  storageLoading.value = false
  storageRefreshing.value = false
}

async function loadActivePodTab() {
  if (tab.value !== 'pods' || !ns.value) return
  const clusterId = currentClusterId.value
  const namespace = ns.value
  await ensureNamespaces()
  if (currentClusterId.value !== clusterId || ns.value !== namespace || tab.value !== 'pods') return
  loadControllerOptions()
  await Promise.all([loadPodMetrics(), loadStorageMetrics(), ensurePodsForOwnerFilter()])
}

function selectTab(next) {
  if (next === 'nodes' && !showNodesTab.value) return
  tab.value = next
  persistMonitorPref()
  if (next === 'nodes') {
    stopPodLoads()
    loadNodes()
    return
  }
  if (next === 'pods') {
    ensureNamespaces().then(() => {
      if (tab.value !== 'pods') return
      if (ns.value) loadActivePodTab()
    })
  }
}

function clearMetricViews() {
  stopPodLoads()
  nodesSeq.bump()
  nodeRows.value = []
  podMetrics.value = []
  storageRows.value = []
  nodeTip.value = ''
  podTip.value = ''
  storageTip.value = ''
  nodeLoading.value = false
  nodeRefreshing.value = false
  selectedControllers.value = []
  controllerOptions.value = []
  podOwnerMap.value = new Map()
}

function enterPage() {
  error.value = ''
  loadInspections()
  if (!currentClusterId.value) {
    resetView()
    return
  }
  // 切集群时先清掉上一集群的表数据，避免短暂串显
  clearMetricViews()
  applyMonitorPref()
  persistMonitorPref()
  if (!currentCluster.value) return
  if (tab.value === 'nodes') {
    if (!showNodesTab.value) {
      tab.value = 'pods'
      persistMonitorPref()
      ensureNamespaces().then(() => {
        if (ns.value) loadActivePodTab()
      })
      return
    }
    loadNodes()
    return
  }
  if (tab.value === 'pods') {
    ensureNamespaces().then(() => {
      if (ns.value) loadActivePodTab()
    })
  }
}

watch(currentClusterId, enterPage)
watch(
  () => currentCluster.value?.permType,
  () => {
    if (!currentClusterId.value || !currentCluster.value) return
    if (isProject.value) {
      if (tab.value === 'nodes') {
        nodesSeq.bump()
        nodeLoading.value = false
        nodeRefreshing.value = false
        tab.value = 'pods'
        persistMonitorPref()
        ensureNamespaces().then(() => {
          if (ns.value) loadActivePodTab()
        })
      } else if (tab.value === 'pods') {
        // Project 仍可看 CPU/内存；存储软跳过
        if (ns.value) loadStorageMetrics()
      }
      return
    }
    if (!tab.value) {
      tab.value = 'nodes'
      persistMonitorPref()
    }
    // 集群列表晚于 clusterId 就绪时，补拉当前 Tab
    if (tab.value === 'nodes') loadNodes()
    else if (tab.value === 'pods' && ns.value) loadActivePodTab()
  }
)
watch(ns, (v) => {
  persistMonitorPref()
  selectedControllers.value = []
  controllerOptions.value = []
  podOwnerMap.value = new Map()
  if (!isPodsTab.value) return
  if (v) {
    loadControllerOptions()
    loadActivePodTab()
  } else {
    stopPodLoads()
    podMetrics.value = []
    storageRows.value = []
    podTip.value = ''
    storageTip.value = ''
  }
})
watch(selectedControllers, () => {
  if (!isPodsTab.value || !ns.value) return
  ensurePodsForOwnerFilter()
})
watch(inspectItems, (items) => {
  if (inspectTargetId.value && !items.some((it) => it.clusterId === inspectTargetId.value)) {
    inspectTargetId.value = ''
  }
})
onMounted(enterPage)
</script>


<template>
  <div>
    <h1>监控</h1>
    <p class="page-desc">
      节点列表含状态、IP、版本与 CPU / 内存（可分配 / 已使用，依赖 metrics-server）。PODs：CPU / 内存按 Pod 聚合容器用量；存储为非持久化（ephemeral）用量，来源与巡检「大 POD」相同（kubelet summary）。需选择命名空间，并可按控制器筛选。CPU / 内存 / 存储表头可点击排序（默认从大到小）。未安装 metrics-server 或权限不足时降级提示。
    </p>
    <div v-if="!currentClusterId" class="empty">请先在顶部选择当前集群，或到「概览」里点选用。</div>
    <template v-else>
      <p v-if="error" class="error">{{ error }}</p>

      <div class="tabs">
        <button
          v-if="showNodesTab"
          class="tab"
          :class="{ active: tab === 'nodes' }"
          @click="selectTab('nodes')"
        >节点</button>
        <button
          class="tab"
          :class="{ active: tab === 'pods' }"
          @click="selectTab('pods')"
        >PODs</button>
      </div>

      <div v-if="!tab" class="empty">请选择「PODs」并指定命名空间。</div>

      <template v-else-if="tab === 'nodes' && showNodesTab">
        <p v-if="nodeTip" class="muted">{{ nodeTip }}</p>
        <p v-if="nodeLoading" class="muted">加载中…</p>
        <p v-else-if="nodeRefreshing" class="muted">更新中…</p>
        <table v-if="nodeRows.length" class="table">
          <thead>
            <tr>
              <th>名称</th>
              <th>状态</th>
              <th>IP</th>
              <th>版本</th>
              <th>CPU（可分配 / 已使用）</th>
              <th>内存（可分配 / 已使用）</th>
              <th>时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in nodeRows" :key="n.name">
              <td>{{ n.name }}</td>
              <td>{{ n.status }}</td>
              <td>{{ n.internalIP }}</td>
              <td>{{ n.version }}</td>
              <td class="res-cell">
                <span>{{ fmtCPU(n.allocatableCPU) }}</span>
                <span class="muted"> / </span>
                <span>{{ fmtCPU(n.usedCPU) }}</span>
              </td>
              <td class="res-cell">
                <span>{{ fmtMemory(n.allocatableMemory) }}</span>
                <span class="muted"> / </span>
                <span>{{ fmtMemory(n.usedMemory) }}</span>
              </td>
              <td class="muted">{{ fmtTime(n.metricsTime) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!nodeLoading" class="muted">
          {{ nodeTip ? '无法加载节点数据。' : '暂无节点。' }}
        </div>
      </template>

      <template v-else-if="isPodsTab">
        <div class="toolbar toolbar-start" style="margin-bottom:12px">
          <FilterSelect
            v-model="ns"
            :options="namespaces"
            empty-label="请选择命名空间"
            placeholder="输入 1–3 个字母筛选命名空间"
            aria-label="选择命名空间"
          />
          <FilterMultiSelect
            v-if="ns"
            v-model="selectedControllers"
            :options="controllerOptions"
            all-label="控制器 · 全部"
            empty-label="控制器未筛选（全部）"
            placeholder="筛选 Deploy / STS / Daemon"
            aria-label="按控制器筛选"
          />
        </div>
        <p v-if="nsLoading" class="muted">加载命名空间列表…</p>
        <div v-else-if="!ns" class="empty">请选择命名空间后再加载指标。</div>
        <template v-else>
          <p v-if="podTip" class="muted">{{ podTip }}</p>
          <p v-if="storageTip" class="muted">{{ storageTip }}</p>
          <p v-if="podsBusy" class="muted">加载中…</p>
          <p v-else-if="podsRefreshing" class="muted">更新中…</p>
          <table v-if="displayPodRows.length" class="table">
            <thead>
              <tr>
                <th>Pod</th>
                <th>
                  <button
                    type="button"
                    class="th-sort"
                    :aria-sort="sortKey === 'cpu' ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                    @click="toggleSort('cpu')"
                  >CPU {{ sortArrow('cpu') }}</button>
                </th>
                <th>
                  <button
                    type="button"
                    class="th-sort"
                    :aria-sort="sortKey === 'memory' ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                    @click="toggleSort('memory')"
                  >内存 {{ sortArrow('memory') }}</button>
                </th>
                <th>
                  <button
                    type="button"
                    class="th-sort"
                    :aria-sort="sortKey === 'storage' ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                    @click="toggleSort('storage')"
                  >存储 {{ sortArrow('storage') }}</button>
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in displayPodRows" :key="r.name">
                <td><PodNameCell :name="r.name" /></td>
                <td>{{ r.cpuCores == null ? '-' : fmtCPU(r.cpuCores) }}</td>
                <td>{{ r.memoryBytes == null ? '-' : fmtMemory(r.memoryBytes) }}</td>
                <td :title="r.node ? `节点 ${r.node}` : undefined">{{ fmtBytesNum(r.usedBytes) }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else-if="!podsBusy && (filterActive || (!podTip && !storageTip))" class="muted">
            {{ filterActive ? '当前控制器筛选下无 Pod。' : '暂无 Pod 指标数据。' }}
          </div>
        </template>
      </template>
    </template>

    <section class="panel inspect-box">
      <div class="toolbar">
        <div class="inspect-head">
          <strong>巡检</strong>
          <span v-if="inspectListCacheTag" class="tag inspect-cache inspect-list-cache-tag">{{ inspectListCacheTag }}</span>
          <span class="muted inspect-meta">{{ inspectMeta }}</span>
        </div>
      </div>
      <p class="inspect-intro muted">
        对每个 kubeconfig 约 2 小时一轮。后台<strong>有界并发</strong>（默认同时 4 路，积压可扩至 8）；两次成功开始至少间隔约 2 秒；同一集群同时只跑一轮。每轮先 Probe（同 KubeConfig「测试」）并回写状态，通过后再扫描节点、负载、Pod 与事件（Project 凭证跳过节点）。定级见下方「基线与降级机制」（含 Restarts）。下方按集群分块展示摘要（每块最多 3 条，只含严重/错误/警告；仅有提示时不单独成块）；点击块在右侧抽屉查看该配置详情<template v-if="canWrite">并触发实时巡检</template>。「预览 / 导出巡检报告」基于各基地<strong>最新快照</strong>生成 Markdown（按日/周/月窗口筛 `sinceAt`）；导出需写权限，并同步一份到运维文档「巡检报告」。
      </p>
      <div class="toolbar toolbar-start inspect-actions">
        <select v-model="inspectTargetId" aria-label="巡检目标 kubeconfig">
          <option value="">选择 kubeconfig</option>
          <option v-for="it in inspectItems" :key="it.clusterId" :value="it.clusterId">
            {{ it.clusterName }}
          </option>
        </select>
        <div class="actions">
          <button
            v-if="canWrite"
            class="btn btn-sm"
            type="button"
            :disabled="!inspectTargetId || inspecting"
            :title="inspectTargetId ? '先展示缓存并立即实时巡检' : '请先选择 kubeconfig'"
            @click="runInspect"
          >{{ inspecting ? '巡检中…' : '一键巡检' }}</button>
          <button
            class="btn btn-sm"
            type="button"
            :disabled="(!inspectTargetId && !inspectIssueBlocks.length) || inspecting"
            @click="openInspectDetail(inspectTargetId || inspectIssueBlocks[0]?.clusterId)"
          >详情</button>
          <select
            v-model="inspectExportPeriod"
            class="inspect-export-select"
            aria-label="报告周期"
            title="报告周期（基于最新快照，按问题 sinceAt 筛入窗口）"
          >
            <option value="day">日</option>
            <option value="week">周</option>
            <option value="month">月</option>
          </select>
          <select
            v-model="inspectExportScope"
            class="inspect-export-select"
            aria-label="报告范围"
            title="当前基地 = 左侧所选 kubeconfig"
          >
            <option value="all">所有基地</option>
            <option value="current">当前基地</option>
          </select>
          <button
            class="btn btn-sm"
            type="button"
            :disabled="inspectPreviewBusy || inspectExportBusy || (inspectExportScope === 'current' && !inspectTargetId)"
            :title="inspectExportScope === 'current' && !inspectTargetId ? '请先选择 kubeconfig' : '预览 Markdown 报告'"
            @click="previewInspectReport"
          >{{ inspectPreviewBusy ? '生成中…' : '预览' }}</button>
          <button
            class="btn btn-sm"
            type="button"
            :disabled="!canWrite || inspectExportBusy || inspectPreviewBusy || (inspectExportScope === 'current' && !inspectTargetId)"
            :title="!canWrite
              ? '导出需运维写权限（将同步到运维文档）'
              : (inspectExportScope === 'current' && !inspectTargetId ? '请先选择 kubeconfig' : '下载 MD 并同步到运维文档「巡检报告」')"
            @click="exportInspectReport"
          >{{ inspectExportBusy ? '导出中…' : '导出巡检报告' }}</button>
        </div>
      </div>
      <p v-if="inspectError" class="error">{{ inspectError }}</p>
      <p v-if="inspectLoading && !inspectItems.length" class="muted">加载巡检结果…</p>
      <div v-else-if="!inspectItems.length && !inspectError" class="muted">暂无已导入的 kubeconfig。</div>
      <div v-else-if="!inspectIssueBlocks.length && inspectItems.length" class="muted">
        <template v-if="!inspectHasRun">{{ canWrite ? '尚未巡检。后台约两小时一轮，也可选择配置后一键巡检。' : '尚未巡检。后台约两小时一轮。' }}</template>
        <template v-else-if="inspectCounts.info">无严重 / 错误 / 警告；仅有 {{ inspectCounts.info }} 提示（不当故障刷，点「详情」可看）。</template>
        <template v-else>全部配置巡检正常。</template>
      </div>
      <div v-else-if="inspectIssueBlocks.length" class="inspect-cluster-blocks">
        <article
          v-for="block in inspectIssueBlocks"
          :key="block.clusterId"
          class="inspect-cluster-block"
          :class="{ 'inspect-cluster-block-active': block.inspecting }"
        >
          <button
            type="button"
            class="inspect-cluster-block-hit"
            @click="openInspectDetail(block.clusterId)"
          >
            <div class="inspect-cluster-block-head">
              <strong>{{ block.clusterName }}</strong>
              <span class="tag" :class="block.inspecting ? 'inspect-running' : 'inspect-cache'">{{ blockCacheTag(block) }}</span>
              <span v-if="block.critical" class="tag sev-critical">{{ block.critical }} 严重</span>
              <span v-if="block.error" class="tag sev-error">{{ block.error }} 错误</span>
              <span v-if="block.warning" class="tag sev-warning">{{ block.warning }} 警告</span>
              <span
                v-if="block.largePods != null && block.largePods > 0"
                class="tag sev-warning"
              >大 POD {{ block.largePods }}</span>
              <span
                v-if="block.nfsUsagePercent != null"
                class="tag"
                :class="block.nfsUsagePercent >= 80 ? 'sev-critical' : 'sev-info'"
                :title="nfsTagTitle(block)"
              >NFS {{ Math.round(block.nfsUsagePercent) }}%</span>
            </div>
            <ul class="inspect-lines">
              <li v-for="(row, idx) in block.lines" :key="idx">
                <div class="inspect-line inspect-line-static">
                  <span class="tag" :class="severityClass(row.severity)">
                    {{ severityLabel(row.severity) }}
                  </span>
                  <span class="inspect-line-text">{{ issueLineBrief(row) }}</span>
                </div>
              </li>
            </ul>
            <span v-if="block.more > 0" class="inspect-more">还有 {{ block.more }} 项，点击查看详情</span>
          </button>
        </article>
      </div>

      <InspectRules class="is-split" title="基线与降级机制" />
    </section>
    <InspectionDrawer ref="inspectDrawer" />
    <InspectReportDrawer
      ref="inspectReportDrawer"
      :can-export="canWrite"
      @export="exportInspectReport"
    />
  </div>
</template>
