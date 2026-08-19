<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { cacheFresh, cacheGet, cacheKey, cacheLoad, cacheSet, setViewPref, ttlOf } from '../utils/resourceCache'
import { permTypeLabel, clusterStatusLabel, clusterStatusClass } from '../utils/labels'

const router = useRouter()
const { clusters, currentClusterId, setCurrentClusterId, refreshClusters } = useClusterStore()

/** 非生产环境标志：test / uat / dev / pre（独立片段，避免误伤 prod） */
const NON_PROD_ENV_RE = /(^|[^a-z0-9])(test|uat|dev|pre)([^a-z0-9]|$)/i

function isNonProdName(name) {
  return NON_PROD_ENV_RE.test(String(name || ''))
}

const sortedClusters = computed(() => {
  const selected = currentClusterId.value
  return [...clusters.value].sort((a, b) => {
    const aSel = a.id === selected ? 0 : 1
    const bSel = b.id === selected ? 0 : 1
    if (aSel !== bSel) return aSel - bSel
    const aNon = isNonProdName(a.name) ? 1 : 0
    const bNon = isNonProdName(b.name) ? 1 : 0
    if (aNon !== bNon) return aNon - bNon
    return String(a.name || '').localeCompare(String(b.name || ''), 'zh')
  })
})

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
let detailSeq = 0

const STAT_TITLES = {
  namespaces: 'Namespaces',
  nodes: 'Nodes',
  pods: 'Pods（抽样）',
  badPods: '异常 Pod（抽样）'
}

const emptyDetail = () => ({
  open: false,
  kind: '',
  clusterId: '',
  clusterName: '',
  title: '',
  loading: false,
  error: '',
  rows: [],
  sampled: false,
  sampleLimit: 0
})

const detail = ref(emptyDetail())

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

async function listNamespacesCached(id, { force = false, selected = true } = {}) {
  const key = cacheKey('namespaces', id)
  const ttl = ttlOf('namespaces', selected)
  const hit = cacheGet(key)
  if (!force && hit && cacheFresh(key, ttl)) return asArray(hit.data)
  return asArray(await cacheLoad(key, ttl, () => readApi.listNamespaces(id), { force }))
}

async function listNodesCached(id, { force = false, selected = true } = {}) {
  const usageKey = cacheKey('nodeUsage', id)
  const nodesKey = cacheKey('nodes', id)
  const ttl = ttlOf('nodes', selected)
  if (!force) {
    if (cacheFresh(nodesKey, ttl)) return asArray(cacheGet(nodesKey)?.data)
    if (cacheFresh(usageKey, ttlOf('nodeUsage', selected))) return asArray(cacheGet(usageKey)?.data)
  }
  return asArray(await cacheLoad(nodesKey, ttl, () => readApi.listNodes(id), { force }))
}

async function listPodsCached(id, nsName, { force = false, selected = true } = {}) {
  const key = cacheKey('pods', id, nsName)
  const ttl = ttlOf('pods', selected)
  const hit = cacheGet(key)
  if (!force && hit && cacheFresh(key, ttl)) return asArray(hit.data)
  return asArray(await cacheLoad(key, ttl, () => readApi.listPods(id, nsName), { force }))
}

