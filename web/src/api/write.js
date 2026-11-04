import http, { getWriteToken } from './http'

function wsURL(path, query = {}) {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const params = new URLSearchParams()
  Object.entries(query).forEach(([k, v]) => {
    if (v == null || v === '') return
    params.set(k, String(v))
  })
  const token = getWriteToken()
  if (token) params.set('token', token)
  const q = params.toString()
  return `${proto}//${window.location.host}/api/v1${path}${q ? `?${q}` : ''}`
}

function parseFilename(cd) {
  if (!cd) return ''
  const star = /filename\*\s*=\s*UTF-8''([^;]+)/i.exec(cd)
  if (star?.[1]) {
    try {
      return decodeURIComponent(star[1].trim())
    } catch {
      /* ignore */
    }
  }
  const plain = /filename\s*=\s*"([^"]+)"/i.exec(cd) || /filename\s*=\s*([^;]+)/i.exec(cd)
  return plain?.[1]?.trim() || ''
}

async function downloadBlob(url, { headers } = {}) {
  const res = await fetch(url, { headers: headers || {}, credentials: 'same-origin' })
  const ct = (res.headers.get('content-type') || '').toLowerCase()
  if (!res.ok || ct.includes('application/json')) {
    let msg = `下载失败 (${res.status})`
    try {
      const body = await res.json()
      if (body?.message) msg = body.message
    } catch {
      /* ignore */
    }
    const e = new Error(msg)
    e.code = res.status
    throw e
  }
  const blob = await res.blob()
  const name = parseFilename(res.headers.get('content-disposition')) || 'download'
  const obj = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = obj
    a.download = name
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(obj)
  }
  return name
}

export const writeApi = {
  createCluster: (payload) => http.post('/write/clusters', payload),
  probeCluster: (payload) => http.post('/write/clusters/probe', payload),
  updateCluster: (id, payload) => http.put(`/write/clusters/${id}`, payload),
  deleteCluster: (id) => http.delete(`/write/clusters/${id}`),
  testCluster: (id) => http.post(`/write/clusters/${id}/test`),
  inspectCluster: (id) => http.post(`/write/clusters/${id}/inspect`, {}, { timeout: 90000 }),
  exportInspectionReport: ({ period, scope, clusterId } = {}) =>
    http.post(
      '/write/inspections/export',
      {
        period,
        scope,
        ...(clusterId ? { clusterId } : {})
      },
      { timeout: 60000 }
    ),
  restartPod: (id, ns, name) =>
    http.post(`/write/clusters/${id}/namespaces/${ns}/pods/${name}/restart`),
  /** WebSocket URL for interactive pod shell (requires write token). */
  podExecURL: (id, ns, name, { container, cols, rows } = {}) =>
    wsURL(`/write/clusters/${id}/namespaces/${ns}/pods/${encodeURIComponent(name)}/exec`, {
      container,
      cols,
      rows
    }),
  /** Download a file or tar.gz directory from a container. */
  downloadPodPath: (id, ns, name, { container, path } = {}) => {
    const params = new URLSearchParams()
    if (container) params.set('container', container)
    if (path) params.set('path', path)
    const q = params.toString()
    const url = `/api/v1/write/clusters/${id}/namespaces/${ns}/pods/${encodeURIComponent(name)}/download${q ? `?${q}` : ''}`
    const headers = {}
    const token = getWriteToken()
    if (token) headers['X-Igo-Token'] = token
    return downloadBlob(url, { headers })
  },
  scaleDeployment: (id, ns, name, replicas) =>
    http.patch(`/write/clusters/${id}/namespaces/${ns}/deployments/${name}/scale`, { replicas }),
  scaleStatefulSet: (id, ns, name, replicas) =>
    http.patch(`/write/clusters/${id}/namespaces/${ns}/statefulsets/${name}/scale`, { replicas }),
  applyYAML: (id, ns, kind, name, yaml) =>
    http.put(`/write/clusters/${id}/namespaces/${ns}/resources/${kind}/${name}/yaml`, { yaml }),
  patchConfigMapData: (id, ns, name, data) =>
    http.patch(`/write/clusters/${id}/namespaces/${ns}/configmaps/${encodeURIComponent(name)}/data`, { data }),
  patchWorkloadSpecEdit: (id, ns, kind, name, payload) =>
    http.patch(`/write/clusters/${id}/namespaces/${ns}/workloads/${kind}/${encodeURIComponent(name)}/spec-edit`, payload),
  createDoc: (payload) => http.post('/write/docs', payload),
  updateDoc: (docId, payload) => http.put(`/write/docs/${docId}`, payload),
  deleteDoc: (docId) => http.delete(`/write/docs/${docId}`),
  createHost: (payload) => http.post('/write/hosts', payload),
  updateHost: (id, payload) => http.put(`/write/hosts/${encodeURIComponent(id)}`, payload),
  deleteHost: (id) => http.delete(`/write/hosts/${encodeURIComponent(id)}`),
  createAlertWebhook: (payload) => http.post('/write/alert-webhooks', payload),
  updateAlertWebhook: (id, payload) => http.put(`/write/alert-webhooks/${id}`, payload),
  deleteAlertWebhook: (id) => http.delete(`/write/alert-webhooks/${id}`),
  testAlertWebhook: (id) => http.post(`/write/alert-webhooks/${id}/test`, {}, { timeout: 20000 }),
  updateAlertSettings: (payload) => http.put('/write/alert-settings', payload)
}
