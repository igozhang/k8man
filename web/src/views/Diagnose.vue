<script setup>
import { ref, watch, onMounted, onUnmounted, computed } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { copyText } from '../utils/clipboard'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import YamlDrawer from '../components/YamlDrawer.vue'
import TextDrawer from '../components/TextDrawer.vue'

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
const explainSeq = createSeq()
const EXPLAIN_LOG_TAIL = 40
const logsDrawer = ref(null)
const yamlDrawer = ref(null)
const explainDrawer = ref(null)

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
  const isPodObj = kindKey(row) === 'pod' && !!row.namespace
  if (!isPodObj || (row.category !== 'pod' && row.category !== 'event')) return false
  return ![
    'Evicted',
    'Shutdown',
    'NodeShutdown',
    'Preempting',
    'Terminated',
    'Unschedulable',
    'Pending',
    'ImagePullBackOff',
    'ErrImagePull',
    'InvalidImageName'
  ].includes(row.reason)
}

function locationLine(row) {
  if (row.node && row.nodeIP) return `${row.node} (${row.nodeIP})`
  return row.node || row.nodeIP || ''
}

function sourceTitle(row) {
  switch (row.category) {
    case 'event':
      return row.reporter
        ? `Kubernetes Warning 事件 · 上报 ${row.reporter}`
        : 'Kubernetes Warning 事件'
    case 'node':
      return '节点条件 / 用量'
    case 'workload':
      return '控制器副本状态'
    case 'pod':
      return 'Pod 状态'
    default:
      return row.category || '诊断扫描'
  }
}

function explainBrief(row) {
  const parts = []
  if (row.message) parts.push(String(row.message).trim())
  const loc = locationLine(row)
  if (loc && row.category !== 'node') parts.push(loc)
  else if (row.category === 'node' && row.nodeIP) parts.push(row.nodeIP)
  const text = parts.filter(Boolean).join(' · ')
  return text || row.reason || '查看完整说明'
}

function evidenceList(row) {
  return Array.isArray(row?.evidence) ? row.evidence.filter((s) => String(s || '').trim()) : []
}

function messageBody(row) {
  return String(row?.message || '').replace(/^x\d+\s*·\s*/, '').trim()
}

function isDuplicateEvidence(row, line) {
  const s = String(line || '').trim()
  if (!s) return true
  if (/^(出处|对象|上报组件|上报节点|容器|次数):/.test(s)) return true
  if (row.node && /^node=/.test(s)) return true
  if (row.category === 'event' && s.startsWith('Warning ')) {
    const body = messageBody(row)
    if (row.reason && s.includes(row.reason) && body && s.includes(body.slice(0, 80))) return true
    if (row.message && s.includes(String(row.message).trim())) return true
  }
  return false
}

function extraEvidence(row) {
  return evidenceList(row).filter((e) => !isDuplicateEvidence(row, e))
}

