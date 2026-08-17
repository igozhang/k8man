<script setup>
import { ref, watch, onMounted } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'

const { currentClusterId, currentCluster } = useClusterStore()
const loading = ref(false)
const error = ref('')
const stats = ref({
  namespaces: 0,
  nodes: 0,
  pods: 0,
  badPods: 0
})

async function load() {
  error.value = ''
  if (!currentClusterId.value) {
    stats.value = { namespaces: 0, nodes: 0, pods: 0, badPods: 0 }
    return
  }
  loading.value = true
  try {
    const ns = await readApi.listNamespaces(currentClusterId.value)
    stats.value.namespaces = ns.length
    let nodes = []
    if (currentCluster.value?.permType !== 'project') {
      try {
        nodes = await readApi.listNodes(currentClusterId.value)
      } catch {
        nodes = []
      }
    }
    stats.value.nodes = nodes.length
    let pods = 0
    let bad = 0
    const sample = ns.slice(0, 8)
    for (const n of sample) {
      try {
        const list = await readApi.listPods(currentClusterId.value, n.name)
        pods += list.length
        bad += list.filter((p) => p.status !== 'Running' && p.status !== 'Succeeded').length
      } catch {
        /* skip ns without access */
      }
    }
    stats.value.pods = pods
    stats.value.badPods = bad
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

watch(currentClusterId, load)
onMounted(load)
</script>

<template>
  <div>
    <h1>概览</h1>
    <p class="page-desc">基于当前导入的 kubeconfig 查看集群概况。</p>
    <div v-if="!currentClusterId" class="empty">请先在「KubeConfig」导入集群，并在上方选择集群。</div>
    <template v-else>
      <div class="panel">
        <div><strong>{{ currentCluster?.name }}</strong>
          <span class="tag" :class="currentCluster?.permType">{{ currentCluster?.permType }}</span>
          <span class="muted"> · {{ currentCluster?.server }}</span>
        </div>
        <div class="muted" style="margin-top:6px">状态：{{ currentCluster?.status }}
          <span v-if="currentCluster?.lastError" class="error"> — {{ currentCluster.lastError }}</span>
        </div>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
      <div class="grid">
        <div class="panel stat"><div class="label">Namespaces</div><div class="value">{{ stats.namespaces }}</div></div>
        <div class="panel stat"><div class="label">Nodes</div><div class="value">{{ currentCluster?.permType === 'project' ? '—' : stats.nodes }}</div></div>
        <div class="panel stat"><div class="label">Pods（抽样）</div><div class="value">{{ loading ? '…' : stats.pods }}</div></div>
        <div class="panel stat"><div class="label">异常 Pod（抽样）</div><div class="value">{{ loading ? '…' : stats.badPods }}</div></div>
      </div>
    </template>
  </div>
</template>
