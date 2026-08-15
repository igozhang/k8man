<script setup>
import { ref, watch, onMounted } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'

const { currentClusterId } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
const tab = ref('pods')
const rows = ref([])
const loading = ref(false)
const error = ref('')
const drawer = ref(null) // { title, content }
const yamlEditor = ref({ open: false, kind: '', name: '', yaml: '' })
const scaleForm = ref({ open: false, name: '', replicas: 1 })

async function loadNamespaces() {
  if (!currentClusterId.value) {
    namespaces.value = []
    ns.value = ''
    rows.value = []
    return
  }
  namespaces.value = await readApi.listNamespaces(currentClusterId.value)
  if (!ns.value && namespaces.value.length) ns.value = namespaces.value[0].name
}

async function loadRows() {
  error.value = ''
  rows.value = []
  if (!currentClusterId.value || !ns.value) return
  loading.value = true
  try {
    if (tab.value === 'pods') rows.value = await readApi.listPods(currentClusterId.value, ns.value)
    else if (tab.value === 'deployments') rows.value = await readApi.listDeployments(currentClusterId.value, ns.value)
    else rows.value = await readApi.listServices(currentClusterId.value, ns.value)
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

async function showLogs(pod) {
  try {
    const r = await readApi.podLogs(currentClusterId.value, ns.value, pod.name)
    drawer.value = { title: `日志 · ${pod.name}`, content: r.logs || '(empty)' }
  } catch (e) {
    error.value = e.message
  }
}

async function restart(pod) {
  if (!confirm(`重启 Pod ${pod.name}？（删除后由控制器重建）`)) return
  try {
    const r = await writeApi.restartPod(currentClusterId.value, ns.value, pod.name)
    alert(r.message)
    await loadRows()
  } catch (e) {
    error.value = e.message
  }
}

async function openYAML(kind, name) {
  try {
    const r = await readApi.getYAML(currentClusterId.value, ns.value, kind, name)
    yamlEditor.value = { open: true, kind, name, yaml: r.yaml || '' }
  } catch (e) {
    error.value = e.message
  }
}

async function saveYAML() {
  try {
    await writeApi.applyYAML(
      currentClusterId.value,
      ns.value,
      yamlEditor.value.kind,
      yamlEditor.value.name,
      yamlEditor.value.yaml
    )
    yamlEditor.value.open = false
    await loadRows()
  } catch (e) {
    error.value = e.message
  }
}

function openScale(d) {
  const replicas = d.spec?.replicas ?? d.status?.replicas ?? 1
  scaleForm.value = { open: true, name: d.metadata?.name || d.name, replicas }
}

async function doScale() {
  try {
    await writeApi.scaleDeployment(
      currentClusterId.value,
      ns.value,
      scaleForm.value.name,
      Number(scaleForm.value.replicas)
    )
    scaleForm.value.open = false
    await loadRows()
  } catch (e) {
    error.value = e.message
  }
}

watch([currentClusterId], async () => {
  try {
    await loadNamespaces()
    await loadRows()
  } catch (e) {
    error.value = e.message
  }
})
watch([ns, tab], loadRows)
onMounted(async () => {
  try {
    await loadNamespaces()
    await loadRows()
  } catch (e) {
    error.value = e.message
  }
})
</script>

<template>
  <div>
    <h1>工作负载</h1>
    <p class="page-desc">查看与运维 Pod / Deployment / Service。</p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
    <template v-else>
      <div class="toolbar">
        <select v-model="ns" style="max-width:240px">
          <option v-for="n in namespaces" :key="n.name" :value="n.name">{{ n.name }}</option>
        </select>
        <div class="tabs">
          <button class="tab" :class="{ active: tab === 'pods' }" @click="tab = 'pods'">Pods</button>
          <button class="tab" :class="{ active: tab === 'deployments' }" @click="tab = 'deployments'">Deployments</button>
          <button class="tab" :class="{ active: tab === 'services' }" @click="tab = 'services'">Services</button>
        </div>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
      <p v-if="loading" class="muted">加载中…</p>

      <table v-if="tab === 'pods'" class="table">
        <thead><tr><th>名称</th><th>状态</th><th>Ready</th><th>重启</th><th>节点</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="p in rows" :key="p.name">
            <td>{{ p.name }}</td>
            <td>{{ p.status }}</td>
            <td>{{ p.ready }}</td>
            <td>{{ p.restarts }}</td>
            <td>{{ p.node }}</td>
            <td class="actions">
              <button class="btn btn-sm" @click="showLogs(p)">日志</button>
              <button class="btn btn-sm" @click="openYAML('pod', p.name)">YAML</button>
              <button class="btn btn-sm btn-danger" @click="restart(p)">重启</button>
            </td>
          </tr>
        </tbody>
      </table>

      <table v-else-if="tab === 'deployments'" class="table">
        <thead><tr><th>名称</th><th>Ready</th><th>Up-to-date</th><th>Available</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="d in rows" :key="d.metadata?.name">
            <td>{{ d.metadata?.name }}</td>
            <td>{{ d.status?.readyReplicas || 0 }}/{{ d.spec?.replicas || 0 }}</td>
            <td>{{ d.status?.updatedReplicas || 0 }}</td>
            <td>{{ d.status?.availableReplicas || 0 }}</td>
            <td class="actions">
              <button class="btn btn-sm" @click="openScale(d)">扩缩容</button>
              <button class="btn btn-sm" @click="openYAML('deployment', d.metadata.name)">YAML</button>
            </td>
          </tr>
        </tbody>
      </table>

      <table v-else class="table">
        <thead><tr><th>名称</th><th>类型</th><th>ClusterIP</th><th>Ports</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="s in rows" :key="s.name">
            <td>{{ s.name }}</td>
            <td>{{ s.type }}</td>
            <td>{{ s.clusterIP }}</td>
            <td>{{ s.ports }}</td>
            <td><button class="btn btn-sm" @click="openYAML('service', s.name)">YAML</button></td>
          </tr>
        </tbody>
      </table>
    </template>

    <div v-if="drawer" class="drawer-backdrop" @click="drawer = null"></div>
    <div v-if="drawer" class="drawer">
      <div class="toolbar">
        <strong>{{ drawer.title }}</strong>
        <button class="btn btn-sm" @click="drawer = null">关闭</button>
      </div>
      <pre class="pre">{{ drawer.content }}</pre>
    </div>

    <div v-if="yamlEditor.open" class="drawer-backdrop" @click="yamlEditor.open = false"></div>
    <div v-if="yamlEditor.open" class="drawer">
      <div class="toolbar">
        <strong>YAML · {{ yamlEditor.kind }}/{{ yamlEditor.name }}</strong>
        <div class="actions">
          <button class="btn btn-primary btn-sm" @click="saveYAML">应用</button>
          <button class="btn btn-sm" @click="yamlEditor.open = false">关闭</button>
        </div>
      </div>
      <textarea v-model="yamlEditor.yaml" style="min-height:70vh"></textarea>
    </div>

    <div v-if="scaleForm.open" class="drawer-backdrop" @click="scaleForm.open = false"></div>
    <div v-if="scaleForm.open" class="drawer" style="width:min(360px,100%)">
      <h3>扩缩容 {{ scaleForm.name }}</h3>
      <div class="form-row">
        <label>Replicas</label>
        <input v-model.number="scaleForm.replicas" type="number" min="0" />
      </div>
      <div class="actions">
        <button class="btn btn-primary" @click="doScale">确定</button>
        <button class="btn" @click="scaleForm.open = false">取消</button>
      </div>
    </div>
  </div>
</template>
