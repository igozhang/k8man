/** 集群权限与连通状态的展示文案 */

export function permTypeLabel(t) {
  if (t === 'project') return '项目'
  if (t === 'cluster') return '集群'
  if (t === 'unknown') return '未知'
  return t || ''
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
