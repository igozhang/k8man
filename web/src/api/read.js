import http from './http'

export const readApi = {
  getUI: () => http.get('/read/ui'),
  listClusters: () => http.get('/read/clusters'),
  getCluster: (id) => http.get(`/read/clusters/${id}`),
  listNamespaces: (id) => http.get(`/read/clusters/${id}/namespaces`),
  listNodes: (id) => http.get(`/read/clusters/${id}/nodes`),
  listPods: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/pods`),
  getPod: (id, ns, name) => http.get(`/read/clusters/${id}/namespaces/${ns}/pods/${name}`),
  podLogs: (id, ns, name, { tail = 200, signal } = {}) =>
    http.get(`/read/clusters/${id}/namespaces/${ns}/pods/${name}/logs`, { params: { tail }, signal }),
  listDeployments: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/deployments`),
  listStatefulSets: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/statefulsets`),
  listDaemonSets: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/daemonsets`),
  listServices: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/services`),
  listConfigMaps: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/configmaps`),
  getConfigMap: (id, ns, name) => http.get(`/read/clusters/${id}/namespaces/${ns}/configmaps/${encodeURIComponent(name)}`),
  getWorkloadSpecEdit: (id, ns, kind, name) =>
    http.get(`/read/clusters/${id}/namespaces/${ns}/workloads/${kind}/${encodeURIComponent(name)}/spec-edit`),
  getYAML: (id, ns, kind, name) =>
    http.get(`/read/clusters/${id}/namespaces/${ns}/resources/${kind}/${name}/yaml`),
  nodeMetrics: (id) => http.get(`/read/clusters/${id}/metrics/nodes`),
  podMetrics: (id, namespace) =>
    http.get(`/read/clusters/${id}/metrics/pods`, { params: { namespace } }),
  listDocs: (params) => http.get('/read/docs', { params }),
  getDoc: (docId) => http.get(`/read/docs/${docId}`),
  listHosts: () => http.get('/read/hosts')
}
