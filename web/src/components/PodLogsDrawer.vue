<script setup>
import { ref, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { readApi } from '../api'
import { createSeq, errMessage } from '../utils/safe'
import { copyText } from '../utils/clipboard'

const props = defineProps({
  clusterId: { type: String, default: '' },
  ns: { type: String, default: '' }
})

const LOG_TAIL = 200
const LOG_INTERVAL_MS = 5000
const LOG_W_MIN = 420
const LOG_W_KEY = 'k8man.logDrawerPx'

const logsSeq = createSeq()
const open = ref(false)
const podName = ref('')
const activeNs = ref('')
const container = ref('')
const previous = ref(false)
const content = ref('')
const auto = ref(true)
const loading = ref(false)
const error = ref('')
const truncated = ref(false)
const updatedAt = ref('')
const logWidth = ref(720)
const logStickBottom = ref(true)
const logViewRef = ref(null)
const copied = ref(false)
let copiedTimer = 0
let logTimer = 0
let logAbort = null

function logMinWidth() {
  return Math.min(LOG_W_MIN, window.innerWidth)
}

function clampLogWidth(n) {
  return Math.min(window.innerWidth, Math.max(logMinWidth(), n))
}

function initLogWidth() {
  try {
    const n = Number(localStorage.getItem(LOG_W_KEY))
    if (Number.isFinite(n) && n >= LOG_W_MIN) {
      logWidth.value = clampLogWidth(n)
      return
    }
  } catch {
    /* ignore */
  }
  logWidth.value = clampLogWidth(720)
}

function stopLogResize() {
  document.body.classList.remove('is-drawer-resizing')
}

function startResizeLogs(e) {
  const startX = e.clientX
  const startW = logWidth.value
  const handle = e.currentTarget
  handle.setPointerCapture?.(e.pointerId)
  const onMove = (ev) => {
    logWidth.value = clampLogWidth(startW + (startX - ev.clientX))
  }
  const onUp = () => {
    handle.releasePointerCapture?.(e.pointerId)
    handle.removeEventListener('pointermove', onMove)
    handle.removeEventListener('pointerup', onUp)
    handle.removeEventListener('pointercancel', onUp)
    stopLogResize()
    try {
      localStorage.setItem(LOG_W_KEY, String(Math.round(logWidth.value)))
    } catch {
      /* ignore */
    }
  }
  document.body.classList.add('is-drawer-resizing')
  handle.addEventListener('pointermove', onMove)
  handle.addEventListener('pointerup', onUp)
  handle.addEventListener('pointercancel', onUp)
}

function stopLogTimer() {
  if (logTimer) {
    window.clearTimeout(logTimer)
    logTimer = 0
  }
}

function close() {
  stopLogTimer()
  logsSeq.bump()
  if (logAbort) {
    logAbort.abort()
    logAbort = null
  }
  open.value = false
  podName.value = ''
  activeNs.value = ''
  container.value = ''
  previous.value = false
  content.value = ''
  auto.value = true
  loading.value = false
  error.value = ''
  truncated.value = false
  updatedAt.value = ''
}

function scheduleLogTick() {
  stopLogTimer()
  if (!open.value || !auto.value || document.hidden) return
  logTimer = window.setTimeout(async () => {
    logTimer = 0
    if (!open.value || !auto.value || document.hidden) return
    await pullLogs()
    scheduleLogTick()
  }, LOG_INTERVAL_MS)
}

async function pullLogs() {
  const ns = activeNs.value || props.ns
  if (!open.value || !props.clusterId || !ns || !podName.value) return
  if (loading.value) return
  const seq = logsSeq.next()
  loading.value = true
  error.value = ''
  const ac = new AbortController()
  logAbort = ac
  try {
    const r = await readApi.podLogs(props.clusterId, ns, podName.value, {
      tail: LOG_TAIL,
      signal: ac.signal,
      container: container.value || undefined,
      previous: previous.value
    })
    if (!logsSeq.is(seq)) return
    content.value = r?.logs || '(empty)'
    truncated.value = !!r?.truncated
    updatedAt.value = new Date().toLocaleTimeString()
    await nextTick()
    if (logStickBottom.value && logViewRef.value) {
      logViewRef.value.scrollTop = logViewRef.value.scrollHeight
    }
  } catch (e) {
    if (!logsSeq.is(seq)) return
    if (e?.code === 'canceled') return
    error.value = errMessage(e)
  } finally {
    if (logsSeq.is(seq)) loading.value = false
    if (logAbort === ac) logAbort = null
  }
}

async function openLogs(name, resourceNs, opts = {}) {
  stopLogTimer()
  logsSeq.bump()
  if (logAbort) {
    logAbort.abort()
    logAbort = null
  }
  logStickBottom.value = true
  open.value = true
  podName.value = name
  activeNs.value = resourceNs || props.ns
  container.value = opts.container || ''
  previous.value = !!opts.previous
  content.value = ''
  auto.value = true
  loading.value = false
  error.value = ''
  truncated.value = false
  updatedAt.value = ''
  await pullLogs()
  scheduleLogTick()
}

function toggleLogAuto() {
  auto.value = !auto.value
  if (auto.value) {
    pullLogs().then(() => scheduleLogTick())
  } else {
    stopLogTimer()
  }
}

function onLogScroll(e) {
  const el = e.target
  logStickBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 48
}

function onLogVisibility() {
  if (document.hidden) {
    stopLogTimer()
    return
  }
  if (open.value && auto.value) {
    pullLogs().then(() => scheduleLogTick())
  }
}

async function onCopy() {
  try {
    await copyText(content.value)
    copied.value = true
    window.clearTimeout(copiedTimer)
    copiedTimer = window.setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    alert('复制失败')
  }
}

watch(
  () => [props.clusterId, props.ns],
  () => {
    if (open.value) close()
  }
)

onMounted(() => {
  initLogWidth()
  document.addEventListener('visibilitychange', onLogVisibility)
})

onUnmounted(() => {
  document.removeEventListener('visibilitychange', onLogVisibility)
  stopLogResize()
  close()
  window.clearTimeout(copiedTimer)
})

defineExpose({ open: openLogs, close })
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click="close"></div>
  <div
    v-if="open"
    class="drawer drawer-logs"
    :style="{ width: logWidth + 'px' }"
  >
    <div
      class="drawer-resize"
      title="拖拽调整宽度"
      @pointerdown.prevent="startResizeLogs"
    ></div>
    <div class="toolbar">
      <strong>
        日志 · {{ podName }}
        <template v-if="container"> · {{ container }}</template>
        <template v-if="previous"> · previous</template>
      </strong>
      <div class="actions">
        <button class="btn btn-sm" type="button" @click="toggleLogAuto">
          {{ auto ? '暂停' : '继续' }}
        </button>
        <button class="btn btn-sm" type="button" :disabled="loading" @click="pullLogs">刷新</button>
        <button class="btn btn-sm" type="button" @click="close">关闭</button>
      </div>
    </div>
    <p class="muted log-hint">
      尾部 {{ LOG_TAIL }} 行 · 每 {{ LOG_INTERVAL_MS / 1000 }}s 刷新
      <template v-if="updatedAt"> · {{ updatedAt }}</template>
      <template v-if="loading"> · 加载中…</template>
      <template v-if="truncated"> · 已截断（≤256KB）</template>
    </p>
    <p v-if="error" class="error">{{ error }}</p>
    <div class="field-copy-wrap log-copy-wrap">
      <textarea
        ref="logViewRef"
        class="log-view"
        readonly
        spellcheck="false"
        :value="content"
        @scroll="onLogScroll"
      ></textarea>
      <button
        type="button"
        class="field-copy"
        :class="{ 'is-copied': copied }"
        @click="onCopy"
      >{{ copied ? '已复制' : '复制' }}</button>
    </div>
  </div>
</template>