function shArg(s) {
  const t = String(s || '')
  if (!t) return "''"
  if (/^[A-Za-z0-9._:/-]+$/.test(t)) return t
  return `'${t.replace(/'/g, `'\\''`)}'`
}

function kubectlKind(kind) {
  const k = String(kind || '').toLowerCase()
  return (
    {
      pod: 'pod',
      deployment: 'deployment',
      statefulset: 'statefulset',
      daemonset: 'daemonset',
      node: 'node',
      replicaset: 'replicaset',
      job: 'job',
      cronjob: 'cronjob',
      service: 'service'
    }[k] || k
  )
}

function kubectlCommands(row, logsInfo) {
  const cmds = []
  const kind = kubectlKind(row.kind)
  const apiKind = row.kind || ''
  const name = shArg(row.name)
  const nsFlag = row.namespace ? ` -n ${shArg(row.namespace)}` : ''
  const container = row.container ? ` -c ${shArg(row.container)}` : ''
  const eventSel = [
    apiKind && apiKind !== 'Event' ? `involvedObject.kind=${apiKind}` : '',
    row.name ? `involvedObject.name=${row.name}` : '',
    'type=Warning'
  ]
    .filter(Boolean)
    .join(',')
  const eventsCmd = row.namespace
    ? `kubectl get events${nsFlag} --field-selector ${eventSel}`
    : `kubectl get events -A --field-selector ${eventSel}`

  if (row.category === 'event') {
    cmds.push({ comment: 'Warning 事件', cmd: eventsCmd })
    if (kind === 'pod' && row.namespace) {
      cmds.push({ comment: 'Pod 所在节点与 IP', cmd: `kubectl get pod ${name}${nsFlag} -o wide` })
    } else if (kind === 'node') {
      cmds.push({ comment: '节点地址与状态', cmd: `kubectl get node ${name} -o wide` })
      cmds.push({ comment: '节点条件', cmd: `kubectl describe node ${name}` })
    } else if (row.namespace && kind && kind !== 'event') {
      cmds.push({ comment: '对象状态', cmd: `kubectl get ${kind} ${name}${nsFlag} -o wide` })
    }
  } else if (row.category === 'node') {
    cmds.push({ comment: '节点地址与用量列', cmd: `kubectl get node ${name} -o wide` })
    cmds.push({ comment: '节点条件', cmd: `kubectl describe node ${name}` })
    if (row.reason === 'CPUHigh' || row.reason === 'MemoryHigh') {
      cmds.push({ comment: '节点实时用量', cmd: `kubectl top node ${name}` })
    }
  } else if (row.category === 'workload') {
    cmds.push({ comment: '副本状态', cmd: `kubectl get ${kind} ${name}${nsFlag}` })
    cmds.push({ comment: '条件与近期事件', cmd: `kubectl describe ${kind} ${name}${nsFlag}` })
  } else if (row.category === 'pod') {
    cmds.push({ comment: 'Pod 所在节点与 IP', cmd: `kubectl get pod ${name}${nsFlag} -o wide` })
    cmds.push({ comment: '容器状态与探针', cmd: `kubectl describe pod ${name}${nsFlag}` })
    cmds.push({ comment: 'Warning 事件', cmd: eventsCmd })
  }

  if (canLogs(row)) {
    const prev = !!(logsInfo && logsInfo.previous)
    cmds.push({
      comment: prev ? '上一实例容器日志' : '容器日志',
      cmd: `kubectl logs ${name}${nsFlag}${container} --tail=${EXPLAIN_LOG_TAIL}${prev ? ' --previous' : ''}`
    })
  }
  return cmds.filter((c) => c.cmd)
}

function explainText(row, logsInfo) {
  const lines = []
  lines.push(`来源: ${sourceTitle(row)}`)
  const obj = [row.kind, row.namespace ? `${row.namespace}/${row.name}` : row.name].filter(Boolean).join(' ')
  if (obj) lines.push(`对象: ${obj}`)
  const loc = locationLine(row)
  if (loc) {
    lines.push(`节点: ${loc}`)
  } else if (row.category === 'event' || row.category === 'pod') {
    lines.push('节点: （未拿到节点名/IP，对象可能已删除或尚未调度）')
  }
  if (row.container) lines.push(`容器: ${row.container}`)
  if (row.count && Number(row.count) > 1) lines.push(`次数: ${row.count}`)
  if (row.reason) lines.push(`原因: ${row.reason}`)
  if (row.since) lines.push(`持续: ${row.since}`)
  if (row.age && row.age !== row.since) lines.push(`创建: ${row.age}`)
  if (row.message) {
    lines.push('')
    lines.push('内容:')
    lines.push(row.message)
  }
  const evLines = extraEvidence(row)
  if (evLines.length) {
    lines.push('')
    lines.push('补充:')
    for (const e of evLines) lines.push(`- ${e}`)
  }
  const cmds = kubectlCommands(row, logsInfo)
  if (cmds.length) {
    lines.push('')
    lines.push('核对命令:')
    for (const c of cmds) {
      if (c.comment) lines.push(`# ${c.comment}`)
      lines.push(c.cmd)
    }
  }
  if (logsInfo?.error) {
    lines.push('')
    lines.push(`容器日志不可用: ${logsInfo.error}`)
  } else if (logsInfo?.loading) {
    lines.push('')
    lines.push('容器日志加载中…')
  } else if (logsInfo && Object.prototype.hasOwnProperty.call(logsInfo, 'logs')) {
    const src = logsInfo.previous ? '上一实例' : '当前'
    const c = row.container ? ` · ${row.container}` : ''
    lines.push('')
    lines.push(`容器日志（${src}${c} · 最近 ${EXPLAIN_LOG_TAIL} 行）:`)
    lines.push('')
    lines.push(String(logsInfo.logs || '').replace(/\r\n/g, '\n').trim() || '(empty log tail)')
  }
  return lines.join('\n')
}

async function fetchExplainLogs(row) {
  const ns = row.namespace
  const id = currentClusterId.value
  if (!id || !ns || !row.name) return { logs: '', previous: false }
  const wantPrev = String(row.logSource || '').startsWith('previous')
  const q = {
    tail: EXPLAIN_LOG_TAIL,
    container: row.container || undefined,
    previous: wantPrev
  }
  try {
    const r = await readApi.podLogs(id, ns, row.name, q)
    return { logs: r?.logs || '', previous: wantPrev }
  } catch (e) {
    if (!wantPrev) throw e
    const r = await readApi.podLogs(id, ns, row.name, { ...q, previous: false })
    return { logs: r?.logs || '', previous: false }
  }
}