async function loadOne(cluster, { force = false, selected = true } = {}) {
  const id = cluster?.id
  if (!id) return
  const seq = seqFor(id).next()
  const key = cacheKey('overview', id)
  const ttl = ttlOf('overview', selected)
  const hit = cacheGet(key)
  const has = !!(hit?.data && typeof hit.data === 'object')
  if (has) {
    patchCard(id, { stats: { ...emptyStats(), ...hit.data }, error: '' })
  } else {
    patchCard(id, { stats: emptyStats(), error: '' })
  }
  if (!selected && typeof document !== 'undefined' && document.hidden && has) {
    patchCard(id, { loading: false, refreshing: false })
    return
  }
  if (!force && cacheFresh(key, ttl)) {
    patchCard(id, { loading: false, refreshing: false })
    return
  }
  patchCard(id, { loading: !has, refreshing: has, error: '' })
  const next = emptyStats()
  try {
    const ns = await listNamespacesCached(id, { selected, force })
    if (!seqFor(id).is(seq)) return
    next.namespaces = ns.length

    let nodes = []
    if (cluster.permType !== 'project') {
      try {
        nodes = await listNodesCached(id, { selected, force })
      } catch {
        nodes = []
      }
    }
    if (!seqFor(id).is(seq)) return
    next.nodes = nodes.length

    let pods = 0
    let bad = 0
    const sample = ns.slice(0, selected ? 8 : 3)
    const parts = await Promise.all(
      sample.map(async (n) => {
        if (!n?.name) return { pods: 0, bad: 0 }
        try {
          const list = await listPodsCached(id, n.name, { selected, force })
          return {
            pods: list.length,
            bad: list.filter((p) => p?.status !== 'Running' && p?.status !== 'Succeeded').length
          }
        } catch {
          return { pods: 0, bad: 0 }
        }
      })
    )
    if (!seqFor(id).is(seq)) return
    for (const p of parts) {
      pods += p.pods
      bad += p.bad
    }
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

  const selectedId = currentClusterId.value
  const current = list.find((c) => c.id === selectedId)
  const idle = list.filter((c) => c.id !== selectedId)
  if (current) {
    await loadOne(current, { force, selected: true })
    if (seq !== loadAllSeq) return
  }
  for (const c of idle) {
    if (seq !== loadAllSeq) return
    await loadOne(c, { force: false, selected: false })
  }
}

function onVis() {
  if (!document.hidden) loadAll()
}

function sampleLimitOf(selected) {
  return selected ? 8 : 3
}

function isBadPod(p) {
  return p?.status !== 'Running' && p?.status !== 'Succeeded'
}

async function collectSamplePods(cluster, { force = false, selected = true, badOnly = false } = {}) {
  const id = cluster?.id
  if (!id) return []
  const ns = await listNamespacesCached(id, { selected, force })
  const sample = ns.slice(0, sampleLimitOf(selected))
  const parts = await Promise.all(
    sample.map(async (n) => {
      if (!n?.name) return []
      try {
        const list = await listPodsCached(id, n.name, { selected, force })
        return list.map((p) => ({ ...p, namespace: p?.namespace || n.name }))
      } catch {
        return []
      }
    })
  )
  const rows = parts.flat()
  return badOnly ? rows.filter(isBadPod) : rows
}

async function loadStatRows(cluster, kind, selected) {
  const id = cluster.id
  if (kind === 'namespaces') return listNamespacesCached(id, { selected })
  if (kind === 'nodes') {
    if (cluster.permType === 'project') return []
    return listNodesCached(id, { selected })
  }
  if (kind === 'pods') return collectSamplePods(cluster, { selected, badOnly: false })
  if (kind === 'badPods') return collectSamplePods(cluster, { selected, badOnly: true })
  return []
}

function closeDetail() {
  detailSeq += 1
  detail.value = emptyDetail()
}

async function openStat(cluster, kind) {
  if (!cluster?.id) return
  if (kind === 'nodes' && cluster.permType === 'project') return
  if (cardOf(cluster.id).loading) return
  const selected = cluster.id === currentClusterId.value
  setCurrentClusterId(cluster.id)
  const seq = ++detailSeq
  detail.value = {
    open: true,
    kind,
    clusterId: cluster.id,
    clusterName: cluster.name,
    title: STAT_TITLES[kind] || kind,
    loading: true,
    error: '',
    rows: [],
    sampled: kind === 'pods' || kind === 'badPods',
    sampleLimit: sampleLimitOf(selected)
  }
  try {
    const rows = await loadStatRows(cluster, kind, selected)
    if (seq !== detailSeq) return
    detail.value = { ...detail.value, rows, loading: false }
  } catch (e) {
    if (seq !== detailSeq) return
    detail.value = { ...detail.value, error: errMessage(e), loading: false }
  }
}

function moreLabel(kind) {
  if (kind === 'nodes') return '去监控'
  if (kind === 'badPods') return '去诊断'
  return '去工作负载'
}

function goMore() {
  const d = detail.value
  const id = d.clusterId
  if (!id) return
  if (d.kind === 'nodes') {
    setViewPref('monitor', id, { tab: 'nodes', ns: '' })
    router.push('/monitor')
    return
  }
  if (d.kind === 'badPods') {
    router.push({ path: '/diagnose', query: { category: 'pod' } })
    return
  }
  setViewPref('workloads', id, { ns: '', tab: 'pods' })
  router.push('/workloads')
}

function openWorkloadNs(nsName) {
  const id = detail.value.clusterId
  if (!id || !nsName) return
  setViewPref('workloads', id, { ns: nsName, tab: 'pods' })
  router.push('/workloads')
}

watch(
  () => [clusters.value.map((c) => c.id).join(','), currentClusterId.value],
  () => {
    loadAll()
  },
  { immediate: true }
)

onMounted(() => {
  document.addEventListener('visibilitychange', onVis)
})
onUnmounted(() => {
  loadAllSeq += 1
  detailSeq += 1
  document.removeEventListener('visibilitychange', onVis)
})

</script>

<template>
  <div>
    <h1>概览</h1>
    <p class="page-desc">查看已导入的全部 kubeconfig。点「选用」会设为顶部当前集群，供工作负载、监控、诊断使用。选用集群按较短间隔更新；其余集群沿用缓存，刷新不必勤。</p>
    <div v-if="!clusters.length" class="empty">请先在「KubeConfig」导入集群。</div>
    <div v-else class="cluster-list">
      <div
        v-for="c in sortedClusters"
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
          <button
            type="button"
            class="stat"
            title="点击查看"
            :disabled="cardOf(c.id).loading"
            @click="openStat(c, 'namespaces')"
          >
            <div class="label">Namespaces</div>
            <div class="value">{{ cardOf(c.id).loading ? '…' : cardOf(c.id).stats.namespaces }}</div>
          </button>
          <button
            v-if="c.permType !== 'project'"
            type="button"
            class="stat"
            title="点击查看"
            :disabled="cardOf(c.id).loading"
            @click="openStat(c, 'nodes')"
          >
            <div class="label">Nodes</div>
            <div class="value">{{ cardOf(c.id).loading ? '…' : cardOf(c.id).stats.nodes }}</div>
          </button>
          <div v-else class="stat">
            <div class="label">Nodes</div>
            <div class="value">—</div>
          </div>
          <button
            type="button"
            class="stat"
            title="点击查看"
            :disabled="cardOf(c.id).loading"
            @click="openStat(c, 'pods')"
          >
            <div class="label">Pods（抽样）</div>
            <div class="value">{{ cardOf(c.id).loading ? '…' : cardOf(c.id).stats.pods }}</div>
          </button>
          <button
            type="button"
            class="stat"
            title="点击查看"
            :disabled="cardOf(c.id).loading"
            @click="openStat(c, 'badPods')"
          >
            <div class="label">异常 Pod（抽样）</div>
            <div
              class="value"
              :class="{ 'is-critical': !cardOf(c.id).loading && cardOf(c.id).stats.badPods > 0 }"
            >{{ cardOf(c.id).loading ? '…' : cardOf(c.id).stats.badPods }}</div>
          </button>
        </div>
      </div>
    </div>

    <div v-if="detail.open" class="drawer-backdrop" @click="closeDetail"></div>
    <div v-if="detail.open" class="drawer">
      <div class="toolbar">
        <strong>{{ detail.title }} · {{ detail.clusterName }}</strong>
        <div class="actions">
          <button type="button" class="btn btn-sm" @click="goMore">{{ moreLabel(detail.kind) }}</button>
          <button type="button" class="btn btn-sm" @click="closeDetail">关闭</button>
        </div>
      </div>
      <p v-if="detail.sampled" class="muted sample-hint">仅抽样前 {{ detail.sampleLimit }} 个命名空间，与卡片数字一致。</p>
      <p v-if="detail.error" class="error">{{ detail.error }}</p>
      <p v-else-if="detail.loading" class="muted">加载中…</p>
      <template v-else>
        <table v-if="detail.rows.length" class="table">
          <thead>
            <tr v-if="detail.kind === 'namespaces'">
              <th>名称</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
            <tr v-else-if="detail.kind === 'nodes'">
              <th>名称</th>
              <th>状态</th>
              <th>IP</th>
            </tr>
            <tr v-else>
              <th>命名空间</th>
              <th>名称</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, idx) in detail.rows" :key="(row.namespace || '') + '/' + (row.name || idx)">
              <template v-if="detail.kind === 'namespaces'">
                <td>{{ row.name }}</td>
                <td>{{ row.status || '—' }}</td>
                <td class="actions">
                  <button type="button" class="btn btn-sm" @click="openWorkloadNs(row.name)">查看</button>
                </td>
              </template>
              <template v-else-if="detail.kind === 'nodes'">
                <td>{{ row.name }}</td>
                <td>{{ row.status || '—' }}</td>
                <td>{{ row.internalIP || '—' }}</td>
              </template>
              <template v-else>
                <td>{{ row.namespace || '—' }}</td>
                <td>{{ row.name }}</td>
                <td>{{ row.status || '—' }}</td>
                <td class="actions">
                  <button type="button" class="btn btn-sm" @click="openWorkloadNs(row.namespace)">查看</button>
                </td>
              </template>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">暂无数据。</div>
      </template>
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
.sample-hint {
  margin: 0 0 12px;
}
.stat .value.is-critical {
  color: var(--danger);
}
</style>
