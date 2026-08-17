import http from './http'

export const readApi = {
  getUI: () => http.get('/read/ui'),
  listClusters: () => http.get('/read/clusters'),
  getCluster: (id) => http.get(`/read/clusters/${id}`),
  testCluster: (id) => http.post(`/read/clusters/${id}/test`),
  listNamespaces: (id) => http.get(`/read/clusters/${id}/namespaces`),
  listNodes: (id) => http.get(`/read/clusters/${id}/nodes`),
  listPods: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/pods`),
  getPod: (id, ns, name) => http.get(`/read/clusters/${id}/namespaces/${ns}/pods/${name}`),
  podLogs: (id, ns, name, tail = 500) =>
    http.get(`/read/clusters/${id}/namespaces/${ns}/pods/${name}/logs`, { params: { tail } }),
  listDeployments: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/deployments`),
  listServices: (id, ns) => http.get(`/read/clusters/${id}/namespaces/${ns}/services`),
  getYAML: (id, ns, kind, name) =>
    http.get(`/read/clusters/${id}/namespaces/${ns}/resources/${kind}/${name}/yaml`),
  nodeMetrics: (id) => http.get(`/read/clusters/${id}/metrics/nodes`),
  podMetrics: (id, namespace) =>
    http.get(`/read/clusters/${id}/metrics/pods`, { params: { namespace } }),
  listDocs: (params) => http.get('/read/docs', { params }),
  getDoc: (docId) => http.get(`/read/docs/${docId}`)
}
