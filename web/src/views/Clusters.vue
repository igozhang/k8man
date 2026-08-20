<script setup>
import { ref, onMounted, nextTick, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { cacheGet, cacheKey, invalidateClusterLists } from '../utils/resourceCache'
import { permTypeLabel, clusterStatusLabel, clusterStatusClass, frontProxyLabel, frontProxyClass } from '../utils/labels'
import { copyText } from '../utils/clipboard'
import { suggestHostsHostname } from '../utils/kubeconfig'

const { clusters, currentClusterId, refreshClusters, setCurrentClusterId } = useClusterStore()
const route = useRoute()
const router = useRouter()
const list = ref(asArray(cacheGet(cacheKey('clusters', 'local'))?.data))
const error = ref('')
const notice = ref('')
const showForm = ref(false)
/** create | edit */
const formMode = ref('create')
const editingId = ref('')
const form = ref({ name: '', description: '', kubeconfig: '', serverOverride: '', insecureSkipTLS: true })
const original = ref({ name: '', description: '', kubeconfig: '', server: '', insecureSkipTLS: true })
const busy = ref(false)
/** paste | upload */
const importMode = ref('paste')
const uploadFileName = ref('')
const fileInput = ref(null)
const yamlInput = ref(null)
const testingId = ref('')
const formTesting = ref(false)
const formLoading = ref(false)
const formError = ref('')
const formNotice = ref('')
const copied = ref(false)
const editSeq = createSeq()
let copiedTimer = 0

const hosts = ref([])
const hostsPath = ref('')
const hostError = ref('')
const hostBusy = ref(false)
const hostEditingId = ref('')
const hostAddLine = ref('')
const hostEditLine = ref('')
const hostInlineInput = ref(null)
const formHostLine = ref('')
const formHostError = ref('')
const formHostAutoLine = ref('')
const formHostSuggesting = ref(false)
const formHostSuggestHint = ref('')
let formHostSuggestTimer = null
let formHostSuggestSeq = 0

function suggestNameFromKubeconfig(text) {
  if (!text) return ''
  const ctx = text.match(/^\s*current-context:\s*["']?([^\s"'#]+)/m)
  if (ctx?.[1]) return ctx[1]
  const named = text.match(/^\s*-\s*name:\s*["']?([^\s"'#]+)/m)
  return named?.[1] || ''
}

function applyKubeconfig(text, { fillName = true } = {}) {
  form.value.kubeconfig = text
  if (fillName && !form.value.name.trim()) {
    const suggested = suggestNameFromKubeconfig(text)
    if (suggested) form.value.name = suggested
  }
  scheduleFormHostSuggest()
}

function resetFormHostSuggest() {
  clearTimeout(formHostSuggestTimer)
  formHostSuggestTimer = null
  formHostAutoLine.value = ''
  formHostSuggestHint.value = ''
  formHostSuggesting.value = false
}

function scheduleFormHostSuggest() {
  if (!showForm.value) return
  clearTimeout(formHostSuggestTimer)
  formHostSuggestTimer = setTimeout(() => {
    formHostSuggestTimer = null
    refreshFormHostSuggest()
  }, 400)
}

/** 从 kubeconfig / API Server 提取域名，用运行环境 DNS 解析后预填 hosts 行 */
async function refreshFormHostSuggest() {
  if (!showForm.value) return

  const hostname = suggestHostsHostname({
    kubeconfig: form.value.kubeconfig,
    serverOverride: form.value.serverOverride
  })
  if (!hostname) {
    formHostSuggestHint.value = ''
    if (!formHostLine.value.trim() || formHostLine.value === formHostAutoLine.value) {
      formHostLine.value = ''
      formHostAutoLine.value = ''
    }
    return
  }

  const current = formHostLine.value.trim()
  if (current && current !== formHostAutoLine.value) return

  const seq = ++formHostSuggestSeq
  formHostSuggesting.value = true
  formHostSuggestHint.value = ''
  try {
    const data = await readApi.resolveHost(hostname)
    if (seq !== formHostSuggestSeq || !showForm.value) return
    const ip = data?.ip || data?.ips?.[0]
    if (!ip) {
      formHostSuggestHint.value = `未能解析 ${hostname}`
      return
    }
    const line = `${ip} ${hostname}`
    formHostAutoLine.value = line
    formHostLine.value = line
    formHostSuggestHint.value = `已根据 kubeconfig 域名与运行环境 DNS 填入，可直接点「添加」`
  } catch (e) {
    if (seq !== formHostSuggestSeq) return
    formHostSuggestHint.value = `解析 ${hostname} 失败：${errMessage(e)}`
  } finally {
    if (seq === formHostSuggestSeq) formHostSuggesting.value = false
  }
}

function onFormHostInput() {
  if (formHostLine.value.trim() !== formHostAutoLine.value) {
    formHostAutoLine.value = ''
    formHostSuggestHint.value = ''
  }
}

function onServerOverrideInput() {
  scheduleFormHostSuggest()
}

function emptyForm() {
  return { name: '', description: '', kubeconfig: '', serverOverride: '', insecureSkipTLS: true }
}

function snapshotOriginal(s = {}) {
  original.value = {
    name: s.name || '',
    description: s.description || '',
    kubeconfig: s.kubeconfig || '',
    server: s.server || '',
    insecureSkipTLS: !!s.insecureSkipTLS
  }
}

function isDirty() {
  if (!showForm.value) return false
  const f = form.value
  const o = original.value
  if (formMode.value === 'create') {
    return !!(f.name.trim() || f.description.trim() || f.kubeconfig.trim() || f.serverOverride.trim())
  }
  return f.name.trim() !== o.name.trim()
    || (f.description || '') !== (o.description || '')
    || f.kubeconfig.trim() !== o.kubeconfig.trim()
    || f.serverOverride.trim() !== o.server.trim()
    || !!f.insecureSkipTLS !== !!o.insecureSkipTLS
}

function confirmDiscard() {
  if (busy.value || formTesting.value) {
    formNotice.value = formTesting.value ? '正在测试，请稍候再关' : '正在保存，请稍候再关'
    return false
  }
  if (!isDirty()) return true
  return confirm('有未保存的修改，确定放弃？')
}

async function load({ force = false } = {}) {
  error.value = ''
  try {
    await refreshClusters(() => readApi.listClusters(), { force })
    list.value = asArray(clusters.value)
  } catch (e) {
    error.value = errMessage(e)
    if (!list.value.length) list.value = asArray(clusters.value)
  }
}

async function loadHosts() {
  hostError.value = ''
  try {
    const data = await readApi.listHosts()
    if (Array.isArray(data)) {
      hosts.value = data
      hostsPath.value = ''
    } else {
      hosts.value = asArray(data?.items)
      hostsPath.value = data?.path || ''
    }
  } catch (e) {
    hostError.value = errMessage(e)
    hosts.value = []
  }
}

function formatHostLine(h) {
  return `${h.ip} ${h.hostname}`
}

/** 解析原生 hosts 行：IP hostname；多个空格/制表符均视为分隔 */
function parseHostLine(line) {
  const parts = String(line || '')
    .replace(/\u00a0/g, ' ')
    .trim()
    .split(/\s+/)
    .filter(Boolean)
  if (parts.length < 2) return null
  return { ip: parts[0], hostname: parts[1].toLowerCase() }
}

function setHostInlineInput(el) {
  hostInlineInput.value = el || null
}

function resetHostEdit() {
  hostEditingId.value = ''
  hostEditLine.value = ''
}

function openHostEdit(h) {
  if (hostEditingId.value === h.id) {
    resetHostEdit()
    return
  }
  hostEditingId.value = h.id
  hostEditLine.value = formatHostLine(h)
  nextTick(() => {
    hostInlineInput.value?.focus()
    hostInlineInput.value?.select()
  })
}

/** 写入 hosts 文件；hostname 已存在则同 IP 跳过、不同 IP 则更新 */
async function persistHostLine(raw, { clearInputRef = null } = {}) {
  const parsed = parseHostLine(raw)
  if (!parsed) {
    return { ok: false, error: '格式应为：IP hostname，例如 10.10.10.10 igozhang.cn（中间一个或多个空格均可）' }
  }
  const line = `${parsed.ip} ${parsed.hostname}`
  const existing = hosts.value.find((h) => h.hostname === parsed.hostname)
  if (existing) {
    if (existing.ip === parsed.ip) {
      if (clearInputRef) clearInputRef.value = line
      return { ok: true, skipped: true, message: `hosts 已有 ${line}` }
    }
    await writeApi.updateHost(existing.id, { hostname: parsed.hostname, ip: parsed.ip })
  } else {
    await writeApi.createHost({ hostname: parsed.hostname, ip: parsed.ip })
  }
  if (clearInputRef) clearInputRef.value = line
  await loadHosts()
  return { ok: true, applied: true, message: `Hosts 已更新：${line}` }
}

async function addFormHost() {
  if (!formHostLine.value.trim()) return
  formHostError.value = ''
  hostBusy.value = true
  try {
    const r = await persistHostLine(formHostLine.value, { clearInputRef: formHostLine })
    if (!r.ok) {
      formHostError.value = r.error
      return
    }
    formNotice.value = r.message
    formError.value = ''
  } catch (e) {
    formHostError.value = errMessage(e)
  } finally {
    hostBusy.value = false
  }
}

/** 测试/导入前：若填写了 hosts 行则先写入（未点「添加」也生效） */
async function ensureFormHostApplied() {
  if (!formHostLine.value.trim()) return true
  formHostError.value = ''
  hostBusy.value = true
  try {
    const r = await persistHostLine(formHostLine.value, { clearInputRef: formHostLine })
    if (!r.ok) {
      formHostError.value = r.error
      return false
    }
    if (r.applied) formNotice.value = r.message
    return true
  } catch (e) {
    formHostError.value = errMessage(e)
    return false
  } finally {
    hostBusy.value = false
  }
}

async function saveHost({ fromEdit = false } = {}) {
  hostBusy.value = true
  hostError.value = ''
  try {
    const raw = fromEdit ? hostEditLine.value : hostAddLine.value
    if (fromEdit && hostEditingId.value) {
      const parsed = parseHostLine(raw)
      if (!parsed) {
        hostError.value = '格式应为：IP hostname，例如 10.10.10.10 igozhang.cn（中间一个或多个空格均可）'
        return
      }
      const line = `${parsed.ip} ${parsed.hostname}`
      hostEditLine.value = line
      await writeApi.updateHost(hostEditingId.value, { hostname: parsed.hostname, ip: parsed.ip })
      resetHostEdit()
    } else {
      const r = await persistHostLine(raw, { clearInputRef: hostAddLine })
      if (!r.ok) {
        hostError.value = r.error
        return
      }
    }
    notice.value = 'Hosts 已更新（对后续「测试」与 API 请求立即生效）'
  } catch (e) {
    hostError.value = errMessage(e)
  } finally {
    hostBusy.value = false
  }
}

async function removeHost(h) {
  if (!confirm(`删除 hosts 记录「${h.hostname}」？`)) return
  try {
    await writeApi.deleteHost(h.id)
    if (hostEditingId.value === h.id) resetHostEdit()
    await loadHosts()
  } catch (e) {
    hostError.value = errMessage(e)
  }
}

function closeForm() {
  editSeq.bump()
  showForm.value = false
  formMode.value = 'create'
  editingId.value = ''
  formLoading.value = false
  formError.value = ''
  formNotice.value = ''
  formHostLine.value = ''
  formHostError.value = ''
  resetFormHostSuggest()
  snapshotOriginal()
}

function requestClose() {
  if (!confirmDiscard()) return
  closeForm()
}

function openCreate() {
  if (showForm.value && !confirmDiscard()) return
  editSeq.bump()
  editingId.value = ''
  formMode.value = 'create'
  form.value = emptyForm()
  snapshotOriginal({ insecureSkipTLS: true })
  importMode.value = 'paste'
  uploadFileName.value = ''
  notice.value = ''
  error.value = ''
  formError.value = ''
  formNotice.value = ''
  formLoading.value = false
  formHostLine.value = ''
  formHostError.value = ''
  resetFormHostSuggest()
  showForm.value = true
  nextTick(() => yamlInput.value?.focus())
}

async function openEdit(c) {
  if (showForm.value && formMode.value === 'edit' && editingId.value === c.id && !formLoading.value) {
    requestClose()
    return
  }
  if (showForm.value && !confirmDiscard()) return
  const seq = editSeq.next()
  editingId.value = c.id
  formMode.value = 'edit'
  form.value = {
    name: c.name,
    description: c.description || '',
    kubeconfig: '',
    serverOverride: c.server || '',
    insecureSkipTLS: !!c.insecureSkipTLS
  }
  snapshotOriginal({
    name: c.name,
    description: c.description || '',
    kubeconfig: '',
    server: c.server || '',
    insecureSkipTLS: !!c.insecureSkipTLS
  })
  importMode.value = 'paste'
  uploadFileName.value = ''
  notice.value = ''
  error.value = ''
  formError.value = ''
  formNotice.value = ''
  formHostLine.value = ''
  formHostError.value = ''
  resetFormHostSuggest()
  formLoading.value = true
  showForm.value = true
  try {
    const detail = await readApi.getCluster(c.id)
    if (!editSeq.is(seq)) return
    const kubeconfig = String(detail?.kubeconfig || '')
    const server = detail?.server || c.server || ''
    form.value = {
      name: detail?.name || c.name,
      description: detail?.description || c.description || '',
      kubeconfig,
      serverOverride: server,
      insecureSkipTLS: detail?.insecureSkipTLS ?? !!c.insecureSkipTLS
    }
    snapshotOriginal({
      name: form.value.name,
      description: form.value.description,
      kubeconfig,
      server,
      insecureSkipTLS: form.value.insecureSkipTLS
    })
    if (!kubeconfig) {
      formError.value = '未读到已保存的 kubeconfig。可粘贴或上传替换；留空保存则不覆盖原文。'
    }
    await nextTick()
    yamlInput.value?.focus()
    scheduleFormHostSuggest()
  } catch (e) {
    if (!editSeq.is(seq)) return
    formError.value = `加载 kubeconfig 失败：${errMessage(e)}`
  } finally {
    if (editSeq.is(seq)) formLoading.value = false
  }
}

async function copyKubeconfig() {
  try {
    await copyText(form.value.kubeconfig)
    copied.value = true
    window.clearTimeout(copiedTimer)
    copiedTimer = window.setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    alert('复制失败')
  }
}

function onKubeconfigInput() {
  if (formMode.value === 'create' && !form.value.name.trim()) {
    const suggested = suggestNameFromKubeconfig(form.value.kubeconfig)
    if (suggested) form.value.name = suggested
  }
  scheduleFormHostSuggest()
}

function onFileChange(e) {
  const file = e.target.files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => {
    const text = String(reader.result || '')
    uploadFileName.value = file.name
    applyKubeconfig(text, { fillName: formMode.value === 'create' })
    formNotice.value = `已读入 ${file.name}，确认后点保存`
  }
  reader.onerror = () => {
    formError.value = '读取文件失败'
  }
  reader.readAsText(file)
}

function clearUpload() {
  uploadFileName.value = ''
  form.value.kubeconfig = formMode.value === 'edit' ? original.value.kubeconfig : ''
  if (fileInput.value) fileInput.value.value = ''
  scheduleFormHostSuggest()
}

async function save() {
  if (formLoading.value) return
  busy.value = true
  formError.value = ''
  formNotice.value = ''
  error.value = ''
  notice.value = ''
  try {
    if (!(await ensureFormHostApplied())) return
    const kubeconfig = form.value.kubeconfig.trim()
    const name = form.value.name.trim() || (
      formMode.value === 'create' ? suggestNameFromKubeconfig(kubeconfig) : ''
    )
    const serverOverride = form.value.serverOverride.trim()
    if (formMode.value === 'create' && !kubeconfig) {
      formError.value = '请粘贴或上传 kubeconfig'
      return
    }
    if (!name) {
      formError.value = formMode.value === 'create'
        ? '请填写标题，或确保 kubeconfig 含 current-context'
        : '请填写标题'
      return
    }
    let saved = null
    if (formMode.value === 'edit') {
      const payload = {
        name,
        description: form.value.description,
        insecureSkipTLS: !!form.value.insecureSkipTLS
      }
      if (kubeconfig && kubeconfig !== original.value.kubeconfig.trim()) {
        payload.kubeconfig = kubeconfig
      }
      if (serverOverride && serverOverride !== original.value.server.trim()) {
        payload.serverOverride = serverOverride
      }
      saved = await writeApi.updateCluster(editingId.value, payload)
    } else {
      saved = await writeApi.createCluster({
        name,
        description: form.value.description,
        kubeconfig,
        serverOverride: serverOverride || undefined,
        insecureSkipTLS: !!form.value.insecureSkipTLS
      })
    }
    const wasCreate = formMode.value === 'create'
    const savedId = saved?.id
    closeForm()
    if (savedId) invalidateClusterLists(savedId)
    await load({ force: true })
    notice.value = wasCreate
      ? '已导入。可用「测试」检查连通，再用「选用」切到该集群。'
      : '已保存'
    if (savedId && !currentClusterId.value) setCurrentClusterId(savedId)
  } catch (e) {
    formError.value = errMessage(e)
  } finally {
    busy.value = false
  }
}

async function remove(c) {
  const hint = c.id === currentClusterId.value ? '（当前选用集群）' : ''
  if (!confirm(`删除集群配置「${c.name}」${hint}？`)) return
  try {
    if (showForm.value && editingId.value === c.id) closeForm()
    await writeApi.deleteCluster(c.id)
    invalidateClusterLists(c.id)
    await load({ force: true })
    notice.value = `已删除「${c.name}」`
  } catch (e) {
    error.value = errMessage(e)
  }
}

function frontProxyHint(kind) {
  const label = frontProxyLabel(kind)
  return label ? ` · ${label}` : ''
}

function applyProbeResult(r, { name = '', inForm = false } = {}) {
  const prefix = name ? `「${name}」` : ''
  if (r.status === 'ok') {
    const msg = `${prefix}测试通过：${permTypeLabel(r.permType)} · ${r.server}${frontProxyHint(r.frontProxy)}`
    if (inForm) {
      formNotice.value = msg
      formError.value = ''
    } else {
      notice.value = msg
      error.value = ''
    }
    return
  }
  const msg = `${prefix}测试失败：${r.lastError || r.status}`
  if (inForm) formError.value = msg
  else error.value = msg
}

async function testClusterConn(c) {
  if (!c?.id) return
  testingId.value = c.id
  const inForm = showForm.value && editingId.value === c.id
  if (inForm) {
    formError.value = ''
    formNotice.value = ''
  } else {
    error.value = ''
    notice.value = ''
  }
  try {
    const r = await writeApi.testCluster(c.id)
    invalidateClusterLists(c.id)
    await load({ force: true })
    if (editingId.value === c.id) {
      const cur = list.value.find((x) => x.id === c.id)
      if (cur) {
        form.value.insecureSkipTLS = !!cur.insecureSkipTLS
        if (cur.server) {
          form.value.serverOverride = cur.server
          original.value.server = cur.server
        }
      }
    }
    applyProbeResult(r, {
      name: c.name,
      inForm: showForm.value && editingId.value === c.id
    })
  } catch (e) {
    const msg = errMessage(e)
    if (showForm.value && editingId.value === c.id) formError.value = msg
    else error.value = msg
  } finally {
    testingId.value = ''
  }
}

async function testForm() {
  const kubeconfig = form.value.kubeconfig.trim()
  if (formMode.value === 'edit' && !kubeconfig) {
    const cur = list.value.find((x) => x.id === editingId.value)
    if (cur) await testClusterConn(cur)
    return
  }
  if (!kubeconfig) {
    formError.value = '请粘贴或上传 kubeconfig'
    return
  }
  formTesting.value = true
  formError.value = ''
  formNotice.value = ''
  formHostError.value = ''
  try {
    if (!(await ensureFormHostApplied())) return
    const r = await writeApi.probeCluster({
      kubeconfig,
      serverOverride: form.value.serverOverride.trim() || undefined,
      insecureSkipTLS: !!form.value.insecureSkipTLS
    })
    const unsaved = isDirty()
    if (r.status === 'ok') {
      formNotice.value = `测试通过${unsaved ? '（未保存，这是当前编辑内容）' : ''}：${permTypeLabel(r.permType)} · ${r.server}${frontProxyHint(r.frontProxy)}`
    } else {
      formError.value = `测试失败${unsaved ? '（未保存，这是当前编辑内容）' : ''}：${r.lastError || r.status}`
    }
  } catch (e) {
    formError.value = errMessage(e)
  } finally {
    formTesting.value = false
  }
}

function onEscape(e) {
  if (e.key !== 'Escape') return
  if (showForm.value) {
    requestClose()
    e.preventDefault()
  } else if (hostEditingId.value) {
    resetHostEdit()
  }
}

onMounted(async () => {
  await load()
  loadHosts()
  window.addEventListener('keydown', onEscape)
  const editId = String(route.query.edit || '')
  if (editId) {
    const c = list.value.find((x) => x.id === editId)
    if (c) openEdit(c)
    router.replace({ path: '/clusters' })
  }
})

onUnmounted(() => {
  window.clearTimeout(copiedTimer)
  window.clearTimeout(formHostSuggestTimer)
  window.removeEventListener('keydown', onEscape)
})
</script>

<template>
  <div>
    <div class="toolbar">
      <div>
        <h1>KubeConfig 管理</h1>
        <p class="page-desc">导入 Rancher 集群管理员或 Project kubeconfig。点「测试」会探测入口是 ingress-nginx 还是外置 nginx，结果写在状态列。后台约 2 小时也会巡检并回写状态；改 YAML / API Server / TLS 会清空入口标签与巡检快照。</p>
      </div>
      <button class="btn btn-primary" @click="openCreate">导入 kubeconfig</button>
    </div>
    <p v-if="error" class="error" style="white-space: pre-wrap">{{ error }}</p>
    <p v-if="notice" class="notice" style="white-space: pre-wrap">{{ notice }}</p>

    <table v-if="list.length" class="table">
      <thead>
        <tr>
          <th>标题</th>
          <th>类型</th>
          <th>Server</th>
          <th>状态</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="c in list" :key="c.id" :class="{ 'is-current': c.id === currentClusterId || (showForm && editingId === c.id) }">
          <td>
            <div class="cluster-name">
              <strong>{{ c.name }}</strong>
              <span v-if="c.id === currentClusterId" class="tag current">当前</span>
            </div>
            <div v-if="c.description" class="muted">{{ c.description }}</div>
            <div v-if="c.insecureSkipTLS" class="muted">TLS: 已跳过校验</div>
          </td>
          <td><span class="tag" :class="c.permType">{{ permTypeLabel(c.permType) }}</span></td>
          <td class="server-cell muted" :title="c.server">{{ c.server }}</td>
          <td>
            <div class="cluster-status">
              <span class="tag" :class="clusterStatusClass(c.status)">{{ clusterStatusLabel(c.status) }}</span>
              <span
                v-if="frontProxyLabel(c.frontProxy)"
                class="tag"
                :class="frontProxyClass(c.frontProxy)"
                :title="'入口探测：' + frontProxyLabel(c.frontProxy)"
              >{{ frontProxyLabel(c.frontProxy) }}</span>
            </div>
            <div
              v-if="c.lastError"
              class="error error-clip"
              :title="c.lastError"
            >{{ c.lastError }}</div>
          </td>
          <td class="actions">
            <button
              type="button"
              class="btn btn-sm"
              :class="{ 'is-current': c.id === currentClusterId }"
              :disabled="c.id === currentClusterId"
              @click="setCurrentClusterId(c.id)"
            >{{ c.id === currentClusterId ? '已选用' : '选用' }}</button>
            <button
              class="btn btn-sm"
              :disabled="!!testingId || formTesting || busy"
              @click="testClusterConn(c)"
            >{{ testingId === c.id ? '测试中…' : '测试' }}</button>
            <button
              type="button"
              class="btn btn-sm"
              :class="{ 'is-current': showForm && formMode === 'edit' && editingId === c.id }"
              @click="openEdit(c)"
            >{{
              showForm && formMode === 'edit' && editingId === c.id
                ? (formLoading ? '加载中…' : '收起')
                : '编辑'
            }}</button>
            <button class="btn btn-sm btn-danger" @click="remove(c)">删除</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">暂无集群配置，请导入 kubeconfig。</div>

    <section class="hosts-section">
      <div class="toolbar">
        <div>
          <h2 class="form-title" style="margin:0">Hosts 管理</h2>
          <p class="page-desc">
            按系统 hosts 原生格式：<code>IP hostname</code>（示例 <code>10.10.10.10 igozhang.cn</code>）。
            持久化文件<template v-if="hostsPath">：<code>{{ hostsPath }}</code></template><template v-else>：etc 目录下的 <code>hosts</code></template>。
            约 2 秒内自动生效。
          </p>
        </div>
      </div>
      <p v-if="hostError" class="error">{{ hostError }}</p>

      <div class="toolbar toolbar-start host-add-bar">
        <input
          v-model="hostAddLine"
          type="text"
          class="host-line-input"
          placeholder="添加：10.10.10.10 igozhang.cn"
          spellcheck="false"
          @keydown.enter.prevent="saveHost({ fromEdit: false })"
        />
        <button class="btn btn-primary" :disabled="hostBusy" @click="saveHost({ fromEdit: false })">添加</button>
      </div>

      <table v-if="hosts.length" class="table">
        <thead>
          <tr>
            <th>hosts</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="h in hosts" :key="h.id" :class="{ 'is-current': hostEditingId === h.id }">
            <td>
              <input
                v-if="hostEditingId === h.id"
                :ref="setHostInlineInput"
                v-model="hostEditLine"
                type="text"
                class="host-line-input host-inline-input"
                spellcheck="false"
                @keydown.enter.prevent="saveHost({ fromEdit: true })"
                @keydown.esc.prevent="resetHostEdit"
              />
              <code v-else class="host-line-cell">{{ formatHostLine(h) }}</code>
            </td>
            <td class="actions">
              <template v-if="hostEditingId === h.id">
                <button class="btn btn-sm btn-primary" :disabled="hostBusy" @click="saveHost({ fromEdit: true })">保存</button>
                <button class="btn btn-sm" @click="resetHostEdit">取消</button>
              </template>
              <template v-else>
                <button
                  type="button"
                  class="btn btn-sm"
                  @click="openHostEdit(h)"
                >编辑</button>
                <button class="btn btn-sm btn-danger" @click="removeHost(h)">删除</button>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">暂无 hosts 记录。DNS 无法解析时在此添加映射。</div>
    </section>

    <div v-if="showForm" class="drawer-backdrop" @click="requestClose"></div>
    <div v-if="showForm" class="drawer drawer-wide cluster-drawer">
      <div class="toolbar">
        <strong>{{ formMode === 'create' ? '导入 kubeconfig' : `编辑 · ${form.name || '集群'}` }}</strong>
        <div class="actions">
          <button
            class="btn btn-primary btn-sm"
            :disabled="busy || formLoading || formTesting || !!testingId || hostBusy"
            @click="save"
          >{{ busy ? '保存中…' : (formMode === 'create' ? '导入' : '保存') }}</button>
          <button
            class="btn btn-sm"
            :disabled="busy || formLoading || formTesting || !!testingId || hostBusy"
            @click="testForm"
          >{{ formTesting || testingId === editingId ? '测试中…' : '测试' }}</button>
          <button class="btn btn-sm" :disabled="busy || formTesting" @click="requestClose">关闭</button>
        </div>
      </div>
      <p v-if="formLoading" class="muted">正在加载已保存的 kubeconfig…</p>
      <p v-if="formError" class="error" style="white-space: pre-wrap">{{ formError }}</p>
      <p v-if="formNotice" class="notice" style="white-space: pre-wrap">{{ formNotice }}</p>

      <div class="form-row">
        <label>
          标题
          <span v-if="formMode === 'create'" class="muted">（可留空，将用 kubeconfig 的 current-context）</span>
        </label>
        <input v-model="form.name" type="text" placeholder="例如 pre-harbor / prod-cluster" :disabled="formLoading" />
      </div>
      <div class="form-row">
        <label>描述 <span class="muted">（可选）</span></label>
        <input v-model="form.description" type="text" placeholder="用途、环境等备注" :disabled="formLoading" />
      </div>

      <div class="form-row">
        <label>{{ formMode === 'edit' ? 'Kubeconfig' : 'Kubeconfig' }}</label>
        <div class="tabs">
          <button
            type="button"
            class="tab"
            :class="{ active: importMode === 'paste' }"
            @click="importMode = 'paste'"
          >{{ formMode === 'edit' ? 'YAML' : '粘贴' }}</button>
          <button
            type="button"
            class="tab"
            :class="{ active: importMode === 'upload' }"
            @click="importMode = 'upload'"
          >{{ formMode === 'edit' ? '上传替换' : '上传文件' }}</button>
        </div>

        <div v-if="importMode === 'paste'" class="field-copy-wrap">
          <textarea
            ref="yamlInput"
            v-model="form.kubeconfig"
            class="kubeconfig-yaml"
            :placeholder="formLoading ? '正在加载已保存的 kubeconfig…' : '粘贴 kubeconfig YAML 内容'"
            :disabled="formLoading"
            @input="onKubeconfigInput"
          ></textarea>
          <button
            v-if="form.kubeconfig"
            type="button"
            class="field-copy"
            :class="{ 'is-copied': copied }"
            @click="copyKubeconfig"
          >{{ copied ? '已复制' : '复制' }}</button>
        </div>

        <div v-else class="upload-box">
          <input
            ref="fileInput"
            type="file"
            accept=".yaml,.yml,.kubeconfig,text/yaml,application/x-yaml,text/plain"
            @change="onFileChange"
          />
          <p v-if="uploadFileName" class="muted">
            已选择：{{ uploadFileName }}
            <button type="button" class="btn btn-sm" @click="clearUpload">清除</button>
          </p>
          <p v-else class="muted">{{ formMode === 'edit' ? '上传新文件将替换当前 YAML' : '选择 .yaml / .yml 文件上传' }}</p>
          <details v-if="form.kubeconfig" class="upload-preview">
            <summary>预览内容</summary>
            <pre class="pre">{{ form.kubeconfig }}</pre>
          </details>
        </div>
      </div>

      <div class="form-row">
        <label>
          API Server
          <span class="muted">（可改地址；DNS 不通时可填 IP 或可连通 URL）</span>
        </label>
        <input
          v-model="form.serverOverride"
          type="text"
          :placeholder="formMode === 'edit' ? '当前 kubeconfig 内 server' : '留空则使用 kubeconfig 内 server'"
          :disabled="formLoading"
          @input="onServerOverrideInput"
        />
      </div>

      <div class="form-row">
        <label>
          Hosts
          <span class="muted">（可选；粘贴 kubeconfig 后自动解析 API 域名并填入，也可手动修改）</span>
        </label>
        <div class="toolbar toolbar-start host-add-bar form-host-bar">
          <input
            v-model="formHostLine"
            type="text"
            class="host-line-input"
            placeholder="10.10.10.10 igozhang.cn"
            spellcheck="false"
            :disabled="formLoading || hostBusy || formHostSuggesting"
            @input="onFormHostInput"
            @keydown.enter.prevent="addFormHost"
          />
          <button
            type="button"
            class="btn btn-sm"
            :disabled="formLoading || hostBusy || formHostSuggesting || !formHostLine.trim()"
            @click="addFormHost"
          >添加</button>
        </div>
        <p v-if="formHostSuggesting" class="muted">正在解析 API 域名…</p>
        <p v-else-if="formHostSuggestHint" class="muted">{{ formHostSuggestHint }}</p>
        <p v-if="formHostError" class="error">{{ formHostError }}</p>
      </div>

      <div class="form-row">
        <label class="check-label">
          <input v-model="form.insecureSkipTLS" type="checkbox" :disabled="formLoading" />
          跳过 TLS 证书校验
          <span class="muted">（内网/自签证书可勾选）</span>
        </label>
      </div>
    </div>
  </div>
</template>

<style scoped>
.form-title {
  margin: 0 0 12px;
  font-size: 15px;
  font-weight: 600;
}
.cluster-name {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.35rem 0.5rem;
}
.table td.actions .btn-sm {
  min-width: 5.8em;
  justify-content: center;
}
.notice {
  color: var(--accent);
  font-size: 13px;
  margin: 0 0 12px;
}
.check-label {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  color: var(--text);
}
.check-label input {
  width: auto;
  margin: 0;
}
.upload-box {
  border: 1px dashed var(--line);
  border-radius: 8px;
  padding: 12px;
  background: var(--surface);
}
.upload-box input[type='file'] {
  width: 100%;
  margin-bottom: 8px;
}
.upload-preview {
  margin-top: 8px;
}
.upload-preview .pre {
  max-height: 240px;
  margin-top: 8px;
}
.kubeconfig-yaml {
  min-height: 36vh;
}
.server-cell {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.error-clip {
  max-width: 280px;
  max-height: 3.2em;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  cursor: help;
  margin-top: 4px;
}
.host-add-bar {
  margin-bottom: 12px;
}
.form-host-bar {
  margin-bottom: 0;
}
.host-add-bar .host-line-input {
  flex: 1 1 18rem;
  max-width: none;
  min-width: 12rem;
}
.host-inline-input {
  width: 100%;
  font-family: var(--mono);
}
.hosts-section {
  margin-top: 28px;
  padding-top: 20px;
  border-top: 1px solid var(--line);
}
.host-line-input {
  font-family: var(--mono);
}
code.host-line-cell,
.host-line-cell {
  font-family: var(--mono);
  font-size: 13px;
  white-space: nowrap;
}
code {
  font-family: var(--mono);
  font-size: 12px;
}
.cluster-drawer .toolbar {
  margin-bottom: 12px;
}
.cluster-drawer .toolbar > strong {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cluster-status {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.35rem 0.5rem;
}
</style>
