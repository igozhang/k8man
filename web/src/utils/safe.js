/** 接口与异步结果的安全取值，避免拉数异常拖垮页面渲染 */

export function errMessage(e, fallback = '请求失败') {
  if (e == null) return fallback
  if (typeof e === 'string') return e || fallback
  const m = e.message ?? e.msg
  if (typeof m === 'string' && m) return m
  if (m != null) return String(m)
  try {
    return String(e) || fallback
  } catch {
    return fallback
  }
}

export function asArray(v) {
  return Array.isArray(v) ? v : []
}

/** 递增序号，用于丢弃过期的异步响应 */
export function createSeq() {
  let n = 0
  return {
    next() {
      return ++n
    },
    is(s) {
      return s === n
    },
    bump() {
      ++n
      return n
    }
  }
}
