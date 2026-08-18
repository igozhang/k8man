<script setup>
import { ref, watch, onMounted, onUnmounted, computed } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { copyText } from '../utils/clipboard'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import YamlDrawer from '../components/YamlDrawer.vue'

const { currentClusterId, currentCluster } = useClusterStore()

const loading = ref(false)
const error = ref('')
const issues = ref([])
const counts = ref(emptyCounts())
const truncated = ref(false)
const skippedNodes = ref(false)
const scanErrors = ref({})
const scan = ref({})
const copiedKey = ref('')
let copiedTimer = 0
const category = ref('')
const nsFilter = ref('')
const severity = ref('')
const loadSeq = createSeq()
const logsDrawer = ref(null)
const yamlDrawer = ref(null)

const isProject = computed(() => currentCluster.value?.permType === 'project')

const namespaces = computed(() => {
  const set = new Set()
  for (const it of issues.value) {
    if (it?.namespace) set.add(it.namespace)
  }
  return [...set].sort()
})

const filtered = computed(() => {
  return issues.value.filter((it) => {
    if (category.value && it.category !== category.value) return false
    if (severity.value && it.severity !== severity.value) return false
    if (nsFilter.value && it.category !== 'node' && it.namespace !== nsFilter.value) return false
    return true
  })
})

const errorEntries = computed(() => {
  const obj = scanErrors.value || {}
  return Object.keys(obj).map((k) => ({ key: k, message: obj[k] }))
})

function emptyCounts() {
  return { nodes: 0, workloads: 0, pods: 0, events: 0, critical: 0, warning: 0 }
}

function emptyReport() {
  issues.value = []
  counts.value = emptyCounts()
  truncated.value = false
  skippedNodes.value = isProject.value
  scanErrors.value = {}
  scan.value = {}
}

function kindKey(row) {
  return String(row?.kind || '').toLowerCase()
}

function canYAML(row) {
  return !!row?.namespace && ['pod', 'deployment', 'statefulset', 'daemonset'].includes(kindKey(row))
}

function canLogs(row) {
  return kindKey(row) === 'pod' && !!row.namespace
}

function severityLabel(s) {
  return s === 'critical' ? '严重' : '警告'
}

function categoryLabel(c) {
  return { node: '节点', workload: '负载', pod: 'Pod', event: '事件' }[c] || c
}

function rowKey(row, idx) {
  return [row.category, row.kind, row.namespace, row.name, row.reason, idx].join('/')
}

function evidenceHead(row) {
  return (row?.evidence || []).slice(0, 2)
}

function evidenceTail(row) {
  return (row?.evidence || []).slice(2)
}

function issueCopyText(row) {
  const loc = row.namespace ? `${row.namespace}/${row.name}` : row.name
  const lines = [`[${severityLabel(row.severity)}] ${row.kind} ${loc} ${row.reason}`]
  if (row.message) lines.push(row.message)
  if (row.node) lines.push(`node=${row.node}`)
  if (row.container) lines.push(`container=${row.container}`)
  if (row.since) lines.push(`since=${row.since}`)
  if (row.age) lines.push(`age=${row.age}`)
  if (row.logPath) lines.push(`logPath=${row.logPath}`)
  if (row.logSource) lines.push(`logSource=${row.logSource}`)
  for (const e of row.evidence || []) lines.push(`- ${e}`)
  for (const e of row.logExcerpt || []) lines.push(`| ${e}`)
  return lines.join('\n')
}

async function copyIssue(row, idx) {
  try {
    await copyText(issueCopyText(row))
    copiedKey.value = rowKey(row, idx)
    window.clearTimeout(copiedTimer)
    copiedTimer = window.setTimeout(() => {
      if (copiedKey.value === rowKey(row, idx)) copiedKey.value = ''
    }, 1500)
  } catch {
    error.value = '复制失败'
  }
}

const scanLine = computed(() => {
  const s = scan.value || {}
  const wl = (s.deployments || 0) + (s.statefulSets || 0) + (s.daemonSets || 0)
  const parts = []
  if (!skippedNodes.value) parts.push(`${s.nodes ?? 0} 节点`)
  parts.push(`${s.pods ?? 0} Pod`)
  parts.push(`${wl} 负载`)
  parts.push(`${s.events ?? 0} 事件`)
  if (s.eventsAttached) parts.push(`${s.eventsAttached} 条已挂到异常`)
  if (s.logsFetched) parts.push(`${s.logsFetched} 份日志摘录`)
  if (s.metricsSkipped) parts.push('指标未采集')
  if (s.elapsedMs != null && s.elapsedMs !== '') parts.push(`${(Number(s.elapsedMs) / 1000).toFixed(1)}s`)
  if (s.fromCache) parts.push('读缓存')
  else if (s.nodes || s.pods) parts.push('未走缓存')
  return parts.length ? `已扫描 ${parts.join(' · ')}` : ''
})

