import axios from 'axios'

const http = axios.create({
  baseURL: '/api/v1',
  timeout: 60000
})

function apiError(message, code) {
  const e = new Error(message || 'request failed')
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
    const data = err.response?.data
    const msg = data?.message || err.message || 'network error'
    const code = typeof data?.code === 'number' ? data.code : err.response?.status
    return Promise.reject(apiError(msg, code))
  }
)

export default http
