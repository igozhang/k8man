<script setup>
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { readApi, writeApi } from '../api'
import { canWrite } from '../stores/opsAuth'
import { createSeq, errMessage } from '../utils/safe'

const props = defineProps({
  clusterId: { type: String, default: '' },
  ns: { type: String, default: '' }
})

const EXEC_W_MIN = 420
const EXEC_W_KEY = 'k8man.execDrawerPx'

const open = ref(false)
const podName = ref('')
const activeNs = ref('')
const containers = ref([])
const container = ref('')
const status = ref('')
const error = ref('')
const connecting = ref(false)
const execWidth = ref(720)
const termHost = ref(null)
const dlPath = ref('')
const downloading = ref(false)
const dlStatus = ref('')
const dlError = ref('')

const openSeq = createSeq()
let term = null
let fitAddon = null
let ws = null
let resizeObs = null

function execMinWidth() {
  return Math.min(EXEC_W_MIN, window.innerWidth)
}

function clampExecWidth(n) {
  return Math.min(window.innerWidth, Math.max(execMinWidth(), n))
}

function initExecWidth() {
  try {
    const n = Number(localStorage.getItem(EXEC_W_KEY))
    if (Number.isFinite(n) && n >= EXEC_W_MIN) {
      execWidth.value = clampExecWidth(n)
      return
    }
  } catch {
    /* ignore */
  }
  execWidth.value = clampExecWidth(720)
}

function stopExecResize() {
  document.body.classList.remove('is-drawer-resizing')
}

function startResizeExec(e) {
  const startX = e.clientX
  const startW = execWidth.value
  const handle = e.currentTarget
  handle.setPointerCapture?.(e.pointerId)
  const onMove = (ev) => {
    execWidth.value = clampExecWidth(startW + (startX - ev.clientX))
  }
  const onUp = () => {
    handle.releasePointerCapture?.(e.pointerId)
    handle.removeEventListener('pointermove', onMove)
    handle.removeEventListener('pointerup', onUp)
    handle.removeEventListener('pointercancel', onUp)
    stopExecResize()
    try {
      localStorage.setItem(EXEC_W_KEY, String(Math.round(execWidth.value)))
    } catch {
      /* ignore */
    }
    fitAndNotify()
  }
  document.body.classList.add('is-drawer-resizing')
  handle.addEventListener('pointermove', onMove)
  handle.addEventListener('pointerup', onUp)
  handle.addEventListener('pointercancel', onUp)
}

function disposeTerm() {
  if (resizeObs) {
    resizeObs.disconnect()
    resizeObs = null
  }
  if (term) {
    try {
      term.dispose()
    } catch {
      /* ignore */
    }
    term = null
  }
  fitAddon = null
}

function closeWS() {
  if (!ws) return
  const sock = ws
  ws = null
  try {
    sock.onopen = null
    sock.onmessage = null
    sock.onerror = null
    sock.onclose = null
    if (sock.readyState === WebSocket.OPEN || sock.readyState === WebSocket.CONNECTING) {
      sock.close()
    }
  } catch {
    /* ignore */
  }
}

function close() {
  openSeq.bump()
  closeWS()
  disposeTerm()
  open.value = false
  podName.value = ''
  activeNs.value = ''
  containers.value = []
  container.value = ''
  status.value = ''
  error.value = ''
  connecting.value = false
  dlPath.value = ''
  downloading.value = false
  dlStatus.value = ''
  dlError.value = ''
}

function fitAndNotify() {
  if (!term || !fitAddon || !open.value) return
  try {
    fitAddon.fit()
  } catch {
    /* ignore */
  }
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
  }
}

function attachResizeObserver() {
  if (!termHost.value || typeof ResizeObserver === 'undefined') return
  resizeObs = new ResizeObserver(() => {
    fitAndNotify()
  })
  resizeObs.observe(termHost.value)
}

function decodeWSData(data) {
  if (typeof data === 'string') return data
  if (data instanceof ArrayBuffer) {
    return new TextDecoder().decode(data)
  }
  if (data instanceof Blob) {
    return null
  }
  return String(data || '')
}

