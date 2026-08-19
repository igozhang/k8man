<script setup>
import { ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { TTL, cacheFresh, cacheGet, cacheKey, cacheSet } from '../utils/resourceCache'
import { permTypeLabel, clusterStatusLabel, clusterStatusClass } from '../utils/labels'

const { clusters, currentClusterId, setCurrentClusterId, refreshClusters } = useClusterStore()

const emptyStats = () => ({ namespaces: 0, nodes: 0, pods: 0, badPods: 0 })
const emptyCard = () => ({
  stats: emptyStats(),
  loading: false,
  refreshing: false,
  error: ''
})

const cards = ref({})
const seqById = new Map()
let loadAllSeq = 0

function seqFor(id) {
  let s = seqById.get(id)
  if (!s) {
    s = createSeq()
    seqById.set(id, s)
  }
  return s
}

function cardOf(id) {
  return cards.value[id] || emptyCard()
}

function patchCard(id, patch) {
  cards.value[id] = { ...cardOf(id), ...patch }
}

function hydrateCard(id) {
  const hit = cacheGet(cacheKey('overview', id))
  const has = !!(hit?.data && typeof hit.data === 'object')
  patchCard(id, {
    stats: has ? { ...emptyStats(), ...hit.data } : emptyStats(),
    loading: !has,
    refreshing: false,
    error: ''
  })
}

function dnsHostHint(cluster, err) {
  const msg = err || ''
  const m = msg.match(/lookup\s+([^\s:]+)\s+on\s+/i)
  if (m?.[1]) return m[1]
  try {
    if (cluster?.server) return new URL(cluster.server).hostname
  } catch {
    /* ignore */
  }
  return ''
}

function isDNSError(err) {
  return /no such host|lookup /i.test(err || '')
}

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

async function loadOne(cluster, { force = false } = {}) {
  const id = cluster?.id
  if (!id) return
  const seq = seqFor(id).next()
  const key = cacheKey('overview', id)
  const hit = cacheGet(key)
  const has = !!(hit?.data && typeof hit.data === 'object')
  if (has) {
    patchCard(id, { stats: { ...emptyStats(), ...hit.data }, error: '' })
  } else {
    patchCard(id, { stats: emptyStats(), error: '' })
  }
  if (!force && cacheFresh(key, TTL.overview)) {
    patchCard(id, { loading: false, refreshing: false })
    return
  }
  patchCard(id, { loading: !has, refreshing: has, error: '' })
  const next = emptyStats()
  try {
    const ns = await listNamespacesCached(id)
    if (!seqFor(id).is(seq)) return
    next.namespaces = ns.length

    let nodes = []
    if (cluster.permType !== 'project') {
      try {
        nodes = await listNodesCached(id)
      } catch {
        nodes = []
      }
    }
    if (!seqFor(id).is(seq)) return
    next.nodes = nodes.length

    let pods = 0
    let bad = 0
    const sample = ns.slice(0, 8)
    for (const n of sample) {
      if (!seqFor(id).is(seq)) return
      if (!n?.name) continue
      try {
        const list = await listPodsCached(id, n.name)
        pods += list.length
        bad += list.filter((p) => p?.status !== 'Running' && p?.status !== 'Succeeded').length
      } catch {
        /* skip ns without access */
      }
    }
    if (!seqFor(id).is(seq)) return
    next.pods = pods
    next.badPods = bad
    patchCard(id, { stats: next, error: '' })
    cacheSet(key, next)
  } catch (e) {
    if (!seqFor(id).is(seq)) return
    patchCard(id, {
      error: errMessage(e),
      stats: has ? cardOf(id).stats : emptyStats()
    })
  } finally {
    if (seqFor(id).is(seq)) {
      patchCard(id, { loading: false, refreshing: false })
    }
  }
}

async function loadAll({ force = false } = {}) {
  const seq = ++loadAllSeq
  try {
    await refreshClusters(() => readApi.listClusters())
  } catch {
    /* store 会保留上次列表 */
  }
  if (seq !== loadAllSeq) return
  const list = clusters.value
  const keep = {}
  for (const c of list) {
    if (!cards.value[c.id]) hydrateCard(c.id)
    keep[c.id] = cards.value[c.id] || emptyCard()
  }
  cards.value = keep
  await Promise.all(list.map((c) => loadOne(c, { force })))
}

watch(
  () => clusters.value.map((c) => c.id).join(','),
  () => {
    loadAll()
  },
  { immediate: true }
)
</script>

<template>
  <div>
    <h1>概览</h1>
    <p class="page-desc">查看已导入的全部 kubeconfig。点「选用」会设为顶部当前集群，供工作负载、监控、诊断使用。</p>
    <div v-if="!clusters.length" class="empty">请先在「KubeConfig」导入集群。</div>
    <div v-else class="cluster-list">
      <div
        v-for="c in clusters"
        :key="c.id"
        class="panel cluster-card"
        :class="{ 'is-current': c.id === currentClusterId }"
      >
        <div class="cluster-card-head">
          <div class="cluster-card-meta">
            <div class="cluster-card-title">
              <strong>{{ c.name }}</strong>
              <span v-if="c.id === currentClusterId" class="tag current">当前</span>
              <span class="tag" :class="c.permType">{{ permTypeLabel(c.permType) }}</span>
              <span class="muted cluster-server">{{ c.server }}</span>
            </div>
            <div class="status-line">
              <span class="tag" :class="clusterStatusClass(c.status)">{{ clusterStatusLabel(c.status) }}</span>
              <span v-if="c.lastError" class="error"> {{ c.lastError }}</span>
              <span v-if="cardOf(c.id).refreshing" class="muted"> · 更新中…</span>
            </div>
          </div>
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'is-current': c.id === currentClusterId }"
            :disabled="c.id === currentClusterId"
            @click="setCurrentClusterId(c.id)"
          >{{ c.id === currentClusterId ? '已选用' : '选用' }}</button>
        </div>

        <div v-if="cardOf(c.id).error" class="error-block">
          <p class="error" style="white-space: pre-wrap; margin: 0">{{ cardOf(c.id).error }}</p>
          <p v-if="isDNSError(cardOf(c.id).error)" class="hint">
            域名无法解析（走的是集群 DNS）。请到
            <RouterLink to="/clusters">KubeConfig → Hosts</RouterLink>
            添加映射，例如：
            <code>10.80.238.34 {{ dnsHostHint(c, cardOf(c.id).error) || 'api-host' }}</code>
            ，保存后再刷新本页。
          </p>
        </div>

        <div class="stats-strip">
          <div class="stat">
            <div class="label">Namespaces</div>
            <div class="value">{{ cardOf(c.id).loading ? '…' : cardOf(c.id).stats.namespaces }}</div>
          </div>
          <div class="stat">
            <div class="label">Nodes</div>
            <div class="value">{{ c.permType === 'project' ? '—' : (cardOf(c.id).loading ? '…' : cardOf(c.id).stats.nodes) }}</div>
          </div>
          <div class="stat">
            <div class="label">Pods（抽样）</div>
            <div class="value">{{ cardOf(c.id).loading ? '…' : cardOf(c.id).stats.pods }}</div>
          </div>
          <div class="stat">
            <div class="label">异常 Pod（抽样）</div>
            <div class="value">{{ cardOf(c.id).loading ? '…' : cardOf(c.id).stats.badPods }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cluster-list {
  display: flex;
  flex-direction: column;
}
.cluster-card {
  padding: 0;
  overflow: hidden;
}
.cluster-card.is-current {
  border-color: var(--accent);
}
.cluster-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px 16px;
  padding: 16px;
}
.cluster-card-meta {
  min-width: 0;
  flex: 1 1 auto;
}
.cluster-card-title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.35rem 0.5rem;
}
.cluster-server {
  min-width: 0;
  word-break: break-all;
}
.status-line {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.35rem 0.5rem;
  margin-top: 6px;
}
.cluster-card-head .btn {
  flex-shrink: 0;
  margin-top: 0;
}
.cluster-card .stats-strip {
  margin: 0;
  border-top: 1px solid var(--line);
  border-radius: 0;
}
.error-block {
  margin: 0;
  padding: 12px 16px;
  border-top: 1px solid var(--line);
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
