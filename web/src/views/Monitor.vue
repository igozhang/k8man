<script setup>
import { ref, watch, onMounted, computed } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { severityLabel, severityClass } from '../utils/labels'
import { issueInspectBrief } from '../utils/inspect'
import {
  TTL,
  cacheFresh,
  cacheGet,
  cacheKey,
  cacheLoad,
  cacheInvalidate,
  notifyInspectionsChanged,
  invalidateDocs
} from '../utils/resourceCache'
import InspectionDrawer from '../components/InspectionDrawer.vue'
import InspectReportDrawer from '../components/InspectReportDrawer.vue'
import InspectRules from '../components/InspectRules.vue'
import { canWrite } from '../stores/opsAuth'

const INSPECT_KEY = cacheKey('inspections', 'local')
const BLOCK_LINES = 3

const { currentCluster, refreshClusters } = useClusterStore()

const inspectItems = ref(asArray(cacheGet(INSPECT_KEY)?.data?.items))
const inspectListFetchedAt = ref(cacheGet(INSPECT_KEY)?.at || null)
const inspectLoading = ref(false)
const inspectError = ref('')
const inspectingId = ref('')
const inspectTargetId = ref('')
const inspectDrawer = ref(null)
const inspectReportDrawer = ref(null)
const inspectExportPeriod = ref('week')
const inspectExportScope = ref('all')
const inspectExportBusy = ref(false)
const inspectPreviewBusy = ref(false)

const inspecting = computed(() => !!inspectingId.value)

const inspectSeq = createSeq()
const inspectRunSeq = createSeq()
const inspectReportSeq = createSeq()

function issueRank(severity) {
  if (severity === 'critical') return 0
  if (severity === 'error') return 1
  if (severity === 'warning') return 2
  if (severity === 'info') return 3
  return 2
}

function sortedPreviewIssues(preview) {
  const rows = asArray(preview)
  return [...rows].sort((a, b) => {
    const d = issueRank(a.severity) - issueRank(b.severity)
    if (d) return d
    return String(a.name || '').localeCompare(String(b.name || ''), 'zh')
  })
}

const inspectIssueBlocks = computed(() => {
  const blocks = []
  for (const it of inspectItems.value) {
    const critical = Number(it.critical) || 0
    const errorN = Number(it.error) || 0
    const warning = Number(it.warning) || 0
    if (!critical && !errorN && !warning) continue
    // 提示不当故障刷：卡片与预览只看严重/错误/警告（服务端 preview 也会跳过 info）
    const sorted = sortedPreviewIssues(it.preview).filter((iss) => iss?.severity !== 'info')
    const lines = sorted.slice(0, BLOCK_LINES).map((iss) => ({
      ...iss,
      clusterId: it.clusterId,
      clusterName: it.clusterName
    }))
    const total = critical + errorN + warning
    blocks.push({
      clusterId: it.clusterId,
      clusterName: it.clusterName || it.clusterId,
      checkedAt: it.checkedAt,
      critical,
      error: errorN,
      warning,
      summary: it.summary,
      largePods: it.largePodSkipped ? null : Number(it.largePods) || 0,
      nfsUsagePercent: (() => {
        if (it.nfsSkipped || it.nfsUsagePercent == null || it.nfsUsagePercent === '') return null
        const n = Number(it.nfsUsagePercent)
        return Number.isNaN(n) ? null : n
      })(),
      nfsStorageClass: it.nfsStorageClass || '',
      nfsServer: it.nfsServer || '',
      nfsPath: it.nfsPath || '',
      lines,
      more: Math.max(0, total - lines.length),
      inspecting: inspectingId.value === it.clusterId
    })
  }
  blocks.sort((a, b) => {
    const rank = (x) => (x.critical > 0 ? 0 : x.error > 0 ? 1 : x.warning > 0 ? 2 : 3)
    const ra = rank(a)
    const rb = rank(b)
    if (ra !== rb) return ra - rb
    return String(a.clusterName).localeCompare(String(b.clusterName), 'zh')
  })
  return blocks
})

