<script setup>
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory, fmtTime, mergeNodeUsage, parseQuantity } from '../utils/quantity'
import { metricsErrorTip } from '../utils/metrics'
import { asArray, createSeq, errMessage } from '../utils/safe'
import {
  TTL,
  cacheFresh,
  cacheGet,
  cacheKey,
  cacheLoad,
  cacheSet,
  cacheInvalidate,
  getViewPref,
  invalidateNsLists,
  setViewPref
} from '../utils/resourceCache'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import PodExecDrawer from '../components/PodExecDrawer.vue'
import YamlDrawer from '../components/YamlDrawer.vue'
import ConfigMapEditor from '../components/ConfigMapEditor.vue'
import FilterSelect from '../components/FilterSelect.vue'
import FilterMultiSelect from '../components/FilterMultiSelect.vue'
import PodNameCell from '../components/PodNameCell.vue'
import { canWrite } from '../stores/opsAuth'

const WORKLOAD_TABS = new Set(['pods', 'controllers', 'services', 'configmaps', 'metrics', 'nodes'])
const NODE_SORT_KEYS = new Set(['name', 'status', 'ip', 'cpu', 'memory'])
const METRIC_SORT_KEYS = new Set(['cpu', 'memory', 'storage'])

const route = useRoute()
const router = useRouter()
const { currentClusterId, currentCluster } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
/** pods | controllers | services | configmaps | metrics | nodes */
const tab = ref('pods')
/** 控制器多选：'Deployment/name'；空=不过滤 */
const selectedControllers = ref([])
const controllerOptions = ref([])
const ctrlOptSeq = createSeq()
const rows = ref([])
const podMetrics = ref([])
const metricsTip = ref('')
const storageRows = ref([])
const storageTip = ref('')
const storageLoading = ref(false)
const storageRefreshing = ref(false)
const metricSortKey = ref('cpu')
const metricSortDir = ref('desc')
const nodeRows = ref([])
const nodeTip = ref('')
const nodeLoading = ref(false)
const nodeRefreshing = ref(false)
const nodeSortKey = ref('name')
const nodeSortDir = ref('asc')
const loading = ref(false)
const refreshing = ref(false)
const error = ref('')
const notice = ref('')
const rowsSeq = createSeq()
const nsSeq = createSeq()
const nodesSeq = createSeq()
const storageSeq = createSeq()

const isProject = computed(() => currentCluster.value?.permType === 'project')
const showNodesTab = computed(() => !isProject.value)
const isNodesTab = computed(() => tab.value === 'nodes')
const isMetricsTab = computed(() => tab.value === 'metrics')
const metricsBusy = computed(() => loading.value || storageLoading.value)
const metricsRefreshing = computed(
  () =>
    (refreshing.value || storageRefreshing.value) &&
    !loading.value &&
    !storageLoading.value
)

const logsDrawer = ref(null)
const execDrawer = ref(null)
const yamlDrawer = ref(null)
const cmEditor = ref(null)

const scaleForm = ref({ open: false, kind: 'deployment', name: '', replicas: 1 })

const specEditor = ref({
  open: false,
  kind: '',
  name: '',
  containers: [],
  configMaps: [],
  secrets: [],
  selected: '',
  image: '',
  env: [],
  envFrom: [],
  resources: { requestsCpu: '', requestsMemory: '', limitsCpu: '', limitsMemory: '' },
  busy: false,
  loading: false,
  error: ''
})
const specSeq = createSeq()

function applyNsList(list) {
  namespaces.value = list
  if (ns.value && !list.some((n) => n.name === ns.value)) ns.value = ''
}

function normalizeWorkloadTab(raw) {
  if (raw === 'nodes') return 'nodes'
  if (WORKLOAD_TABS.has(raw)) return raw
  return 'pods'
}

function consumeTabQuery() {
  const q = normalizeWorkloadTab(route.query.tab)
  if (!route.query.tab) return ''
  // 一次性消费 ?tab=，避免刷新后粘住覆盖用户偏好
  const nextQuery = { ...route.query }
  delete nextQuery.tab
  router.replace({ path: route.path, query: nextQuery }).catch(() => {})
  return q
}

function applyWorkloadsPref() {
  const pref = getViewPref('workloads', currentClusterId.value)
  const fromQuery = consumeTabQuery()
  let next = fromQuery || normalizeWorkloadTab(pref?.tab)
  if (next === 'nodes' && !showNodesTab.value) next = 'pods'
  tab.value = next
  ns.value = pref?.ns || ''
}

function persistWorkloadsPref() {
  if (!currentClusterId.value) return
  setViewPref('workloads', currentClusterId.value, { ns: ns.value, tab: tab.value })
}

function applyCachedRows(data) {
  if (tab.value === 'metrics') podMetrics.value = asArray(data)
  else {
    rows.value = asArray(data)
    if (tab.value === 'pods') refreshPodOwnerMap(rows.value)
    if (tab.value === 'controllers') {
      controllerOptions.value = rows.value.map(controllerOptionFromRow)
    }
  }
}

function stopStorageLoad() {
  storageSeq.bump()
  storageLoading.value = false
  storageRefreshing.value = false
}

function clearRows() {
  // 与列表一并作废进行中的存储请求，避免切集群/切 Tab 后旧响应写回
  stopStorageLoad()
  rows.value = []
  podMetrics.value = []
  metricsTip.value = ''
  storageRows.value = []
  storageTip.value = ''
  podOwnerMap.value = new Map()
}

