<script setup>
import { ref, watch, onMounted } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory, mergeNodeUsage } from '../utils/quantity'
import { metricsErrorTip } from '../utils/metrics'

const { currentClusterId, currentCluster } = useClusterStore()
const rows = ref([])
const error = ref('')
const tip = ref('')
const loading = ref(false)

async function load() {
  error.value = ''
  tip.value = ''
  rows.value = []
  if (!currentClusterId.value) return
  if (currentCluster.value?.permType === 'project') {
    error.value = 'Project kubeconfig 无法查看节点'
    return
  }
  loading.value = true
  try {
    const [nodes, metrics] = await Promise.all([
      readApi.listNodes(currentClusterId.value),
      readApi.nodeMetrics(currentClusterId.value).catch((e) => {
        tip.value = metricsErrorTip(e)
        return []
      })
    ])
    rows.value = mergeNodeUsage(nodes || [], metrics || [])
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

watch([currentClusterId, currentCluster], load)
onMounted(load)
</script>

<template>
  <div>
    <h1>节点</h1>
    <p class="page-desc">集群级节点列表（需要 Cluster kubeconfig）。CPU / 内存展示可分配与已使用（已使用依赖 metrics-server）。</p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
    <p v-else-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading" class="muted">加载中…</p>
    <template v-else>
      <p v-if="tip" class="muted">{{ tip }}</p>
      <table v-if="rows.length" class="table">
        <thead>
          <tr>
            <th>名称</th>
            <th>状态</th>
            <th>IP</th>
            <th>版本</th>
            <th>CPU（可分配 / 已使用）</th>
            <th>内存（可分配 / 已使用）</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="n in rows" :key="n.name">
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
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">暂无节点。</div>
    </template>
  </div>
</template>
