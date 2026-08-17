<script setup>
import { ref, watch, onMounted, nextTick } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory } from '../utils/quantity'
import { metricsErrorTip } from '../utils/metrics'
import { asArray, createSeq, errMessage } from '../utils/safe'

const { currentClusterId } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
/** pods | controllers | services | configmaps | metrics */
const tab = ref('pods')
const rows = ref([])
const podMetrics = ref([])
const metricsTip = ref('')
const loading = ref(false)
const error = ref('')
const drawer = ref(null) // { title, content }
const metricsSeq = createSeq()
const rowsSeq = createSeq()
const nsSeq = createSeq()

const yamlEditor = ref({ open: false, kind: '', name: '', yaml: '' })
const scaleForm = ref({ open: false, kind: 'deployment', name: '', replicas: 1 })

const cmEditor = ref({
  open: false,
  name: '',
  mode: 'kv', // kv | yaml
  entries: [], // { key, value }
  binaryKeys: [],
  yaml: '',
  busy: false
})
/** 复制反馈：'yaml' | `v:${index}` | '' */
const cmCopied = ref('')
let cmCopiedTimer = 0
const CM_VAL_MIN_PX = 120
const CM_VAL_MAX_VH = 0.55
const CM_VAL_MAX_PX = 560

const specEditor = ref({
  open: false,
  kind: '',
  name: '',
  containers: [],
  configMaps: [],
  secrets: [],
  selected: '',
  image: '',
  env: [],
  envFrom: [],
  resources: { requestsCpu: '', requestsMemory: '', limitsCpu: '', limitsMemory: '' },
  busy: false
})

async function loadNamespaces() {
  const seq = nsSeq.next()
  if (!currentClusterId.value) {
    namespaces.value = []
    ns.value = ''
    rows.value = []
    podMetrics.value = []
    loading.value = false
    return
  }
  try {
    const list = asArray(await readApi.listNamespaces(currentClusterId.value))
    if (!nsSeq.is(seq)) return
    namespaces.value = list
    // 换集群后清空选择，不自动选中、不拉业务数据
    ns.value = ''
    rows.value = []
    podMetrics.value = []
    metricsTip.value = ''
    error.value = ''
    rowsSeq.bump()
    metricsSeq.bump()
    loading.value = false
  } catch (e) {
    if (!nsSeq.is(seq)) return
    namespaces.value = []
    ns.value = ''
    rows.value = []
    podMetrics.value = []
    metricsTip.value = ''
    rowsSeq.bump()
    metricsSeq.bump()
    loading.value = false
    error.value = errMessage(e)
  }
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
  const settled = await Promise.allSettled([
    readApi.listDeployments(id, namespace),
    readApi.listStatefulSets(id, namespace),
    readApi.listDaemonSets(id, namespace)
  ])
  const deps = settled[0].status === 'fulfilled' ? asArray(settled[0].value) : []
  const sts = settled[1].status === 'fulfilled' ? asArray(settled[1].value) : []
  const dss = settled[2].status === 'fulfilled' ? asArray(settled[2].value) : []
  const failed = settled.filter((s) => s.status === 'rejected')
  if (failed.length === settled.length) {
    throw failed[0].reason || new Error('控制器列表加载失败')
  }
  if (failed.length) {
    error.value = errMessage(failed[0].reason, '部分控制器类型加载失败')
  }
  const list = [
    ...deps.map(mapDeployment),
    ...sts.map(mapStatefulSet),
    ...dss.map(mapDaemonSet)
  ]
  list.sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name))
  return list
}

async function loadPodMetrics() {
  const seq = metricsSeq.next()
  metricsTip.value = ''
  podMetrics.value = []
  if (!currentClusterId.value || !ns.value) return
  try {
    const items = asArray(await readApi.podMetrics(currentClusterId.value, ns.value))
    if (!metricsSeq.is(seq)) return
    podMetrics.value = items
  } catch (e) {
    if (!metricsSeq.is(seq)) return
    metricsTip.value = metricsErrorTip(e)
    podMetrics.value = []
  }
}