function clearNodes() {
  nodesSeq.bump()
  nodeRows.value = []
  nodeTip.value = ''
  nodeLoading.value = false
  nodeRefreshing.value = false
}

async function loadNamespaces({ force = false } = {}) {
  const seq = nsSeq.next()
  const clusterId = currentClusterId.value
  if (!clusterId) {
    namespaces.value = []
    ns.value = ''
    clearRows()
    loading.value = false
    refreshing.value = false
    return
  }
  const key = cacheKey('namespaces', clusterId)
  const hit = cacheGet(key)
  if (hit) applyNsList(asArray(hit.data))
  if (!force && cacheFresh(key, TTL.namespaces)) return
  try {
    const list = asArray(
      await cacheLoad(key, TTL.namespaces, () => readApi.listNamespaces(clusterId), { force })
    )
    if (!nsSeq.is(seq)) return
    applyNsList(list)
    error.value = ''
  } catch (e) {
    if (!nsSeq.is(seq)) return
    if (!hit) {
      namespaces.value = []
      ns.value = ''
      clearRows()
    }
    error.value = errMessage(e)
  }
}

function mapDeployment(d) {
  const name = d.metadata?.name || ''
  const desired = d.spec?.replicas ?? 0
  const ready = d.status?.readyReplicas ?? 0
  return {
    kind: 'Deployment',
    name,
    ready: `${ready}/${desired}`,
    extra: `更新 ${d.status?.updatedReplicas ?? 0} · 可用 ${d.status?.availableReplicas ?? 0}`,
    canScale: true,
    replicas: desired
  }
}

function mapStatefulSet(s) {
  const name = s.metadata?.name || ''
  const desired = s.spec?.replicas ?? 0
  const ready = s.status?.readyReplicas ?? 0
  return {
    kind: 'StatefulSet',
    name,
    ready: `${ready}/${desired}`,
    extra: `当前 ${s.status?.currentReplicas ?? 0} · 更新 ${s.status?.updatedReplicas ?? 0}`,
    canScale: true,
    replicas: desired
  }
}

function mapDaemonSet(ds) {
  const name = ds.metadata?.name || ''
  const desired = ds.status?.desiredNumberScheduled ?? 0
  const ready = ds.status?.numberReady ?? 0
  return {
    kind: 'DaemonSet',
    name,
    ready: `${ready}/${desired}`,
    extra: `可用 ${ds.status?.numberAvailable ?? 0} · 更新 ${ds.status?.updatedNumberScheduled ?? 0}`,
    canScale: false,
    replicas: desired
  }
}


function controllerKey(kind, name) {
  return `${kind}/${name}`
}

function controllerOptionFromRow(r) {
  return {
    value: controllerKey(r.kind, r.name),
    label: `${shortKind(r.kind)} / ${r.name}`
  }
}

function shortKind(kind) {
  const k = String(kind || '')
  if (k === 'Deployment') return 'Deploy'
  if (k === 'StatefulSet') return 'STS'
  if (k === 'DaemonSet') return 'Daemon'
  return k
}

/**
 * 实际生效的控制器过滤集。
 * - 未选 / 选空 → 不过滤（显示全部，含无主 Pod）
 * - 勾选了「当前全部控制器选项」→ 视为不过滤（与空选同义，避免无主 Pod 被误藏）
 */
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

const displayRows = computed(() => {
  const list = rows.value || []
  const set = activeControllerFilter.value
  if (!set) return list
  if (tab.value === 'controllers') {
    return list.filter((r) => set.has(controllerKey(r.kind, r.name)))
  }
  if (tab.value === 'pods') {
    return list.filter((p) => p.ownerKind && p.ownerName && set.has(controllerKey(p.ownerKind, p.ownerName)))
  }
  return list
})

/** pod name → controllerKey，供指标 Tab 过滤（需主动 refresh） */
const podOwnerMap = ref(new Map())

function refreshPodOwnerMap(pods) {
  const map = new Map()
  for (const p of asArray(pods)) {
    if (p?.name && p.ownerKind && p.ownerName) {
      map.set(p.name, controllerKey(p.ownerKind, p.ownerName))
    }
  }
  podOwnerMap.value = map
}

