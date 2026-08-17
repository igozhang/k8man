<script setup>
import { ref, watch, onMounted } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory, fmtTime } from '../utils/quantity'

const { currentClusterId, currentCluster } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
const nodeMetrics = ref([])
const podMetrics = ref([])
const error = ref('')
const tip = ref('')

function metricCPU(m) {
  return m.usage?.cpu || m.Usage?.cpu || null
}
function metricMem(m) {
  return m.usage?.memory || m.Usage?.memory || null
}

async function load() {
  error.value = ''
  tip.value = ''
  nodeMetrics.value = []
  podMetrics.value = []
  if (!currentClusterId.value) return
  try {
    namespaces.value = await readApi.listNamespaces(currentClusterId.value)
    if (!ns.value && namespaces.value.length) ns.value = namespaces.value[0].name
  } catch (e) {
    error.value = e.message
    return
  }
  if (currentCluster.value?.permType !== 'project') {
    try {
      const items = await readApi.nodeMetrics(currentClusterId.value)
      nodeMetrics.value = items || []
    } catch (e) {
      tip.value = e.message
    }
  }
  if (ns.value) {
    try {
      const items = await readApi.podMetrics(currentClusterId.value, ns.value)
      podMetrics.value = items || []
    } catch (e) {
      tip.value = tip.value || e.message
    }
  }
}

watch([currentClusterId], load)
watch(ns, async () => {
  if (!currentClusterId.value || !ns.value) return
  try {
    podMetrics.value = (await readApi.podMetrics(currentClusterId.value, ns.value)) || []
  } catch (e) {
    tip.value = e.message
    podMetrics.value = []
  }
})
onMounted(load)
</script>

<template>
  <div>
    <h1>监控</h1>
    <p class="page-desc">
      基于目标集群 metrics-server 的 CPU / 内存用量（CPU：m=毫核；内存：Ki/Mi/Gi）。若未安装 metrics-server 将降级提示。
    </p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
    <template v-else>
      <p v-if="error" class="error">{{ error }}</p>
      <p v-if="tip" class="muted">{{ tip }}</p>

      <div v-if="currentCluster?.permType !== 'project'" class="panel">
        <h3 style="margin-top:0">节点指标</h3>
        <table v-if="nodeMetrics.length" class="table">
          <thead><tr><th>节点</th><th>CPU</th><th>内存</th><th>时间</th></tr></thead>
          <tbody>
            <tr v-for="m in nodeMetrics" :key="m.metadata?.name || m.name">
              <td>{{ m.metadata?.name }}</td>
              <td>{{ fmtCPU(metricCPU(m)) }}</td>
              <td>{{ fmtMemory(metricMem(m)) }}</td>
              <td class="muted">{{ fmtTime(m.timestamp || m.metadata?.creationTimestamp) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="muted">暂无节点指标数据。</div>
      </div>

      <div class="panel">
        <div class="toolbar">
          <h3 style="margin:0">Pod 指标</h3>
          <select v-model="ns" style="max-width:220px">
            <option v-for="n in namespaces" :key="n.name" :value="n.name">{{ n.name }}</option>
          </select>
        </div>
        <table v-if="podMetrics.length" class="table">
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
        <div v-else class="muted">暂无 Pod 指标数据。</div>
      </div>
    </template>
  </div>
</template>
