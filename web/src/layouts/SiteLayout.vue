<script setup>
import { computed, onMounted } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import SiteHeader from '../components/SiteHeader.vue'
import SiteFooter from '../components/SiteFooter.vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { permTypeLabel } from '../utils/labels'

const {
  clusters,
  currentClusterId,
  setCurrentClusterId,
  refreshClusters
} = useClusterStore()
const route = useRoute()
const showClusterBar = computed(() => !String(route.path || '').startsWith('/docs'))

onMounted(() => {
  refreshClusters(() => readApi.listClusters()).catch(() => {})
})
</script>

<template>
  <div class="layout">
    <SiteHeader />
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
    <SiteFooter />
  </div>
</template>