async function loadRows() {
  const seq = rowsSeq.next()
  error.value = ''
  rows.value = []
  metricsTip.value = ''
  if (tab.value !== 'metrics') {
    metricsSeq.bump()
    podMetrics.value = []
  }
  if (!currentClusterId.value || !ns.value) {
    if (rowsSeq.is(seq)) loading.value = false
    return
  }
  loading.value = true
  try {
    if (tab.value === 'metrics') {
      await loadPodMetrics()
    } else if (tab.value === 'pods') {
      const items = asArray(await readApi.listPods(currentClusterId.value, ns.value))
      if (!rowsSeq.is(seq)) return
      rows.value = items
    } else if (tab.value === 'controllers') {
      const items = await loadControllers()
      if (!rowsSeq.is(seq)) return
      rows.value = items
    } else if (tab.value === 'configmaps') {
      const items = asArray(await readApi.listConfigMaps(currentClusterId.value, ns.value))
      if (!rowsSeq.is(seq)) return
      rows.value = items
    } else if (tab.value === 'services') {
      const items = asArray(await readApi.listServices(currentClusterId.value, ns.value))
      if (!rowsSeq.is(seq)) return
      rows.value = items
    }
  } catch (e) {
    if (!rowsSeq.is(seq)) return
    error.value = errMessage(e)
    rows.value = []
  } finally {
    if (rowsSeq.is(seq)) loading.value = false
  }
}

async function showLogs(pod) {
  try {
    const r = await readApi.podLogs(currentClusterId.value, ns.value, pod.name)
    drawer.value = { title: `日志 · ${pod.name}`, content: r?.logs || '(empty)' }
  } catch (e) {
    error.value = errMessage(e)
  }
}

async function restart(pod) {
  if (!confirm(`重启 Pod ${pod.name}？（删除后由控制器重建）`)) return
  try {
    const r = await writeApi.restartPod(currentClusterId.value, ns.value, pod.name)
    alert(r?.message || '已提交重启')
    await loadRows()
  } catch (e) {
    error.value = errMessage(e)
  }
}

async function openYAML(kind, name) {
  try {
    const r = await readApi.getYAML(currentClusterId.value, ns.value, kind, name)
    yamlEditor.value = { open: true, kind, name, yaml: r?.yaml || '' }
  } catch (e) {
    error.value = errMessage(e)
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
    error.value = errMessage(e)
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
    error.value = errMessage(e)
  }
}

function yamlKind(kind) {
  return String(kind || '').toLowerCase()
}

function dataToEntries(data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) return []
  return Object.keys(data)
    .sort()
    .map((key) => ({ key, value: data[key] ?? '' }))
}

function entriesToData(entries) {
  const data = {}
  for (const e of asArray(entries)) {
    const k = String(e.key || '').trim()
    if (!k) continue
    if (Object.prototype.hasOwnProperty.call(data, k)) {
      throw new Error(`重复键：${k}`)
    }
    data[k] = e.value ?? ''
  }
  return data
}

function cmValMaxPx() {
  return Math.round(Math.min(window.innerHeight * CM_VAL_MAX_VH, CM_VAL_MAX_PX))
}

/** 按内容增高，夹在最小/最大高度之间 */
function fitCmValue(el) {
  if (!el) return
  const max = cmValMaxPx()
  el.style.height = '0px'
  const content = el.scrollHeight
  const next = Math.min(max, Math.max(CM_VAL_MIN_PX, content))
  el.style.height = `${next}px`
  el.style.overflowY = content > max ? 'auto' : 'hidden'
}

function fitAllCmValues() {
  nextTick(() => {
    document.querySelectorAll('.cm-kv-row textarea.kv-val').forEach(fitCmValue)
  })
}

async function copyText(text, token) {
  const t = String(text ?? '')
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(t)
    } else {
      const ta = document.createElement('textarea')
      ta.value = t
      ta.setAttribute('readonly', '')
      ta.style.position = 'fixed'
      ta.style.left = '-9999px'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    cmCopied.value = token
    window.clearTimeout(cmCopiedTimer)
    cmCopiedTimer = window.setTimeout(() => {
      if (cmCopied.value === token) cmCopied.value = ''
    }, 1500)
  } catch {
    alert('复制失败')
  }
}

