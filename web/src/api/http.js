import axios from 'axios'

const http = axios.create({
  baseURL: '/api/v1',
  timeout: 60000
})

function apiError(message, code) {
  const e = new Error(typeof message === 'string' && message ? message : 'request failed')
  if (code != null) e.code = code
  return e
}

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
    return Promise.reject(apiError(msg, code))
  }
)

export default http
