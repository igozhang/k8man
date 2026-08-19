<script setup>
import { ref, onMounted } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { asArray, errMessage } from '../utils/safe'
import { cacheGet, cacheKey } from '../utils/resourceCache'
import { permTypeLabel, clusterStatusLabel, clusterStatusClass } from '../utils/labels'

const { currentClusterId, refreshClusters, setCurrentClusterId } = useClusterStore()
const list = ref(asArray(cacheGet(cacheKey('clusters', 'local'))?.data))
const error = ref('')
const showForm = ref(false)
/** create | edit */
const formMode = ref('create')
const editingId = ref('')
const form = ref({ name: '', description: '', kubeconfig: '', serverOverride: '', insecureSkipTLS: true })
const busy = ref(false)
/** paste | upload */
const importMode = ref('paste')
const uploadFileName = ref('')
const fileInput = ref(null)
const notice = ref('')
const testingId = ref('')

const hosts = ref([])
const hostsPath = ref('')
const hostError = ref('')
const hostBusy = ref(false)
const hostEditingId = ref('')
const hostForm = ref({ line: '', note: '' })

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
}

async function load() {
  error.value = ''
  try {
    list.value = asArray(await readApi.listClusters())
    await refreshClusters(async () => list.value, { force: true })
  } catch (e) {
    error.value = errMessage(e)
    if (!list.value.length) list.value = []
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

function resetHostForm() {
  hostEditingId.value = ''
  hostForm.value = { line: '', note: '' }
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

function openHostEdit(h) {
  hostEditingId.value = h.id
  hostForm.value = {
    line: formatHostLine(h),
    note: h.note || ''
  }
}

async function saveHost() {
  hostBusy.value = true
  hostError.value = ''
  try {
    const parsed = parseHostLine(hostForm.value.line)
    if (!parsed) {
      hostError.value = '格式应为：IP hostname，例如 10.10.10.10 igozhang.cn（中间一个或多个空格均可）'
      return
    }
    // 防呆：写回规范化「IP hostname」单空格
    hostForm.value.line = `${parsed.ip} ${parsed.hostname}`
    const note = hostForm.value.note.trim()
    const { hostname, ip } = parsed
    if (hostEditingId.value) {
      await writeApi.updateHost(hostEditingId.value, { hostname, ip, note })
    } else {
      await writeApi.createHost({ hostname, ip, note })
    }
    resetHostForm()
    await loadHosts()
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
    if (hostEditingId.value === h.id) resetHostForm()
    await loadHosts()
  } catch (e) {
    hostError.value = errMessage(e)
  }
}

function closeForm() {
  showForm.value = false
  formMode.value = 'create'
  editingId.value = ''
}

function openCreate() {
  editingId.value = ''
  formMode.value = 'create'
  form.value = { name: '', description: '', kubeconfig: '', serverOverride: '', insecureSkipTLS: true }
  importMode.value = 'paste'
  uploadFileName.value = ''
  notice.value = ''
  showForm.value = true
}

function openEdit(c) {
  editingId.value = c.id
  formMode.value = 'edit'
  form.value = {
    name: c.name,
    description: c.description || '',
    kubeconfig: '',
    serverOverride: '',
    insecureSkipTLS: !!c.insecureSkipTLS
  }
  importMode.value = 'paste'
  uploadFileName.value = ''
  notice.value = ''
  error.value = ''
  showForm.value = true
}

function onKubeconfigPaste() {
  if (formMode.value === 'create' && !form.value.name.trim()) {
    const suggested = suggestNameFromKubeconfig(form.value.kubeconfig)
    if (suggested) form.value.name = suggested
  }
}

function onFileChange(e) {
  const file = e.target.files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => {
    const text = String(reader.result || '')
    uploadFileName.value = file.name
    applyKubeconfig(text, { fillName: formMode.value === 'create' })
  }
  reader.onerror = () => {
    error.value = '读取文件失败'
  }
  reader.readAsText(file)
}

function clearUpload() {
  uploadFileName.value = ''
  form.value.kubeconfig = ''
  if (fileInput.value) fileInput.value.value = ''
}

async function save() {
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const kubeconfig = form.value.kubeconfig.trim()
    const name = form.value.name.trim() || (
      formMode.value === 'create' ? suggestNameFromKubeconfig(kubeconfig) : ''
    )
    const serverOverride = form.value.serverOverride.trim()
    if (formMode.value === 'create' && !kubeconfig) {
      error.value = '请粘贴或上传 kubeconfig'
      return
    }
    if (!name) {
      error.value = formMode.value === 'create'
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
      if (kubeconfig) payload.kubeconfig = kubeconfig
      if (serverOverride) payload.serverOverride = serverOverride
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
    closeForm()
    await load()
    if (saved?.status === 'error' && saved?.lastError) {
      notice.value = `已保存，但连通性检测失败：${saved.lastError}`
    } else {
      notice.value = '已保存'
    }
  } catch (e) {
    error.value = errMessage(e)
  } finally {
    busy.value = false
  }
}

async function remove(c) {
  if (!confirm(`删除集群配置「${c.name}」？`)) return
  try {
    await writeApi.deleteCluster(c.id)
    await load()
  } catch (e) {
    error.value = errMessage(e)
  }
}

async function testClusterConn(c) {
  if (!c?.id) return
  testingId.value = c.id
  error.value = ''
  notice.value = ''
  try {
    const r = await writeApi.testCluster(c.id)
    await load()
    if (editingId.value === c.id) {
      const cur = list.value.find((x) => x.id === c.id)
      if (cur) form.value.insecureSkipTLS = !!cur.insecureSkipTLS
    }
    if (r.status === 'ok') {
      notice.value = `「${c.name}」测试通过：${permTypeLabel(r.permType)} · ${r.server}`
    } else {
      error.value = `「${c.name}」测试失败：${r.lastError || r.status}`
    }
  } catch (e) {
    error.value = errMessage(e)
  } finally {
    testingId.value = ''
  }
}

async function testCurrent() {
  const cur = list.value.find((x) => x.id === editingId.value)
  if (!cur) return
  await testClusterConn(cur)
}

onMounted(() => {
  load()
  loadHosts()
})
</script>

<template>
  <div>
    <div class="toolbar">
      <div>
        <h1>KubeConfig 管理</h1>
        <p class="page-desc">导入 Rancher 集群管理员或 Project kubeconfig，作为运维与监控凭证。</p>
      </div>
      <button class="btn btn-primary" @click="openCreate">导入 kubeconfig</button>
    </div>
    <p v-if="error" class="error" style="white-space: pre-wrap">{{ error }}</p>
    <p v-if="notice" class="notice" style="white-space: pre-wrap">{{ notice }}</p>

    <div v-if="showForm" class="panel">
      <h2 class="form-title">
        {{ formMode === 'create' ? '导入 kubeconfig' : '编辑集群' }}
      </h2>
      <div class="form-row">
        <label>
          标题
          <span v-if="formMode === 'create'" class="muted">（可留空，将使用 kubeconfig 的 current-context）</span>
          <span v-else class="muted">（可直接修改显示名称）</span>
        </label>
        <input v-model="form.name" type="text" placeholder="例如 pre-harbor / prod-cluster" />
      </div>
      <div class="form-row">
        <label>描述</label>
        <input v-model="form.description" type="text" />
      </div>

      <div class="form-row">
        <label>{{ formMode === 'edit' ? 'Kubeconfig（留空则不更新）' : 'Kubeconfig' }}</label>
        <div class="tabs">
          <button
            type="button"
            class="tab"
            :class="{ active: importMode === 'paste' }"
            @click="importMode = 'paste'"
          >粘贴导入</button>
          <button
            type="button"
            class="tab"
            :class="{ active: importMode === 'upload' }"
            @click="importMode = 'upload'"
          >上传文件</button>
        </div>

        <textarea
          v-if="importMode === 'paste'"
          v-model="form.kubeconfig"
          placeholder="粘贴 kubeconfig YAML 内容"
          @blur="onKubeconfigPaste"
        ></textarea>

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
          <p v-else class="muted">选择 .yaml / .yml 文件上传</p>
          <details v-if="form.kubeconfig" class="upload-preview">
            <summary>预览内容</summary>
            <pre class="pre">{{ form.kubeconfig }}</pre>
          </details>
        </div>
      </div>

      <div class="form-row">
        <label>
          API Server 覆盖
          <span class="muted">（可选；当前环境 DNS 解析失败时可填可连通地址，如 https://x.x.x.x/k8s/clusters/local）</span>
        </label>
        <input
          v-model="form.serverOverride"
          type="text"
          placeholder="留空则使用 kubeconfig 内 server"
        />
      </div>

      <div class="form-row">
        <label class="check-label">
          <input v-model="form.insecureSkipTLS" type="checkbox" />
          跳过 TLS 证书校验
          <span class="muted">（添加时默认跳过；证书受信时请取消勾选。等同 kubeconfig 的 insecure-skip-tls-verify）</span>
        </label>
      </div>

      <div class="actions">
        <button class="btn btn-primary" :disabled="busy" @click="save">保存</button>
        <button
          v-if="formMode === 'edit'"
          class="btn"
          :disabled="!!testingId || busy"
          @click="testCurrent"
        >{{ testingId === editingId ? '测试中…' : '测试连接' }}</button>
        <button class="btn" @click="closeForm">取消</button>
      </div>
    </div>

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
        <tr v-for="c in list" :key="c.id" :class="{ 'is-current': c.id === currentClusterId }">
          <td>
            <div class="cluster-name">
              <strong>{{ c.name }}</strong>
              <span v-if="c.id === currentClusterId" class="tag current">当前</span>
            </div>
            <div class="muted">{{ c.description }}</div>
            <div v-if="c.insecureSkipTLS" class="muted">TLS: 已跳过校验</div>
          </td>
          <td><span class="tag" :class="c.permType">{{ permTypeLabel(c.permType) }}</span></td>
          <td class="muted">{{ c.server }}</td>
          <td>
            <span class="tag" :class="clusterStatusClass(c.status)">{{ clusterStatusLabel(c.status) }}</span>
            <div v-if="c.lastError" class="error" style="white-space: pre-wrap; max-width: 420px">{{ c.lastError }}</div>
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
              :disabled="!!testingId"
              @click="testClusterConn(c)"
            >{{ testingId === c.id ? '测试中…' : '测试' }}</button>
            <button
              type="button"
              class="btn btn-sm"
              :class="{ 'is-current': showForm && formMode === 'edit' && editingId === c.id }"
              @click="openEdit(c)"
            >{{ showForm && formMode === 'edit' && editingId === c.id ? '编辑中' : '编辑' }}</button>
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
            按系统 hosts 原生格式：<code>IP hostname</code>（示例 <code>10.10.10.10 igozhang.cn</code>，中间多个空格亦可）。
            持久化文件<template v-if="hostsPath">：<code>{{ hostsPath }}</code></template><template v-else>：etc 目录下的 <code>hosts</code></template>。
            可直接改该文件，也可在此增删改；约 2 秒内自动生效。
          </p>
        </div>
      </div>
      <p v-if="hostError" class="error">{{ hostError }}</p>

      <div class="panel host-form">
        <div class="form-row">
          <label>hosts 行（IP 与 hostname 空格分隔，多个空格亦可）</label>
          <input
            v-model="hostForm.line"
            type="text"
            class="host-line-input"
            placeholder="10.10.10.10 igozhang.cn"
            spellcheck="false"
            @keydown.enter.prevent="saveHost"
          />
        </div>
        <div class="actions">
          <button class="btn btn-primary" :disabled="hostBusy" @click="saveHost">
            {{ hostEditingId ? '保存修改' : '添加' }}
          </button>
          <button v-if="hostEditingId" class="btn" @click="resetHostForm">取消编辑</button>
        </div>
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
            <td><code class="host-line-cell">{{ formatHostLine(h) }}</code></td>
            <td class="actions">
              <button
                type="button"
                class="btn btn-sm"
                :class="{ 'is-current': hostEditingId === h.id }"
                @click="openHostEdit(h)"
              >{{ hostEditingId === h.id ? '编辑中' : '编辑' }}</button>
              <button class="btn btn-sm btn-danger" @click="removeHost(h)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">暂无 hosts 记录。DNS 无法解析时在此添加映射。</div>
    </section>
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
  min-width: 5.2em;
  justify-content: center;
}
.notice {
  color: var(--accent);
  font-size: 13px;
  margin: 0 0 12px;
}
.check-label {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  cursor: pointer;
  color: var(--text);
}
.check-label input {
  width: auto;
  margin-top: 3px;
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
</style>