function copyCmValue(i) {
  const e = cmEditor.value.entries[i]
  if (!e) return
  copyText(e.value ?? '', `v:${i}`)
}

function copyCmYaml() {
  copyText(cmEditor.value.yaml || '', 'yaml')
}

async function openConfigMap(name) {
  error.value = ''
  cmCopied.value = ''
  try {
    const detail = await readApi.getConfigMap(currentClusterId.value, ns.value, name)
    cmEditor.value = {
      open: true,
      name,
      mode: 'kv',
      entries: dataToEntries(detail?.data),
      binaryKeys: asArray(detail?.binaryKeys),
      yaml: '',
      busy: false
    }
    fitAllCmValues()
  } catch (e) {
    error.value = errMessage(e)
  }
}

function addCmEntry() {
  cmEditor.value.entries.push({ key: '', value: '' })
  fitAllCmValues()
}

function removeCmEntry(i) {
  cmEditor.value.entries.splice(i, 1)
}

async function switchCmMode(mode) {
  if (mode === cmEditor.value.mode) return
  if (
    !confirm(
      mode === 'yaml'
        ? '切换到 YAML 会丢弃未保存的键值修改，并从集群重新加载。继续？'
        : '切换到键值会丢弃未保存的 YAML 修改，并从集群重新加载。继续？'
    )
  ) {
    return
  }
  error.value = ''
  if (mode === 'yaml') {
    try {
      const r = await readApi.getYAML(
        currentClusterId.value,
        ns.value,
        'configmap',
        cmEditor.value.name
      )
      cmEditor.value.yaml = r?.yaml || ''
      cmEditor.value.mode = 'yaml'
    } catch (e) {
      error.value = errMessage(e)
    }
    return
  }
  try {
    const detail = await readApi.getConfigMap(
      currentClusterId.value,
      ns.value,
      cmEditor.value.name
    )
    cmEditor.value.entries = dataToEntries(detail?.data)
    cmEditor.value.binaryKeys = asArray(detail?.binaryKeys)
    cmEditor.value.mode = 'kv'
    fitAllCmValues()
  } catch (e) {
    error.value = errMessage(e)
  }
}

async function saveConfigMap() {
  error.value = ''
  cmEditor.value.busy = true
  try {
    if (cmEditor.value.mode === 'yaml') {
      await writeApi.applyYAML(
        currentClusterId.value,
        ns.value,
        'configmap',
        cmEditor.value.name,
        cmEditor.value.yaml
      )
    } else {
      const data = entriesToData(cmEditor.value.entries)
      await writeApi.patchConfigMapData(
        currentClusterId.value,
        ns.value,
        cmEditor.value.name,
        data
      )
    }
    cmEditor.value.open = false
    await loadRows()
  } catch (e) {
    error.value = errMessage(e)
  } finally {
    cmEditor.value.busy = false
  }
}

function applyContainerToForm(ctr) {
  if (!ctr) return
  specEditor.value.selected = ctr.name
  specEditor.value.image = ctr.image || ''
  specEditor.value.env = asArray(ctr.env).map((e) => ({
    name: e.name || '',
    value: e.value || '',
    valueFrom: e.valueFrom ? { ...e.valueFrom } : null
  }))
  specEditor.value.envFrom = asArray(ctr.envFrom).map((e) => ({
    type: e.type || 'configMapRef',
    name: e.name || '',
    optional: !!e.optional,
    prefix: e.prefix || ''
  }))
  const r = ctr.resources || {}
  specEditor.value.resources = {
    requestsCpu: r.requestsCpu || '',
    requestsMemory: r.requestsMemory || '',
    limitsCpu: r.limitsCpu || '',
    limitsMemory: r.limitsMemory || ''
  }
}