const inspectListRefreshing = computed(
  () => inspectLoading.value && inspectItems.value.length > 0
)
const inspectListCacheTag = computed(() => {
  if (!inspectItems.value.length) return ''
  if (inspectListRefreshing.value) return '摘要刷新中…'
  const at = inspectListFetchedAt.value
  if (!at) return ''
  return `摘要缓存 · ${fmtInspectTime(new Date(at))}`
})
const inspectCounts = computed(() => {
  let critical = 0
  let errorN = 0
  let warning = 0
  let info = 0
  for (const it of inspectItems.value) {
    critical += Number(it.critical) || 0
    errorN += Number(it.error) || 0
    warning += Number(it.warning) || 0
    info += Number(it.info) || 0
  }
  return { critical, error: errorN, warning, info }
})
const inspectHasRun = computed(() => inspectItems.value.some((it) => !!it.checkedAt))
const inspectMeta = computed(() => {
  const items = inspectItems.value
  const n = items.length
  if (!n) return '暂无 kubeconfig'
  let last = null
  let next = null
  for (const it of items) {
    if (it.checkedAt) {
      const t = new Date(it.checkedAt)
      if (!Number.isNaN(t.getTime()) && (!last || t > last)) last = t
    }
    if (it.nextDue) {
      const t = new Date(it.nextDue)
      if (!Number.isNaN(t.getTime()) && (!next || t < next)) next = t
    }
  }
  const parts = [`${n} 个配置`]
  if (last) parts.push(`上次 ${fmtInspectTime(last)}`)
  if (next) parts.push(`下次 ${fmtInspectTime(next)}`)
  const { critical, error: errorN, warning, info } = inspectCounts.value
  if (critical || errorN || warning) {
    const bits = []
    if (critical) bits.push(`${critical} 严重`)
    if (errorN) bits.push(`${errorN} 错误`)
    if (warning) bits.push(`${warning} 警告`)
    parts.push(bits.join(' / '))
  } else if (info) {
    parts.push(`${info} 提示`)
  } else if (last) {
    parts.push('全部正常')
  } else {
    parts.push('尚未巡检')
  }
  if (inspectListRefreshing.value) parts.push('刷新中')
  return parts.join(' · ')
})

