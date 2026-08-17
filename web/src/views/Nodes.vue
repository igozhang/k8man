<script setup>
import { ref, watch, onMounted } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory } from '../utils/quantity'

const { currentClusterId, currentCluster } = useClusterStore()
const rows = ref([])
const error = ref('')
const loading = ref(false)

async function load() {
  error.value = ''
  rows.value = []
  if (!currentClusterId.value) return
  if (currentCluster.value?.permType === 'project') {
    error.value = 'Project kubeconfig 无法查看节点'
    return
  }
  loading.value = true
  try {
    rows.value = await readApi.listNodes(currentClusterId.value)
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
    <p class="page-desc">集群级节点列表（需要 Cluster kubeconfig）。</p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
    <p v-else-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading" class="muted">加载中…</p>
    <table v-else class="table">
      <thead>
        <tr>
          <th>名称</th>
          <th>状态</th>
          <th>IP</th>
          <th>版本</th>
          <th>可分配 CPU</th>
          <th>可分配内存</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="n in rows" :key="n.name">
          <td>{{ n.name }}</td>
          <td>{{ n.status }}</td>
          <td>{{ n.internalIP }}</td>
          <td>{{ n.version }}</td>
          <td>{{ fmtCPU(n.allocatableCPU) }}</td>
          <td>{{ fmtMemory(n.allocatableMemory) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