async function openSpecEdit(row) {
  error.value = ''
  const kind = yamlKind(row.kind)
  try {
    const data = await readApi.getWorkloadSpecEdit(
      currentClusterId.value,
      ns.value,
      kind,
      row.name
    )
    const containers = asArray(data?.containers)
    specEditor.value = {
      open: true,
      kind,
      name: row.name,
      containers,
      configMaps: asArray(data?.configMaps),
      secrets: asArray(data?.secrets),
      selected: '',
      image: '',
      env: [],
      envFrom: [],
      resources: { requestsCpu: '', requestsMemory: '', limitsCpu: '', limitsMemory: '' },
      busy: false
    }
    if (containers.length) applyContainerToForm(containers[0])
  } catch (e) {
    error.value = errMessage(e)
  }
}

function onSpecContainerChange() {
  const ctr = specEditor.value.containers.find((c) => c.name === specEditor.value.selected)
  if (ctr) applyContainerToForm(ctr)
}

function onEnvFromTypeChange(row) {
  row.name = ''
}

function addEnvRow() {
  specEditor.value.env.push({ name: '', value: '', valueFrom: null })
}

function removeEnvRow(i) {
  specEditor.value.env.splice(i, 1)
}

function addEnvFromRow() {
  specEditor.value.envFrom.push({
    type: 'configMapRef',
    name: '',
    optional: false,
    prefix: ''
  })
}

function removeEnvFromRow(i) {
  specEditor.value.envFrom.splice(i, 1)
}

function envFromNameOptions(row) {
  if (row.type === 'secretRef') return asArray(specEditor.value.secrets)
  return asArray(specEditor.value.configMaps)
}

async function saveSpecEdit() {
  error.value = ''
  if (!specEditor.value.selected) {
    error.value = '请选择容器'
    return
  }
  if (!String(specEditor.value.image || '').trim()) {
    error.value = '镜像不能为空'
    return
  }
  specEditor.value.busy = true
  try {
    const env = specEditor.value.env
      .filter((e) => String(e.name || '').trim())
      .map((e) => {
        if (e.valueFrom) {
          return { name: e.name, valueFrom: e.valueFrom }
        }
        return { name: e.name, value: e.value }
      })
    const envFrom = specEditor.value.envFrom.filter((e) => String(e.name || '').trim())
    await writeApi.patchWorkloadSpecEdit(
      currentClusterId.value,
      ns.value,
      specEditor.value.kind,
      specEditor.value.name,
      {
        container: specEditor.value.selected,
        image: specEditor.value.image,
        env,
        envFrom,
        resources: { ...specEditor.value.resources }
      }
    )
    specEditor.value.open = false
    await loadRows()
  } catch (e) {
    error.value = errMessage(e)
  } finally {
    specEditor.value.busy = false
  }
}

watch([currentClusterId], async () => {
  await loadNamespaces()
})
onMounted(async () => {
  await loadNamespaces()
})
watch([ns, tab], () => {
  if (!ns.value) {
    rowsSeq.bump()
    metricsSeq.bump()
    rows.value = []
    podMetrics.value = []
    metricsTip.value = ''
    loading.value = false
    return
  }
  loadRows()
})
</script>