function showExplain(row) {
  logsDrawer.value?.close()
  yamlDrawer.value?.close()
  const seq = explainSeq.next()
  const loc = row.namespace ? `${row.namespace}/${row.name}` : row.name
  const title = `说明 · ${row.kind} ${loc}`
  if (!canLogs(row) || !currentClusterId.value) {
    explainDrawer.value?.open(title, explainText(row))
    return
  }
  explainDrawer.value?.open(title, explainText(row, { loading: true }))
  fetchExplainLogs(row)
    .then((got) => {
      if (!explainSeq.is(seq)) return
      explainDrawer.value?.update(explainText(row, { logs: got.logs, previous: got.previous }))
    })
    .catch((e) => {
      if (!explainSeq.is(seq)) return
      explainDrawer.value?.update(explainText(row, { error: errMessage(e) }))
    })
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

function issueCopyText(row) {
  return explainText(row) || '（无说明）'
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

function showLogs(row) {
  explainDrawer.value?.close()
  yamlDrawer.value?.close()
  logsDrawer.value?.open(row.name, row.namespace, {
    container: row.container,
    previous: String(row.logSource || '').startsWith('previous')
  })
}

function openYAML(row) {
  explainDrawer.value?.close()
  logsDrawer.value?.close()
  yamlDrawer.value?.open(kindKey(row), row.name, row.namespace)
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

function toggleCategory(cat) {
  if (cat === 'node' && isProject.value) return
  category.value = category.value === cat ? '' : cat
}

function onChildError(msg) {
  error.value = msg
}

watch(currentClusterId, () => {
  category.value = ''
  nsFilter.value = ''
  severity.value = ''
  copiedKey.value = ''
  explainSeq.bump()
  logsDrawer.value?.close()
  yamlDrawer.value?.close()
  explainDrawer.value?.close()
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
        <button
          v-if="!isProject"
          type="button"
          class="panel stat"
          :class="{ 'is-active': category === 'node' }"
          :disabled="loading"
          @click="toggleCategory('node')"
        >
          <div class="label">节点异常</div>
          <div class="value" :class="{ 'is-critical': !loading && counts.nodes > 0 }">
            {{ loading ? '…' : counts.nodes }}
          </div>
        </button>
        <div v-else class="panel stat">
          <div class="label">节点异常</div>
          <div class="value">—</div>
        </div>
        <button
          type="button"
          class="panel stat"
          :class="{ 'is-active': category === 'workload' }"
          :disabled="loading"
          @click="toggleCategory('workload')"
        >
          <div class="label">负载异常</div>
          <div class="value" :class="{ 'is-critical': !loading && counts.workloads > 0 }">
            {{ loading ? '…' : counts.workloads }}
          </div>
        </button>
        <button
          type="button"
          class="panel stat"
          :class="{ 'is-active': category === 'pod' }"
          :disabled="loading"
          @click="toggleCategory('pod')"
        >
          <div class="label">Pod 异常</div>
          <div class="value" :class="{ 'is-critical': !loading && counts.pods > 0 }">
            {{ loading ? '…' : counts.pods }}
          </div>
        </button>
        <button
          type="button"
          class="panel stat"
          :class="{ 'is-active': category === 'event' }"
          :disabled="loading"
          @click="toggleCategory('event')"
        >
          <div class="label">Warning 事件</div>
          <div class="value" :class="{ 'is-critical': !loading && counts.events > 0 }">
            {{ loading ? '…' : counts.events }}
          </div>
        </button>
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
              <button
                class="explain-brief"
                type="button"
                :title="'点击查看完整说明'"
                @click="showExplain(row)"
              >
                <span class="explain-brief-text">{{ explainBrief(row) }}</span>
              </button>
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
    <TextDrawer ref="explainDrawer" />
  </div>
</template>

<style scoped>
.grid {
  margin-bottom: 16px;
}
.grid > .stat {
  margin-bottom: 0;
}
button.stat {
  appearance: none;
  -webkit-appearance: none;
  width: 100%;
  margin-bottom: 0;
  text-align: left;
  cursor: pointer;
  font: inherit;
  color: inherit;
}
button.stat:disabled {
  cursor: default;
}
button.stat.is-active {
  border-color: var(--accent);
  box-shadow: 0 0 0 1px var(--accent);
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
  max-width: 28rem;
  vertical-align: middle;
}
.explain-brief {
  appearance: none;
  -webkit-appearance: none;
  display: inline;
  max-width: 28rem;
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--text);
  font: inherit;
  font-size: var(--ctrl-font);
  line-height: var(--ctrl-line);
  text-align: left;
  cursor: pointer;
  vertical-align: middle;
}
.explain-brief-text {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-word;
  color: inherit;
  line-height: var(--ctrl-line);
}
.explain-brief:hover .explain-brief-text,
.explain-brief:focus-visible .explain-brief-text {
  color: var(--accent);
  text-decoration: underline;
}
.explain-brief:focus-visible {
  outline: none;
}
</style>
