<script setup>
import { ref, watch } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'
import { fmtCPU, fmtMemory } from '../utils/quantity'
import { metricsErrorTip } from '../utils/metrics'
import { asArray, createSeq, errMessage } from '../utils/safe'
import {
  TTL,
  cacheFresh,
  cacheGet,
  cacheKey,
  cacheLoad,
  getViewPref,
  invalidateNsLists,
  setViewPref
} from '../utils/resourceCache'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import YamlDrawer from '../components/YamlDrawer.vue'
import ConfigMapEditor from '../components/ConfigMapEditor.vue'
import FilterSelect from '../components/FilterSelect.vue'
import { canWrite } from '../stores/opsAuth'

const { currentClusterId } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
/** pods | controllers | services | configmaps | metrics */
const tab = ref('pods')
const rows = ref([])
const podMetrics = ref([])
const metricsTip = ref('')
const loading = ref(false)
const refreshing = ref(false)
const error = ref('')
const notice = ref('')
const rowsSeq = createSeq()
const nsSeq = createSeq()

const logsDrawer = ref(null)
const yamlDrawer = ref(null)
const cmEditor = ref(null)

const scaleForm = ref({ open: false, kind: 'deployment', name: '', replicas: 1 })

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
  busy: false,
  loading: false,
  error: ''
})
const specSeq = createSeq()

function applyNsList(list) {
  namespaces.value = list
  if (ns.value && !list.some((n) => n.name === ns.value)) ns.value = ''
}

function applyWorkloadsPref() {
  const pref = getViewPref('workloads', currentClusterId.value)
  tab.value = pref?.tab || 'pods'
  ns.value = pref?.ns || ''
}

function persistWorkloadsPref() {
  if (!currentClusterId.value) return
  setViewPref('workloads', currentClusterId.value, { ns: ns.value, tab: tab.value })
}

function applyCachedRows(data) {
  if (tab.value === 'metrics') podMetrics.value = asArray(data)
  else rows.value = asArray(data)
}

function clearRows() {
  rows.value = []
  podMetrics.value = []
  metricsTip.value = ''
}

async function loadNamespaces({ force = false } = {}) {
  const seq = nsSeq.next()
  const clusterId = currentClusterId.value
  if (!clusterId) {
    namespaces.value = []
    ns.value = ''
    clearRows()
    loading.value = false
    refreshing.value = false
    return
  }
  const key = cacheKey('namespaces', clusterId)
  const hit = cacheGet(key)
  if (hit) applyNsList(asArray(hit.data))
  if (!force && cacheFresh(key, TTL.namespaces)) return
  try {
    const list = asArray(
      await cacheLoad(key, TTL.namespaces, () => readApi.listNamespaces(clusterId), { force })
    )
    if (!nsSeq.is(seq)) return
    applyNsList(list)
    error.value = ''
  } catch (e) {
    if (!nsSeq.is(seq)) return
    if (!hit) {
      namespaces.value = []
      ns.value = ''
      clearRows()
    }
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
    replicas: desired
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
    replicas: desired
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
    replicas: desired
  }
}

