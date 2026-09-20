<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { marked } from 'marked'
import { RouterLink } from 'vue-router'
import { copyText } from '../utils/clipboard'

const W_MIN = 420
const W_KEY = 'k8man.inspectReportDrawerPx'

const open = ref(false)
const title = ref('')
const markdown = ref('')
const summary = ref('')
const docId = ref('')
const loading = ref(false)
const error = ref('')
const exporting = ref(false)
const mode = ref('preview') // preview | source
const width = ref(720)
const copied = ref(false)
let copiedTimer = 0

const emit = defineEmits(['export'])

defineProps({
  canExport: { type: Boolean, default: false }
})

const html = computed(() => {
  const md = String(markdown.value || '').trim()
  if (!md) return ''
  try {
    return marked.parse(md)
  } catch {
    return '<p class="error">Markdown 解析失败</p>'
  }
})

function minWidth() {
  return Math.min(W_MIN, window.innerWidth)
}

function clampWidth(n) {
  return Math.min(window.innerWidth, Math.max(minWidth(), n))
}

function initWidth() {
  try {
    const n = Number(localStorage.getItem(W_KEY))
    if (Number.isFinite(n) && n >= W_MIN) {
      width.value = clampWidth(n)
      return
    }
  } catch {
    /* ignore */
  }
  width.value = clampWidth(820)
}

function stopResize() {
  document.body.classList.remove('is-drawer-resizing')
}

function startResize(e) {
  const startX = e.clientX
  const startW = width.value
  const handle = e.currentTarget
  handle.setPointerCapture?.(e.pointerId)
  const onMove = (ev) => {
    width.value = clampWidth(startW + (startX - ev.clientX))
  }
  const onUp = () => {
    handle.releasePointerCapture?.(e.pointerId)
    handle.removeEventListener('pointermove', onMove)
    handle.removeEventListener('pointerup', onUp)
    handle.removeEventListener('pointercancel', onUp)
    stopResize()
    try {
      localStorage.setItem(W_KEY, String(Math.round(width.value)))
    } catch {
      /* ignore */
    }
  }
  document.body.classList.add('is-drawer-resizing')
  handle.addEventListener('pointermove', onMove)
  handle.addEventListener('pointerup', onUp)
  handle.addEventListener('pointercancel', onUp)
}

function close() {
  if (loading.value || exporting.value) return
  open.value = false
  title.value = ''
  markdown.value = ''
  summary.value = ''
  docId.value = ''
  loading.value = false
  error.value = ''
  exporting.value = false
  mode.value = 'preview'
}

function forceClose() {
  loading.value = false
  exporting.value = false
  open.value = false
  title.value = ''
  markdown.value = ''
  summary.value = ''
  docId.value = ''
  error.value = ''
  mode.value = 'preview'
}

function onBackdrop() {
  close()
}

function show(opts = {}) {
  title.value = opts.title || '巡检报告预览'
  markdown.value = opts.markdown || ''
  summary.value = opts.summary || ''
  docId.value = opts.docId || ''
  loading.value = !!opts.loading
  error.value = opts.error || ''
  exporting.value = !!opts.exporting
  mode.value = opts.mode || 'preview'
  open.value = true
}

function setLoading(v, err = '') {
  loading.value = !!v
  if (err != null) error.value = err
}

function update(opts = {}) {
  if (!open.value) return
  if (opts.title != null) title.value = opts.title
  if (opts.markdown != null) markdown.value = opts.markdown
  if (opts.summary != null) summary.value = opts.summary
  if (opts.docId != null) docId.value = opts.docId
  if (opts.loading != null) loading.value = !!opts.loading
  if (opts.error != null) error.value = opts.error
  if (opts.exporting != null) exporting.value = !!opts.exporting
}

async function onCopy() {
  try {
    await copyText(markdown.value || '')
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
  () => open.value,
  (v) => {
    if (!v) copied.value = false
  }
)

onMounted(initWidth)
onUnmounted(() => {
  stopResize()
  forceClose()
  window.clearTimeout(copiedTimer)
})

defineExpose({ open: show, close, forceClose, update, setLoading })
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click="onBackdrop"></div>
  <div
    v-if="open"
    class="drawer drawer-logs inspect-report-drawer"
    :style="{ width: width + 'px' }"
  >
    <div
      class="drawer-resize"
      title="拖拽调整宽度"
      @pointerdown.prevent="startResize"
    ></div>
    <div class="toolbar">
      <strong>{{ title }}</strong>
      <div class="actions">
        <button
          class="btn btn-sm"
          type="button"
          :class="{ 'btn-primary': mode === 'preview' }"
          :disabled="loading"
          @click="mode = 'preview'"
        >预览</button>
        <button
          class="btn btn-sm"
          type="button"
          :class="{ 'btn-primary': mode === 'source' }"
          :disabled="loading"
          @click="mode = 'source'"
        >源码</button>
        <button
          v-if="canExport"
          class="btn btn-sm"
          type="button"
          :disabled="!markdown || exporting || loading"
          @click="emit('export')"
        >{{ exporting ? '导出中…' : '导出并同步' }}</button>
        <RouterLink
          v-if="docId"
          class="btn btn-sm"
          :to="`/docs/${docId}`"
        >打开文档</RouterLink>
        <button
          class="btn btn-sm"
          type="button"
          :disabled="loading || exporting"
          :title="loading || exporting ? '生成/导出完成前请稍候' : '关闭'"
          @click="close"
        >关闭</button>
      </div>
    </div>
    <p v-if="summary" class="muted log-hint">{{ summary }}</p>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="loading && !markdown" class="muted log-hint">正在生成报告…</p>
    <div v-else-if="mode === 'source'" class="field-copy-wrap log-copy-wrap">
      <textarea
        class="log-view is-wrap"
        readonly
        spellcheck="false"
        :value="markdown"
      ></textarea>
      <button
        type="button"
        class="field-copy"
        :class="{ 'is-copied': copied }"
        @click="onCopy"
      >{{ copied ? '已复制' : '复制' }}</button>
    </div>
    <article
      v-else
      class="panel markdown-body inspect-report-md"
      v-html="html || '<p class=muted>（空报告）</p>'"
    ></article>
  </div>
</template>
