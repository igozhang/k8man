<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import SiteHeader from '../components/SiteHeader.vue'
import SiteFooter from '../components/SiteFooter.vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { permTypeLabel } from '../utils/labels'
import {
  cacheLoad,
  cacheGet,
  cacheKey,
  TTL,
  cacheFresh,
  INSPECTIONS_CHANGED
} from '../utils/resourceCache'

const {
  clusters,
  currentClusterId,
  setCurrentClusterId,
  refreshClusters
} = useClusterStore()
const route = useRoute()
const showClusterBar = computed(() => !String(route.path || '').startsWith('/docs'))

const inspectItems = ref([])

function applyInspectItems(data) {
  inspectItems.value = Array.isArray(data?.items) ? data.items : []
}

async function loadInspectSummary({ force = false } = {}) {
  const key = cacheKey('inspections', 'local')
  const hit = cacheGet(key)
  if (hit?.data?.items) applyInspectItems(hit.data)
  if (!force && cacheFresh(key, TTL.inspections)) return
  try {
    const data = await cacheLoad(key, TTL.inspections, () => readApi.listInspections(), { force })
    applyInspectItems(data)
  } catch {
    /* 顶栏统计失败不影响主流程 */
  }
}

function onInspectionsChanged() {
  const hit = cacheGet(cacheKey('inspections', 'local'))
  if (hit?.data?.items) applyInspectItems(hit.data)
  else loadInspectSummary({ force: true })
}

const hasPodStats = computed(() => inspectItems.value.some((it) => !!it?.hasScan))

const totalPods = computed(() => {
  let sum = 0
  for (const it of inspectItems.value) sum += Number(it?.scanPods) || 0
  return sum
})

const totalBadPods = computed(() => {
  let sum = 0
  for (const it of inspectItems.value) sum += Number(it?.podIssues) || 0
  return sum
})

watch(
  () => route.path,
  () => {
    if (showClusterBar.value) loadInspectSummary()
  }
)

onMounted(() => {
  refreshClusters(() => readApi.listClusters()).catch(() => {})
  loadInspectSummary()
  window.addEventListener(INSPECTIONS_CHANGED, onInspectionsChanged)
})

onUnmounted(() => {
  window.removeEventListener(INSPECTIONS_CHANGED, onInspectionsChanged)
})
</script>

<template>
  <div class="layout">
    <SiteHeader />
    <main class="body shell">
      <div v-if="showClusterBar" class="cluster-bar">
        <select
          id="cluster-select"
          class="cluster-select"
          aria-label="当前集群"
          :value="currentClusterId"
          @change="setCurrentClusterId($event.target.value)"
        >
          <option value="">未选择</option>
          <option v-for="c in clusters" :key="c.id" :value="c.id">
            {{ c.name }} · {{ permTypeLabel(c.permType) }}
          </option>
        </select>
        <span class="cluster-stats muted">
          {{ clusters.length }} 集群<template v-if="hasPodStats"> · {{ totalPods }} Pods</template><template v-if="totalBadPods"> · <span class="cluster-stats-bad">{{ totalBadPods }} 异常</span></template>
        </span>
      </div>
      <RouterView />
    </main>
    <SiteFooter />
  </div>
</template>
