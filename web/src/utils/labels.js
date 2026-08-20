/** 集群权限与连通状态的展示文案 */

export function permTypeLabel(t) {
  if (t === 'project') return '项目'
  if (t === 'cluster') return '集群'
  if (t === 'unknown') return '未知'
  return t || ''
}

export function frontProxyLabel(t) {
  if (t === 'ingress-nginx') return 'ingress-nginx'
  if (t === 'nginx') return '外置 nginx'
  if (t === 'rancher') return 'Rancher'
  return ''
}

export function frontProxyClass(t) {
  if (t === 'ingress-nginx') return 'ingress'
  if (t === 'nginx') return 'nginx-front'
  if (t === 'rancher') return 'rancher'
  return ''
}

export function clusterStatusLabel(status) {
  if (status === 'ok') return '正常'
  if (status === 'error') return '失败'
  if (status === 'unknown') return '未测试'
  return status || '未知'
}

export function clusterStatusClass(status) {
  if (status === 'ok') return 'ok'
  if (status === 'error') return 'error'
  return ''
}

export function severityLabel(s) {
  return s === 'critical' ? '严重' : '警告'
}

export function issueCategoryLabel(c) {
  return { kubeconfig: '配置', node: '节点', workload: '负载', pod: 'Pod', event: '事件' }[c] || c || ''
}
