import axios from 'axios'

const http = axios.create({
  baseURL: '/api/v1',
  timeout: 60000
})

let writeToken = ''
let onWriteUnauthorized = () => {}

export function setWriteToken(token) {
  writeToken = typeof token === 'string' ? token : ''
}

export function getWriteToken() {
  return writeToken
}

export function onWriteAuthLost(fn) {
  onWriteUnauthorized = typeof fn === 'function' ? fn : () => {}
}

function apiError(message, code) {
  const e = new Error(typeof message === 'string' && message ? message : 'request failed')
  if (code != null) e.code = code
  return e
}

function isWriteURL(url) {
  const u = String(url || '')
  return u.includes('/write/') || u.endsWith('/write') || u.startsWith('/write')
}

function isKubeconfigURL(config) {
  const u = String(config?.url || '')
  return /\/read\/clusters\/[^/?]+$/.test(u.split('?')[0])
}

http.interceptors.request.use((config) => {
  if (writeToken && (isWriteURL(config.url) || isKubeconfigURL(config))) {
    config.headers = config.headers || {}
    config.headers['X-Igo-Token'] = writeToken
  }
  return config
})

http.interceptors.response.use(
  (res) => {
    const body = res.data
    if (body && typeof body.code === 'number' && body.code !== 0) {
      return Promise.reject(apiError(body.message || 'request failed', body.code))
    }
    return body?.data
  },
  (err) => {
    if (axios.isCancel?.(err) || err?.code === 'ERR_CANCELED') {
      return Promise.reject(apiError('请求已取消', 'canceled'))
    }
    if (err?.code === 'ECONNABORTED' || /timeout/i.test(err?.message || '')) {
      return Promise.reject(apiError('请求超时，请稍后重试', 408))
    }
    const data = err.response?.data
    let msg = data?.message || err.message || 'network error'
    if (typeof msg !== 'string') {
      try {
        msg = JSON.stringify(msg)
      } catch {
        msg = 'network error'
      }
    }
    const code = typeof data?.code === 'number' ? data.code : err.response?.status
    if (code === 401 && isWriteURL(err.config?.url)) {
      // 仅当失败请求仍是当前写 token 时清权，避免旧请求 401 误清新登录
      const hdrs = err.config?.headers
      const sent =
        (typeof hdrs?.get === 'function' ? hdrs.get('X-Igo-Token') : null) ||
        hdrs?.['X-Igo-Token'] ||
        hdrs?.['x-igo-token'] ||
        ''
      if (!sent || sent === writeToken) {
        onWriteUnauthorized(msg)
      }
    }
    return Promise.reject(apiError(msg, code))
  }
)

export default http
