<script setup>
import { onMounted, computed, ref } from 'vue'
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

const ui = ref({
  title: 'k8s多集群运维工具',
  tagline: '',
  hasBanner: false,
  bannerUrl: '/banner',
  version: '0.1.0'
})

const bannerStyle = computed(() => {
  if (!ui.value.hasBanner) return {}
  const url = `${ui.value.bannerUrl}?t=${Date.now()}`
  return { '--site-head-banner': `url('${url}')` }
})

onMounted(() => {
  refreshClusters(() => readApi.listClusters()).catch(() => {})
  readApi
    .getUI()
    .then((data) => {
      if (data) {
        ui.value = {
          title: data.title || ui.value.title,
          tagline: data.tagline || '',
          hasBanner: !!data.hasBanner,
          bannerUrl: data.bannerUrl || '/banner',
          version: data.version || '0.1.0'
        }
        if (data.title) document.title = data.title
      }
    })
    .catch(() => {})
})
</script>

<template>
  <div class="layout">
    <header class="site-head" :class="{ 'has-banner': ui.hasBanner }" :style="bannerStyle">
      <div class="shell site-head-inner">
        <!-- 标题 + 导航：同组居中排版 -->
        <div class="head-mid">
          <div class="brand">{{ ui.title }}</div>
          <nav class="nav">
            <RouterLink to="/">概览</RouterLink>
            <RouterLink to="/clusters">KubeConfig</RouterLink>
            <RouterLink to="/workloads">工作负载</RouterLink>
            <RouterLink v-if="!isProject" to="/nodes">节点</RouterLink>
            <RouterLink to="/monitor">监控</RouterLink>
            <RouterLink to="/docs">运维文档</RouterLink>
          </nav>
        </div>
        <p v-if="ui.tagline" class="site-tagline">{{ ui.tagline }}</p>
      </div>
    </header>

    <main class="body shell">
      <div class="cluster-bar">
        <label class="cluster-label" for="cluster-select">选择集群</label>
        <select
          id="cluster-select"
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
      <RouterView />
    </main>

    <footer class="footer">
      <div class="shell footer-inner">
        k8man · 通过导入 kubeconfig 管理与监控 K8s 集群 · v{{ ui.version }}
      </div>
    </footer>
  </div>
</template>
