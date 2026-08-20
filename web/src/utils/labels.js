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

/** 原因码 → 业务可读短名（未知则原样返回） */
export function issueReasonLabel(reason) {
  const r = String(reason || '').trim()
  return (
    {
      NotReady: '未就绪',
      Restarts: '重启偏多',
      CrashLoopBackOff: '反复崩溃重启',
      ImagePullBackOff: '镜像拉取失败',
      ErrImagePull: '镜像拉取出错',
      OOMKilled: '内存超限被杀',
      CreateContainerError: '容器创建失败',
      CreateContainerConfigError: '容器配置错误',
      InvalidImageName: '镜像名无效',
      Unschedulable: '无法调度',
      FailedScheduling: '调度失败',
      DiskPressure: '磁盘压力',
      MemoryPressure: '内存压力',
      PIDPressure: '进程数压力',
      NetworkUnavailable: '网络不可用',
      CPUHigh: 'CPU 偏高',
      MemoryHigh: '内存偏高',
      Misscheduled: '错位调度',
      ProgressDeadlineExceeded: '发布超时',
      ContainerCreating: '容器创建中',
      Unknown: '状态未知',
      Failed: '已失败',
      Unhealthy: '探针未通过',
      Killing: '容器被终止',
      BackOff: '重启退避中',
      Error: '异常退出',
      Completed: '正常退出',
      Evicted: '被驱逐',
      NodeNotReady: '节点未就绪',
      FailedMount: '挂载失败',
      FailedAttachVolume: '卷挂载失败',
      FailedCreate: '创建失败',
      FailedBinding: '绑定失败',
      ProbeWarning: '探针告警'
    }[r] || r
  )
}

/** 展示用：中文名（保留原 reason，便于对照 kubectl） */
export function issueReasonDisplay(reason) {
  const r = String(reason || '').trim()
  if (!r) return ''
  const label = issueReasonLabel(r)
  return label === r ? r : `${label}（${r}）`
}
