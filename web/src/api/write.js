import http from './http'

export const writeApi = {
  createCluster: (payload) => http.post('/write/clusters', payload),
  updateCluster: (id, payload) => http.put(`/write/clusters/${id}`, payload),
  deleteCluster: (id) => http.delete(`/write/clusters/${id}`),
  restartPod: (id, ns, name) =>
    http.post(`/write/clusters/${id}/namespaces/${ns}/pods/${name}/restart`),
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
