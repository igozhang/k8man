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

export const writeApi = {
  createCluster: (payload) => http.post('/write/clusters', payload),
  probeCluster: (payload) => http.post('/write/clusters/probe', payload),
  updateCluster: (id, payload) => http.put(`/write/clusters/${id}`, payload),
  deleteCluster: (id) => http.delete(`/write/clusters/${id}`),
  testCluster: (id) => http.post(`/write/clusters/${id}/test`),
  inspectCluster: (id) => http.post(`/write/clusters/${id}/inspect`, {}, { timeout: 90000 }),
  restartPod: (id, ns, name) =>
    http.post(`/write/clusters/${id}/namespaces/${ns}/pods/${name}/restart`),
  /** WebSocket URL for interactive pod shell (requires write token). */
  podExecURL: (id, ns, name, { container, cols, rows } = {}) =>
    wsURL(`/write/clusters/${id}/namespaces/${ns}/pods/${encodeURIComponent(name)}/exec`, {
      container,
      cols,
      rows
    }),
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
  deleteHost: (id) => http.delete(`/write/hosts/${encodeURIComponent(id)}`)
}
