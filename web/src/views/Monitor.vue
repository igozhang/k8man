<script setup>
import { ref, watch, onMounted, computed } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory, fmtTime, mergeNodeUsage } from '../utils/quantity'
import { metricsErrorTip } from '../utils/metrics'

const { currentClusterId, currentCluster } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
/** nodes | pods — 初始 pods，避免 project 下 nodes+隐藏 tab 导致空白；换集群再落到默认 */
const tab = ref('pods')
const nodeRows = ref([])
const podMetrics = ref([])
const error = ref('')
const nodeTip = ref('')
const podTip = ref('')
const nodeLoading = ref(false)
const podLoading = ref(false)

let nodesSeq = 0
let podsSeq = 0
/** 抑制 load() 内改 tab/ns 触发的 watch，避免进页误拉未选中 tab 的数据 */
let syncing = false
let lastClusterId = undefined

const isProject = computed(() => currentCluster.value?.permType === 'project')
const showNodesTab = computed(() => !isProject.value)

function pickNamespace(list) {
  const names = (list || []).map((n) => n.name)
  if (!names.length) {
    ns.value = ''
    return
  }
  if (!names.includes(ns.value)) ns.value = names[0]
}

function coerceTab() {
  if (!showNodesTab.value) {
    tab.value = 'pods'
    return
  }
  if (tab.value !== 'nodes' && tab.value !== 'pods') tab.value = 'nodes'
}

async function loadNodes() {
  const seq = ++nodesSeq
  nodeRows.value = []
  nodeTip.value = ''
  if (!currentClusterId.value || isProject.value) return
  nodeLoading.value = true
  try {
    const [nodes, metrics] = await Promise.all([
      readApi.listNodes(currentClusterId.value),
      readApi.nodeMetrics(currentClusterId.value).catch((e) => {
        if (seq === nodesSeq) nodeTip.value = metricsErrorTip(e)
        return []
      })
    ])
    if (seq !== nodesSeq) return
    nodeRows.value = mergeNodeUsage(nodes || [], metrics || [])
  } catch (e) {
    if (seq !== nodesSeq) return
    nodeTip.value = e.message
  } finally {
    if (seq === nodesSeq) nodeLoading.value = false
  }
}

async function loadPodMetrics() {
  const seq = ++podsSeq
  podMetrics.value = []
  podTip.value = ''
  if (!currentClusterId.value || !ns.value) return
  podLoading.value = true
  try {
    const items = (await readApi.podMetrics(currentClusterId.value, ns.value)) || []
    if (seq !== podsSeq) return
    podMetrics.value = items
  } catch (e) {
    if (seq !== podsSeq) return
    podTip.value = metricsErrorTip(e)
  } finally {
    if (seq === podsSeq) podLoading.value = false
  }
}

async function loadActive() {
  if (tab.value === 'nodes') await loadNodes()
  else if (tab.value === 'pods') await loadPodMetrics()
}

async function load() {
  error.value = ''
  nodeTip.value = ''
  podTip.value = ''
  nodeRows.value = []
  podMetrics.value = []
  namespaces.value = []
  nodesSeq++
  podsSeq++
  nodeLoading.value = false
  podLoading.value = false

  const id = currentClusterId.value
  if (!id) {
    ns.value = ''
    lastClusterId = id
    return
  }

  syncing = true
  try {
    try {
      namespaces.value = (await readApi.listNamespaces(id)) || []
    } catch (e) {
      error.value = e.message
      ns.value = ''
      lastClusterId = id
      return
    }
    pickNamespace(namespaces.value)
    // 换集群时回到默认 tab；同集群刷新保留用户当前 tab（仅校正非法）
    if (id !== lastClusterId) {
      tab.value = showNodesTab.value ? 'nodes' : 'pods'
      lastClusterId = id
    } else {
      coerceTab()
    }
  } finally {
    syncing = false
  }
  await loadActive()
}

watch([currentClusterId, currentCluster], load)
watch(tab, () => {
  if (!syncing) loadActive()
})
watch(ns, () => {
  if (!syncing && tab.value === 'pods') loadPodMetrics()
})
onMounted(load)
</script>

<template>
  <div>
    <h1>监控</h1>
    <p class="page-desc">
      基于目标集群 metrics-server 的 CPU / 内存用量（CPU：m=毫核；内存：Ki/Mi/Gi）。按 Tab 按需加载；未安装或权限不足时降级提示。
    </p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
    <template v-else>
      <p v-if="error" class="error">{{ error }}</p>

      <div class="tabs">
        <button
          v-if="showNodesTab"
          class="tab"
          :class="{ active: tab === 'nodes' }"
          @click="tab = 'nodes'"
        >节点指标</button>
        <button
          class="tab"
          :class="{ active: tab === 'pods' }"
          @click="tab = 'pods'"
        >Pod 指标</button>
      </div>

      <template v-if="tab === 'nodes' && showNodesTab">
        <p v-if="nodeTip" class="muted">{{ nodeTip }}</p>
        <p v-if="nodeLoading" class="muted">加载中…</p>
        <table v-else-if="nodeRows.length" class="table">
          <thead>
            <tr>
              <th>节点</th>
              <th>CPU（可分配 / 已使用）</th>
              <th>内存（可分配 / 已使用）</th>
              <th>时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in nodeRows" :key="n.name">
              <td>{{ n.name }}</td>
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
        <div class="toolbar" style="margin-bottom:12px">
          <select v-model="ns" style="max-width:220px">
            <option v-for="n in namespaces" :key="n.name" :value="n.name">{{ n.name }}</option>
          </select>
        </div>
        <p v-if="podTip" class="muted">{{ podTip }}</p>
        <p v-if="podLoading" class="muted">加载中…</p>
        <table v-else-if="podMetrics.length" class="table">
          <thead><tr><th>Pod</th><th>容器</th><th>CPU</th><th>内存</th></tr></thead>
          <tbody>
            <template v-for="m in podMetrics" :key="m.metadata?.name">
              <tr v-for="c in (m.containers || [])" :key="m.metadata.name + c.name">
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
  </div>
</template>