async function connectSession(seq) {
  if (!canWrite.value) {
    error.value = '需要可操作权限'
    connecting.value = false
    status.value = '错误'
    return
  }
  const cid = props.clusterId
  const ns = activeNs.value || props.ns
  const name = podName.value
  const ctn = container.value
  if (!cid || !ns || !name) {
    connecting.value = false
    error.value = '缺少集群 / 命名空间 / Pod'
    status.value = '错误'
    return
  }

  closeWS()
  connecting.value = true
  error.value = ''
  status.value = '连接中…'

  await nextTick()
  if (!openSeq.is(seq) || !termHost.value) {
    connecting.value = false
    return
  }

  disposeTerm()
  term = new Terminal({
    cursorBlink: true,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    fontSize: 13,
    theme: {
      background: '#1e2a1e',
      foreground: '#e2e8e2',
      cursor: '#e2e8e2',
      selectionBackground: 'rgba(120, 160, 120, 0.35)'
    }
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(termHost.value)
  fitAddon.fit()
  attachResizeObserver()

  const url = writeApi.podExecURL(cid, ns, name, {
    container: ctn || undefined,
    cols: term.cols,
    rows: term.rows
  })
  const sock = new WebSocket(url)
  sock.binaryType = 'arraybuffer'
  ws = sock

  term.onData((data) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(new TextEncoder().encode(data))
    }
  })

  sock.onopen = () => {
    if (!openSeq.is(seq) || ws !== sock) return
    connecting.value = false
    // 浏览器 WS 已通，但 apiserver SPDY/exec 可能仍在握手
    status.value = '正在进入容器…'
    fitAndNotify()
  }

  sock.onmessage = async (ev) => {
    if (!openSeq.is(seq) || ws !== sock || !term) return
    if (typeof ev.data === 'string') {
      try {
        const msg = JSON.parse(ev.data)
        if (msg?.type === 'error') {
          error.value = msg.data || 'exec 失败'
          status.value = '错误'
          return
        }
        if (msg?.type === 'status') {
          if (msg.data === 'connected') {
            if (status.value !== '已连接') status.value = '正在进入容器…'
          } else {
            status.value = String(msg.data || '')
          }
          return
        }
        if (msg?.type === 'stdout' && msg.data) {
          status.value = '已连接'
          term.write(msg.data)
        }
      } catch {
        status.value = '已连接'
        term.write(ev.data)
      }
      return
    }
    if (ev.data instanceof Blob) {
      const buf = await ev.data.arrayBuffer()
      if (!openSeq.is(seq) || ws !== sock || !term) return
      status.value = '已连接'
      term.write(new TextDecoder().decode(buf))
      return
    }
    const text = decodeWSData(ev.data)
    if (text != null) {
      status.value = '已连接'
      term.write(text)
    }
  }

  sock.onerror = () => {
    if (!openSeq.is(seq) || ws !== sock) return
    connecting.value = false
    error.value = error.value || 'WebSocket 连接失败'
    status.value = '断开'
  }

  sock.onclose = () => {
    if (!openSeq.is(seq)) return
    if (ws === sock) ws = null
    connecting.value = false
    if (status.value !== '错误') status.value = '已断开'
  }
}

async function openExec(name, resourceNs) {
  if (!canWrite.value) return
  const seq = openSeq.next()
  closeWS()
  disposeTerm()
  open.value = true
  podName.value = name
  activeNs.value = resourceNs || props.ns
  containers.value = []
  container.value = ''
  status.value = '准备中…'
  error.value = ''
  connecting.value = true
  dlError.value = ''
  dlStatus.value = ''
  downloading.value = false

  try {
    const pod = await readApi.getPod(props.clusterId, activeNs.value, name)
    if (!openSeq.is(seq)) return
    const list = (pod?.spec?.containers || []).map((c) => c?.name).filter(Boolean)
    containers.value = list
    container.value = list[0] || ''
    if (!list.length) {
      connecting.value = false
      error.value = 'Pod 无可用容器'
      status.value = '错误'
      return
    }
    const phase = pod?.status?.phase
    if (phase && phase !== 'Running') {
      connecting.value = false
      error.value = `Pod 未在运行（${phase}）`
      status.value = '错误'
      return
    }
  } catch (e) {
    if (!openSeq.is(seq)) return
    connecting.value = false
    error.value = errMessage(e)
    status.value = '错误'
    return
  }

  await connectSession(seq)
}

