import { issueCategoryLabel, issueReasonDisplay, issueReasonLabel } from './labels'

/** 巡检/监控摘要行：原因 + 说明 + 时间（若有） */
export function issueInspectBrief(row) {
  const cat = issueCategoryLabel(row?.category)
  const where = row?.namespace ? `${row.namespace}/${row.name}` : (row?.name || '')
  const reason = String(row?.reason || '').trim()
  const reasonText = issueReasonDisplay(reason)
  const message = formatIssueMessage(row)
  const sinceSuffix = issueMessageSuffix(row)

  let line = `${cat} ${where}`.trim()
  if (reasonText) line += `：${reasonText}`

  if (message) {
    line += reasonText ? ` ${message}` : `：${message}`
  } else if (!sinceSuffix && reason === 'Restarts') {
    line += ' · 计入重启偏多（Completed / exit=0 不计）'
  }
  if (sinceSuffix) line += sinceSuffix
  return line
}

/** 抽屉/表格：message 后补时间说明（message 未含时间时） */
export function issueMessageSuffix(row) {
  const since = String(row?.since || '').trim()
  if (!since) return ''
  const msg = formatIssueMessage(row)
  if (msgIncludesSince(msg, since)) return ''
  return ` · ${issueSincePhrase(row, since)}`
}

/** 诊断页「时间」列：把 since/age 说清楚 */
export function issueTimeLabel(row) {
  const since = String(row?.since || '').trim()
  const age = String(row?.age || '').trim()
  if (row?.category === 'event') {
    const t = since || age
    return t ? `最近出现于 ${t} 前` : '—'
  }
  if (since) {
    const phrase = issueSincePhrase(row, since)
    // since 已代表创建时间时，不再重复「对象已创建」
    if (age && age !== since && !isCreationAnchored(row, since, age)) {
      return `${phrase} · 对象已创建 ${age}`
    }
    return phrase
  }
  if (age) return `对象已创建 ${age}`
  return '—'
}

export function formatIssueMessage(row) {
  const reason = String(row?.reason || '').trim()
  let msg = String(row?.message || '').trim()
  if (!msg) return ''

  if (reason === 'NotReady' || reason === 'Misscheduled') {
    msg = msg
      .replace(/^ready\s+(\d+)\/(\d+)/i, '当前 $1/$2 Ready')
      .replace(/·\s*misscheduled\s+(\d+)/i, '· 错位调度 $1 个')
      .replace(/^misscheduled\s+(\d+)$/i, '错位调度 $1 个')
  }

  // 英文相对时间统一成中文，避免「12d ago」歧义
  msg = msg.replace(/(\d+[smhd])\s*ago\b/gi, '$1 前')

  // 说明与原因完全重复时不再跟一遍（如节点 NotReady 的 message 也是 NotReady）
  const label = issueReasonLabel(reason)
  if (msg === reason || msg === label || msg === `${label}（${reason}）`) return ''

  if (msg === 'Pod phase is Failed') return 'Pod 处于 Failed 阶段'
  if (msg === 'Pod phase is Unknown') return 'Pod 处于 Unknown 阶段'

  return msg
}

/** 证据行轻度可读化（保留原字段含义） */
export function formatEvidenceLine(line) {
  let s = String(line || '').trim()
  if (!s) return ''
  s = s.replace(/(\d+[smhd])\s*ago\b/gi, '$1 前')
  s = s.replace(/\bmisscheduled=(\d+)/gi, '错位调度=$1')
  s = s.replace(/\bready=(\d+)\/(\d+)/gi, 'Ready=$1/$2')
  if (s.startsWith('severityNote: ')) {
    s = '降级: ' + s.slice('severityNote: '.length)
  }
  return s
}

/**
 * since 在后端语义并不统一，必须按原因选择措辞，避免“已持续”误用：
 * - Restarts：最近一次异常退出
 * - event：最近出现
 * - Failed/Unknown 且 since==age：多为创建时间锚点
 * - 条件类与容器 Waiting（NotReady / CrashLoop / ImagePull 等）：条件首次成立至今（降级时钟同源）
 */
function issueSincePhrase(row, since) {
  const reason = String(row?.reason || '').trim()
  const category = String(row?.category || '').trim()
  const age = String(row?.age || '').trim()

  if (reason === 'Restarts') return `最近一次异常退出 ${since} 前`
  if (category === 'event') return `最近出现于 ${since} 前`

  if (isCreationAnchored(row, since, age)) {
    if (reason === 'Failed') return `自创建起处于失败（已 ${since}）`
    if (reason === 'Unknown') return `自创建起状态未知（已 ${since}）`
    return `自创建起（已 ${since}）`
  }

  if (isConditionDurationReason(reason)) return `已持续 ${since}`
  return `自 ${since} 前起`
}

function isCreationAnchored(row, since, age) {
  const reason = String(row?.reason || '').trim()
  if (!since || !age || since !== age) return false
  return reason === 'Failed' || reason === 'Unknown'
}

/** 条件 LastTransition：从那时起一直处于该异常 */
function isConditionDurationReason(reason) {
  return [
    'NotReady',
    'Unschedulable',
    'DiskPressure',
    'MemoryPressure',
    'PIDPressure',
    'NetworkUnavailable',
    'CPUHigh',
    'MemoryHigh',
    'Misscheduled',
    'ProgressDeadlineExceeded',
    'ContainerCreating',
    'Unhealthy',
    'CrashLoopBackOff',
    'ImagePullBackOff',
    'ErrImagePull',
    'OOMKilled',
    'CreateContainerError',
    'CreateContainerConfigError',
    'InvalidImageName'
  ].includes(reason)
}

function msgIncludesSince(msg, since) {
  if (!msg || !since) return false
  if (msg.includes(since)) return true
  // 已格式化成「12d 前」时，不再追加 since
  return new RegExp(`${escapeReg(since)}\\s*前`).test(msg) || msgHasRelativeTime(msg)
}

function msgHasRelativeTime(msg) {
  return /\d+[smhd]\s*(ago|前)/i.test(String(msg || ''))
}

function escapeReg(s) {
  return String(s).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
