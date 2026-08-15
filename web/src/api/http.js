import axios from 'axios'

const http = axios.create({
  baseURL: '/api/v1',
  timeout: 60000
})

http.interceptors.response.use(
  (res) => {
    const body = res.data
    if (body && typeof body.code === 'number' && body.code !== 0) {
      return Promise.reject(new Error(body.message || 'request failed'))
    }
    return body?.data
  },
  (err) => {
    const msg = err.response?.data?.message || err.message || 'network error'
    return Promise.reject(new Error(msg))
  }
)

export default http
