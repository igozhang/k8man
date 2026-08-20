<script setup>
import { ref, computed, onUnmounted } from 'vue'
import { asArray } from '../utils/safe'
import { severityLabel, issueCategoryLabel } from '../utils/labels'
import { fmtTime } from '../utils/quantity'

const open = ref(false)
const detail = ref(null)

const issues = computed(() => asArray(detail.value?.issues))
const scanErrors = computed(() => {
  const err = detail.value?.diagnose?.errors
  if (!err || typeof err !== 'object') return []
  return Object.keys(err).map((k) => ({ key: k, message: err[k] }))
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
}

function show(payload) {
  detail.value = payload && typeof payload === 'object' ? payload : null
  open.value = true
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
  const err = d.probeError || d.probe?.lastError || ''
  if (st === 'error' || err) {
    return err ? `kubeconfig 无效：${err}` : 'kubeconfig 无效'
  }
  return '尚未巡检'
}

function emptyHint(d) {
  const st = probeStatusOf(d)
  if (st === 'ok') return '本轮巡检未发现问题。'
  if (st === 'error') return 'kubeconfig 无效，未做诊断扫描。'
  return d?.summary || '尚未巡检，可对当前集群点「一键巡检」。'
}

onUnmounted(close)

defineExpose({ open: show, close, update: show })
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
    <p class="muted inspect-drawer-probe">{{ probeLine(detail) }}</p>
    <p v-if="detail?.summary && probeStatusOf(detail)" class="muted">{{ detail.summary }}</p>
    <p v-for="e in scanErrors" :key="e.key" class="muted">{{ issueCategoryLabel(e.key) }}扫描失败：{{ e.message }}</p>
    <div v-if="!issues.length" class="empty">{{ emptyHint(detail) }}</div>
    <div v-else class="inspect-issue-list">
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