async function onContainerChange() {
  if (!open.value || !podName.value) return
  const seq = openSeq.next()
  await connectSession(seq)
}

function reconnect() {
  if (!open.value || !podName.value) return
  const seq = openSeq.next()
  connectSession(seq)
}

async function startDownload() {
  if (!canWrite.value) {
    dlError.value = '需要可操作权限'
    return
  }
  const path = dlPath.value.trim()
  if (!path) {
    dlError.value = '请填写容器内绝对路径'
    return
  }
  if (!path.startsWith('/')) {
    dlError.value = '路径必须是绝对路径（以 / 开头）'
    return
  }
  const cid = props.clusterId
  const ns = activeNs.value || props.ns
  const name = podName.value
  if (!cid || !ns || !name) {
    dlError.value = '缺少集群 / 命名空间 / Pod'
    return
  }
  downloading.value = true
  dlError.value = ''
  dlStatus.value = '下载中…'
  try {
    const saved = await writeApi.downloadPodPath(cid, ns, name, {
      container: container.value || undefined,
      path
    })
    dlStatus.value = saved ? `已保存 ${saved}` : '已完成'
  } catch (e) {
    dlError.value = errMessage(e)
    dlStatus.value = ''
  } finally {
    downloading.value = false
  }
}

watch(
  () => [props.clusterId, props.ns],
  () => {
    if (open.value) close()
  }
)

watch(canWrite, (ok) => {
  if (!ok && open.value) close()
})

onMounted(() => {
  initExecWidth()
})

onUnmounted(() => {
  stopExecResize()
  close()
})

defineExpose({ open: openExec, close })
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click="close"></div>
  <div
    v-if="open"
    class="drawer drawer-logs drawer-exec"
    :style="{ width: execWidth + 'px' }"
  >
    <div
      class="drawer-resize"
      title="拖拽调整宽度"
      @pointerdown.prevent="startResizeExec"
    ></div>
    <div class="toolbar">
      <strong>
        终端 · {{ podName }}
        <template v-if="container"> · {{ container }}</template>
      </strong>
      <div class="actions">
        <select
          v-if="containers.length > 1"
          v-model="container"
          @change="onContainerChange"
        >
          <option v-for="c in containers" :key="c" :value="c">{{ c }}</option>
        </select>
        <button class="btn btn-sm" type="button" :disabled="connecting" @click="reconnect">重连</button>
        <button class="btn btn-sm" type="button" @click="close">关闭</button>
      </div>
    </div>
    <p class="muted log-hint">
      交互 shell（/bin/sh）· 无输入约 30 分钟自动断开
      <template v-if="status"> · {{ status }}</template>
    </p>
    <p v-if="error" class="error">{{ error }}</p>
    <div ref="termHost" class="exec-term"></div>
    <div class="exec-download">
      <div class="toolbar toolbar-start">
        <span class="exec-download-label">下载</span>
        <input
          v-model="dlPath"
          type="text"
          class="exec-download-path"
          placeholder="容器内绝对路径，如 /var/log 或 /etc/hosts"
          :disabled="downloading"
          @keydown.enter.prevent="startDownload"
        />
        <div class="actions">
          <button
            class="btn btn-sm"
            type="button"
            :disabled="downloading || !dlPath.trim()"
            @click="startDownload"
          >
            {{ downloading ? '下载中…' : '下载' }}
          </button>
        </div>
      </div>
      <p class="muted log-hint">
        文件直接下载；目录 tar.gz 打包（包内一层目录名，递归内容）·
        包名：目录名+北京年月日小时+序号
        <template v-if="dlStatus"> · {{ dlStatus }}</template>
      </p>
      <p v-if="dlError" class="error">{{ dlError }}</p>
    </div>
  </div>
</template>
