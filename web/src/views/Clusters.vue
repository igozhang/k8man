<script setup>
import { ref, onMounted } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'

const { refreshClusters, setCurrentClusterId } = useClusterStore()
const list = ref([])
const error = ref('')
const showForm = ref(false)
/** create | edit | rename */
const formMode = ref('create')
const editingId = ref('')
const form = ref({ name: '', description: '', kubeconfig: '' })
const busy = ref(false)
/** paste | upload */
const importMode = ref('paste')
const uploadFileName = ref('')
const fileInput = ref(null)

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
    list.value = await readApi.listClusters()
    await refreshClusters(async () => list.value)
  } catch (e) {
    error.value = e.message
  }
}

function openCreate() {
  editingId.value = ''
  formMode.value = 'create'
  form.value = { name: '', description: '', kubeconfig: '' }
  importMode.value = 'paste'
  uploadFileName.value = ''
  showForm.value = true
}

function openEdit(c) {
  editingId.value = c.id
  formMode.value = 'edit'
  form.value = { name: c.name, description: c.description || '', kubeconfig: '' }
  importMode.value = 'paste'
  uploadFileName.value = ''
  showForm.value = true
}

function openRename(c) {
  editingId.value = c.id
  formMode.value = 'rename'
  form.value = { name: c.name, description: c.description || '', kubeconfig: '' }
  uploadFileName.value = ''
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
  try {
    const kubeconfig = form.value.kubeconfig.trim()
    const name = form.value.name.trim() || (
      formMode.value === 'create' ? suggestNameFromKubeconfig(kubeconfig) : ''
    )
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
    if (formMode.value === 'rename') {
      await writeApi.updateCluster(editingId.value, { name })
    } else if (formMode.value === 'edit') {
      const payload = {
        name,
        description: form.value.description
      }
      if (kubeconfig) payload.kubeconfig = kubeconfig
      await writeApi.updateCluster(editingId.value, payload)
    } else {
      await writeApi.createCluster({
        name,
        description: form.value.description,
        kubeconfig
      })
    }
    showForm.value = false
    await load()
  } catch (e) {
    error.value = e.message
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
    error.value = e.message
  }
}

async function test(c) {
  try {
    const r = await readApi.testCluster(c.id)
    alert(`状态: ${r.status}\n类型: ${r.permType}\nServer: ${r.server}\nNamespaces: ${(r.namespaces || []).length}\n${r.lastError || ''}`)
  } catch (e) {
    error.value = e.message
  }
}

onMounted(load)
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
    <p v-if="error" class="error">{{ error }}</p>

    <div v-if="showForm" class="panel">
      <h2 class="form-title">
        {{ formMode === 'create' ? '导入 kubeconfig' : formMode === 'rename' ? '修改标题' : '编辑集群' }}
      </h2>
      <div class="form-row">
        <label>
          标题
          <span v-if="formMode === 'create'" class="muted">（可留空，将使用 kubeconfig 的 current-context）</span>
        </label>
        <input v-model="form.name" type="text" placeholder="例如 pre-harbor / prod-cluster" />
      </div>
      <div v-if="formMode !== 'rename'" class="form-row">
        <label>描述</label>
        <input v-model="form.description" type="text" />
      </div>

      <div v-if="formMode !== 'rename'" class="form-row">
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

      <div class="actions">
        <button class="btn btn-primary" :disabled="busy" @click="save">
          {{ formMode === 'rename' ? '保存标题' : '保存' }}
        </button>
        <button class="btn" @click="showForm = false">取消</button>
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
        <tr v-for="c in list" :key="c.id">
          <td>
            <strong>{{ c.name }}</strong>
            <div class="muted">{{ c.description }}</div>
          </td>
          <td><span class="tag" :class="c.permType">{{ c.permType }}</span></td>
          <td class="muted">{{ c.server }}</td>
          <td>
            {{ c.status }}
            <div v-if="c.lastError" class="error">{{ c.lastError }}</div>
          </td>
          <td class="actions">
            <button class="btn btn-sm" @click="setCurrentClusterId(c.id)">选用</button>
            <button class="btn btn-sm" @click="test(c)">测试</button>
            <button class="btn btn-sm" @click="openRename(c)">改标题</button>
            <button class="btn btn-sm" @click="openEdit(c)">编辑</button>
            <button class="btn btn-sm btn-danger" @click="remove(c)">删除</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">暂无集群配置，请导入 kubeconfig。</div>
  </div>
</template>

<style scoped>
.form-title {
  margin: 0 0 12px;
  font-size: 16px;
  font-weight: 650;
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
</style>