function fmtInspectTime(t) {
  const d = t instanceof Date ? t : new Date(t)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function issueLineBrief(row) {
  return issueInspectBrief(row)
}

function nfsTagTitle(block) {
  const parts = []
  const server = String(block?.nfsServer || '').trim()
  const path = String(block?.nfsPath || '').trim()
  if (server) {
    parts.push(path ? (path.startsWith('/') ? `${server}:${path}` : `${server}:/${path}`) : server)
  }
  if (block?.nfsStorageClass) parts.push(`SC=${block.nfsStorageClass}`)
  return parts.join(' · ')
}

function blockCacheTag(block) {
  if (block.inspecting) return '实时巡检中…'
  if (block.checkedAt) return `缓存 · ${fmtInspectTime(block.checkedAt)}`
  return '尚未巡检'
}

function applyInspectList(data, fetchedAt) {
  inspectItems.value = asArray(data?.items)
  if (fetchedAt) inspectListFetchedAt.value = fetchedAt
}

async function loadInspections({ force = false } = {}) {
  const seq = inspectSeq.next()
  inspectError.value = ''
  const hit = cacheGet(INSPECT_KEY)
  if (hit?.data) applyInspectList(hit.data, hit.at)
  if (!force && cacheFresh(INSPECT_KEY, TTL.inspections)) {
    inspectLoading.value = false
    return
  }
  inspectLoading.value = !inspectItems.value.length
  try {
    const data = await cacheLoad(INSPECT_KEY, TTL.inspections, () => readApi.listInspections(), { force })
    if (!inspectSeq.is(seq)) return
    const fresh = cacheGet(INSPECT_KEY)
    applyInspectList(data, fresh?.at || Date.now())
    notifyInspectionsChanged()
  } catch (e) {
    if (!inspectSeq.is(seq)) return
    inspectError.value = errMessage(e)
    if (!hit) inspectItems.value = []
  } finally {
    if (inspectSeq.is(seq)) inspectLoading.value = false
  }
}

function detailFromListItem(item, clusterId, clusterName) {
  return {
    clusterId,
    clusterName: item?.clusterName || clusterName || '',
    checkedAt: item?.checkedAt,
    summary: item?.summary || '',
    probeStatus: item?.probeStatus,
    probeError: item?.probeError,
    issues: asArray(item?.preview),
    elapsedMs: item?.elapsedMs
  }
}

function clusterLabel(clusterId) {
  const item = inspectItems.value.find((it) => it.clusterId === clusterId)
  return item?.clusterName || currentCluster.value?.name || ''
}

async function runLiveInspect(clusterId, { openDrawer = true } = {}) {
  if (!canWrite.value || !clusterId) return
  if (inspectingId.value) {
    inspectError.value = '已有巡检进行中，请稍候'
    return
  }
  const seq = inspectRunSeq.next()
  inspectingId.value = clusterId
  inspectError.value = ''

  const item = inspectItems.value.find((it) => it.clusterId === clusterId)
  const hasCache = !!item?.checkedAt
  let liveDone = false

  if (openDrawer) {
    inspectDrawer.value?.open(detailFromListItem(item, clusterId, clusterLabel(clusterId)), {
      source: hasCache ? 'cache' : 'none',
      running: true
    })
  }

  readApi.getInspection(clusterId).then((data) => {
    if (!inspectRunSeq.is(seq) || liveDone) return
    if (openDrawer) {
      inspectDrawer.value?.open(
        {
          ...data,
          issues: asArray(data?.issues).length ? data.issues : asArray(data?.preview)
        },
        { source: 'cache', running: true }
      )
    }
  }).catch(() => {})

  try {
    const data = await writeApi.inspectCluster(clusterId)
    if (!inspectRunSeq.is(seq)) return
    liveDone = true
    cacheInvalidate(INSPECT_KEY)
    if (openDrawer) {
      inspectDrawer.value?.updateLive({
        ...data,
        issues: asArray(data?.issues).length ? data.issues : asArray(data?.preview)
      })
    }
    await Promise.all([
      loadInspections({ force: true }),
      refreshClusters(() => readApi.listClusters(), { force: true }).catch(() => {})
    ])
  } catch (e) {
    if (!inspectRunSeq.is(seq)) return
    liveDone = true
    const msg = errMessage(e)
    inspectError.value = msg
    if (openDrawer) {
      inspectDrawer.value?.setRunning(false, msg)
    }
  } finally {
    if (inspectRunSeq.is(seq)) inspectingId.value = ''
  }
}

async function openInspectDetail(clusterId) {
  if (!clusterId) return
  if (canWrite.value) {
    await runLiveInspect(clusterId, { openDrawer: true })
    return
  }
  const item = inspectItems.value.find((it) => it.clusterId === clusterId)
  inspectDrawer.value?.open(detailFromListItem(item, clusterId, clusterLabel(clusterId)), {
    source: item?.checkedAt ? 'cache' : 'none',
    running: false
  })
  try {
    const data = await readApi.getInspection(clusterId)
    inspectDrawer.value?.open(
      {
        ...data,
        issues: asArray(data?.issues).length ? data.issues : asArray(data?.preview)
      },
      { source: 'cache', running: false }
    )
  } catch {
    /* 无缓存时抽屉已展示空快照 */
  }
}

async function runInspect() {
  const targetId = inspectTargetId.value
  if (!targetId) return
  await runLiveInspect(targetId, { openDrawer: true })
}

function inspectExportParams() {
  const period = inspectExportPeriod.value || 'week'
  const scope = inspectExportScope.value || 'all'
  const clusterId = scope === 'current' ? inspectTargetId.value : ''
  return { period, scope, clusterId }
}

function downloadMarkdownFile(filename, text) {
  const raw = String(text || '')
  if (!raw.trim()) {
    throw new Error('报告内容为空，未下载')
  }
  let name = String(filename || 'k8man-inspect.md').replace(/[<>:"/\\|?*\x00-\x1f]/g, '_')
  if (!name.toLowerCase().endsWith('.md')) name += '.md'
  const blob = new Blob([raw], { type: 'text/markdown;charset=utf-8' })
  const obj = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = obj
    a.download = name
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(obj)
  }
}

async function previewInspectReport() {
  if (inspectPreviewBusy.value || inspectExportBusy.value) return
  const { period, scope, clusterId } = inspectExportParams()
  if (scope === 'current' && !clusterId) {
    inspectError.value = '请先选择 kubeconfig（当前基地）'
    return
  }
  inspectError.value = ''
  const seq = inspectReportSeq.next()
  inspectPreviewBusy.value = true
  inspectReportDrawer.value?.open({
    title: '巡检报告预览',
    loading: true,
    markdown: '',
    summary: '',
    docId: '',
    error: ''
  })
  try {
    const data = await readApi.previewInspectionReport({ period, scope, clusterId })
    if (!inspectReportSeq.is(seq)) return
    inspectReportDrawer.value?.update({
      title: data?.title || '巡检报告预览',
      markdown: data?.markdown || '',
      summary: data?.summary || '',
      loading: false,
      error: ''
    })
  } catch (e) {
    if (!inspectReportSeq.is(seq)) return
    const msg = errMessage(e)
    inspectError.value = msg
    inspectReportDrawer.value?.update({ loading: false, error: msg })
  } finally {
    if (inspectReportSeq.is(seq)) inspectPreviewBusy.value = false
  }
}

async function exportInspectReport() {
  if (!canWrite.value) {
    inspectError.value = '导出需运维写权限（将同步到运维文档「巡检报告」）'
    return
  }
  if (inspectExportBusy.value || inspectPreviewBusy.value) return
  const { period, scope, clusterId } = inspectExportParams()
  if (scope === 'current' && !clusterId) {
    inspectError.value = '请先选择 kubeconfig（当前基地）'
    return
  }
  inspectError.value = ''
  const seq = inspectReportSeq.next()
  inspectExportBusy.value = true
  inspectReportDrawer.value?.open({
    title: '正在导出巡检报告…',
    loading: true,
    exporting: true,
    summary: '生成 Markdown 并同步到运维文档…',
    error: '',
    docId: ''
  })
  try {
    const data = await writeApi.exportInspectionReport({ period, scope, clusterId })
    if (!inspectReportSeq.is(seq)) return
    try {
      downloadMarkdownFile(data?.filename, data?.markdown)
    } catch (dlErr) {
      // 文档已写入，下载失败仍展示结果
      inspectError.value = errMessage(dlErr)
    }
    if (data?.doc?.id) invalidateDocs(data.doc.id)
    inspectReportDrawer.value?.open({
      title: data?.title || '巡检报告',
      markdown: data?.markdown || '',
      summary: (data?.summary || '') + (data?.doc?.id ? ' · 已同步到运维文档' : ''),
      docId: data?.doc?.id || '',
      loading: false,
      exporting: false,
      error: inspectError.value || ''
    })
  } catch (e) {
    if (!inspectReportSeq.is(seq)) return
    const msg = errMessage(e)
    inspectError.value = msg
    inspectReportDrawer.value?.update({ loading: false, exporting: false, error: msg })
  } finally {
    if (inspectReportSeq.is(seq)) {
      inspectExportBusy.value = false
      inspectReportDrawer.value?.update({ exporting: false, loading: false })
    }
  }
}

watch(inspectItems, (items) => {
  if (inspectTargetId.value && !items.some((it) => it.clusterId === inspectTargetId.value)) {
    inspectTargetId.value = ''
  }
})
onMounted(() => {
  loadInspections()
})
</script>

<template>
  <div>
    <h1>巡检</h1>
    <p class="page-desc">
      对每个 kubeconfig 约 2 小时一轮。后台<strong>有界并发</strong>（默认同时 4 路，积压可扩至 8）；两次成功开始至少间隔约 2 秒；同一集群同时只跑一轮。每轮先 Probe（同 KubeConfig「测试」）并回写状态，通过后再扫描节点、负载、Pod 与事件（Project 凭证跳过节点）。节点与 Pod 用量请到「工作负载」查看。定级见下方「基线与降级机制」（含 Restarts）。下方按集群分块展示摘要（每块最多 3 条，只含严重/错误/警告；仅有提示时不单独成块）；点击块在右侧抽屉查看该配置详情<template v-if="canWrite">并触发实时巡检</template>。「预览 / 导出巡检报告」基于各基地<strong>最新快照</strong>生成 Markdown（按日/周/月窗口筛 `sinceAt`）；导出需写权限，并同步一份到运维文档「巡检报告」。
    </p>

    <section class="panel inspect-box">
      <div class="toolbar">
        <div class="inspect-head">
          <strong>巡检摘要</strong>
          <span v-if="inspectListCacheTag" class="tag inspect-cache inspect-list-cache-tag">{{ inspectListCacheTag }}</span>
          <span class="muted inspect-meta">{{ inspectMeta }}</span>
        </div>
      </div>
      <div class="toolbar toolbar-start inspect-actions">
        <select v-model="inspectTargetId" aria-label="巡检目标 kubeconfig">
          <option value="">选择 kubeconfig</option>
          <option v-for="it in inspectItems" :key="it.clusterId" :value="it.clusterId">
            {{ it.clusterName }}
          </option>
        </select>
        <div class="actions">
          <button
            v-if="canWrite"
            class="btn btn-sm"
            type="button"
            :disabled="!inspectTargetId || inspecting"
            :title="inspectTargetId ? '先展示缓存并立即实时巡检' : '请先选择 kubeconfig'"
            @click="runInspect"
          >{{ inspecting ? '巡检中…' : '一键巡检' }}</button>
          <button
            class="btn btn-sm"
            type="button"
            :disabled="(!inspectTargetId && !inspectIssueBlocks.length) || inspecting"
            @click="openInspectDetail(inspectTargetId || inspectIssueBlocks[0]?.clusterId)"
          >详情</button>
          <select
            v-model="inspectExportPeriod"
            class="inspect-export-select"
            aria-label="报告周期"
            title="报告周期（基于最新快照，按问题 sinceAt 筛入窗口）"
          >
            <option value="day">日</option>
            <option value="week">周</option>
            <option value="month">月</option>
          </select>
          <select
            v-model="inspectExportScope"
            class="inspect-export-select"
            aria-label="报告范围"
            title="当前基地 = 左侧所选 kubeconfig"
          >
            <option value="all">所有基地</option>
            <option value="current">当前基地</option>
          </select>
          <button
            class="btn btn-sm"
            type="button"
            :disabled="inspectPreviewBusy || inspectExportBusy || (inspectExportScope === 'current' && !inspectTargetId)"
            :title="inspectExportScope === 'current' && !inspectTargetId ? '请先选择 kubeconfig' : '预览 Markdown 报告'"
            @click="previewInspectReport"
          >{{ inspectPreviewBusy ? '生成中…' : '预览' }}</button>
          <button
            class="btn btn-sm"
            type="button"
            :disabled="!canWrite || inspectExportBusy || inspectPreviewBusy || (inspectExportScope === 'current' && !inspectTargetId)"
            :title="!canWrite
              ? '导出需运维写权限（将同步到运维文档）'
              : (inspectExportScope === 'current' && !inspectTargetId ? '请先选择 kubeconfig' : '下载 MD 并同步到运维文档「巡检报告」')"
            @click="exportInspectReport"
          >{{ inspectExportBusy ? '导出中…' : '导出巡检报告' }}</button>
        </div>
      </div>
      <p v-if="inspectError" class="error">{{ inspectError }}</p>
      <p v-if="inspectLoading && !inspectItems.length" class="muted">加载巡检结果…</p>
      <div v-else-if="!inspectItems.length && !inspectError" class="muted">暂无已导入的 kubeconfig。</div>
      <div v-else-if="!inspectIssueBlocks.length && inspectItems.length" class="muted">
        <template v-if="!inspectHasRun">{{ canWrite ? '尚未巡检。后台约两小时一轮，也可选择配置后一键巡检。' : '尚未巡检。后台约两小时一轮。' }}</template>
        <template v-else-if="inspectCounts.info">无严重 / 错误 / 警告；仅有 {{ inspectCounts.info }} 提示（不当故障刷，点「详情」可看）。</template>
        <template v-else>全部配置巡检正常。</template>
      </div>
      <div v-else-if="inspectIssueBlocks.length" class="inspect-cluster-blocks">
        <article
          v-for="block in inspectIssueBlocks"
          :key="block.clusterId"
          class="inspect-cluster-block"
          :class="{ 'inspect-cluster-block-active': block.inspecting }"
        >
          <button
            type="button"
            class="inspect-cluster-block-hit"
            @click="openInspectDetail(block.clusterId)"
          >
            <div class="inspect-cluster-block-head">
              <strong>{{ block.clusterName }}</strong>
              <span class="tag" :class="block.inspecting ? 'inspect-running' : 'inspect-cache'">{{ blockCacheTag(block) }}</span>
              <span v-if="block.critical" class="tag sev-critical">{{ block.critical }} 严重</span>
              <span v-if="block.error" class="tag sev-error">{{ block.error }} 错误</span>
              <span v-if="block.warning" class="tag sev-warning">{{ block.warning }} 警告</span>
              <span
                v-if="block.largePods != null && block.largePods > 0"
                class="tag sev-warning"
              >大 POD {{ block.largePods }}</span>
              <span
                v-if="block.nfsUsagePercent != null"
                class="tag"
                :class="block.nfsUsagePercent >= 80 ? 'sev-critical' : 'sev-info'"
                :title="nfsTagTitle(block)"
              >NFS {{ Math.round(block.nfsUsagePercent) }}%</span>
            </div>
            <ul class="inspect-lines">
              <li v-for="(row, idx) in block.lines" :key="idx">
                <div class="inspect-line inspect-line-static">
                  <span class="tag" :class="severityClass(row.severity)">
                    {{ severityLabel(row.severity) }}
                  </span>
                  <span class="inspect-line-text">{{ issueLineBrief(row) }}</span>
                </div>
              </li>
            </ul>
            <span v-if="block.more > 0" class="inspect-more">还有 {{ block.more }} 项，点击查看详情</span>
          </button>
        </article>
      </div>

      <InspectRules class="is-split" title="基线与降级机制" />
    </section>
    <InspectionDrawer ref="inspectDrawer" />
    <InspectReportDrawer
      ref="inspectReportDrawer"
      :can-export="canWrite"
      @export="exportInspectReport"
    />
  </div>
</template>
