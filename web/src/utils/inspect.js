import { issueCategoryLabel } from './labels'

/** 巡检/监控摘要行：原因 + 说明 + 时间（若有） */
export function issueInspectBrief(row) {
  const cat = issueCategoryLabel(row?.category)
  const where = row?.namespace ? `${row.namespace}/${row.name}` : (row?.name || '')
  const reason = String(row?.reason || '').trim()
  const message = String(row?.message || '').trim()
  const since = String(row?.since || '').trim()

  let line = `${cat} ${where}`.trim()
  if (reason) line += `：${reason}`

  if (message) {
    line += reason ? ` ${message}` : `：${message}`
  } else if (since) {
    if (reason === 'Restarts') line += ` · 近 2h 内无重启，最近一次 ${since} ago`
    else line += ` · 持续 ${since}`
  } else if (reason === 'Restarts') {
    line += ' · 近 2h 内无重启记录'
  }
  return line
}

/** 抽屉详情：message 后补 since（message 未含时间时） */
export function issueMessageSuffix(row) {
  const since = String(row?.since || '').trim()
  if (!since) return ''
  const msg = String(row?.message || '')
  if (msg.includes(since) || /\d+[smhd]\s*ago/i.test(msg)) return ''
  return ` · 最近 ${since} ago`
}
