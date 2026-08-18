<script setup>
import { ref, watch, onMounted, computed } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory, fmtTime, mergeNodeUsage } from '../utils/quantity'
import { metricsErrorTip } from '../utils/metrics'
import { asArray, createSeq, errMessage } from '../utils/safe'

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
const podLoading = ref(false)
const nsLoading = ref(false)

const nodesSeq = createSeq()
const podsSeq = createSeq()
const pageSeq = createSeq()

const isProject = computed(() => currentCluster.value?.permType === 'project')
const showNodesTab = computed(() => !isProject.value)

function clearAll() {
  nodesSeq.bump()
  podsSeq.bump()
  nodeRows.value = []
  podMetrics.value = []
  nodeTip.value = ''
  podTip.value = ''
  nodeLoading.value = false
  podLoading.value = false
  nsLoading.value = false
  namespaces.value = []
  ns.value = ''
  error.value = ''
}

async function ensureNamespaces() {
  if (!currentClusterId.value) return
  if (namespaces.value.length || nsLoading.value) return
  const clusterId = currentClusterId.value
  nsLoading.value = true
  try {
    const list = asArray(await readApi.listNamespaces(clusterId))
    // 请求期间若已换集群，丢弃结果
    if (currentClusterId.value !== clusterId) return
    namespaces.value = list
  } catch (e) {
    if (currentClusterId.value !== clusterId) return
    error.value = errMessage(e)
    namespaces.value = []
  } finally {
    nsLoading.value = false
  }
}

async function loadNodes() {
  const seq = nodesSeq.next()
  nodeRows.value = []
  nodeTip.value = ''
  if (!currentClusterId.value || isProject.value) return
  nodeLoading.value = true
  try {
    const [nodes, metrics] = await Promise.all([
      readApi.listNodes(currentClusterId.value),
      readApi.nodeMetrics(currentClusterId.value).catch((e) => {
        if (nodesSeq.is(seq)) nodeTip.value = metricsErrorTip(e)
        return []
      })
    ])
    if (!nodesSeq.is(seq)) return
    nodeRows.value = mergeNodeUsage(asArray(nodes), asArray(metrics))
  } catch (e) {
    if (!nodesSeq.is(seq)) return
    nodeTip.value = errMessage(e)
    nodeRows.value = []
  } finally {
    if (nodesSeq.is(seq)) nodeLoading.value = false
  }
}

async function loadPodMetrics() {
  const seq = podsSeq.next()
  podMetrics.value = []
  podTip.value = ''
  if (!currentClusterId.value || !ns.value) return
  podLoading.value = true
  try {
    const items = asArray(await readApi.podMetrics(currentClusterId.value, ns.value))
    if (!podsSeq.is(seq)) return
    podMetrics.value = items
  } catch (e) {
    if (!podsSeq.is(seq)) return
    podTip.value = metricsErrorTip(e)
    podMetrics.value = []
  } finally {
    if (podsSeq.is(seq)) podLoading.value = false
  }
}

function selectTab(next) {
  if (next === 'nodes' && !showNodesTab.value) return
  tab.value = next
  if (next === 'nodes') {
    ns.value = ''
    podsSeq.bump()
    podMetrics.value = []
    podTip.value = ''
    loadNodes()
    return
  }
  if (next === 'pods') {
    nodesSeq.bump()
    nodeRows.value = []
    nodeTip.value = ''
    ns.value = ''
    podMetrics.value = []
    podTip.value = ''
    ensureNamespaces()
  }
}

function enterPage() {
  const seq = pageSeq.next()
  clearAll()
  tab.value = ''
  if (!currentClusterId.value) return
  // 等集群元数据就绪再决定是否自动拉节点，避免 project 误打 nodes API
  if (!currentCluster.value) return
  if (showNodesTab.value) {
    tab.value = 'nodes'
    loadNodes().then(() => {
      if (!pageSeq.is(seq)) return
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
        nodeRows.value = []
        nodeTip.value = ''
        nodeLoading.value = false
        tab.value = ''
      }
      return
    }
    // cluster 元数据刚就绪且尚未选 tab：自动进入节点
    if (!tab.value) {
      tab.value = 'nodes'
      loadNodes()
    }
  }
)
watch(ns, (v) => {
  if (tab.value !== 'pods') return
  if (v) loadPodMetrics()
  else {
    podsSeq.bump()
    podMetrics.value = []
    podTip.value = ''
    podLoading.value = false
  }
})
onMounted(enterPage)
</script>

<template>
  <div>
    <h1>监控</h1>
    <p class="page-desc">
      节点列表含状态、IP、版本与 CPU / 内存（可分配 / 已使用，依赖 metrics-server）；进入页面默认加载。Pod 指标需手动选择命名空间。未安装或权限不足时降级提示。
    </p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
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
        <table v-else-if="nodeRows.length" class="table">
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
          <select v-model="ns" style="max-width:220px">
            <option value="">请选择命名空间</option>
            <option v-for="n in namespaces" :key="n.name" :value="n.name">{{ n.name }}</option>
          </select>
        </div>
        <p v-if="nsLoading" class="muted">加载命名空间列表…</p>
        <div v-else-if="!ns" class="empty">请选择命名空间后再加载 Pod 指标。</div>
        <template v-else>
          <p v-if="podTip" class="muted">{{ podTip }}</p>
          <p v-if="podLoading" class="muted">加载中…</p>
          <table v-else-if="podMetrics.length" class="table">
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