async function load() {
  const seq = loadSeq.next()
  error.value = ''
  if (!currentClusterId.value) {
    emptyReport()
    loading.value = false
    return
  }
  loading.value = true
  try {
    const data = await readApi.diagnose(currentClusterId.value)
    if (!loadSeq.is(seq)) return
    issues.value = asArray(data?.issues)
    counts.value = { ...emptyCounts(), ...(data?.counts || {}) }
    truncated.value = !!data?.truncated
    skippedNodes.value = !!data?.skippedNodes
    scanErrors.value = data?.errors && typeof data.errors === 'object' ? data.errors : {}
    scan.value = data?.scan && typeof data.scan === 'object' ? data.scan : {}
  } catch (e) {
    if (!loadSeq.is(seq)) return
    error.value = errMessage(e)
    emptyReport()
  } finally {
    if (loadSeq.is(seq)) loading.value = false
  }
}

function showLogs(row) {
  logsDrawer.value?.open(row.name, row.namespace, {
    container: row.container,
    previous: String(row.logSource || '').startsWith('previous')
  })
}

function openYAML(row) {
  yamlDrawer.value?.open(kindKey(row), row.name, row.namespace)
}

function onChildError(msg) {
  error.value = msg
}

watch(currentClusterId, () => {
  category.value = ''
  nsFilter.value = ''
  severity.value = ''
  copiedKey.value = ''
  logsDrawer.value?.close()
  yamlDrawer.value?.close()
  load()
})
onMounted(load)
onUnmounted(() => {
  window.clearTimeout(copiedTimer)
})
</script>

