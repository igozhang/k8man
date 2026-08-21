<script setup>
import { ref, watch, onMounted, onUnmounted, computed, nextTick } from 'vue'
import { useRoute } from 'vue-router'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { copyText } from '../utils/clipboard'
import { TTL, cacheFresh, cacheGet, cacheKey, cacheLoad } from '../utils/resourceCache'
import { permTypeLabel, severityLabel, severityClass, issueCategoryLabel, issueReasonDisplay } from '../utils/labels'
import { formatIssueMessage, issueMessageSuffix, issueTimeLabel, issueInspectBrief, formatEvidenceLine } from '../utils/inspect'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import YamlDrawer from '../components/YamlDrawer.vue'
import TextDrawer from '../components/TextDrawer.vue'
import FilterSelect from '../components/FilterSelect.vue'
import InspectRules from '../components/InspectRules.vue'

const { currentClusterId, currentCluster } = useClusterStore()
const route = useRoute()
const DIAG_CATS = new Set(['node', 'workload', 'pod', 'event'])

const busy = ref(false)
const hasReport = ref(false)
const loading = computed(() => busy.value && !hasReport.value)
const refreshing = computed(() => busy.value && hasReport.value)
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
const showInfo = ref(false)
const loadSeq = createSeq()
const explainSeq = createSeq()
const EXPLAIN_LOG_TAIL = 40
const logsDrawer = ref(null)
const yamlDrawer = ref(null)
const explainDrawer = ref(null)
const RULES_KEY = 'k8man.diagnoseRulesOpen'
const rulesOpen = ref(true)
const rulesEl = ref(null)

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
    if (!severity.value && !showInfo.value && it.severity === 'info') return false
    if (nsFilter.value && it.category !== 'node' && it.namespace !== nsFilter.value) return false
    return true
  })
})

const errorEntries = computed(() => {
  const obj = scanErrors.value || {}
  return Object.keys(obj).map((k) => ({ key: k, message: obj[k] }))
})

function emptyTableHint() {
  if (!issues.value.length) return '未发现异常。'
  const onlyInfoHidden =
    !showInfo.value &&
    !severity.value &&
    issues.value.every((it) => it.severity === 'info')
  if (onlyInfoHidden) return '仅有提示级异常，勾选「显示提示」查看。'
  return '当前筛选下没有匹配项。'
}

function emptyCounts() {
  return { nodes: 0, workloads: 0, pods: 0, events: 0, critical: 0, error: 0, warning: 0, info: 0 }
}

function emptyReport() {
  issues.value = []
  counts.value = emptyCounts()
  truncated.value = false
  skippedNodes.value = isProject.value
  scanErrors.value = {}
  scan.value = {}
  hasReport.value = false
}

function applyReport(data) {
  issues.value = asArray(data?.issues)
  counts.value = { ...emptyCounts(), ...(data?.counts || {}) }
  truncated.value = !!data?.truncated
  skippedNodes.value = !!data?.skippedNodes
  scanErrors.value = data?.errors && typeof data.errors === 'object' ? data.errors : {}
  scan.value = data?.scan && typeof data.scan === 'object' ? data.scan : {}
  hasReport.value = true
}

{
  const hit = currentClusterId.value ? cacheGet(cacheKey('diagnose', currentClusterId.value)) : null
  if (hit?.data) applyReport(hit.data)
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
    'InvalidImageName',
    'FailedScheduling',
    'FailedMount',
    'FailedAttachVolume',
    'FailedCreate',
    'FailedBinding'
  ].includes(row.reason)
}

function typeLabel(row) {
  const cat = issueCategoryLabel(row.category)
  const kind = String(row?.kind || '').trim()
  if (!kind) return cat
  if (row.category === 'pod' && kind.toLowerCase() === 'pod') return 'Pod'
  if (row.category === 'node' && kind.toLowerCase() === 'node') return '节点'
  return `${cat} · ${kind}`
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
  const msg = formatIssueMessage(row)
  const suffix = issueMessageSuffix(row)
  if (msg || suffix) parts.push(`${msg}${suffix}`.trim())
  const loc = locationLine(row)
  if (loc && row.category !== 'node') parts.push(loc)
  else if (row.category === 'node' && row.nodeIP) parts.push(row.nodeIP)
  const text = parts.filter(Boolean).join(' · ')
  return text || issueReasonDisplay(row.reason) || '查看完整说明'
}

