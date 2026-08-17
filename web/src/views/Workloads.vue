<script setup>
import { ref, watch, onMounted } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'

const { currentClusterId } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
/** pods | controllers | services */
const tab = ref('pods')
const rows = ref([])
const loading = ref(false)
const error = ref('')
const drawer = ref(null) // { title, content }
const yamlEditor = ref({ open: false, kind: '', name: '', yaml: '' })
const scaleForm = ref({ open: false, kind: 'deployment', name: '', replicas: 1 })

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

function mapDeployment(d) {
  const name = d.metadata?.name || ''
  const desired = d.spec?.replicas ?? 0
  const ready = d.status?.readyReplicas ?? 0
  return {
    kind: 'Deployment',
    name,
    ready: `${ready}/${desired}`,
    extra: `更新 ${d.status?.updatedReplicas ?? 0} · 可用 ${d.status?.availableReplicas ?? 0}`,
    canScale: true,
    replicas: desired,
    raw: d
  }
}

function mapStatefulSet(s) {
  const name = s.metadata?.name || ''
  const desired = s.spec?.replicas ?? 0
  const ready = s.status?.readyReplicas ?? 0
  return {
    kind: 'StatefulSet',
    name,
    ready: `${ready}/${desired}`,
    extra: `当前 ${s.status?.currentReplicas ?? 0} · 更新 ${s.status?.updatedReplicas ?? 0}`,
    canScale: true,
    replicas: desired,
    raw: s
  }
}

function mapDaemonSet(ds) {
  const name = ds.metadata?.name || ''
  const desired = ds.status?.desiredNumberScheduled ?? 0
  const ready = ds.status?.numberReady ?? 0
  return {
    kind: 'DaemonSet',
    name,
    ready: `${ready}/${desired}`,
    extra: `可用 ${ds.status?.numberAvailable ?? 0} · 更新 ${ds.status?.updatedNumberScheduled ?? 0}`,
    canScale: false,
    replicas: desired,
    raw: ds
  }
}

async function loadControllers() {
  const id = currentClusterId.value
  const namespace = ns.value
  const [deps, sts, dss] = await Promise.all([
    readApi.listDeployments(id, namespace),
    readApi.listStatefulSets(id, namespace),
    readApi.listDaemonSets(id, namespace)
  ])
  const list = [
    ...(deps || []).map(mapDeployment),
    ...(sts || []).map(mapStatefulSet),
    ...(dss || []).map(mapDaemonSet)
  ]
  list.sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name))
  return list
}

async function loadRows() {
  error.value = ''
  rows.value = []
  if (!currentClusterId.value || !ns.value) return
  loading.value = true
  try {
    if (tab.value === 'pods') {
      rows.value = await readApi.listPods(currentClusterId.value, ns.value)
    } else if (tab.value === 'controllers') {
      rows.value = await loadControllers()
    } else {
      rows.value = await readApi.listServices(currentClusterId.value, ns.value)
    }
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

function openScale(row) {
  scaleForm.value = {
    open: true,
    kind: row.kind.toLowerCase(),
    name: row.name,
    replicas: row.replicas ?? 1
  }
}

async function doScale() {
  try {
    const replicas = Number(scaleForm.value.replicas)
    if (scaleForm.value.kind === 'statefulset') {
      await writeApi.scaleStatefulSet(
        currentClusterId.value,
        ns.value,
        scaleForm.value.name,
        replicas
      )
    } else {
      await writeApi.scaleDeployment(
        currentClusterId.value,
        ns.value,
        scaleForm.value.name,
        replicas
      )
    }
    scaleForm.value.open = false
    await loadRows()
  } catch (e) {
    error.value = e.message
  }
}

function yamlKind(kind) {
  return String(kind || '').toLowerCase()
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
    <p class="page-desc">查看与运维 Pod / 控制器（Deployment · StatefulSet · DaemonSet）/ Service。</p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
    <template v-else>
      <div class="toolbar">
        <select v-model="ns" style="max-width:240px">
          <option v-for="n in namespaces" :key="n.name" :value="n.name">{{ n.name }}</option>
        </select>
        <div class="tabs">
          <button class="tab" :class="{ active: tab === 'pods' }" @click="tab = 'pods'">Pods</button>
          <button
            class="tab"
            :class="{ active: tab === 'controllers' }"
            @click="tab = 'controllers'"
          >控制器</button>
          <button class="tab" :class="{ active: tab === 'services' }" @click="tab = 'services'">Services</button>
        </div>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
      <p v-if="loading" class="muted">加载中…</p>

      <template v-if="tab === 'pods'">
        <table v-if="rows.length" class="table">
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
        <div v-else-if="!loading" class="empty">该命名空间下暂无 Pod。</div>
      </template>

      <template v-else-if="tab === 'controllers'">
        <table v-if="rows.length" class="table">
          <thead>
            <tr>
              <th>名称</th>
              <th>类型</th>
              <th>Ready</th>
              <th>详情</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.kind + '/' + r.name">
              <td><strong>{{ r.name }}</strong></td>
              <td><span class="tag">{{ r.kind }}</span></td>
              <td>{{ r.ready }}</td>
              <td class="muted">{{ r.extra }}</td>
              <td class="actions">
                <button
                  v-if="r.canScale"
                  class="btn btn-sm"
                  @click="openScale(r)"
                >扩缩容</button>
                <button
                  class="btn btn-sm"
                  @click="openYAML(yamlKind(r.kind), r.name)"
                >YAML</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty">
          该命名空间下暂无 Deployment / StatefulSet / DaemonSet。
        </div>
      </template>

      <template v-else-if="tab === 'services'">
        <table v-if="rows.length" class="table">
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
        <div v-else-if="!loading" class="empty">该命名空间下暂无 Service。</div>
      </template>
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
      <h3>扩缩容 {{ scaleForm.kind }} / {{ scaleForm.name }}</h3>
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