<template>
  <div>
    <h1>诊断</h1>
    <p class="page-desc">扫描当前集群的节点、控制器、Pod 与近期 Warning 事件，只列出异常。</p>
    <div v-if="!currentClusterId" class="empty">请先在「KubeConfig」导入集群，并在上方选择集群。</div>
    <template v-else>
      <div class="grid">
        <div class="panel stat">
          <div class="label">节点异常</div>
          <div class="value" :class="{ 'is-critical': !loading && !skippedNodes && counts.nodes > 0 }">
            {{ skippedNodes ? '—' : (loading ? '…' : counts.nodes) }}
          </div>
        </div>
        <div class="panel stat">
          <div class="label">负载异常</div>
          <div class="value" :class="{ 'is-critical': !loading && counts.workloads > 0 }">
            {{ loading ? '…' : counts.workloads }}
          </div>
        </div>
        <div class="panel stat">
          <div class="label">Pod 异常</div>
          <div class="value" :class="{ 'is-critical': !loading && counts.pods > 0 }">
            {{ loading ? '…' : counts.pods }}
          </div>
        </div>
        <div class="panel stat">
          <div class="label">Warning 事件</div>
          <div class="value" :class="{ 'is-critical': !loading && counts.events > 0 }">
            {{ loading ? '…' : counts.events }}
          </div>
        </div>
      </div>

      <div class="toolbar toolbar-start">
        <div class="tabs">
          <button class="tab" :class="{ active: category === '' }" type="button" @click="category = ''">全部</button>
          <button
            v-if="!isProject"
            class="tab"
            :class="{ active: category === 'node' }"
            type="button"
            @click="category = 'node'"
          >节点</button>
          <button class="tab" :class="{ active: category === 'workload' }" type="button" @click="category = 'workload'">负载</button>
          <button class="tab" :class="{ active: category === 'pod' }" type="button" @click="category = 'pod'">Pod</button>
          <button class="tab" :class="{ active: category === 'event' }" type="button" @click="category = 'event'">事件</button>
        </div>
        <select v-model="nsFilter">
          <option value="">全部命名空间</option>
          <option v-for="n in namespaces" :key="n" :value="n">{{ n }}</option>
        </select>
        <select v-model="severity">
          <option value="">全部严重度</option>
          <option value="critical">严重</option>
          <option value="warning">警告</option>
        </select>
        <button class="btn btn-sm" type="button" :disabled="loading" @click="load">刷新</button>
      </div>

      <p v-if="error" class="error">{{ error }}</p>
      <p v-for="e in errorEntries" :key="e.key" class="muted">{{ categoryLabel(e.key) }}扫描失败：{{ e.message }}</p>
      <p v-if="scanLine && !loading" class="muted">{{ scanLine }}</p>
      <p v-if="scan.metricsSkipped && !loading" class="muted">节点指标未采集，CPU/内存异常无法判断：{{ scan.metricsSkipped }}</p>
      <p v-if="truncated" class="muted">结果已截断（见上方扫描统计），仅展示部分异常。</p>
      <p v-if="loading" class="muted">加载中…</p>

      <table v-else-if="filtered.length" class="table">
        <thead>
          <tr>
            <th>严重程度</th>
            <th>类型</th>
            <th>命名空间</th>
            <th>名称</th>
            <th>原因</th>
            <th>说明</th>
            <th>持续</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, idx) in filtered" :key="rowKey(row, idx)">
            <td>
              <span class="tag" :class="row.severity === 'critical' ? 'sev-critical' : 'sev-warning'">
                {{ severityLabel(row.severity) }}
              </span>
            </td>
            <td>{{ categoryLabel(row.category) }} · {{ row.kind }}</td>
            <td>{{ row.namespace || '—' }}</td>
            <td>{{ row.name }}</td>
            <td>{{ row.reason }}</td>
            <td class="msg">
              <div>{{ row.message }}</div>
              <ul v-if="evidenceHead(row).length" class="evidence-preview">
                <li v-for="(ev, ei) in evidenceHead(row)" :key="'h' + ei">{{ ev }}</li>
              </ul>
              <details v-if="evidenceTail(row).length" class="evidence">
                <summary>其余 {{ evidenceTail(row).length }} 条证据</summary>
                <ul>
                  <li v-for="(ev, ei) in evidenceTail(row)" :key="'t' + ei">{{ ev }}</li>
                </ul>
              </details>
              <div v-if="row.logPath || (row.logExcerpt && row.logExcerpt.length)" class="log-evidence">
                <div v-if="row.logPath" class="log-path">{{ row.logPath }}</div>
                <div v-if="row.logSource" class="muted">{{ row.logSource }}</div>
                <pre v-if="row.logExcerpt && row.logExcerpt.length" class="log-excerpt">{{ row.logExcerpt.join('\n') }}</pre>
              </div>
            </td>
            <td>
              {{ row.since || row.age || '—' }}
              <span v-if="row.since && row.age && row.since !== row.age" class="muted"> / {{ row.age }}</span>
            </td>
            <td class="actions">
              <button v-if="canLogs(row)" class="btn btn-sm" type="button" @click="showLogs(row)">日志</button>
              <button v-if="canYAML(row)" class="btn btn-sm" type="button" @click="openYAML(row)">YAML</button>
              <button class="btn btn-sm" type="button" @click="copyIssue(row, idx)">
                {{ copiedKey === rowKey(row, idx) ? '已复制' : '复制' }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else-if="!loading" class="empty">
        {{ issues.length ? '当前筛选下没有匹配项。' : '未发现异常。' }}
      </div>
    </template>

    <PodLogsDrawer ref="logsDrawer" :cluster-id="currentClusterId" />
    <YamlDrawer ref="yamlDrawer" :cluster-id="currentClusterId" read-only @error="onChildError" />
  </div>
</template>

<style scoped>
.grid {
  margin-bottom: 16px;
}
.stat .value.is-critical {
  color: var(--danger);
}
.tag.sev-critical {
  background: #fef2f2;
  color: #b91c1c;
}
.tag.sev-warning {
  background: #fffbeb;
  color: var(--warn);
}
.msg {
  max-width: 22rem;
  word-break: break-word;
  color: var(--muted);
}
.evidence-preview,
.evidence ul {
  margin: 6px 0 0;
  padding-left: 1.1em;
}
.evidence-preview li,
.evidence li {
  font-size: 12px;
  line-height: 1.45;
  word-break: break-word;
}
.evidence {
  margin-top: 6px;
}
.evidence summary {
  cursor: pointer;
  color: var(--text);
  font-size: 12px;
  line-height: 1.4;
}
.log-evidence {
  margin-top: 8px;
}
.log-path {
  font-family: var(--mono);
  font-size: 11px;
  line-height: 1.4;
  word-break: break-all;
  color: var(--text);
}
.log-excerpt {
  margin: 6px 0 0;
  padding: 8px 10px;
  max-height: 9.5em;
  overflow: auto;
  font-family: var(--mono);
  font-size: 11px;
  line-height: 1.4;
  white-space: pre-wrap;
  word-break: break-word;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 8px;
}
</style>
