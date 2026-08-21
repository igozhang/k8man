<script setup>
import { ref, watch, onMounted, computed } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory, fmtTime, mergeNodeUsage } from '../utils/quantity'
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
  setViewPref
} from '../utils/resourceCache'
import InspectionDrawer from '../components/InspectionDrawer.vue'
import InspectRules from '../components/InspectRules.vue'
import FilterSelect from '../components/FilterSelect.vue'
import { canWrite } from '../stores/opsAuth'

const INSPECT_KEY = cacheKey('inspections', 'local')
const BLOCK_LINES = 3

const { currentClusterId, currentCluster, refreshClusters } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
/** '' | nodes | pods */
const tab = ref('')
const nodeRows = ref([])
const podMetrics = ref([])
const error = ref('')
const nodeTip = ref('')
const podTip = ref('')
const nodeLoading = ref(false)
const nodeRefreshing = ref(false)
const podLoading = ref(false)
const podRefreshing = ref(false)
const nsLoading = ref(false)

const inspectItems = ref(asArray(cacheGet(INSPECT_KEY)?.data?.items))
const inspectListFetchedAt = ref(cacheGet(INSPECT_KEY)?.at || null)
const inspectLoading = ref(false)
const inspectError = ref('')
const inspectingId = ref('')
const inspectTargetId = ref('')
const inspectDrawer = ref(null)

const inspecting = computed(() => !!inspectingId.value)

const nodesSeq = createSeq()
const podsSeq = createSeq()
const inspectSeq = createSeq()
const inspectRunSeq = createSeq()

const isProject = computed(() => currentCluster.value?.permType === 'project')
const showNodesTab = computed(() => !isProject.value)

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

function resetView() {
  nodesSeq.bump()
  podsSeq.bump()
  nodeRows.value = []
  podMetrics.value = []
  nodeTip.value = ''
  podTip.value = ''
  nodeLoading.value = false
  nodeRefreshing.value = false
  podLoading.value = false
  podRefreshing.value = false
  nsLoading.value = false
  namespaces.value = []
  ns.value = ''
  tab.value = ''
  error.value = ''
}

function applyMonitorPref() {
  const pref = getViewPref('monitor', currentClusterId.value)
  ns.value = pref?.ns || ''
  if (pref?.tab === 'pods') tab.value = 'pods'
  else if (pref?.tab === 'nodes' && showNodesTab.value) tab.value = 'nodes'
  else tab.value = showNodesTab.value ? 'nodes' : ''
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

function selectTab(next) {
  if (next === 'nodes' && !showNodesTab.value) return
  tab.value = next
  persistMonitorPref()
  if (next === 'nodes') {
    podsSeq.bump()
    podLoading.value = false
    podRefreshing.value = false
    loadNodes()
    return
  }
  if (next === 'pods') {
    ensureNamespaces()
    if (ns.value) loadPodMetrics()
  }
}

function enterPage() {
  error.value = ''
  loadInspections()
  if (!currentClusterId.value) {
    resetView()
    return
  }
  applyMonitorPref()
  persistMonitorPref()
  if (!currentCluster.value) return
  if (tab.value === 'nodes') {
    if (!showNodesTab.value) {
      tab.value = ''
      persistMonitorPref()
      return
    }
    loadNodes()
    return
  }
  if (tab.value === 'pods') {
    ensureNamespaces()
    if (ns.value) loadPodMetrics()
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
        tab.value = ''
        persistMonitorPref()
      }
      return
    }
    if (!tab.value) {
      tab.value = 'nodes'
      persistMonitorPref()
    }
    if (tab.value === 'nodes') loadNodes()
  }
)
watch(ns, (v) => {
  persistMonitorPref()
  if (tab.value !== 'pods') return
  if (v) loadPodMetrics()
  else {
    podsSeq.bump()
    podMetrics.value = []
    podTip.value = ''
    podLoading.value = false
    podRefreshing.value = false
  }
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
      节点列表含状态、IP、版本与 CPU / 内存（可分配 / 已使用，依赖 metrics-server）。当前选用集群在 TTL 内复用并合并重复请求；过期后后台更新，不先清空。Pod 指标需选择命名空间。未安装或权限不足时降级提示。
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
        >Pod 指标</button>
      </div>

      <div v-if="!tab" class="empty">请选择「Pod 指标」并指定命名空间。</div>

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

      <template v-else-if="tab === 'pods'">
        <div class="toolbar toolbar-start" style="margin-bottom:12px">
          <FilterSelect
            v-model="ns"
            :options="namespaces"
            empty-label="请选择命名空间"
            placeholder="输入 1–3 个字母筛选命名空间"
            aria-label="选择命名空间"
          />
        </div>
        <p v-if="nsLoading" class="muted">加载命名空间列表…</p>
        <div v-else-if="!ns" class="empty">请选择命名空间后再加载 Pod 指标。</div>
        <template v-else>
          <p v-if="podTip" class="muted">{{ podTip }}</p>
          <p v-if="podLoading" class="muted">加载中…</p>
          <p v-else-if="podRefreshing" class="muted">更新中…</p>
          <table v-if="podMetrics.length" class="table">
            <thead><tr><th>Pod</th><th>容器</th><th>CPU</th><th>内存</th></tr></thead>
            <tbody>
              <template v-for="m in podMetrics" :key="m.metadata?.name || m.name">
                <tr v-for="c in asArray(m.containers)" :key="(m.metadata?.name || '') + c.name">
                  <td>{{ m.metadata?.name }}</td>
                  <td>{{ c.name }}</td>
                  <td>{{ fmtCPU(c.usage?.cpu) }}</td>
                  <td>{{ fmtMemory(c.usage?.memory) }}</td>
                </tr>
              </template>
            </tbody>
          </table>
          <div v-else-if="!podLoading" class="muted">
            {{ podTip ? '无法加载 Pod 指标。' : '暂无 Pod 指标数据。' }}
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
        对每个 kubeconfig 约 2 小时一轮、错开串行：先 Probe（同 KubeConfig「测试」）并回写状态，通过后再扫描节点、负载、Pod 与事件（Project 凭证跳过节点）。定级与「诊断」页相同（含 Restarts）。下方按集群分块展示摘要（每块最多 3 条，只含严重/错误/警告；仅有提示时不单独成块）；点击块在右侧抽屉查看该配置详情<template v-if="canWrite">并触发实时巡检</template>。
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
  </div>
</template>