/** 旧缓存可能没有 ownerKind；有 Pod 却全无归属字段时需强刷 */
function podsMissingOwnerFields(pods) {
  const list = asArray(pods)
  if (!list.length) return false
  return list.every((p) => !p.ownerKind || !p.ownerName)
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

function cmpText(a, b, dir) {
  const d = String(a || '').localeCompare(String(b || ''), 'zh')
  if (d === 0) return 0
  return dir === 'asc' ? d : -d
}

function cmpIP(a, b, dir) {
  const pa = String(a || '')
    .split('.')
    .map((x) => Number(x))
  const pb = String(b || '')
    .split('.')
    .map((x) => Number(x))
  const aOk = pa.length === 4 && pa.every((n) => Number.isFinite(n))
  const bOk = pb.length === 4 && pb.every((n) => Number.isFinite(n))
  if (!aOk && !bOk) return cmpText(a, b, dir)
  if (!aOk) return 1
  if (!bOk) return -1
  for (let i = 0; i < 4; i++) {
    if (pa[i] !== pb[i]) {
      const d = pa[i] - pb[i]
      return dir === 'asc' ? d : -d
    }
  }
  return 0
}

function toggleNodeSort(kind) {
  if (!NODE_SORT_KEYS.has(kind)) return
  if (nodeSortKey.value === kind) {
    nodeSortDir.value = nodeSortDir.value === 'asc' ? 'desc' : 'asc'
    return
  }
  nodeSortKey.value = kind
  nodeSortDir.value = kind === 'cpu' || kind === 'memory' ? 'desc' : 'asc'
}

function nodeSortArrow(kind) {
  if (nodeSortKey.value !== kind) return ''
  return nodeSortDir.value === 'asc' ? '↑' : '↓'
}

function nodeCpuValue(n) {
  return parseQuantity(n?.usedCPU, { asCPU: true })
}

function nodeMemValue(n) {
  return parseQuantity(n?.usedMemory)
}

const displayNodeRows = computed(() => {
  const list = [...asArray(nodeRows.value)]
  const key = NODE_SORT_KEYS.has(nodeSortKey.value) ? nodeSortKey.value : 'name'
  const dir = nodeSortDir.value === 'desc' ? 'desc' : 'asc'
  list.sort((a, b) => {
    let d = 0
    if (key === 'status') d = cmpText(a.status, b.status, dir)
    else if (key === 'ip') d = cmpIP(a.internalIP, b.internalIP, dir)
    else if (key === 'cpu') d = cmpNullableNum(nodeCpuValue(a), nodeCpuValue(b), dir)
    else if (key === 'memory') d = cmpNullableNum(nodeMemValue(a), nodeMemValue(b), dir)
    else d = cmpText(a.name, b.name, dir)
    if (d) return d
    return String(a.name || '').localeCompare(String(b.name || ''), 'zh')
  })
  return list
})

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

function toggleMetricSort(kind) {
  if (!METRIC_SORT_KEYS.has(kind)) return
  if (metricSortKey.value === kind) {
    metricSortDir.value = metricSortDir.value === 'desc' ? 'asc' : 'desc'
  } else {
    metricSortKey.value = kind
    metricSortDir.value = 'desc'
  }
}

function metricSortArrow(kind) {
  if (metricSortKey.value !== kind) return ''
  return metricSortDir.value === 'asc' ? '↑' : '↓'
}

function fmtBytesNum(bytes) {
  if (bytes == null || Number.isNaN(Number(bytes))) return '-'
  return fmtMemory(Number(bytes))
}

const displayPodMetricRows = computed(() => {
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
  const list = [...byName.values()].filter((r) => passControllerFilter(r.name))
  const key = METRIC_SORT_KEYS.has(metricSortKey.value) ? metricSortKey.value : 'cpu'
  const dir = metricSortDir.value === 'asc' ? 'asc' : 'desc'
  const valOf = (r) => {
    if (key === 'memory') return r.memoryBytes
    if (key === 'storage') return r.usedBytes
    return r.cpuCores
  }
  list.sort((a, b) => {
    const d = cmpNullableNum(valOf(a), valOf(b), dir)
    if (d) return d
    return String(a.name).localeCompare(String(b.name), 'zh')
  })
  return list
})

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
  const ttl = TTL.controllers
  try {
    const list = asArray(
      await cacheLoad(key, ttl, () => loadControllers(clusterId, namespace), { force })
    )
    if (!ctrlOptSeq.is(seq)) return
    controllerOptions.value = list.map(controllerOptionFromRow)
    // 剔除失效选中
    const valid = new Set(controllerOptions.value.map((o) => o.value))
    selectedControllers.value = (selectedControllers.value || []).filter((v) => valid.has(v))
  } catch (e) {
    if (!ctrlOptSeq.is(seq)) return
    // 失败时保留上次 options，避免「界面像全部、实际仍在筛」
  }
}

async function ensurePodsForOwnerFilter() {
  if (!filterActive.value) return
  const clusterId = currentClusterId.value
  const namespace = ns.value
  if (!clusterId || !namespace) return
  const key = cacheKey('pods', clusterId, namespace)
  const hit = cacheGet(key)
  const cached = hit ? asArray(hit.data) : tab.value === 'pods' ? asArray(rows.value) : []
  const force = podsMissingOwnerFields(cached)
  try {
    const pods = asArray(
      await cacheLoad(key, TTL.pods, () => readApi.listPods(clusterId, namespace), { force })
    )
    refreshPodOwnerMap(pods)
    if (tab.value === 'pods' && force) {
      rows.value = pods
    }
  } catch {
    /* 保留已有 map；指标侧会显示空而非误显示全部 */
  }
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
  if (failed.length) {
    error.value = errMessage(failed[0].reason, '部分控制器类型加载失败')
  }
  const list = [
    ...deps.map(mapDeployment),
    ...sts.map(mapStatefulSet),
    ...dss.map(mapDaemonSet)
  ]
  list.sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name))
  return list
}

async function fetchRowItems(clusterId, namespace, kind) {
  if (kind === 'metrics') {
    return asArray(await readApi.podMetrics(clusterId, namespace))
  }
  if (kind === 'pods') {
    return asArray(await readApi.listPods(clusterId, namespace))
  }
  if (kind === 'controllers') {
    return await loadControllers(clusterId, namespace)
  }
  if (kind === 'configmaps') {
    return asArray(await readApi.listConfigMaps(clusterId, namespace))
  }
  if (kind === 'services') {
    return asArray(await readApi.listServices(clusterId, namespace))
  }
  return []
}

