<script setup>
import { ref, computed, onUnmounted } from 'vue'
import { asArray } from '../utils/safe'
import { severityLabel, issueCategoryLabel } from '../utils/labels'
import { fmtTime } from '../utils/quantity'

const open = ref(false)
const detail = ref(null)
/** cache | live | none */
const source = ref('none')
const running = ref(false)
const runError = ref('')

const issues = computed(() => asArray(detail.value?.issues))
const scanErrors = computed(() => {
  const err = detail.value?.diagnose?.errors
  if (!err || typeof err !== 'object') return []
  return Object.keys(err).map((k) => ({ key: k, message: err[k] }))
})

const statusTags = computed(() => {
  const tags = []
  if (running.value) tags.push({ cls: 'inspect-running', text: '实时巡检中…' })
  else if (source.value === 'live') tags.push({ cls: 'inspect-live', text: '刚刚巡检' })
  else if (source.value === 'cache') tags.push({ cls: 'inspect-cache', text: '缓存快照' })
  return tags
})

const statusHint = computed(() => {
  if (runError.value) return runError.value
  if (running.value) {
    return source.value === 'cache'
      ? '下方为上次巡检结果；实时巡检完成后自动更新。'
      : '正在连接集群并扫描，请稍候…'
  }
  if (source.value === 'live' && detail.value?.diagnose?.scan?.fromCache) {
    return '列表数据来自 apiserver watch cache，与 etcd 可能有数秒延迟。'
  }
  return ''
})

function typeLabel(row) {
  const cat = issueCategoryLabel(row?.category)
  const kind = String(row?.kind || '').trim()
  if (!kind || kind.toLowerCase() === cat.toLowerCase()) return cat
  if (row?.category === 'pod' && kind.toLowerCase() === 'pod') return 'Pod'
  if (row?.category === 'node' && kind.toLowerCase() === 'node') return '节点'
  if (row?.category === 'kubeconfig') return '配置'
  return `${cat} · ${kind}`
}

function close() {
  open.value = false
  detail.value = null
  source.value = 'none'
  running.value = false
  runError.value = ''
}

function show(payload, opts = {}) {
  detail.value = payload && typeof payload === 'object' ? payload : null
  source.value = opts.source || (detail.value?.checkedAt ? 'cache' : 'none')
  running.value = !!opts.running
  runError.value = opts.runError || ''
  open.value = true
}

function setRunning(isRunning, err = '') {
  running.value = isRunning
  runError.value = err || ''
}

function updateLive(payload) {
  detail.value = payload && typeof payload === 'object' ? payload : null
  source.value = 'live'
  running.value = false
  runError.value = ''
}

function probeStatusOf(d) {
  return d?.probeStatus || d?.probe?.status || ''
}

function probeLine(d) {
  if (!d) return ''
  const st = probeStatusOf(d)
  if (st === 'ok') {
    const elapsed = d.elapsedMs ? `，耗时 ${d.elapsedMs}ms` : ''
    return `kubeconfig 有效${elapsed}`
  }
  const err = d.probeError || d?.probe?.lastError || ''
  if (st === 'error' || err) {
    return err ? `kubeconfig 无效：${err}` : 'kubeconfig 无效'
  }
  return '尚未巡检'
}

function emptyHint(d) {
  if (running.value) return '实时巡检进行中…'
  const st = probeStatusOf(d)
  if (st === 'ok') return '本轮巡检未发现问题。'
  if (st === 'error') return 'kubeconfig 无效，未做诊断扫描。'
  return d?.summary || '尚无巡检记录；打开后将自动触发实时巡检。'
}

onUnmounted(close)

defineExpose({ open: show, close, update: show, updateLive, setRunning })
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click="close"></div>
  <div v-if="open" class="drawer drawer-wide inspect-drawer">
    <div class="toolbar">
      <strong>{{ detail?.clusterName || '巡检' }}<template v-if="detail?.checkedAt"> · {{ fmtTime(detail.checkedAt) }}</template></strong>
      <div class="actions">
        <button class="btn btn-sm" type="button" @click="close">关闭</button>
      </div>
    </div>
    <div v-if="statusTags.length || statusHint" class="inspect-status-bar">
      <span v-for="t in statusTags" :key="t.cls" class="tag" :class="t.cls">{{ t.text }}</span>
      <span v-if="statusHint" class="inspect-status-hint" :class="{ error: runError }">{{ statusHint }}</span>
    </div>
    <p class="muted inspect-drawer-probe">{{ probeLine(detail) }}</p>
    <p v-if="detail?.summary && probeStatusOf(detail)" class="muted">{{ detail.summary }}</p>
    <p v-for="e in scanErrors" :key="e.key" class="muted">{{ issueCategoryLabel(e.key) }}扫描失败：{{ e.message }}</p>
    <div v-if="!issues.length" class="empty">{{ emptyHint(detail) }}</div>
    <div v-else class="inspect-issue-list" :class="{ 'inspect-issue-list-dim': running && source === 'cache' }">
      <article v-for="(row, idx) in issues" :key="idx" class="inspect-issue">
        <div class="inspect-issue-head">
          <span class="tag" :class="row.severity === 'critical' ? 'sev-critical' : 'sev-warning'">
            {{ severityLabel(row.severity) }}
          </span>
          <strong>{{ typeLabel(row) }}</strong>
          <span class="muted">{{ row.namespace ? `${row.namespace} / ${row.name}` : row.name }}</span>
        </div>
        <p v-if="row.reason || row.message" class="inspect-issue-msg">
          <template v-if="row.reason">{{ row.reason }}</template>
          <template v-if="row.reason && row.message">：</template>
          {{ row.message }}
        </p>
        <ul v-if="asArray(row.evidence).length" class="inspect-evidence">
          <li v-for="(ev, i) in asArray(row.evidence)" :key="i">{{ ev }}</li>
        </ul>
        <pre v-if="asArray(row.logExcerpt).length" class="inspect-log">{{ asArray(row.logExcerpt).join('\n') }}</pre>
      </article>
    </div>
  </div>
</template>
