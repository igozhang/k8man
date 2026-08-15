import { ref, watch, computed } from 'vue'

const STORAGE_KEY = 'k8man.currentClusterId'

const clusters = ref([])
const currentClusterId = ref(localStorage.getItem(STORAGE_KEY) || '')
const loadingClusters = ref(false)

export function useClusterStore() {
  const currentCluster = computed(() =>
    clusters.value.find((c) => c.id === currentClusterId.value) || null
  )

  function setCurrentClusterId(id) {
    currentClusterId.value = id || ''
    if (id) localStorage.setItem(STORAGE_KEY, id)
    else localStorage.removeItem(STORAGE_KEY)
  }

  async function refreshClusters(fetcher) {
    loadingClusters.value = true
    try {
      const list = await fetcher()
      clusters.value = list || []
      if (currentClusterId.value && !clusters.value.some((c) => c.id === currentClusterId.value)) {
        setCurrentClusterId(clusters.value[0]?.id || '')
      } else if (!currentClusterId.value && clusters.value.length) {
        setCurrentClusterId(clusters.value[0].id)
      }
    } finally {
      loadingClusters.value = false
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
