<script setup>
import { ref, watch, onMounted } from 'vue'
import { readApi } from '../api'
import { useClusterStore } from '../stores/cluster'

const { currentClusterId, currentCluster } = useClusterStore()
const namespaces = ref([])
const ns = ref('')
const nodeMetrics = ref([])
const podMetrics = ref([])
const error = ref('')
const tip = ref('')

/** 把 metrics Quantity（字符串或对象）解析为数值：CPU→核，Memory→字节 */
function parseQuantity(q, { asCPU = false } = {}) {
  if (q == null || q === '' || q === '-') return null
  if (typeof q === 'object') {
    if (typeof q.number === 'number') return q.number
    if (q.Format != null && q.number != null) return Number(q.number)
    q = String(q.string || q.String || q)
  }
  const s = String(q).trim()
  if (!s) return null

  if (asCPU) {
    // 1 = 1 core；1m = 1e-3；1n = 1e-9；1u = 1e-6
    const m = s.match(/^([0-9]+(?:\.[0-9]+)?)(n|u|m)?$/)
    if (!m) return null
    const n = parseFloat(m[1])
    const unit = m[2] || ''
    if (unit === 'n') return n / 1e9
    if (unit === 'u') return n / 1e6
    if (unit === 'm') return n / 1e3
    return n
  }

  // memory: Ki/Mi/Gi/Ti or K/M/G/T or bare bytes
  const m = s.match(/^([0-9]+(?:\.[0-9]+)?)(Ki|Mi|Gi|Ti|Pi|Ei|k|K|M|G|T|P|E)?$/)
  if (!m) return null
  const n = parseFloat(m[1])
  const unit = m[2] || ''
  const binary = {
    Ki: 1024,
    Mi: 1024 ** 2,
    Gi: 1024 ** 3,
    Ti: 1024 ** 4,
    Pi: 1024 ** 5,
    Ei: 1024 ** 6
  }
  const decimal = {
    k: 1e3,
    K: 1e3,
    M: 1e6,
    G: 1e9,
    T: 1e12,
    P: 1e15,
    E: 1e18
  }
  if (binary[unit]) return n * binary[unit]
  if (decimal[unit]) return n * decimal[unit]
  return n
}

function fmtCPU(q) {
  const cores = parseQuantity(q, { asCPU: true })
  if (cores == null || Number.isNaN(cores)) return '-'
  if (cores < 0.001) return `${(cores * 1e6).toFixed(0)}µ`
  if (cores < 1) return `${Math.round(cores * 1000)}m`
  if (cores < 10) return `${cores.toFixed(2)} 核`
  return `${cores.toFixed(1)} 核`
}

function fmtMemory(q) {
  const bytes = parseQuantity(q, { asCPU: false })
  if (bytes == null || Number.isNaN(bytes)) return '-'
  const units = ['B', 'Ki', 'Mi', 'Gi', 'Ti', 'Pi']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  const digits = v >= 100 ? 0 : v >= 10 ? 1 : 2
  return `${v.toFixed(digits)} ${units[i]}`
}

function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return String(t)
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

function metricCPU(m) {
  return m.usage?.cpu || m.Usage?.cpu || null
}
function metricMem(m) {
  return m.usage?.memory || m.Usage?.memory || null
}

async function load() {
  error.value = ''
  tip.value = ''
  nodeMetrics.value = []
  podMetrics.value = []
  if (!currentClusterId.value) return
  try {
    namespaces.value = await readApi.listNamespaces(currentClusterId.value)
    if (!ns.value && namespaces.value.length) ns.value = namespaces.value[0].name
  } catch (e) {
    error.value = e.message
    return
  }
  if (currentCluster.value?.permType !== 'project') {
    try {
      const items = await readApi.nodeMetrics(currentClusterId.value)
      nodeMetrics.value = items || []
    } catch (e) {
      tip.value = e.message
    }
  }
  if (ns.value) {
    try {
      const items = await readApi.podMetrics(currentClusterId.value, ns.value)
      podMetrics.value = items || []
    } catch (e) {
      tip.value = tip.value || e.message
    }
  }
}

watch([currentClusterId], load)
watch(ns, async () => {
  if (!currentClusterId.value || !ns.value) return
  try {
    podMetrics.value = (await readApi.podMetrics(currentClusterId.value, ns.value)) || []
  } catch (e) {
    tip.value = e.message
    podMetrics.value = []
  }
})
onMounted(load)
</script>

<template>
  <div>
    <h1>监控</h1>
    <p class="page-desc">
      基于目标集群 metrics-server 的 CPU / 内存用量（CPU：m=毫核；内存：Ki/Mi/Gi）。若未安装 metrics-server 将降级提示。
    </p>
    <div v-if="!currentClusterId" class="empty">请先选择集群。</div>
    <template v-else>
      <p v-if="error" class="error">{{ error }}</p>
      <p v-if="tip" class="muted">{{ tip }}</p>

      <div v-if="currentCluster?.permType !== 'project'" class="panel">
        <h3 style="margin-top:0">节点指标</h3>
        <table v-if="nodeMetrics.length" class="table">
          <thead><tr><th>节点</th><th>CPU</th><th>内存</th><th>时间</th></tr></thead>
          <tbody>
            <tr v-for="m in nodeMetrics" :key="m.metadata?.name || m.name">
              <td>{{ m.metadata?.name }}</td>
              <td>{{ fmtCPU(metricCPU(m)) }}</td>
              <td>{{ fmtMemory(metricMem(m)) }}</td>
              <td class="muted">{{ fmtTime(m.timestamp || m.metadata?.creationTimestamp) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="muted">暂无节点指标数据。</div>
      </div>

      <div class="panel">
        <div class="toolbar">
          <h3 style="margin:0">Pod 指标</h3>
          <select v-model="ns" style="max-width:220px">
            <option v-for="n in namespaces" :key="n.name" :value="n.name">{{ n.name }}</option>
          </select>
        </div>
        <table v-if="podMetrics.length" class="table">
          <thead><tr><th>Pod</th><th>容器</th><th>CPU</th><th>内存</th></tr></thead>
          <tbody>
            <template v-for="m in podMetrics" :key="m.metadata?.name">
              <tr v-for="c in (m.containers || [])" :key="m.metadata.name + c.name">
                <td>{{ m.metadata?.name }}</td>
                <td>{{ c.name }}</td>
                <td>{{ fmtCPU(c.usage?.cpu) }}</td>
                <td>{{ fmtMemory(c.usage?.memory) }}</td>
              </tr>
            </template>
          </tbody>
        </table>
        <div v-else class="muted">暂无 Pod 指标数据。</div>
      </div>
    </template>
  </div>
</template>
