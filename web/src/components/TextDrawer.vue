<script setup>
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { copyText } from '../utils/clipboard'

const W_MIN = 420
const W_KEY = 'k8man.textDrawerPx'

const open = ref(false)
const title = ref('')
const content = ref('')
const width = ref(720)
const copied = ref(false)
let copiedTimer = 0

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
  width.value = clampWidth(720)
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
  open.value = false
  title.value = ''
  content.value = ''
}

function openText(heading, body) {
  const text = String(body || '').trim()
  title.value = heading || '说明'
  content.value = text || '（无说明）'
  open.value = true
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
  () => open.value,
  (v) => {
    if (!v) copied.value = false
  }
)

onMounted(initWidth)
onUnmounted(() => {
  stopResize()
  close()
  window.clearTimeout(copiedTimer)
})

defineExpose({ open: openText, close })
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click="close"></div>
  <div
    v-if="open"
    class="drawer drawer-logs"
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
        <button class="btn btn-sm" type="button" @click="close">关闭</button>
      </div>
    </div>
    <div class="field-copy-wrap log-copy-wrap">
      <textarea
        class="log-view is-wrap"
        readonly
        spellcheck="false"
        :value="content"
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
