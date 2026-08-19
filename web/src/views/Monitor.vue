<script setup>
import { ref, watch, onMounted, computed } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory, fmtTime, mergeNodeUsage } from '../utils/quantity'
import { metricsErrorTip } from '../utils/metrics'
import { asArray, createSeq, errMessage } from '../utils/safe'
import {
  TTL,
  cacheFresh,
  cacheGet,
  cacheKey,
  cacheLoad,
  cacheSet,
  getViewPref,
  setViewPref
} from '../utils/resourceCache'

const { currentClusterId, currentCluster } = useClusterStore()
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

const nodesSeq = createSeq()
const podsSeq = createSeq()

const isProject = computed(() => currentCluster.value?.permType === 'project')
const showNodesTab = computed(() => !isProject.value)

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
          <select v-model="ns">
            <option value="">请选择命名空间</option>
            <option v-for="n in namespaces" :key="n.name" :value="n.name">{{ n.name }}</option>
          </select>
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
  </div>
</template>
