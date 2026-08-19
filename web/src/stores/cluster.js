import { ref, watch, computed } from 'vue'
import { asArray } from '../utils/safe'
import { TTL, cacheGet, cacheKey, cacheLoad, cacheFresh } from '../utils/resourceCache'

const STORAGE_KEY = 'k8man.currentClusterId'
const CLUSTERS_KEY = cacheKey('clusters', 'local')

function initialClusters() {
  const hit = cacheGet(CLUSTERS_KEY)
  return Array.isArray(hit?.data) ? hit.data : []
}

const clusters = ref(initialClusters())
const currentClusterId = ref(localStorage.getItem(STORAGE_KEY) || '')
const loadingClusters = ref(false)
let refreshSeq = 0

export function useClusterStore() {
  const currentCluster = computed(() =>
    clusters.value.find((c) => c.id === currentClusterId.value) || null
  )

  function setCurrentClusterId(id) {
    currentClusterId.value = id || ''
    if (id) localStorage.setItem(STORAGE_KEY, id)
    else localStorage.removeItem(STORAGE_KEY)
  }

  async function refreshClusters(fetcher, { force = false } = {}) {
    if (!force && clusters.value.length && cacheFresh(CLUSTERS_KEY, TTL.clusters)) {
      return
    }
    const seq = ++refreshSeq
    loadingClusters.value = !clusters.value.length
    try {
      const list = asArray(await cacheLoad(CLUSTERS_KEY, TTL.clusters, fetcher, { force }))
      if (seq !== refreshSeq) return
      clusters.value = list
      if (currentClusterId.value && !clusters.value.some((c) => c.id === currentClusterId.value)) {
        setCurrentClusterId(clusters.value[0]?.id || '')
      } else if (!currentClusterId.value && clusters.value.length) {
        setCurrentClusterId(clusters.value[0].id)
      }
    } catch {
      // 保留上次列表，避免顶部集群选择器被清空导致整页不可用
    } finally {
      if (seq === refreshSeq) loadingClusters.value = false
    }
  }

  watch(currentClusterId, (id) => {
    if (id) localStorage.setItem(STORAGE_KEY, id)
  })

  return {
    clusters,
    currentClusterId,
    currentCluster,
    loadingClusters,
    setCurrentClusterId,
    refreshClusters
  }
}