function ttlForTab(kind) {
  if (kind === 'metrics') return TTL.podMetrics
  if (kind === 'controllers') return TTL.controllers
  if (kind === 'services') return TTL.services
  if (kind === 'configmaps') return TTL.configmaps
  return TTL.pods
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

function isTransientStorageSkip(skipped) {
  const s = String(skipped || '')
  return s === 'timeout budget exhausted' || /timeout/i.test(s)
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
    const merged = mergeNodeUsage(asArray(nodes), asArray(metrics))
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
    if (data?.skipped) storageTip.value = storageSkipTip(data.skipped)
    else if (data?.truncated) storageTip.value = '部分节点 summary 未取到，列表可能不完整。'
    else storageTip.value = ''
    if (isTransientStorageSkip(data?.skipped)) cacheInvalidate(key)
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

async function loadRows({ force = false } = {}) {
  const seq = rowsSeq.next()
  const clusterId = currentClusterId.value
  const namespace = ns.value
  const kind = tab.value
  error.value = ''
  if (kind === 'nodes') {
    clearRows()
    if (rowsSeq.is(seq)) {
      loading.value = false
      refreshing.value = false
    }
    return loadNodes({ force })
  }
  if (kind !== 'metrics') {
    stopStorageLoad()
    podMetrics.value = []
    metricsTip.value = ''
    storageRows.value = []
    storageTip.value = ''
  }
  if (!clusterId || !namespace) {
    clearRows()
    if (rowsSeq.is(seq)) {
      loading.value = false
      refreshing.value = false
    }
    return
  }

  const key = cacheKey(kind === 'metrics' ? 'podMetrics' : kind, clusterId, namespace)
  const ttl = ttlForTab(kind)
  const hit = cacheGet(key)
  if (hit) applyCachedRows(hit.data)
  else if (kind === 'metrics') podMetrics.value = []
  else rows.value = []

  if (!force && cacheFresh(key, ttl)) {
    loading.value = false
    refreshing.value = false
    if (kind === 'metrics') loadStorageMetrics({ force })
    return
  }

  const has = kind === 'metrics' ? podMetrics.value.length > 0 : rows.value.length > 0
  loading.value = !has
  refreshing.value = has
  try {
    const items = await cacheLoad(key, ttl, () => fetchRowItems(clusterId, namespace, kind), { force })
    if (!rowsSeq.is(seq)) return
    applyCachedRows(items)
    if (kind === 'metrics') {
      metricsTip.value = ''
      loadStorageMetrics({ force })
    }
  } catch (e) {
    if (!rowsSeq.is(seq)) return
    if (kind === 'metrics') {
      metricsTip.value = metricsErrorTip(e)
      error.value = ''
      if (!hit) podMetrics.value = []
      loadStorageMetrics({ force })
    } else {
      error.value = errMessage(e)
      if (!hit) rows.value = []
    }
  } finally {
    if (rowsSeq.is(seq)) {
      loading.value = false
      refreshing.value = false
    }
  }
}

function selectTab(next) {
  const want = normalizeWorkloadTab(next)
  if (want === 'nodes' && !showNodesTab.value) return
  tab.value = want
}

function afterWrite() {
  invalidateNsLists(currentClusterId.value, ns.value)
  loadControllerOptions({ force: true })
  return loadRows({ force: true })
}

async function restart(pod) {
  if (!canWrite.value) return
  if (!confirm(`重启 Pod ${pod.name}？（删除后由控制器重建）`)) return
  error.value = ''
  notice.value = ''
  try {
    const r = await writeApi.restartPod(currentClusterId.value, ns.value, pod.name)
    notice.value = r?.message || `已提交重启 ${pod.name}`
    await afterWrite()
  } catch (e) {
    error.value = errMessage(e)
  }
}

function showLogs(pod) {
  logsDrawer.value?.open(pod.name)
}

function showExec(pod) {
  if (!canWrite.value) return
  execDrawer.value?.open(pod.name)
}

function openYAML(kind, name) {
  yamlDrawer.value?.open(kind, name)
}

function openConfigMap(name) {
  cmEditor.value?.open(name)
}

function closeLogsOnly() {
  logsDrawer.value?.close()
  execDrawer.value?.close()
}

/** 换集群 / 换命名空间时关闭全部编辑抽屉，避免对着旧上下文保存 */
function closeAllEditors() {
  logsDrawer.value?.close()
  execDrawer.value?.close()
  yamlDrawer.value?.close()
  cmEditor.value?.close()
  scaleForm.value.open = false
  specSeq.bump()
  specEditor.value.open = false
  specEditor.value.loading = false
}

function openScale(row) {
  if (!canWrite.value) return
  scaleForm.value = {
    open: true,
    kind: row.kind.toLowerCase(),
    name: row.name,
    replicas: row.replicas ?? 1
  }
}

async function doScale() {
  if (!canWrite.value) return
  try {
    const replicas = Number(scaleForm.value.replicas)
    if (scaleForm.value.kind === 'statefulset') {
      await writeApi.scaleStatefulSet(
        currentClusterId.value,
        ns.value,
        scaleForm.value.name,
        replicas
      )
    } else {
      await writeApi.scaleDeployment(
        currentClusterId.value,
        ns.value,
        scaleForm.value.name,
        replicas
      )
    }
    scaleForm.value.open = false
    await afterWrite()
  } catch (e) {
    error.value = errMessage(e)
  }
}

function yamlKind(kind) {
  return String(kind || '').toLowerCase()
}

function applyContainerToForm(ctr) {
  if (!ctr) return
  specEditor.value.selected = ctr.name
  specEditor.value.image = ctr.image || ''
  specEditor.value.env = asArray(ctr.env).map((e) => ({
    name: e.name || '',
    value: e.value || '',
    valueFrom: e.valueFrom ? { ...e.valueFrom } : null
  }))
  specEditor.value.envFrom = asArray(ctr.envFrom).map((e) => ({
    type: e.type || 'configMapRef',
    name: e.name || '',
    optional: !!e.optional,
    prefix: e.prefix || ''
  }))
  const r = ctr.resources || {}
  specEditor.value.resources = {
    requestsCpu: r.requestsCpu || '',
    requestsMemory: r.requestsMemory || '',
    limitsCpu: r.limitsCpu || '',
    limitsMemory: r.limitsMemory || ''
  }
}

async function openSpecEdit(row) {
  if (!canWrite.value) return
  const kind = yamlKind(row.kind)
  const seq = specSeq.next()
  specEditor.value = {
    open: true,
    kind,
    name: row.name,
    containers: [],
    configMaps: [],
    secrets: [],
    selected: '',
    image: '',
    env: [],
    envFrom: [],
    resources: { requestsCpu: '', requestsMemory: '', limitsCpu: '', limitsMemory: '' },
    busy: false,
    loading: true,
    error: ''
  }
  try {
    const data = await readApi.getWorkloadSpecEdit(
      currentClusterId.value,
      ns.value,
      kind,
      row.name
    )
    if (!specSeq.is(seq)) return
    const containers = asArray(data?.containers)
    specEditor.value.containers = containers
    specEditor.value.configMaps = asArray(data?.configMaps)
    specEditor.value.secrets = asArray(data?.secrets)
    specEditor.value.loading = false
    if (containers.length) applyContainerToForm(containers[0])
    else specEditor.value.error = `未读到 ${kind}/${row.name} 的容器规格`
  } catch (e) {
    if (!specSeq.is(seq)) return
    specEditor.value.loading = false
    specEditor.value.error = errMessage(e)
  }
}

function closeSpecEdit() {
  specSeq.bump()
  specEditor.value.open = false
  specEditor.value.loading = false
  specEditor.value.busy = false
}

function onSpecContainerChange() {
  const ctr = specEditor.value.containers.find((c) => c.name === specEditor.value.selected)
  if (ctr) applyContainerToForm(ctr)
}

function onEnvFromTypeChange(row) {
  row.name = ''
}

function addEnvRow() {
  specEditor.value.env.push({ name: '', value: '', valueFrom: null })
}

function removeEnvRow(i) {
  specEditor.value.env.splice(i, 1)
}

function addEnvFromRow() {
  specEditor.value.envFrom.push({
    type: 'configMapRef',
    name: '',
    optional: false,
    prefix: ''
  })
}

function removeEnvFromRow(i) {
  specEditor.value.envFrom.splice(i, 1)
}

function envFromNameOptions(row) {
  if (row.type === 'secretRef') return asArray(specEditor.value.secrets)
  return asArray(specEditor.value.configMaps)
}

async function saveSpecEdit() {
  if (!canWrite.value) return
  specEditor.value.error = ''
  if (!specEditor.value.selected) {
    specEditor.value.error = '请选择容器'
    return
  }
  if (!String(specEditor.value.image || '').trim()) {
    specEditor.value.error = '镜像不能为空'
    return
  }
  specEditor.value.busy = true
  try {
    const env = specEditor.value.env
      .filter((e) => String(e.name || '').trim())
      .map((e) => {
        if (e.valueFrom) {
          return { name: e.name, valueFrom: e.valueFrom }
        }
        return { name: e.name, value: e.value }
      })
    const envFrom = specEditor.value.envFrom.filter((e) => String(e.name || '').trim())
    await writeApi.patchWorkloadSpecEdit(
      currentClusterId.value,
      ns.value,
      specEditor.value.kind,
      specEditor.value.name,
      {
        container: specEditor.value.selected,
        image: specEditor.value.image,
        env,
        envFrom,
        resources: { ...specEditor.value.resources }
      }
    )
    specEditor.value.open = false
    specEditor.value.loading = false
    await afterWrite()
  } catch (e) {
    specEditor.value.error = errMessage(e)
  } finally {
    specEditor.value.busy = false
  }
}

function onChildError(msg) {
  error.value = msg
}

watch(canWrite, (ok) => {
  if (ok) return
  scaleForm.value.open = false
  execDrawer.value?.close()
  if (specEditor.value.open) closeSpecEdit()
})

watch(currentClusterId, async () => {
  closeAllEditors()
  // ns 名可能跨集群相同，必须先清控制器筛选，避免串用
  selectedControllers.value = []
  controllerOptions.value = []
  podOwnerMap.value = new Map()
  clearNodes()
  clearRows()
  applyWorkloadsPref()
  // Project 凭证不能看节点；cluster 对象可能稍后才就绪，permType watch 会再纠一次
  if (tab.value === 'nodes' && !showNodesTab.value) {
    tab.value = 'pods'
  }
  await loadNamespaces()
  persistWorkloadsPref()
  // 同 Tab/同 ns 切集群时 watch([ns,tab]) 不会触发，这里必须主动拉数
  if (tab.value === 'nodes') {
    if (showNodesTab.value) loadNodes()
    else {
      tab.value = 'pods'
      persistWorkloadsPref()
      if (ns.value) {
        loadControllerOptions()
        loadRows()
      }
    }
    return
  }
  if (ns.value) {
    loadControllerOptions()
    loadRows()
  } else {
    rowsSeq.bump()
    clearRows()
    loading.value = false
    refreshing.value = false
  }
}, { immediate: true })

watch(
  () => currentCluster.value?.permType,
  () => {
    if (!currentClusterId.value || !currentCluster.value) return
    if (isProject.value && tab.value === 'nodes') {
      clearNodes()
      tab.value = 'pods'
      persistWorkloadsPref()
      return
    }
    if (tab.value === 'nodes') loadNodes()
    else if (tab.value === 'metrics' && ns.value) loadStorageMetrics()
  }
)

watch(ns, () => {
  closeAllEditors()
  selectedControllers.value = []
  controllerOptions.value = []
  if (ns.value && !isNodesTab.value) loadControllerOptions()
})
watch(tab, () => {
  // 同命名空间切 Tab 只关日志；保留 YAML/ConfigMap 未保存编辑
  closeLogsOnly()
})

watch(selectedControllers, async () => {
  if (!filterActive.value) return
  if (tab.value === 'metrics' || tab.value === 'pods') {
    await ensurePodsForOwnerFilter()
  }
})

watch([ns, tab], async () => {
  persistWorkloadsPref()
  if (tab.value === 'nodes') {
    rowsSeq.bump()
    clearRows()
    loading.value = false
    refreshing.value = false
    if (showNodesTab.value) loadNodes()
    else {
      clearNodes()
      tab.value = 'pods'
    }
    return
  }
  clearNodes()
  if (!ns.value) {
    rowsSeq.bump()
    clearRows()
    loading.value = false
    refreshing.value = false
    return
  }
  // 从「节点」切回其它 Tab 时 ns 未变，需补拉控制器选项供筛选
  loadControllerOptions()
  if (filterActive.value && (tab.value === 'metrics' || tab.value === 'pods')) {
    await ensurePodsForOwnerFilter()
  }
  loadRows()
})
</script>

<template>
  <div>
    <h1>工作负载</h1>
    <p class="page-desc">
      查看与运维节点 / Pod / 控制器 / Service / ConfigMap。节点含状态、IP、版本与 CPU / 内存（可分配 / 已使用，依赖 metrics-server）；名称 / 状态 / IP / CPU / 内存表头可点击排序（默认按名称）。Pod 指标按 Pod 聚合 CPU / 内存，并含非持久化存储用量；需选择命名空间。当前选用集群在 TTL 内复用列表并合并重复请求；切页不先清空。
    </p>
    <div v-if="!currentClusterId" class="empty">请先在顶部选择当前集群，或到「概览」里点选用。</div>
    <template v-else>
      <div class="toolbar" :class="{ 'toolbar-start': isNodesTab }">
        <FilterSelect
          v-if="!isNodesTab"
          v-model="ns"
          :options="namespaces"
          empty-label="请选择命名空间"
          placeholder="输入 1–3 个字母筛选命名空间"
          aria-label="选择命名空间"
        />
        <FilterMultiSelect
          v-if="!isNodesTab && ns"
          v-model="selectedControllers"
          :options="controllerOptions"
          all-label="控制器 · 全部"
          empty-label="控制器未筛选（全部）"
          placeholder="筛选 Deploy / STS / Daemon"
          aria-label="按控制器筛选"
        />
        <div class="tabs">
          <button
            v-if="showNodesTab"
            class="tab"
            :class="{ active: tab === 'nodes' }"
            @click="selectTab('nodes')"
          >节点</button>
          <button class="tab" :class="{ active: tab === 'pods' }" @click="selectTab('pods')">Pods</button>
          <button
            class="tab"
            :class="{ active: tab === 'controllers' }"
            @click="selectTab('controllers')"
          >控制器</button>
          <button class="tab" :class="{ active: tab === 'services' }" @click="selectTab('services')">Services</button>
          <button
            class="tab"
            :class="{ active: tab === 'configmaps' }"
            @click="selectTab('configmaps')"
          >ConfigMap</button>
          <button
            class="tab"
            :class="{ active: tab === 'metrics' }"
            @click="selectTab('metrics')"
          >Pod 指标</button>
        </div>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
      <p v-if="notice" class="notice">{{ notice }}</p>

      <template v-if="isNodesTab && showNodesTab">
        <p v-if="nodeTip" class="muted">{{ nodeTip }}</p>
        <p v-if="nodeLoading" class="muted">加载中…</p>
        <p v-else-if="nodeRefreshing" class="muted">更新中…</p>
        <table v-if="displayNodeRows.length" class="table">
          <thead>
            <tr>
              <th>
                <button
                  type="button"
                  class="th-sort"
                  :aria-sort="nodeSortKey === 'name' ? (nodeSortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                  @click="toggleNodeSort('name')"
                >名称 {{ nodeSortArrow('name') }}</button>
              </th>
              <th>
                <button
                  type="button"
                  class="th-sort"
                  :aria-sort="nodeSortKey === 'status' ? (nodeSortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                  @click="toggleNodeSort('status')"
                >状态 {{ nodeSortArrow('status') }}</button>
              </th>
              <th>
                <button
                  type="button"
                  class="th-sort"
                  :aria-sort="nodeSortKey === 'ip' ? (nodeSortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                  @click="toggleNodeSort('ip')"
                >IP {{ nodeSortArrow('ip') }}</button>
              </th>
              <th>版本</th>
              <th>
                <button
                  type="button"
                  class="th-sort"
                  :aria-sort="nodeSortKey === 'cpu' ? (nodeSortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                  @click="toggleNodeSort('cpu')"
                >CPU（可分配 / 已使用） {{ nodeSortArrow('cpu') }}</button>
              </th>
              <th>
                <button
                  type="button"
                  class="th-sort"
                  :aria-sort="nodeSortKey === 'memory' ? (nodeSortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                  @click="toggleNodeSort('memory')"
                >内存（可分配 / 已使用） {{ nodeSortArrow('memory') }}</button>
              </th>
              <th>时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in displayNodeRows" :key="n.name">
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

      <div v-else-if="isNodesTab" class="muted">Project 凭证无法查看节点。</div>
      <div v-else-if="!ns" class="empty">请选择命名空间后再加载资源。</div>
      <template v-else>
      <p v-if="isMetricsTab ? metricsBusy : loading" class="muted">加载中…</p>
      <p v-else-if="isMetricsTab ? metricsRefreshing : refreshing" class="muted">更新中…</p>

      <template v-if="tab === 'pods'">
        <table v-if="displayRows.length" class="table">
          <thead><tr><th>名称</th><th>状态</th><th>Ready</th><th>重启</th><th>节点</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="p in displayRows" :key="p.name">
              <td>{{ p.name }}</td>
              <td>{{ p.status }}</td>
              <td>{{ p.ready }}</td>
              <td>{{ p.restarts }}</td>
              <td>{{ p.node }}</td>
              <td class="actions">
                <button class="btn btn-sm" @click="showLogs(p)">日志</button>
                <button class="btn btn-sm" @click="openYAML('pod', p.name)">YAML</button>
                <button v-if="canWrite" class="btn btn-sm" @click="showExec(p)">终端</button>
                <button v-if="canWrite" class="btn btn-sm btn-danger" @click="restart(p)">重启</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty">{{ filterActive && rows.length ? '所选控制器下暂无 Pod。' : '该命名空间下暂无 Pod。' }}</div>
      </template>

      <template v-else-if="tab === 'controllers'">
        <table v-if="displayRows.length" class="table">
          <thead>
            <tr>
              <th>名称</th>
              <th>类型</th>
              <th>Ready</th>
              <th>详情</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in displayRows" :key="r.kind + '/' + r.name">
              <td><strong>{{ r.name }}</strong></td>
              <td><span class="tag">{{ r.kind }}</span></td>
              <td>{{ r.ready }}</td>
              <td class="muted">{{ r.extra }}</td>
              <td class="actions">
                <button
                  v-if="canWrite && r.canScale"
                  class="btn btn-sm"
                  @click="openScale(r)"
                >扩缩容</button>
                <button v-if="canWrite" class="btn btn-sm" @click="openSpecEdit(r)">常用编辑</button>
                <button
                  class="btn btn-sm"
                  @click="openYAML(yamlKind(r.kind), r.name)"
                >YAML</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty">
          {{ filterActive && rows.length ? '没有匹配所选条件的控制器。' : '该命名空间下暂无 Deployment / StatefulSet / DaemonSet。' }}
        </div>
      </template>

      <template v-else-if="tab === 'services'">
        <table v-if="rows.length" class="table">
          <thead><tr><th>名称</th><th>类型</th><th>ClusterIP</th><th>Ports</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="s in rows" :key="s.name">
              <td>{{ s.name }}</td>
              <td>{{ s.type }}</td>
              <td>{{ s.clusterIP }}</td>
              <td>{{ s.ports }}</td>
              <td class="actions">
                <button class="btn btn-sm" @click="openYAML('service', s.name)">YAML</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty">该命名空间下暂无 Service。</div>
      </template>

      <template v-else-if="tab === 'configmaps'">
        <table v-if="rows.length" class="table">
          <thead>
            <tr>
              <th>名称</th>
              <th>键数量</th>
              <th>Age</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="cm in rows" :key="cm.name">
              <td><strong>{{ cm.name }}</strong></td>
              <td>{{ cm.keyCount }}</td>
              <td>{{ cm.age }}</td>
              <td class="actions">
                <button class="btn btn-sm" @click="openConfigMap(cm.name)">{{ canWrite ? '编辑' : '查看' }}</button>
                <button class="btn btn-sm" @click="openYAML('configmap', cm.name)">YAML</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty">该命名空间下暂无 ConfigMap。</div>
      </template>

      <template v-else-if="tab === 'metrics'">
        <p v-if="metricsTip" class="muted">{{ metricsTip }}</p>
        <p v-if="storageTip" class="muted">{{ storageTip }}</p>
        <table v-if="displayPodMetricRows.length" class="table">
          <thead>
            <tr>
              <th>Pod</th>
              <th>
                <button
                  type="button"
                  class="th-sort"
                  :aria-sort="metricSortKey === 'cpu' ? (metricSortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                  @click="toggleMetricSort('cpu')"
                >CPU {{ metricSortArrow('cpu') }}</button>
              </th>
              <th>
                <button
                  type="button"
                  class="th-sort"
                  :aria-sort="metricSortKey === 'memory' ? (metricSortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                  @click="toggleMetricSort('memory')"
                >内存 {{ metricSortArrow('memory') }}</button>
              </th>
              <th>
                <button
                  type="button"
                  class="th-sort"
                  :aria-sort="metricSortKey === 'storage' ? (metricSortDir === 'asc' ? 'ascending' : 'descending') : 'none'"
                  @click="toggleMetricSort('storage')"
                >存储 {{ metricSortArrow('storage') }}</button>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in displayPodMetricRows" :key="r.name">
              <td><PodNameCell :name="r.name" /></td>
              <td>{{ r.cpuCores == null ? '-' : fmtCPU(r.cpuCores) }}</td>
              <td>{{ r.memoryBytes == null ? '-' : fmtMemory(r.memoryBytes) }}</td>
              <td :title="r.node ? `节点 ${r.node}` : undefined">{{ fmtBytesNum(r.usedBytes) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!metricsBusy && (filterActive || (!metricsTip && !storageTip))" class="empty">
          {{ filterActive ? '所选控制器下暂无 Pod 指标。' : (metricsTip ? '无法加载 Pod 指标。' : '该命名空间下暂无 Pod 指标数据。') }}
        </div>
      </template>
      </template>
    </template>

    <PodLogsDrawer
      ref="logsDrawer"
      :cluster-id="currentClusterId"
      :ns="ns"
    />
    <PodExecDrawer
      ref="execDrawer"
      :cluster-id="currentClusterId"
      :ns="ns"
    />
    <YamlDrawer
      ref="yamlDrawer"
      :cluster-id="currentClusterId"
      :ns="ns"
      :read-only="!canWrite"
      @saved="afterWrite"
      @error="onChildError"
    />
    <ConfigMapEditor
      ref="cmEditor"
      :cluster-id="currentClusterId"
      :ns="ns"
      :read-only="!canWrite"
      @saved="afterWrite"
      @error="onChildError"
    />

    <div v-if="scaleForm.open" class="drawer-backdrop" @click="scaleForm.open = false"></div>
    <div v-if="scaleForm.open" class="drawer" style="width:min(360px,100%)">
      <div class="toolbar">
        <strong>扩缩容 · {{ scaleForm.kind }}/{{ scaleForm.name }}</strong>
        <div class="actions">
          <button class="btn btn-sm" @click="scaleForm.open = false">关闭</button>
        </div>
      </div>
      <div class="form-row">
        <label>Replicas</label>
        <input v-model.number="scaleForm.replicas" type="number" min="0" />
      </div>
      <div class="actions">
        <button v-if="canWrite" class="btn btn-primary" @click="doScale">确定</button>
        <button class="btn" @click="scaleForm.open = false">取消</button>
      </div>
    </div>

    <!-- Spec edit -->
    <div v-if="specEditor.open" class="drawer-backdrop" @click="closeSpecEdit"></div>
    <div v-if="specEditor.open" class="drawer drawer-wide">
      <div class="toolbar">
        <strong>常用编辑 · {{ specEditor.kind }}/{{ specEditor.name }}</strong>
        <div class="actions">
          <button v-if="canWrite" class="btn btn-primary btn-sm" :disabled="specEditor.busy || specEditor.loading" @click="saveSpecEdit">
            {{ specEditor.busy ? '保存中…' : '保存' }}
          </button>
          <button class="btn btn-sm" @click="closeSpecEdit">关闭</button>
        </div>
      </div>
      <p class="muted">仅改镜像、环境变量、envFrom、资源；复杂变更请用 YAML。</p>
      <p v-if="specEditor.loading" class="muted">正在加载当前规格…</p>
      <p v-if="specEditor.error" class="error">{{ specEditor.error }}</p>

      <template v-if="!specEditor.loading && specEditor.containers.length">
      <div class="form-row">
        <label>容器</label>
        <select v-model="specEditor.selected" @change="onSpecContainerChange">
          <option v-for="c in specEditor.containers" :key="c.name" :value="c.name">{{ c.name }}</option>
        </select>
      </div>
      <div class="form-row">
        <label>镜像</label>
        <input v-model="specEditor.image" />
      </div>

      <h4>环境变量</h4>
      <div class="kv-list">
        <div v-for="(e, i) in specEditor.env" :key="'env-' + i" class="kv-row">
          <template v-if="e.valueFrom">
            <input v-model="e.name" class="kv-key" disabled />
            <input
              class="kv-val"
              disabled
              :value="`${e.valueFrom.type}: ${e.valueFrom.ref}`"
            />
            <button class="btn btn-sm btn-danger" @click="removeEnvRow(i)">删</button>
          </template>
          <template v-else>
            <input v-model="e.name" placeholder="NAME" class="kv-key" />
            <input v-model="e.value" placeholder="value" class="kv-val" />
            <button class="btn btn-sm btn-danger" @click="removeEnvRow(i)">删</button>
          </template>
        </div>
      </div>
      <button class="btn btn-sm" @click="addEnvRow">添加明文环境变量</button>

      <h4 style="margin-top:16px">envFrom</h4>
      <div class="kv-list">
        <div v-for="(e, i) in specEditor.envFrom" :key="'ef-' + i" class="kv-row envfrom-row">
          <select v-model="e.type" class="kv-key" @change="onEnvFromTypeChange(e)">
            <option value="configMapRef">ConfigMap</option>
            <option value="secretRef">Secret</option>
          </select>
          <select v-model="e.name" class="kv-val">
            <option value="">选择…</option>
            <option
              v-for="n in envFromNameOptions(e)"
              :key="n"
              :value="n"
            >{{ n }}</option>
          </select>
          <button class="btn btn-sm btn-danger" @click="removeEnvFromRow(i)">删</button>
        </div>
      </div>
      <button class="btn btn-sm" @click="addEnvFromRow">添加 envFrom</button>

      <h4 style="margin-top:16px">资源</h4>
      <div class="resource-grid">
        <div class="form-row">
          <label>requests CPU</label>
          <input v-model="specEditor.resources.requestsCpu" placeholder="如 100m" />
        </div>
        <div class="form-row">
          <label>requests 内存</label>
          <input v-model="specEditor.resources.requestsMemory" placeholder="如 256Mi" />
        </div>
        <div class="form-row">
          <label>limits CPU</label>
          <input v-model="specEditor.resources.limitsCpu" placeholder="如 500m" />
        </div>
        <div class="form-row">
          <label>limits 内存</label>
          <input v-model="specEditor.resources.limitsMemory" placeholder="如 512Mi" />
        </div>
      </div>
      </template>
    </div>
  </div>
</template>
