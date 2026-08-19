<script setup>
import { onMounted, computed, ref } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { readApi } from './api'
import { useClusterStore } from './stores/cluster'
import { permTypeLabel } from './utils/labels'

const {
  clusters,
  currentClusterId,
  setCurrentClusterId,
  refreshClusters
} = useClusterStore()
const route = useRoute()
const showClusterBar = computed(() => !String(route.path || '').startsWith('/docs'))

const ui = ref({
  title: 'k8s多集群运维工具',
  tagline: 'practice makes perfect',
  hasBanner: false,
  bannerUrl: '/banner',
  iconUrl: '/icon',
  version: '0.1.0'
})

const mediaStamp = ref(Date.now())

const bannerStyle = computed(() => {
  if (!ui.value.hasBanner) return {}
  const url = `${ui.value.bannerUrl}?t=${mediaStamp.value}`
  return { '--site-head-banner': `url('${url}')` }
})

const iconSrc = computed(() => `${ui.value.iconUrl || '/icon'}?t=${mediaStamp.value}`)

function setFavicon(href) {
  let link = document.querySelector('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  link.removeAttribute('type')
  link.href = href
}

onMounted(() => {
  setFavicon(iconSrc.value)
  refreshClusters(() => readApi.listClusters()).catch(() => {})
  readApi
    .getUI()
    .then((data) => {
      if (data) {
        mediaStamp.value = Date.now()
        ui.value = {
          title: data.title || ui.value.title,
          tagline: data.tagline || 'practice makes perfect',
          hasBanner: !!data.hasBanner,
          bannerUrl: data.bannerUrl || '/banner',
          iconUrl: data.iconUrl || '/icon',
          version: data.version || '0.1.0'
        }
        if (data.title) document.title = data.title
        setFavicon(`${ui.value.iconUrl}?t=${mediaStamp.value}`)
      }
    })
    .catch(() => {})
})
</script>

<template>
  <div class="layout">
    <header class="site-head" :class="{ 'has-banner': ui.hasBanner }" :style="bannerStyle">
      <div class="shell site-head-inner">
        <!-- 标题靠左，导航靠右，与正文同宽 -->
        <div class="head-mid">
          <div class="brand-block">
            <RouterLink to="/" class="brand-home brand-home-icon">
              <img class="brand-icon" :src="iconSrc" width="36" height="36" alt="" />
            </RouterLink>
            <div class="brand-copy">
              <RouterLink to="/" class="brand brand-home">{{ ui.title }}</RouterLink>
              <p v-if="ui.tagline" class="site-tagline">{{ ui.tagline }}</p>
            </div>
          </div>
          <nav class="nav">
            <RouterLink to="/">概览</RouterLink>
            <RouterLink to="/clusters">KubeConfig</RouterLink>
            <RouterLink to="/workloads">工作负载</RouterLink>
            <RouterLink to="/monitor">监控</RouterLink>
            <RouterLink to="/diagnose">诊断</RouterLink>
            <RouterLink to="/docs">运维文档</RouterLink>
          </nav>
        </div>
      </div>
    </header>

    <main class="body shell">
      <div v-if="showClusterBar" class="cluster-bar">
        <label class="cluster-label" for="cluster-select">当前集群</label>
        <select
          id="cluster-select"
          class="cluster-select"
          :value="currentClusterId"
          @change="setCurrentClusterId($event.target.value)"
        >
          <option value="">未选择</option>
          <option v-for="c in clusters" :key="c.id" :value="c.id">
            {{ c.name }} · {{ permTypeLabel(c.permType) }}
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
