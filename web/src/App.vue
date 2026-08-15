<script setup>
import { onMounted, computed } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import { readApi } from './api'
import { useClusterStore } from './stores/cluster'

const {
  clusters,
  currentClusterId,
  currentCluster,
  setCurrentClusterId,
  refreshClusters
} = useClusterStore()

const isProject = computed(() => currentCluster.value?.permType === 'project')

onMounted(() => {
  refreshClusters(() => readApi.listClusters()).catch(() => {})
})
</script>

<template>
  <div class="layout">
    <header class="header">
      <div class="brand">k8s多集群运维工具</div>
      <nav class="nav">
        <RouterLink to="/">概览</RouterLink>
        <RouterLink to="/clusters">KubeConfig</RouterLink>
        <RouterLink to="/workloads">工作负载</RouterLink>
        <RouterLink v-if="!isProject" to="/nodes">节点</RouterLink>
        <RouterLink to="/monitor">监控</RouterLink>
        <RouterLink to="/docs">运维文档</RouterLink>
      </nav>
      <div class="header-right">
        <select
          class="cluster-select"
          :value="currentClusterId"
          @change="setCurrentClusterId($event.target.value)"
        >
          <option value="">选择集群</option>
          <option v-for="c in clusters" :key="c.id" :value="c.id">
            {{ c.name }} ({{ c.permType }})
          </option>
        </select>
      </div>
    </header>
    <main class="body">
      <RouterView />
    </main>
    <footer class="footer">k8man · 通过导入 kubeconfig 管理与监控 K8s 集群 · v0.1.0</footer>
  </div>
</template>