<template>
  <div>
    <h1>工作负载</h1>
    <p class="page-desc">
      查看与运维 Pod / 控制器 / Service / ConfigMap；请先选择命名空间后再加载。Pod 指标仅在选中对应 Tab 且已选命名空间时请求。
    </p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
    <template v-else>
      <div class="toolbar">
        <select v-model="ns" style="max-width:240px">
          <option value="">请选择命名空间</option>
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
          <button
            class="tab"
            :class="{ active: tab === 'configmaps' }"
            @click="tab = 'configmaps'"
          >ConfigMap</button>
          <button
            class="tab"
            :class="{ active: tab === 'metrics' }"
            @click="tab = 'metrics'"
          >Pod 指标</button>
        </div>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
      <div v-if="!ns" class="empty">请选择命名空间后再加载资源。</div>
      <template v-else>
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
                <button class="btn btn-sm" @click="openSpecEdit(r)">常用编辑</button>
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

      <template v-else-if="tab === 'configmaps'">
        <table v-if="rows.length" class="table">
          <thead>
            <tr>
              <th>名称</th>
              <th>键数量</th>
              <th>Age</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="cm in rows" :key="cm.name">
              <td><strong>{{ cm.name }}</strong></td>
              <td>{{ cm.keyCount }}</td>
              <td>{{ cm.age }}</td>
              <td class="actions">
                <button class="btn btn-sm" @click="openConfigMap(cm.name)">编辑</button>
                <button class="btn btn-sm" @click="openYAML('configmap', cm.name)">YAML</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty">该命名空间下暂无 ConfigMap。</div>
      </template>

      <template v-else-if="tab === 'metrics'">
        <p v-if="metricsTip" class="muted">{{ metricsTip }}</p>
        <table v-if="!loading && podMetrics.length" class="table">
          <thead><tr><th>Pod</th><th>容器</th><th>CPU</th><th>内存</th></tr></thead>
          <tbody>
            <template v-for="m in podMetrics" :key="m.metadata?.name">
              <tr v-for="c in asArray(m.containers)" :key="(m.metadata?.name || '') + c.name">
                <td>{{ m.metadata?.name }}</td>
                <td>{{ c.name }}</td>
                <td>{{ fmtCPU(c.usage?.cpu) }}</td>
                <td>{{ fmtMemory(c.usage?.memory) }}</td>
              </tr>
            </template>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty">
          {{ metricsTip ? '无法加载 Pod 指标。' : '该命名空间下暂无 Pod 指标数据。' }}
        </div>
      </template>
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
    <div v-if="yamlEditor.open" class="drawer drawer-wide">
      <div class="toolbar">
        <strong>YAML · {{ yamlEditor.kind }}/{{ yamlEditor.name }}</strong>
        <div class="actions">
          <button class="btn btn-sm" @click="copyText(yamlEditor.yaml, 'res-yaml')">
            {{ cmCopied === 'res-yaml' ? '已复制' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="saveYAML">应用</button>
          <button class="btn btn-sm" @click="yamlEditor.open = false">关闭</button>
        </div>
      </div>
      <textarea v-model="yamlEditor.yaml" class="cm-yaml"></textarea>
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

    <!-- ConfigMap editor -->
    <div v-if="cmEditor.open" class="drawer-backdrop" @click="cmEditor.open = false"></div>
    <div v-if="cmEditor.open" class="drawer drawer-wide">
      <div class="toolbar">
        <strong>ConfigMap · {{ cmEditor.name }}</strong>
        <div class="actions">
          <button
            class="btn btn-sm"
            :class="{ 'btn-primary': cmEditor.mode === 'kv' }"
            @click="switchCmMode('kv')"
          >键值</button>
          <button
            class="btn btn-sm"
            :class="{ 'btn-primary': cmEditor.mode === 'yaml' }"
            @click="switchCmMode('yaml')"
          >YAML</button>
          <button
            v-if="cmEditor.mode === 'yaml'"
            class="btn btn-sm"
            @click="copyCmYaml"
          >{{ cmCopied === 'yaml' ? '已复制' : '复制' }}</button>
          <button class="btn btn-primary btn-sm" :disabled="cmEditor.busy" @click="saveConfigMap">
            {{ cmEditor.busy ? '保存中…' : '保存' }}
          </button>
          <button class="btn btn-sm" @click="cmEditor.open = false">关闭</button>
        </div>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
      <p v-if="cmEditor.binaryKeys.length" class="muted">
        含二进制键（不可在键值表编辑）：{{ cmEditor.binaryKeys.join(', ') }}
      </p>
      <template v-if="cmEditor.mode === 'kv'">
        <div class="kv-list cm-kv-list">
          <div v-for="(e, i) in cmEditor.entries" :key="i" class="kv-row cm-kv-row">
            <input v-model="e.key" placeholder="key" class="kv-key" />
            <div class="cm-kv-actions">
              <button class="btn btn-sm" @click="copyCmValue(i)">
                {{ cmCopied === 'v:' + i ? '已复制' : '复制' }}
              </button>
              <button class="btn btn-sm btn-danger" @click="removeCmEntry(i)">删</button>
            </div>
            <textarea
              v-model="e.value"
              placeholder="value"
              class="kv-val"
              rows="4"
              @input="fitCmValue($event.target)"
            ></textarea>
          </div>
        </div>
        <button class="btn btn-sm" @click="addCmEntry">添加键</button>
      </template>
      <textarea
        v-else
        v-model="cmEditor.yaml"
        class="cm-yaml"
      ></textarea>
    </div>

    <!-- Spec edit -->
    <div v-if="specEditor.open" class="drawer-backdrop" @click="specEditor.open = false"></div>
    <div v-if="specEditor.open" class="drawer drawer-wide">
      <div class="toolbar">
        <strong>常用编辑 · {{ specEditor.kind }}/{{ specEditor.name }}</strong>
        <div class="actions">
          <button class="btn btn-primary btn-sm" :disabled="specEditor.busy" @click="saveSpecEdit">
            {{ specEditor.busy ? '保存中…' : '保存' }}
          </button>
          <button class="btn btn-sm" @click="specEditor.open = false">关闭</button>
        </div>
      </div>
      <p class="muted">仅改镜像、环境变量、envFrom、资源；复杂变更请用 YAML。</p>
      <p v-if="error" class="error">{{ error }}</p>

      <div class="form-row">
        <label>容器</label>
        <select v-model="specEditor.selected" @change="onSpecContainerChange">
          <option v-for="c in specEditor.containers" :key="c.name" :value="c.name">{{ c.name }}</option>
        </select>
      </div>
      <div class="form-row">
        <label>镜像</label>
        <input v-model="specEditor.image" />
      </div>

      <h4>环境变量</h4>
      <div class="kv-list">
        <div v-for="(e, i) in specEditor.env" :key="'env-' + i" class="kv-row">
          <template v-if="e.valueFrom">
            <input v-model="e.name" class="kv-key" disabled />
            <input
              class="kv-val"
              disabled
              :value="`${e.valueFrom.type}: ${e.valueFrom.ref}`"
            />
            <button class="btn btn-sm btn-danger" @click="removeEnvRow(i)">删</button>
          </template>
          <template v-else>
            <input v-model="e.name" placeholder="NAME" class="kv-key" />
            <input v-model="e.value" placeholder="value" class="kv-val" />
            <button class="btn btn-sm btn-danger" @click="removeEnvRow(i)">删</button>
          </template>
        </div>
      </div>
      <button class="btn btn-sm" @click="addEnvRow">添加明文环境变量</button>

      <h4 style="margin-top:16px">envFrom</h4>
      <div class="kv-list">
        <div v-for="(e, i) in specEditor.envFrom" :key="'ef-' + i" class="kv-row envfrom-row">
          <select v-model="e.type" class="kv-key" @change="onEnvFromTypeChange(e)">
            <option value="configMapRef">ConfigMap</option>
            <option value="secretRef">Secret</option>
          </select>
          <select v-model="e.name" class="kv-val">
            <option value="">选择…</option>
            <option
              v-for="n in envFromNameOptions(e)"
              :key="n"
              :value="n"
            >{{ n }}</option>
          </select>
          <button class="btn btn-sm btn-danger" @click="removeEnvFromRow(i)">删</button>
        </div>
      </div>
      <button class="btn btn-sm" @click="addEnvFromRow">添加 envFrom</button>

      <h4 style="margin-top:16px">资源</h4>
      <div class="resource-grid">
        <div class="form-row">
          <label>requests CPU</label>
          <input v-model="specEditor.resources.requestsCpu" placeholder="如 100m" />
        </div>
        <div class="form-row">
          <label>requests 内存</label>
          <input v-model="specEditor.resources.requestsMemory" placeholder="如 256Mi" />
        </div>
        <div class="form-row">
          <label>limits CPU</label>
          <input v-model="specEditor.resources.limitsCpu" placeholder="如 500m" />
        </div>
        <div class="form-row">
          <label>limits 内存</label>
          <input v-model="specEditor.resources.limitsMemory" placeholder="如 512Mi" />
        </div>
      </div>
    </div>
  </div>
</template>
