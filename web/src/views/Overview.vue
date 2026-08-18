<script setup>
import { ref, watch, onMounted, computed } from 'vue'
import { RouterLink } from 'vue-router'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { TTL, cacheFresh, cacheGet, cacheKey, cacheSet } from '../utils/resourceCache'

const { currentClusterId, currentCluster } = useClusterStore()

const emptyStats = () => ({ namespaces: 0, nodes: 0, pods: 0, badPods: 0 })

const overviewHit = currentClusterId.value ? cacheGet(cacheKey('overview', currentClusterId.value)) : null
const stats = ref(
  overviewHit?.data && typeof overviewHit.data === 'object'
    ? { ...emptyStats(), ...overviewHit.data }
    : emptyStats()
)
const loading = ref(!!currentClusterId.value && !overviewHit)
const refreshing = ref(false)
const error = ref('')
const loadSeq = createSeq()

const dnsHostHint = computed(() => {
  const msg = error.value || ''
  const m = msg.match(/lookup\s+([^\s:]+)\s+on\s+/i)
  if (m?.[1]) return m[1]
  try {
    if (currentCluster.value?.server) {
      return new URL(currentCluster.value.server).hostname
    }
  } catch {
    /* ignore */
  }
  return ''
})

const isDNSError = computed(() => /no such host|lookup /i.test(error.value || ''))

async function listNamespacesCached(id, { force = false } = {}) {
  const key = cacheKey('namespaces', id)
  const hit = cacheGet(key)
  if (!force && hit && cacheFresh(key, TTL.namespaces)) return asArray(hit.data)
  const list = asArray(await readApi.listNamespaces(id))
  cacheSet(key, list)
  return list
}

async function listNodesCached(id, { force = false } = {}) {
  const usageKey = cacheKey('nodeUsage', id)
  const nodesKey = cacheKey('nodes', id)
  if (!force) {
    if (cacheFresh(nodesKey, TTL.nodes)) return asArray(cacheGet(nodesKey)?.data)
    if (cacheFresh(usageKey, TTL.nodeUsage)) return asArray(cacheGet(usageKey)?.data)
  }
  const list = asArray(await readApi.listNodes(id))
  cacheSet(nodesKey, list)
  return list
}

async function listPodsCached(id, nsName, { force = false } = {}) {
  const key = cacheKey('pods', id, nsName)
  const hit = cacheGet(key)
  if (!force && hit && cacheFresh(key, TTL.pods)) return asArray(hit.data)
  const list = asArray(await readApi.listPods(id, nsName))
  cacheSet(key, list)
  return list
}

async function load({ force = false } = {}) {
  const seq = loadSeq.next()
  error.value = ''
  if (!currentClusterId.value) {
    stats.value = emptyStats()
    loading.value = false
    refreshing.value = false
    return
  }
  const key = cacheKey('overview', currentClusterId.value)
  const hit = cacheGet(key)
  if (hit && hit.data && typeof hit.data === 'object') {
    stats.value = { ...emptyStats(), ...hit.data }
  } else {
    stats.value = emptyStats()
  }
  if (!force && cacheFresh(key, TTL.overview)) {
    loading.value = false
    refreshing.value = false
    return
  }
  const has = !!(hit && hit.data)
  loading.value = !has
  refreshing.value = has
  const next = emptyStats()
  try {
    const ns = await listNamespacesCached(currentClusterId.value)
    if (!loadSeq.is(seq)) return
    next.namespaces = ns.length

    let nodes = []
    if (currentCluster.value?.permType !== 'project') {
      try {
        nodes = await listNodesCached(currentClusterId.value)
      } catch {
        nodes = []
      }
    }
    if (!loadSeq.is(seq)) return
    next.nodes = nodes.length

    let pods = 0
    let bad = 0
    const sample = ns.slice(0, 8)
    for (const n of sample) {
      if (!loadSeq.is(seq)) return
      if (!n?.name) continue
      try {
        const list = await listPodsCached(currentClusterId.value, n.name)
        pods += list.length
        bad += list.filter((p) => p?.status !== 'Running' && p?.status !== 'Succeeded').length
      } catch {
        /* skip ns without access */
      }
    }
    if (!loadSeq.is(seq)) return
    next.pods = pods
    next.badPods = bad
    stats.value = next
    cacheSet(key, next)
  } catch (e) {
    if (!loadSeq.is(seq)) return
    error.value = errMessage(e)
    if (!has) stats.value = emptyStats()
  } finally {
    if (loadSeq.is(seq)) {
      loading.value = false
      refreshing.value = false
    }
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
      <div v-if="error" class="error-block">
        <p class="error" style="white-space: pre-wrap; margin: 0">{{ error }}</p>
        <p v-if="isDNSError" class="hint">
          域名无法解析（走的是集群 DNS）。请到
          <RouterLink to="/clusters">KubeConfig → Hosts</RouterLink>
          添加映射，例如：
          <code>10.80.238.34 {{ dnsHostHint || 'api-host' }}</code>
          ，保存后再刷新本页。列表里显示「暂无 hosts」时，概览一定会失败。
        </p>
      </div>
      <div class="panel stats-strip">
        <div class="stat"><div class="label">Namespaces</div><div class="value">{{ loading ? '…' : stats.namespaces }}</div></div>
        <div class="stat"><div class="label">Nodes</div><div class="value">{{ currentCluster?.permType === 'project' ? '—' : (loading ? '…' : stats.nodes) }}</div></div>
        <div class="stat"><div class="label">Pods（抽样）</div><div class="value">{{ loading ? '…' : stats.pods }}</div></div>
        <div class="stat"><div class="label">异常 Pod（抽样）</div><div class="value">{{ loading ? '…' : stats.badPods }}</div></div>
      </div>
      <p v-if="refreshing" class="muted">更新中…</p>
    </template>
  </div>
</template>

<style scoped>
.error-block {
  margin-bottom: 12px;
}
.hint {
  margin: 8px 0 0;
  font-size: 13px;
  color: var(--text);
  line-height: 1.5;
}
.hint code {
  font-family: var(--mono);
  font-size: 12px;
}
</style>