async function loadControllers(clusterId, namespace) {
  const settled = await Promise.allSettled([
    readApi.listDeployments(clusterId, namespace),
    readApi.listStatefulSets(clusterId, namespace),
    readApi.listDaemonSets(clusterId, namespace)
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

async function fetchRowItems(clusterId, namespace, kind) {
  if (kind === 'metrics') {
    return asArray(await readApi.podMetrics(clusterId, namespace))
  }
  if (kind === 'pods') {
    return asArray(await readApi.listPods(clusterId, namespace))
  }
  if (kind === 'controllers') {
    return await loadControllers(clusterId, namespace)
  }
  if (kind === 'configmaps') {
    return asArray(await readApi.listConfigMaps(clusterId, namespace))
  }
  if (kind === 'services') {
    return asArray(await readApi.listServices(clusterId, namespace))
  }
  return []
}

function ttlForTab(kind) {
  if (kind === 'metrics') return TTL.podMetrics
  if (kind === 'controllers') return TTL.controllers
  if (kind === 'services') return TTL.services
  if (kind === 'configmaps') return TTL.configmaps
  return TTL.pods
}

async function loadRows({ force = false } = {}) {
  const seq = rowsSeq.next()
  const clusterId = currentClusterId.value
  const namespace = ns.value
  const kind = tab.value
  error.value = ''
  if (kind !== 'metrics') {
    podMetrics.value = []
    metricsTip.value = ''
  }
  if (!clusterId || !namespace) {
    clearRows()
    if (rowsSeq.is(seq)) {
      loading.value = false
      refreshing.value = false
    }
    return
  }

  const key = cacheKey(kind === 'metrics' ? 'podMetrics' : kind, clusterId, namespace)
  const ttl = ttlForTab(kind)
  const hit = cacheGet(key)
  if (hit) applyCachedRows(hit.data)
  else if (kind === 'metrics') podMetrics.value = []
  else rows.value = []

  if (!force && cacheFresh(key, ttl)) {
    loading.value = false
    refreshing.value = false
    return
  }

  const has = kind === 'metrics' ? podMetrics.value.length > 0 : rows.value.length > 0
  loading.value = !has
  refreshing.value = has
  try {
    const items = await cacheLoad(key, ttl, () => fetchRowItems(clusterId, namespace, kind), { force })
    if (!rowsSeq.is(seq)) return
    applyCachedRows(items)
    if (kind === 'metrics') metricsTip.value = ''
  } catch (e) {
    if (!rowsSeq.is(seq)) return
    if (kind === 'metrics') {
      metricsTip.value = metricsErrorTip(e)
      error.value = ''
      if (!hit) podMetrics.value = []
    } else {
      error.value = errMessage(e)
      if (!hit) rows.value = []
    }
  } finally {
    if (rowsSeq.is(seq)) {
      loading.value = false
      refreshing.value = false
    }
  }
}

function afterWrite() {
  invalidateNsLists(currentClusterId.value, ns.value)
  return loadRows({ force: true })
}

async function restart(pod) {
  if (!canWrite.value) return
  if (!confirm(`重启 Pod ${pod.name}？（删除后由控制器重建）`)) return
  error.value = ''
  notice.value = ''
  try {
    const r = await writeApi.restartPod(currentClusterId.value, ns.value, pod.name)
    notice.value = r?.message || `已提交重启 ${pod.name}`
    await afterWrite()
  } catch (e) {
    error.value = errMessage(e)
  }
}

function showLogs(pod) {
  logsDrawer.value?.open(pod.name)
}

function openYAML(kind, name) {
  yamlDrawer.value?.open(kind, name)
}

function openConfigMap(name) {
  cmEditor.value?.open(name)
}

function closeLogsOnly() {
  logsDrawer.value?.close()
}

/** 换集群 / 换命名空间时关闭全部编辑抽屉，避免对着旧上下文保存 */
function closeAllEditors() {
  logsDrawer.value?.close()
  yamlDrawer.value?.close()
  cmEditor.value?.close()
  scaleForm.value.open = false
  specSeq.bump()
  specEditor.value.open = false
  specEditor.value.loading = false
}

function openScale(row) {
  if (!canWrite.value) return
  scaleForm.value = {
    open: true,
    kind: row.kind.toLowerCase(),
    name: row.name,
    replicas: row.replicas ?? 1
  }
}

async function doScale() {
  if (!canWrite.value) return
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
    await afterWrite()
  } catch (e) {
    error.value = errMessage(e)
  }
}

function yamlKind(kind) {
  return String(kind || '').toLowerCase()
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
  if (!canWrite.value) return
  const kind = yamlKind(row.kind)
  const seq = specSeq.next()
  specEditor.value = {
    open: true,
    kind,
    name: row.name,
    containers: [],
    configMaps: [],
    secrets: [],
    selected: '',
    image: '',
    env: [],
    envFrom: [],
    resources: { requestsCpu: '', requestsMemory: '', limitsCpu: '', limitsMemory: '' },
    busy: false,
    loading: true,
    error: ''
  }
  try {
    const data = await readApi.getWorkloadSpecEdit(
      currentClusterId.value,
      ns.value,
      kind,
      row.name
    )
    if (!specSeq.is(seq)) return
    const containers = asArray(data?.containers)
    specEditor.value.containers = containers
    specEditor.value.configMaps = asArray(data?.configMaps)
    specEditor.value.secrets = asArray(data?.secrets)
    specEditor.value.loading = false
    if (containers.length) applyContainerToForm(containers[0])
    else specEditor.value.error = `未读到 ${kind}/${row.name} 的容器规格`
  } catch (e) {
    if (!specSeq.is(seq)) return
    specEditor.value.loading = false
    specEditor.value.error = errMessage(e)
  }
}

function closeSpecEdit() {
  specSeq.bump()
  specEditor.value.open = false
  specEditor.value.loading = false
  specEditor.value.busy = false
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
  if (!canWrite.value) return
  specEditor.value.error = ''
  if (!specEditor.value.selected) {
    specEditor.value.error = '请选择容器'
    return
  }
  if (!String(specEditor.value.image || '').trim()) {
    specEditor.value.error = '镜像不能为空'
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
    specEditor.value.loading = false
    await afterWrite()
  } catch (e) {
    specEditor.value.error = errMessage(e)
  } finally {
    specEditor.value.busy = false
  }
}

function onChildError(msg) {
  error.value = msg
}

watch(canWrite, (ok) => {
  if (ok) return
  scaleForm.value.open = false
  if (specEditor.value.open) closeSpecEdit()
})

watch(currentClusterId, async () => {
  closeAllEditors()
  applyWorkloadsPref()
  await loadNamespaces()
  persistWorkloadsPref()
  if (ns.value) loadRows()
  else {
    rowsSeq.bump()
    clearRows()
    loading.value = false
    refreshing.value = false
  }
}, { immediate: true })
watch(ns, () => {
  closeAllEditors()
})
watch(tab, () => {
  // 同命名空间切 Tab 只关日志；保留 YAML/ConfigMap 未保存编辑
  closeLogsOnly()
})
watch([ns, tab], () => {
  persistWorkloadsPref()
  if (!ns.value) {
    rowsSeq.bump()
    clearRows()
    loading.value = false
    refreshing.value = false
    return
  }
  loadRows()
})
</script>

<template>
  <div>
    <h1>工作负载</h1>
    <p class="page-desc">
      查看与运维 Pod / 控制器 / Service / ConfigMap；首次请选择命名空间。当前选用集群在 TTL 内复用列表并合并重复请求；切页不先清空。
    </p>
    <div v-if="!currentClusterId" class="empty">请先在顶部选择当前集群，或到「概览」里点选用。</div>
    <template v-else>
      <div class="toolbar">
        <FilterSelect
          v-model="ns"
          :options="namespaces"
          empty-label="请选择命名空间"
          placeholder="输入 1–3 个字母筛选命名空间"
          aria-label="选择命名空间"
        />
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
      <p v-if="notice" class="notice">{{ notice }}</p>
      <div v-if="!ns" class="empty">请选择命名空间后再加载资源。</div>
      <template v-else>
      <p v-if="loading" class="muted">加载中…</p>
      <p v-else-if="refreshing" class="muted">更新中…</p>

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
                <button v-if="canWrite" class="btn btn-sm btn-danger" @click="restart(p)">重启</button>
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
                  v-if="canWrite && r.canScale"
                  class="btn btn-sm"
                  @click="openScale(r)"
                >扩缩容</button>
                <button v-if="canWrite" class="btn btn-sm" @click="openSpecEdit(r)">常用编辑</button>
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
              <td class="actions">
                <button class="btn btn-sm" @click="openYAML('service', s.name)">YAML</button>
              </td>
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
                <button class="btn btn-sm" @click="openConfigMap(cm.name)">{{ canWrite ? '编辑' : '查看' }}</button>
                <button class="btn btn-sm" @click="openYAML('configmap', cm.name)">YAML</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty">该命名空间下暂无 ConfigMap。</div>
      </template>

      <template v-else-if="tab === 'metrics'">
        <p v-if="metricsTip" class="muted">{{ metricsTip }}</p>
        <table v-if="podMetrics.length" class="table">
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

    <PodLogsDrawer
      ref="logsDrawer"
      :cluster-id="currentClusterId"
      :ns="ns"
    />
    <YamlDrawer
      ref="yamlDrawer"
      :cluster-id="currentClusterId"
      :ns="ns"
      :read-only="!canWrite"
      @saved="afterWrite"
      @error="onChildError"
    />
    <ConfigMapEditor
      ref="cmEditor"
      :cluster-id="currentClusterId"
      :ns="ns"
      :read-only="!canWrite"
      @saved="afterWrite"
      @error="onChildError"
    />

    <div v-if="scaleForm.open" class="drawer-backdrop" @click="scaleForm.open = false"></div>
    <div v-if="scaleForm.open" class="drawer" style="width:min(360px,100%)">
      <div class="toolbar">
        <strong>扩缩容 · {{ scaleForm.kind }}/{{ scaleForm.name }}</strong>
        <div class="actions">
          <button class="btn btn-sm" @click="scaleForm.open = false">关闭</button>
        </div>
      </div>
      <div class="form-row">
        <label>Replicas</label>
        <input v-model.number="scaleForm.replicas" type="number" min="0" />
      </div>
      <div class="actions">
        <button v-if="canWrite" class="btn btn-primary" @click="doScale">确定</button>
        <button class="btn" @click="scaleForm.open = false">取消</button>
      </div>
    </div>

    <!-- Spec edit -->
    <div v-if="specEditor.open" class="drawer-backdrop" @click="closeSpecEdit"></div>
    <div v-if="specEditor.open" class="drawer drawer-wide">
      <div class="toolbar">
        <strong>常用编辑 · {{ specEditor.kind }}/{{ specEditor.name }}</strong>
        <div class="actions">
          <button v-if="canWrite" class="btn btn-primary btn-sm" :disabled="specEditor.busy || specEditor.loading" @click="saveSpecEdit">
            {{ specEditor.busy ? '保存中…' : '保存' }}
          </button>
          <button class="btn btn-sm" @click="closeSpecEdit">关闭</button>
        </div>
      </div>
      <p class="muted">仅改镜像、环境变量、envFrom、资源；复杂变更请用 YAML。</p>
      <p v-if="specEditor.loading" class="muted">正在加载当前规格…</p>
      <p v-if="specEditor.error" class="error">{{ specEditor.error }}</p>

      <template v-if="!specEditor.loading && specEditor.containers.length">
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
      </template>
    </div>
  </div>
</template>