function evidenceList(row) {
  return Array.isArray(row?.evidence) ? row.evidence.filter((s) => String(s || '').trim()) : []
}

function messageBody(row) {
  return String(row?.message || '').replace(/^x\d+\s*·\s*/, '').trim()
}

function evidenceSelector(row) {
  for (const e of evidenceList(row)) {
    const s = String(e || '').trim()
    if (s.startsWith('selector=')) return s.slice('selector='.length).trim()
  }
  return ''
}

function hasRestartEvidence(row) {
  return evidenceList(row).some((e) => /^restart\[\d+\]/.test(String(e || '').trim()))
}

function isDuplicateEvidence(row, line) {
  const s = String(line || '').trim()
  if (!s) return true
  if (/^(出处|对象|上报组件|上报节点|容器|次数):/.test(s)) return true
  if (row.node && /^node=/.test(s)) return true
  if (/^selector=/.test(s)) return true
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
    const sel = evidenceSelector(row)
    if (sel && row.namespace) {
      cmds.push({
        comment: '下属 Pod 重启计数与上次异常退出',
        cmd: `kubectl get pods${nsFlag} -l ${shArg(sel)} -o 'custom-columns=NAME:.metadata.name,RESTARTS:.status.containerStatuses[0].restartCount,LAST:.status.containerStatuses[0].lastState.terminated.reason,FINISHED:.status.containerStatuses[0].lastState.terminated.finishedAt'`
      })
    }
  } else if (row.category === 'pod') {
    cmds.push({ comment: 'Pod 所在节点与 IP', cmd: `kubectl get pod ${name}${nsFlag} -o wide` })
    cmds.push({ comment: '容器状态与探针', cmd: `kubectl describe pod ${name}${nsFlag}` })
    cmds.push({ comment: 'Warning 事件', cmd: eventsCmd })
    if (hasRestartEvidence(row) && row.namespace) {
      cmds.push({
        comment: '上次退出（API 只保留 1 次；Completed / exit=0 不计入 Restarts）',
        cmd: `kubectl get pod ${name}${nsFlag} -o jsonpath='{range .status.containerStatuses[*]}{.name} lastState={.lastState.terminated.reason} exit={.lastState.terminated.exitCode} finished={.lastState.terminated.finishedAt} restarts={.restartCount}{"\\n"}{end}'`
      })
      cmds.push({
        comment: '重启相关事件（含聚合 BackOff）',
        cmd: `kubectl get events${nsFlag} --field-selector involvedObject.kind=Pod,involvedObject.name=${name} --sort-by=.lastTimestamp`
      })
    }
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
  if (row.reason) lines.push(`原因: ${issueReasonDisplay(row.reason)}`)
  if (row.baseSeverity && row.baseSeverity !== row.severity) {
    lines.push(`级别: ${severityLabel(row.baseSeverity)} → ${severityLabel(row.severity)}（长期未处理已降级）`)
  }
  if (row.category === 'event') {
    if (row.since || row.age) lines.push(`上次: ${row.since || row.age} 前`)
  } else {
    if (row.since) lines.push(`时间: ${issueTimeLabel({ ...row, age: '' })}`)
    if (row.age && row.age !== row.since) lines.push(`对象创建: ${row.age}`)
  }
  const msg = formatIssueMessage(row)
  if (msg) {
    lines.push('')
    lines.push('内容:')
    lines.push(msg)
  }
  const evLines = extraEvidence(row)
  if (evLines.length) {
    lines.push('')
    lines.push('补充:')
    for (const e of evLines) lines.push(`- ${formatEvidenceLine(e)}`)
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

function categoryLabel(c) {
  return issueCategoryLabel(c)
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
  parts.push(`${s.events ?? 0} 条 Warning`)
  if (s.eventsAttached) parts.push(`${s.eventsAttached} 条已挂到异常`)
  if (s.logsFetched) parts.push(`${s.logsFetched} 份日志摘录`)
  if (s.metricsSkipped) parts.push('指标未采集')
  if (s.elapsedMs != null && s.elapsedMs !== '') parts.push(`${(Number(s.elapsedMs) / 1000).toFixed(1)}s`)
  if (s.fromCache) parts.push('读缓存')
  else if (s.nodes || s.pods) parts.push('未走缓存')
  return parts.length ? `已扫描 ${parts.join(' · ')}` : ''
})

function clipSummaryMsg(s, n = 200) {
  const t = String(s || '').replace(/\s+/g, ' ').trim()
  if (t.length <= n) return t
  return `${t.slice(0, n)}…`
}

function issueSummaryLine(row) {
  return clipSummaryMsg(issueInspectBrief(row), 240)
}

function scanHasError(cat) {
  return !!(scanErrors.value && scanErrors.value[cat])
}

function healthyCheckLines() {
  const s = scan.value || {}
  const lines = []
  const pods = s.pods ?? 0
  const nodes = s.nodes ?? 0
  const dep = s.deployments || 0
  const sts = s.statefulSets || 0
  const ds = s.daemonSets || 0
  const wl = dep + sts + ds
  const ev = s.events ?? 0

  if (skippedNodes.value) {
    lines.push('- 节点: 未检查（项目凭证）')
  } else if (!scanHasError('nodes')) {
    if (!nodes) {
      lines.push('- 节点: 范围内无节点')
    } else if (s.metricsSkipped) {
      lines.push(`- 节点: ${nodes} 个均 Ready，无 Disk/Memory/PID/Network 压力；用量未采集（${s.metricsSkipped}）`)
    } else {
      lines.push(`- 节点: ${nodes} 个均 Ready，无 Disk/Memory/PID/Network 压力，CPU/内存用量未达 90%`)
    }
  }

  if (!scanHasError('workloads')) {
    if (!wl) {
      lines.push('- 负载: 范围内无 Deployment / StatefulSet / DaemonSet')
    } else {
      const kinds = []
      if (dep) kinds.push(`${dep} Deployment`)
      if (sts) kinds.push(`${sts} StatefulSet`)
      if (ds) kinds.push(`${ds} DaemonSet`)
      lines.push(`- 负载: ${kinds.join(' / ')} 副本就绪，无 misscheduled`)
    }
  }

  if (!scanHasError('pods')) {
    if (!pods) {
      lines.push('- Pod: 范围内无 Pod')
    } else {
      lines.push(`- Pod: ${pods} 个无 CrashLoop / 拉镜像失败 / CreateContainerError / OOM；无 Failed；无长时间 Pending；无 Restarts（计入 ≥ 3 且上次异常退出 ≤ 30 天；Completed / exit=0 不计）`)
    }
  }

  if (!scanHasError('events')) {
    lines.push(`- 事件: 已看 ${ev} 条 Warning，无近 2 小时独立成行的调度/挂载/绑定失败`)
  }

  return lines
}

const summaryFull = computed(() => {
  const lines = []
  const name = currentCluster.value?.name || ''
  const perm = permTypeLabel(currentCluster.value?.permType || '')
  if (!currentClusterId.value) {
    return '结论: 尚未选择集群。'
  }
  lines.push(`集群: ${name || '未命名'}${perm ? `（${perm}）` : ''}`)
  if (loading.value) {
    lines.push('正在扫描…')
    return lines.join('\n')
  }
  if (error.value) {
    lines.push(`扫描失败: ${error.value}`)
  }
  const c = counts.value
  const total = issues.value.length
  if (!total && !error.value) {
    lines.push('结论: 未发现当前问题。')
    const checks = healthyCheckLines()
    if (checks.length) {
      lines.push('检查结果:')
      lines.push(...checks)
    }
  } else if (total) {
    lines.push(
      `结论: 发现 ${total} 条（严重 ${c.critical || 0} · 错误 ${c.error || 0} · 警告 ${c.warning || 0}${c.info ? ` · 提示 ${c.info}` : ''}）。`
    )
  }
  if (scanLine.value) lines.push(scanLine.value)
  if (skippedNodes.value && total) lines.push('节点: 项目凭证未扫描。')
  if (scan.value?.metricsSkipped && total) lines.push(`节点指标未采集: ${scan.value.metricsSkipped}`)
  if (truncated.value) lines.push('结果已截断，仅展示部分条目。')
  for (const e of errorEntries.value) {
    lines.push(`${categoryLabel(e.key)}扫描失败: ${e.message}`)
  }
  const crit = issues.value.filter((it) => it.severity === 'critical')
  const errs = issues.value.filter((it) => it.severity === 'error')
  const warn = issues.value.filter((it) => it.severity === 'warning')
  const infos = issues.value.filter((it) => it.severity === 'info')
  if (crit.length) {
    lines.push('')
    lines.push(`严重 (${crit.length}):`)
    for (const it of crit) lines.push(`- ${issueSummaryLine(it)}`)
  }
  if (errs.length) {
    lines.push('')
    lines.push(`错误 (${errs.length}):`)
    for (const it of errs) lines.push(`- ${issueSummaryLine(it)}`)
  }
  if (warn.length) {
    lines.push('')
    lines.push(`警告 (${warn.length}):`)
    for (const it of warn) lines.push(`- ${issueSummaryLine(it)}`)
  }
  if (infos.length) {
    lines.push('')
    lines.push(`提示 (${infos.length}):`)
    for (const it of infos) lines.push(`- ${issueSummaryLine(it)}`)
  }
  return lines.join('\n')
})

const summaryBox = ref(null)
const summaryOverflow = ref(false)

function measureSummary() {
  const el = summaryBox.value
  if (!el) {
    summaryOverflow.value = false
    return
  }
  summaryOverflow.value = el.scrollHeight > el.clientHeight + 1
}

function showSummary() {
  if (!summaryOverflow.value) return
  logsDrawer.value?.close()
  yamlDrawer.value?.close()
  explainSeq.bump()
  explainDrawer.value?.open('诊断总结', summaryFull.value)
}

async function load(opts = {}) {
  const force = opts?.force === true
  const seq = loadSeq.next()
  error.value = ''
  if (!currentClusterId.value) {
    emptyReport()
    busy.value = false
    return
  }
  const clusterId = currentClusterId.value
  const key = cacheKey('diagnose', clusterId)
  const hit = cacheGet(key)
  if (hit?.data) applyReport(hit.data)
  else emptyReport()
  if (!force && cacheFresh(key, TTL.diagnose)) {
    busy.value = false
    return
  }
  busy.value = true
  try {
    const data = await cacheLoad(
      key,
      TTL.diagnose,
      () => readApi.diagnose(clusterId),
      { force }
    )
    if (!loadSeq.is(seq)) return
    if (data) applyReport(data)
  } catch (e) {
    if (!loadSeq.is(seq)) return
    error.value = errMessage(e)
    if (!hit) emptyReport()
  } finally {
    if (loadSeq.is(seq)) busy.value = false
  }
}

function toggleCategory(cat) {
  if (cat === 'node' && isProject.value) return
  category.value = category.value === cat ? '' : cat
}

function applyQueryCategory() {
  const q = String(route.query.category || '')
  if (!DIAG_CATS.has(q)) return
  if (q === 'node' && isProject.value) return
  category.value = q
}

function onChildError(msg) {
  error.value = msg
}

function onRulesToggle(e) {
  const open = !!e.target.open
  rulesOpen.value = open
  try {
    localStorage.setItem(RULES_KEY, open ? '1' : '0')
  } catch {
    /* ignore */
  }
}

watch(summaryFull, () => {
  nextTick(measureSummary)
})
watch(currentClusterId, () => {
  category.value = ''
  nsFilter.value = ''
  severity.value = ''
  copiedKey.value = ''
  explainSeq.bump()
  logsDrawer.value?.close()
  yamlDrawer.value?.close()
  explainDrawer.value?.close()
  applyQueryCategory()
  load()
})
watch(() => String(route.query.category || ''), applyQueryCategory)
onMounted(() => {
  try {
    const v = localStorage.getItem(RULES_KEY)
    if (v === '0') rulesOpen.value = false
    if (v === '1') rulesOpen.value = true
  } catch {
    /* ignore */
  }
  rulesEl.value?.setOpen(rulesOpen.value)
  window.addEventListener('resize', measureSummary)
  applyQueryCategory()
  load()
  nextTick(measureSummary)
})
onUnmounted(() => {
  window.removeEventListener('resize', measureSummary)
  window.clearTimeout(copiedTimer)
})
</script>

<template>
  <div>
    <h1>诊断</h1>
    <p class="page-desc">列出当前仍成立的问题。滚动发布中的副本缺口、探针未达阈值的 Warning 事件不计入。Restarts 例外：Pod 已 Ready，但计入重启 ≥ 3（Completed / exit=0 不计）且上次异常退出在 30 天内仍会列出。同一会话内 90 秒内再次打开沿用上次扫描；需要最新结果时点「刷新」。判定规则见页底。全配置约 2 小时一轮的定时巡检在「监控」页底部，不替代本页即时扫描。</p>
    <div v-if="!currentClusterId" class="empty">请先在顶部选择当前集群，或到「概览」里点选用。</div>
    <template v-else>
      <div class="panel stats-strip">
        <button
          v-if="!isProject"
          type="button"
          class="stat"
          :class="{ 'is-active': category === 'node' }"
          :disabled="loading"
          @click="toggleCategory('node')"
        >
          <div class="label">节点异常</div>
          <div class="value" :class="{ 'is-critical': !loading && counts.nodes > 0 }">
            {{ loading ? '…' : counts.nodes }}
          </div>
        </button>
        <div v-else class="stat">
          <div class="label">节点异常</div>
          <div class="value">—</div>
        </div>
        <button
          type="button"
          class="stat"
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
          class="stat"
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
          class="stat"
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
        <FilterSelect
          v-model="nsFilter"
          :options="namespaces"
          empty-label="全部命名空间"
          placeholder="输入 1–3 个字母筛选命名空间"
          aria-label="筛选命名空间"
        />
        <select v-model="severity">
          <option value="">全部严重度</option>
          <option value="critical">严重</option>
          <option value="error">错误</option>
          <option value="warning">警告</option>
          <option value="info">提示</option>
        </select>
        <label class="show-info">
          <input v-model="showInfo" type="checkbox" />
          显示提示
        </label>
        <button class="btn btn-sm" type="button" :disabled="busy" @click="load({ force: true })">刷新</button>
      </div>

      <p v-if="error" class="error">{{ error }}</p>
      <p v-for="e in errorEntries" :key="e.key" class="muted">{{ categoryLabel(e.key) }}扫描失败：{{ e.message }}</p>
      <p v-if="scanLine && hasReport" class="muted">{{ scanLine }}</p>
      <p v-if="scan.metricsSkipped && hasReport" class="muted">节点指标未采集，CPU/内存异常无法判断：{{ scan.metricsSkipped }}</p>
      <p v-if="truncated" class="muted">结果已截断（见上方扫描统计），仅展示部分条目。</p>
      <p v-if="loading" class="muted">加载中…</p>
      <p v-else-if="refreshing" class="muted">更新中…</p>

      <table v-if="filtered.length" class="table">
        <thead>
          <tr>
            <th>严重程度</th>
            <th>类型</th>
            <th>命名空间</th>
            <th>名称</th>
            <th>原因</th>
            <th>说明</th>
            <th>时间</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, idx) in filtered" :key="rowKey(row, idx)">
            <td>
              <span class="tag" :class="severityClass(row.severity)">
                {{ severityLabel(row.severity) }}
              </span>
            </td>
            <td>{{ typeLabel(row) }}</td>
            <td>{{ row.namespace || '—' }}</td>
            <td>{{ row.name }}</td>
            <td>{{ issueReasonDisplay(row.reason) }}</td>
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
            <td>{{ issueTimeLabel(row) }}</td>
            <td class="actions">
              <button v-if="canLogs(row)" class="btn btn-sm" type="button" @click="showLogs(row)">日志</button>
              <button v-if="canYAML(row)" class="btn btn-sm" type="button" @click="openYAML(row)">YAML</button>
              <button
                class="btn btn-sm"
                type="button"
                :class="{ 'is-current': copiedKey === rowKey(row, idx) }"
                @click="copyIssue(row, idx)"
              >
                {{ copiedKey === rowKey(row, idx) ? '已复制' : '复制' }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else-if="!loading" class="empty">
        {{ emptyTableHint() }}
      </div>
    </template>

    <div class="panel summary-panel">
      <div class="summary-head">
        <span class="summary-title">诊断总结</span>
        <button
          v-if="summaryOverflow"
          class="btn btn-sm summary-more"
          type="button"
          @click.stop="showSummary"
        >查看全部</button>
      </div>
      <div
        ref="summaryBox"
        class="summary-preview-text"
        :class="{ 'is-clickable': summaryOverflow }"
        :role="summaryOverflow ? 'button' : undefined"
        :tabindex="summaryOverflow ? 0 : undefined"
        :title="summaryOverflow ? '点击在右侧查看全部' : undefined"
        @click="showSummary"
        @keydown.enter.prevent="showSummary"
      >{{ summaryFull }}</div>
    </div>

    <InspectRules ref="rulesEl" class="panel" title="判定规则" @toggle="onRulesToggle" />

    <PodLogsDrawer ref="logsDrawer" :cluster-id="currentClusterId" />
    <YamlDrawer ref="yamlDrawer" :cluster-id="currentClusterId" read-only @error="onChildError" />
    <TextDrawer ref="explainDrawer" />
  </div>
</template>

<style scoped>
.stat .value.is-critical {
  color: var(--danger);
}
.tag.sev-critical {
  background: rgba(180, 35, 24, 0.1);
  color: var(--danger);
}
.tag.sev-error {
  background: rgba(194, 65, 12, 0.12);
  color: var(--error);
}
.tag.sev-warning {
  background: rgba(154, 103, 0, 0.12);
  color: var(--warn);
}
.tag.sev-info {
  background: rgba(90, 107, 90, 0.12);
  color: var(--muted);
}
.show-info {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  color: var(--muted);
  font-size: var(--ctrl-font);
  line-height: var(--ctrl-line);
  white-space: nowrap;
}
.show-info input {
  margin: 0;
}
.msg {
  max-width: 28rem;
  vertical-align: middle;
}
.table td.actions .btn-sm {
  min-width: 4.6em;
  justify-content: center;
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
.summary-panel {
  margin-top: 20px;
  margin-bottom: 0;
  padding: 12px 16px;
}
.summary-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 6px;
}
.summary-title {
  margin: 0;
  font-size: 0.85rem;
  font-weight: 600;
  color: var(--muted);
  line-height: var(--ctrl-line);
}
.summary-more {
  margin-top: 0;
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.12s ease;
}
.summary-panel:hover .summary-more,
.summary-panel:focus-within .summary-more {
  opacity: 1;
  pointer-events: auto;
}
.summary-preview-text {
  display: -webkit-box;
  -webkit-line-clamp: 5;
  line-clamp: 5;
  -webkit-box-orient: vertical;
  overflow: hidden;
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 0.85rem;
  line-height: 1.45;
  color: var(--text);
}
.summary-preview-text.is-clickable {
  cursor: pointer;
}
.summary-preview-text.is-clickable:hover,
.summary-preview-text.is-clickable:focus-visible {
  color: var(--accent);
}
</style>
